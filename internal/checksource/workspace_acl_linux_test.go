//go:build linux

package checksource

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// setPOSIXAccessACL writes an access list as setfacl would: owner, owning
// group and mask with read, write and search, the named account, and nothing
// for others.
func setPOSIXAccessACL(t *testing.T, path string, named uint32, namedPermissions uint16) {
	t.Helper()
	entries := []struct {
		tag, permissions uint16
		id               uint32
	}{
		{0x01, 7, 0xffffffff},
		{posixACLUser, namedPermissions, named},
		{posixACLGroupObj, 7, 0xffffffff},
		{posixACLMask, 7, 0xffffffff},
		{0x20, 0, 0xffffffff},
	}
	value := binary.LittleEndian.AppendUint32(nil, posixACLVersion)
	for _, entry := range entries {
		value = binary.LittleEndian.AppendUint16(value, entry.tag)
		value = binary.LittleEndian.AppendUint16(value, entry.permissions)
		value = binary.LittleEndian.AppendUint32(value, entry.id)
	}
	if err := unix.Lsetxattr(path, "system.posix_acl_access", value, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip("this file system has no POSIX access lists")
		}
		t.Fatal(err)
	}
}

func TestAcquireWorkspaceRootRefusesParentACLThatLetsAnotherAccountWrite(t *testing.T) {
	scratch := resolvedTempDir(t)
	parent := directoryWithMode(t, filepath.Join(scratch, "acl"), 0o770)
	noErr(t, os.Chown(parent, -1, os.Getegid()))
	setPOSIXAccessACL(t, parent, 65534, 7)

	root := filepath.Join(parent, "workspace")
	_, err := AcquireWorkspaceRoot(root)
	requireUnsafeParent(t, err, root, parent)
	var unsafe *UnsafeWorkspaceRootError
	if errors.As(err, &unsafe) && unsafe.Fix != "chmod g-w,o-w '"+parent+"'" {
		t.Fatalf("refusal fix %q, want a chmod that clears the mask", unsafe.Fix)
	}
	requireNoWorkspaceBelow(t, parent)

	if !ownPrivateGroup(uint32(os.Getegid())) {
		return
	}
	// Control: the same list with read access alone is accepted for an
	// account with its own private group.
	readOnly := directoryWithMode(t, filepath.Join(scratch, "read-only"), 0o770)
	noErr(t, os.Chown(readOnly, -1, os.Getegid()))
	setPOSIXAccessACL(t, readOnly, 65534, 5)
	workspace, err := AcquireWorkspaceRoot(filepath.Join(readOnly, "workspace"))
	noErr(t, err)
	workspace.Close()
}
