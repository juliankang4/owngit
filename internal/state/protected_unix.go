//go:build !windows

package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// MaximumLinks bounds the symbolic links followed on the way to a path.
const MaximumLinks = 40

// walkWay resolves the absolute path from "/" one name at a time through
// directory handles, never letting the system follow a link: it opens each
// directory relative to the one before it without following a link, and
// reads and follows each link itself. It calls check for "/" and for every
// directory before it looks inside, for every link before it follows it,
// and for what path leads to, with last set. A directory is thus checked
// while it is held open, and nothing found inside it is trusted before that.
//
// When a name on the way does not exist, walkWay returns the directory that
// should hold it and the name, unless mayCreate is set: it then asks
// mayCreate about the holding directory's entry and creates the name as a
// directory only this account may use. Only the path itself may lead to a
// missing name; a link that does is refused.
//
// Every directory it holds is classified once, through the held handle
// (filesystemEnforced), and check gets the result with each entry.
//
// It returns the last directory it reached, open, and the resolved path of
// what path leads to, which is that directory unless path leads to another
// kind of file.
func walkWay(path string, check func(wayEntry) error, mayCreate func(wayEntry) error) (dir *os.File, resolved, missing string, err error) {
	root, err := openDirectoryAt(unix.AT_FDCWD, "/", "/")
	if err != nil {
		return nil, "", "", err
	}
	walk := &wayWalk{check: check, mayCreate: mayCreate}
	if err := walk.push(root); err != nil {
		return nil, "", "", err
	}
	defer walk.close()
	if missing, err = walk.walk(path, true); err != nil {
		return nil, "", "", err
	}
	top := walk.way[len(walk.way)-1]
	resolved = top.file.Name()
	if missing == "" {
		entry := top.entry()
		entry.last = true
		if walk.file != "" {
			entry.path, entry.info, entry.dir = walk.file, walk.fileInfo, nil
			resolved = walk.file
		}
		if err := check(entry); err != nil {
			return nil, "", "", err
		}
	}
	walk.way = walk.way[:len(walk.way)-1]
	return top.file, resolved, missing, nil
}

// wayEntry is an entry that walkWay passes to its check.
type wayEntry struct {
	path string
	info os.FileInfo
	// dir is the held directory, or nil for a link or another kind of file.
	dir *os.File
	// last is set for what the path leads to.
	last bool
	// enforced says whether this computer enforces the owners and modes of
	// the filesystem that holds the entry (ownershipEnforced): a directory's
	// own, or for a link or another kind of file, that of the folder that
	// holds it, whose server or program also decides what the link says.
	enforced bool
}

// wayWalk is the state of walkWay: the directories from "/" to the current
// one, all open.
type wayWalk struct {
	check     func(wayEntry) error
	mayCreate func(wayEntry) error
	way       []wayDirectory
	links     int
	// file and fileInfo name a final entry that is not a directory.
	file     string
	fileInfo os.FileInfo
}

type wayDirectory struct {
	file     *os.File
	info     os.FileInfo
	enforced bool
	checked  bool
}

// entry is the held directory as an entry on the way.
func (dir *wayDirectory) entry() wayEntry {
	return wayEntry{path: dir.file.Name(), info: dir.info, dir: dir.file, enforced: dir.enforced}
}

func (walk *wayWalk) push(dir *os.File) error {
	info, err := dir.Stat()
	var enforced bool
	if err == nil {
		enforced, err = filesystemEnforced(dir)
	}
	if err != nil {
		dir.Close()
		return err
	}
	walk.way = append(walk.way, wayDirectory{file: dir, info: info, enforced: enforced})
	return nil
}

func (walk *wayWalk) pop() {
	walk.way[len(walk.way)-1].file.Close()
	walk.way = walk.way[:len(walk.way)-1]
}

func (walk *wayWalk) close() {
	for len(walk.way) > 0 {
		walk.pop()
	}
}

