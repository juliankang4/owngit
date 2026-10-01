//go:build darwin

package state

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS access list entries (sys/kauth.h).
const (
	kauthFilesecMagic = 0x012cc16d
	kauthFilesecNoACL = 0xffffffff
	kauthACEKindMask  = 0xf
	kauthACEPermit    = 1
	kauthUIDNone      = 0xffffffff - 100
	// Rights that let a holder read file data, including generic grants.
	kauthReadRights = 1<<1 | 1<<21 | 1<<24
	// Rights that let a holder add, remove or rename entries, remove the
	// directory itself, or change its owner or access list.
	kauthChangeRights = 1<<2 | 1<<4 | 1<<5 | 1<<6 | 1<<12 | 1<<13 | 1<<21 | 1<<23
	// kauth_filesec: magic, owner and group GUIDs, then the ACL entry count
	// and flags; each entry is a GUID, flags and rights.
	kauthEntriesOffset = 4 + 16 + 16 + 4 + 4
	kauthEntrySize     = 16 + 4 + 4
)

// accessListFix refuses any macOS access list entry that allows changes. The
// entry names an account by GUID, and mapping that GUID needs the system
// library, so an entry for this account is refused too.
func accessListFix(path string, _ os.FileInfo) (string, error) {
	return ChangeAccessListFix(path)
}

// ChangeAccessListFix returns "chmod -N PATH" when an access list entry lets
// an account change path, which an owner-only mode leaves in place.
func ChangeAccessListFix(path string) (string, error) {
	filesec, err := extendedSecurity(path, nil, unix.FSOPT_NOFOLLOW)
	if err != nil {
		return "", err
	}
	if found, err := permitEntry(filesec, kauthChangeRights); err != nil || !found {
		return "", err
	}
	return "chmod -N " + shellQuote(path), nil
}

// validatePrivateInputAccessList refuses a read permit for anyone except the
// file owner. Every group permit is refused explicitly, without trying to
// prove its primary and nested membership. The supplied file is never changed.
func validatePrivateInputAccessList(path string) error {
	filesec, err := extendedSecurity(path, nil, 0)
	if err != nil {
		return err
	}
	found, err := permitEntry(filesec, kauthReadRights)
	if err != nil || !found {
		return err
	}
	owner, err := accessListOwner(path)
	if err != nil {
		return err
	}
	identities, err := matchingPermitIdentities(filesec, kauthReadRights, owner)
	if err != nil || len(identities) == 0 {
		return err
	}
	problem := "its access list gives other accounts read access"
	for _, identity := range identities {
		if accessListIdentityIsGroup(identity) {
			problem = "its access list lets a group read it"
			break
		}
	}
	return &NotPrivateError{
		Problem: problem,
		Fix:     "chmod -N " + shellQuote(operandPath(path)),
	}
}

// accessListOwner returns the GUID that macOS puts in an access-list entry for
// the file owner. Numeric user IDs have no fixed GUID representation, so use
// the system membership service that ACL evaluation uses.
func accessListOwner(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("file owner is unavailable")
	}
	output, err := runMembershipCommand("getuuid", "-u", strconv.FormatUint(uint64(stat.Uid), 10))
	if err != nil {
		return nil, fmt.Errorf("find the file owner's access-list identity: %w", err)
	}
	encoded := strings.ReplaceAll(strings.TrimSpace(string(output)), "-", "")
	owner, err := hex.DecodeString(encoded)
	if err != nil || len(owner) != 16 {
		return nil, errors.New("the file owner's access-list identity is unavailable")
	}
	return owner, nil
}

func runMembershipCommand(arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, commandErr := exec.CommandContext(ctx, "/usr/bin/dsmemberutil", arguments...).Output()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output, commandErr
}

type accessListIdentity [16]byte

func accessListIdentityIsGroup(identity accessListIdentity) bool {
	output, err := runMembershipCommand("getid", "-X", accessListIdentityString(identity))
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), "gid:")
}