func (walk *wayWalk) walk(relative string, outermost bool) (missing string, err error) {
	for _, name := range strings.Split(relative, "/") {
		if name == "" || name == "." {
			continue
		}
		if walk.file != "" {
			return "", fmt.Errorf("%s is not a directory", walk.file)
		}
		if name == ".." {
			if len(walk.way) > 1 {
				walk.pop()
			}
			continue
		}
		current := &walk.way[len(walk.way)-1]
		if !current.checked {
			if err := walk.check(current.entry()); err != nil {
				return "", err
			}
			current.checked = true
		}
		parent, next := int(current.file.Fd()), filepath.Join(current.file.Name(), name)
		dir, err := openDirectoryAt(parent, name, next)
		if errors.Is(err, fs.ErrNotExist) {
			if !outermost {
				return "", fmt.Errorf("a link on the way to %s leads to missing %s", relative, next)
			}
			if walk.mayCreate == nil {
				return name, nil
			}
			if err := walk.mayCreate(current.entry()); err != nil {
				return "", err
			}
			if err := unix.Mkdirat(parent, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				return "", &os.PathError{Op: "create", Path: next, Err: err}
			}
			dir, err = openDirectoryAt(parent, name, next)
		}
		runtime.KeepAlive(current.file)
		if err == nil {
			if err := walk.push(dir); err != nil {
				return "", err
			}
			continue
		}
		info, statErr := lstatAt(current.file, name, next)
		switch {
		case statErr != nil:
			return "", statErr
		case info.Mode()&os.ModeSymlink == 0 && info.IsDir():
			return "", err
		case info.Mode()&os.ModeSymlink == 0:
			walk.file, walk.fileInfo = next, info
			continue
		}
		if err := walk.check(wayEntry{path: next, info: info, enforced: current.enforced}); err != nil {
			return "", err
		}
		if walk.links++; walk.links > MaximumLinks {
			return "", fmt.Errorf("too many links on the way to %s", relative)
		}
		target, err := readlinkAt(current.file, name, next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			for len(walk.way) > 1 {
				walk.pop()
			}
		}
		if _, err := walk.walk(target, false); err != nil {
			return "", err
		}
	}
	return "", nil
}

// openDirectoryAt opens the directory name in dirfd without following a
// link, for looking up names in it; path names it in errors.
func openDirectoryAt(dirfd int, name, path string) (*os.File, error) {
	descriptor, err := unix.Openat(dirfd, name, searchOnly|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

// lstatAt returns the entry name of the held directory dir without following
// it. The os.FileInfo comes from path, so it must name the same entry.
func lstatAt(dir *os.File, name, path string) (os.FileInfo, error) {
	var held unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &held, unix.AT_SYMLINK_NOFOLLOW)
	runtime.KeepAlive(dir)
	if err != nil {
		return nil, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || uint64(stat.Dev) != uint64(held.Dev) || uint64(stat.Ino) != uint64(held.Ino) {
		return nil, fmt.Errorf("%s changed while it was checked", path)
	}
	return info, nil
}

// readlinkAt reads the link name in the held directory dir.
func readlinkAt(dir *os.File, name, path string) (string, error) {
	for size := 256; ; size *= 2 {
		buffer := make([]byte, size)
		length, err := unix.Readlinkat(int(dir.Fd()), name, buffer)
		runtime.KeepAlive(dir)
		if err != nil {
			return "", &os.PathError{Op: "readlink", Path: path, Err: err}
		}
		if length < size {
			return string(buffer[:length]), nil
		}
	}
}

// WalkProtected resolves path like walkWay and calls check for "/", for
// every directory before it looks inside, for every link before it follows
// it, and for what path leads to. When a name on the way does not exist,
// it returns the resolved directory that should hold it and the missing
// name; otherwise it returns the resolved path.
func WalkProtected(path string, check func(string, os.FileInfo) error) (resolved, missing string, err error) {
	dir, resolved, missing, err := walkWay(path, func(entry wayEntry) error {
		return check(entry.path, entry.info)
	}, nil)
	if err != nil {
		return "", "", err
	}
	dir.Close()
	return resolved, missing, nil
}

// OthersCanChange reports whether an account other than this one or root
// could change path, or rename or replace what it holds, and a shell command
// that removes that access, or "" when changing the mode does not help. It
// must belong to this account or to root. It must not let others write unless
// it is a sticky directory, where only an entry's owner may rename that
// entry. Group write is accepted only for the account's own private group,
// whose members are treated as the account itself. Access list entries that
// let another account write are refused as well.
func OthersCanChange(path string, info os.FileInfo) (bool, string, error) {
	return othersCanChange(path, info, false, true)
}

// othersCanChange is OthersCanChange; with rootGroups it also accepts group
// write for macOS groups whose members may act as root. allowSticky accepts a
// writable sticky directory when only its existing entries need protection.
func othersCanChange(path string, info os.FileInfo, rootGroups, allowSticky bool) (bool, string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, "", errors.New("owner is unavailable")
	}
	if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		return true, "", nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, "", nil
	}
	permissions := info.Mode().Perm()
	if (!allowSticky || info.Mode()&os.ModeSticky == 0) && (permissions&0o002 != 0 ||
		permissions&0o020 != 0 && !OwnPrivateGroup(stat.Gid) && !(rootGroups && rootEquivalentGroup(stat.Gid))) {
		return true, "chmod g-w,o-w " + shellQuote(path), nil
	}
	fix, err := accessListFix(path, info)
	return fix != "", fix, err
}