func accessListIdentityString(identity accessListIdentity) string {
	encoded := strings.ToUpper(hex.EncodeToString(identity[:]))
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func clearAccessList(file *os.File) error { return clearAccessListOf(file) }

// clearAccessListOf removes the access list of an open file of this account
// when an entry allows anything. The explicit owner check keeps this fail closed.
func clearAccessListOf(file *os.File) error {
	path := file.Name()
	allows := func() (bool, error) {
		filesec, err := extendedSecurity(path, file, 0)
		if err != nil {
			return false, err
		}
		return permitEntry(filesec, ^uint32(0))
	}
	found, err := allows()
	if err != nil || !found {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) == os.Geteuid() {
		unix.Syscall6(unix.SYS_FCHMOD_EXTENDED, file.Fd(), kauthUIDNone, kauthUIDNone, ^uintptr(0), 1, 0)
		if found, err = allows(); err != nil {
			return err
		}
	}
	if found {
		return &NotPrivateError{Problem: "its access list gives other accounts access", Fix: "chmod -N " + shellQuote(operandPath(path))}
	}
	return nil
}

// permitEntry reports whether the kauth_filesec holds an entry that allows
// any of rights.
func permitEntry(filesec []byte, rights uint32) (bool, error) {
	identities, err := matchingPermitIdentities(filesec, rights, nil)
	return len(identities) != 0, err
}

func matchingPermitIdentities(filesec []byte, rights uint32, ignoredIdentity []byte) ([]accessListIdentity, error) {
	if len(filesec) == 0 {
		return nil, nil
	}
	if len(filesec) < kauthEntriesOffset || binary.NativeEndian.Uint32(filesec) != kauthFilesecMagic {
		return nil, errors.New("unrecognized access list")
	}
	count := binary.NativeEndian.Uint32(filesec[kauthEntriesOffset-8:])
	if count == kauthFilesecNoACL {
		return nil, nil
	}
	if uint64(count) > uint64(len(filesec)-kauthEntriesOffset)/kauthEntrySize {
		return nil, errors.New("truncated access list")
	}
	var identities []accessListIdentity
	for index := range int(count) {
		entry := filesec[kauthEntriesOffset+index*kauthEntrySize:]
		flags := binary.NativeEndian.Uint32(entry[16:])
		if flags&kauthACEKindMask != kauthACEPermit || binary.NativeEndian.Uint32(entry[20:])&rights == 0 ||
			(len(ignoredIdentity) != 0 && bytes.Equal(entry[:16], ignoredIdentity)) {
			continue
		}
		var identity accessListIdentity
		copy(identity[:], entry[:16])
		identities = append(identities, identity)
	}
	return identities, nil
}

var getattrlistErr = func(trap, a1, a2, a3, a4, a5, a6 uintptr) unix.Errno {
	_, _, errno := unix.Syscall6(trap, a1, a2, a3, a4, a5, a6)
	return errno
}

// extendedSecurity returns the kauth_filesec of the open file, or of path
// with the getattrlist options, or nothing when it has no access list.
// golang.org/x/sys/unix does not wrap getattrlist, so it is called directly.
func extendedSecurity(path string, file *os.File, options uintptr) ([]byte, error) {
	request := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	buffer := make([]byte, 64<<10)
	var errno unix.Errno
	if file != nil {
		errno = getattrlistErr(unix.SYS_FGETATTRLIST, file.Fd(), uintptr(unsafe.Pointer(&request)),
			uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	} else {
		name, err := unix.BytePtrFromString(path)
		if err != nil {
			return nil, err
		}
		errno = getattrlistErr(unix.SYS_GETATTRLIST, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&request)),
			uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), options, 0)
	}
	if errno == unix.EINVAL || errno == unix.ENOTSUP || errno == unix.EOPNOTSUPP {
		return nil, nil
	}
	if errno != 0 {
		return nil, errno
	}
	// The buffer holds its total length, then an attribute reference: an
	// offset from the reference itself and a length.
	total := binary.NativeEndian.Uint32(buffer)
	if total < 12 || total > uint32(len(buffer)) {
		return nil, errors.New("unexpected attribute buffer")
	}
	offset := int32(binary.NativeEndian.Uint32(buffer[4:]))
	length := binary.NativeEndian.Uint32(buffer[8:])
	start := 4 + int64(offset)
	if length == 0 {
		return nil, nil
	}
	if offset < 0 || start+int64(length) > int64(total) {
		return nil, errors.New("unexpected attribute buffer")
	}
	return buffer[start : start+int64(length)], nil
}