// RequireProtectedPath refuses path when another account could change it or
// anything on the way to it; see WalkProtected and OthersCanChange. On macOS
// it accepts group write for wheel and admin, like Homebrew's folders. A
// writable sticky directory is accepted only as an ancestor, not as path.
func RequireProtectedPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolved, missing, err := WalkProtected(absolute, protectedCheck(true))
	if err == nil && missing != "" {
		return fmt.Errorf("%s does not exist", path)
	}
	if err != nil {
		return err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return err
	}
	return protectedCheck(false)(resolved, info)
}

// rootEquivalentGroup reports whether gid is a macOS group whose members may
// act as root: wheel and admin, which own Homebrew's and the system's folders.
// Their members can become root with sudo, so write access for these groups
// is inside the administrator boundary that root already is, not another
// account's authority. InspectFolderWay relies on this too.
func rootEquivalentGroup(gid uint32) bool {
	return runtime.GOOS == "darwin" && (gid == 0 || gid == 80)
}

// protectedCheck is the WalkProtected check of RequireProtectedPath: it
// refuses a path that another account can change, with the command that
// fixes it when there is one.
func protectedCheck(allowSticky bool) func(string, os.FileInfo) error {
	return func(name string, info os.FileInfo) error {
		changeable, fix, err := othersCanChange(name, info, true, allowSticky)
		if err == nil && changeable {
			err = fmt.Errorf("another account can change %s", name)
			if fix != "" {
				err = fmt.Errorf("%w (%s fixes that)", err, fix)
			}
		}
		return err
	}
}

// OpenDirectory opens the directory at path for files that OwnGit keeps
// there, such as its log, and creates it and its missing parents when
// create is set. It walks the way with walkWay, so it follows no link that
// it did not check, and the check holds for the directory it returns, not
// for whatever path names later.
//
// Every folder and link on the way must be one that no other account can
// change (see RequireProtectedPath): a sticky folder that every account may
// write, such as /tmp, is accepted on the way, because others cannot rename
// what is in it. A name is created only in a folder that no other account
// can write, because in a sticky folder others can still take a name that
// does not exist yet. The directory itself must belong to this account;
// OwnGit makes what it keeps inside private. When root runs the command, a
// folder that another account owns belongs to that account, and so would
// OwnGit's files inside it; files that root created there would lock that
// account out, so the refusal is an *OtherAccountError that says to run the
// command as that account.
//
// Owners and modes mean this only where this computer enforces them, not on
// a filesystem whose server or program decides them (ownershipEnforced),
// such as a network share. There, OpenDirectory follows no link, since the
// server decides where it leads, and could lead into this account's own
// files; and when root runs the command it uses no folder there at all,
// since the server could show another account's folder as root's. An
// account other than root may keep its log on a share: whoever serves it
// could change it, but reaches nothing else through it.
func OpenDirectory(path string, create bool) (*os.File, error) {
	return openDirectory(path, create, false)
}

// openStateDirectory is CreateDirectory, or OpenStateDirectory when create
// is not set.
func openStateDirectory(dir string, create bool) (*os.File, error) {
	return openDirectory(dir, create, true)
}

// openDirectory is OpenDirectory; with local set, every folder on the way
// must be on a filesystem that this computer enforces as well, for every
// account: a share's server could otherwise rename a folder on the way
// after the check and put another state at the path.
func openDirectory(path string, create, local bool) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	protected := protectedCheck(true)
	check := func(entry wayEntry) error {
		name, info, last := entry.path, entry.info, entry.last
		stat, ok := info.Sys().(*syscall.Stat_t)
		switch {
		case !entry.enforced && os.Geteuid() == 0:
			return fmt.Errorf("%s is %s; OwnGit run as root uses nothing there, choose a folder on a local disk", name, notKnownLocal)
		case !entry.enforced && local:
			return notLocalState(name)
		case !entry.enforced && info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is a link %s, whose server decides where it leads, so OwnGit does not follow it; use the real path that the link leads to", name, notKnownLocal)
		case !ok:
			return fmt.Errorf("the owner of %s is unavailable", name)
		case last && !info.IsDir():
			return fmt.Errorf("%s is not a directory", name)
		case last && int(stat.Uid) == os.Geteuid():
			return nil
		case os.Geteuid() == 0 && stat.Uid != 0:
			return &OtherAccountError{Path: name, Account: accountName(stat.Uid), UID: stat.Uid}
		case last && stat.Uid == 0:
			return fmt.Errorf("%s belongs to root, not to this account; choose a folder of this account", name)
		case last:
			return fmt.Errorf("%s belongs to another account; run the command as its owner", name)
		}
		return notProtected(absolute, protected(name, info))
	}
	var mayCreate func(wayEntry) error
	if create {
		mayCreate = func(entry wayEntry) error {
			return notProtected(absolute, requireNoOtherWriter(entry.path, entry.info))
		}
	}
	dir, _, missing, err := walkWay(absolute, check, mayCreate)
	if err != nil {
		return nil, err
	}
	if missing != "" {
		dir.Close()
		return nil, &os.PathError{Op: "open", Path: absolute, Err: fs.ErrNotExist}
	}
	return dir, nil
}

// notKnownLocal describes a filesystem that ownershipEnforced does not
// know to be local.
const notKnownLocal = "on a filesystem that OwnGit does not know to be local, such as a network share, a FUSE filesystem or a virtual machine's shared folder"

// notLocalState refuses the folder name for the state.
func notLocalState(name string) error {
	return fmt.Errorf("%s is %s; %s", name, notKnownLocal, stateOnLocalDisk)
}

// holdWay has nothing to hold on Unix: every folder on the way to the state
// is on a local filesystem (openStateDirectory) and one that no other
// account can change (OpenDirectory), so the path that SQLite opens names
// the held directory while this account does not change it.
func holdWay(*os.File) (func(), error) { return func() {}, nil }

// requireNoOtherWriter refuses a folder that another account could create
// names in, sticky or not.
func requireNoOtherWriter(name string, info os.FileInfo) error {
	changeable, _, err := othersCanChange(name, info, true, false)
	if err == nil && changeable {
		err = fmt.Errorf("other accounts can create names in %s, where the missing folders would be created", name)
	}
	return err
}

// notProtected explains a refusal of a folder on the way to path.
func notProtected(path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s is not protected: %w; choose a folder that other accounts cannot change", path, err)
}

// RequireStateParent refuses a state destination that does not exist yet,
// such as a restore target, unless OwnGit could create it: its parent must
// pass OpenStateDirectory, and no other account may be able to create names in
// the parent. The parent stays as the check found it, since no other account
// can change the way to it.
func RequireStateParent(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir, err := openStateDirectory(filepath.Dir(absolute), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	return notProtected(absolute, requireNoOtherWriter(dir.Name(), info))
}

// accountName is the name of the account uid, or "" when it has no name.
func accountName(uid uint32) string {
	if account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
		return account.Username
	}
	return ""
}

// filesystemEnforced is ownershipEnforced; tests replace it.
var filesystemEnforced = ownershipEnforced

// InspectFolderWay walks the absolute path like OpenDirectory and describes
// its way. A way that cannot be walked does not count as OnlyRoot.
func InspectFolderWay(path string) FolderWay {
	way := FolderWay{OnlyRoot: true}
	dir, _, _, err := walkWay(filepath.Clean(path), func(entry wayEntry) error {
		if !onlyRootCanChangeEntry(entry.path, entry.info) {
			way.OnlyRoot = false
		}
		if !entry.enforced {
			way.Shared = true
		}
		return nil
	}, nil)
	if err != nil {
		way.OnlyRoot = false
	} else {
		dir.Close()
	}
	way.OnlyRoot = way.OnlyRoot && !way.Shared
	return way
}

// onlyRootCanChangeEntry reports whether only root can change the entry:
// it belongs to root, and unless it is a link, no other account may write
// to it.
func onlyRootCanChangeEntry(name string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	permissions := info.Mode().Perm()
	if permissions&0o002 != 0 || permissions&0o020 != 0 && !rootEquivalentGroup(stat.Gid) {
		return false
	}
	fix, err := accessListFix(name, info)
	return err == nil && fix == ""
}

// OwnPrivateGroup reports whether gid is the private group of a runner that
// is not root: the account's primary and effective group, named like the
// account. Many Linux systems give each user such a group and a umask of 002,
// so group write access there is the user's own. A failed lookup counts as a
// shared group.
func OwnPrivateGroup(gid uint32) bool {
	if os.Geteuid() == 0 || int(gid) != os.Getegid() {
		return false
	}
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || account.Gid != strconv.FormatUint(uint64(gid), 10) {
		return false
	}
	group, err := user.LookupGroupId(account.Gid)
	return err == nil && group.Name == account.Username
}
