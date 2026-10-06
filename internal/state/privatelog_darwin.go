//go:build darwin

package state

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// publishedMark is the extended attribute that marks an inode that OwnGit
// made private before publishing it.
const publishedMark = "com.owngit.log"

// lockFolder takes an exclusive lock that lasts until the folder is closed.
// When it cannot, the second result says why.
func lockFolder(dir *os.File, path string) (bool, string) {
	err := unix.Flock(int(dir.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case err == nil:
		return true, ""
	case errors.Is(err, unix.EWOULDBLOCK):
		return false, fmt.Sprintf("another process holds the lock on %s (another OwnGit, or another log in a shared folder), so the log files there are left as they are", path)
	default:
		return false, fmt.Sprintf("%s cannot be locked (%v), so the log files there are left as they are", path, err)
	}
}

// accessListPermits reports whether the open file has an access list entry
// that allows anything.
func accessListPermits(file *os.File) (bool, error) {
	filesec, err := extendedSecurity(file.Name(), file, 0)
	if err != nil {
		return false, err
	}
	return permitEntry(filesec, ^uint32(0))
}

func clearFolderAccessList(dir *os.File) error { return clearAccessList(dir) }

// stagingFolder opens the private staging folder of the log folder dir,
// made private before it is used.
func stagingFolder(dir *os.File) (*os.File, error) {
	path := filepath.Join(dir.Name(), LogStagingName)
	if err := MkdirPrivate(path); err != nil && !errors.Is(err, ErrPrivateDirectoryExists) {
		return nil, err
	}
	staging, err := openDirectoryAt(int(dir.Fd()), LogStagingName, path)
	if err != nil {
		return nil, err
	}
	owned, err := OwnedByCurrentUser(staging)
	if err == nil && !owned {
		err = fmt.Errorf("%s belongs to another account", path)
	}
	if err == nil {
		err = ProtectPrivateHandle(staging, true)
	}
	if err != nil {
		staging.Close()
		return nil, err
	}
	return staging, nil
}

// stagedFile makes the empty private file name in the staging folder.
func stagedFile(staging *os.File, name string, flag int) (*os.File, error) {
	file, err := OpenOwnFile(staging, name, flag|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	if err = file.Truncate(0); err == nil {
		err = ProtectPrivateHandle(file, false)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// markPublished is best effort: on a filesystem without extended attributes
// the file is replaced again at every start.
func markPublished(file *os.File) { _ = unix.Fsetxattr(int(file.Fd()), publishedMark, []byte{1}, 0) }

func currentlyPrivate(file *os.File) (bool, error) {
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	permits, err := accessListPermits(file)
	return !permits && info.Mode().Perm()&0o077 == 0, err
}

func isPublished(file *os.File) bool {
	size, err := unix.Fgetxattr(int(file.Fd()), publishedMark, nil)
	return err == nil && size > 0
}

func publish(staging *os.File, from string, dir *os.File, to string, flags uint32) error {
	err := unix.RenameatxNp(int(staging.Fd()), from, int(dir.Fd()), to, flags)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: filepath.Join(staging.Name(), from), New: filepath.Join(dir.Name(), to), Err: err}
	}
	return nil
}

// createLogFile creates the log file name through the staging folder and
// publishes it only if the name is still free.
func createLogFile(dir *os.File, name string) (*os.File, error) {
	staging, err := stagingFolder(dir)
	if err != nil {
		return nil, err
	}
	defer staging.Close()
	temporary := name + ".new"
	file, err := stagedFile(staging, temporary, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return nil, err
	}
	markPublished(file)
	err = publish(staging, temporary, dir, name, unix.RENAME_EXCL)
	if err == nil {
		return file, nil
	}
	file.Close()
	_ = removeOwnFile(staging, temporary)
	if errors.Is(err, unix.EEXIST) {
		return OpenOwnFile(dir, name, os.O_WRONLY|os.O_APPEND)
	}
	return nil, err
}

// replaceWithPrivateCopy gives the file name in the held folder dir a new
// private file with the same content, published through the staging folder,
// and then empties the retired file, unless OwnGit published the file and it
// is still private (no access list entry that allows anything, no mode bits
// for the group or others), which an owner command or a restore can undo. A handle that another account opened while the old file was
// readable then reaches its end instead of reading later lines. A missing
// file is left alone. The caller holds the folder's lock, so no other OwnGit
// process appends to the old file.
func replaceWithPrivateCopy(dir *os.File, name string) error {
	old, err := OpenOwnFile(dir, name, os.O_RDWR)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer old.Close()
	if private, err := currentlyPrivate(old); err != nil || (private && isPublished(old)) {
		return err
	}
	staging, err := stagingFolder(dir)
	if err != nil {
		return err
	}
	defer staging.Close()
	temporary := name + ".new"
	fresh, err := stagedFile(staging, temporary, os.O_WRONLY)
	if err != nil {
		return err
	}
	_, err = io.Copy(fresh, old)
	if err == nil {
		markPublished(fresh)
		err = fresh.Sync()
	}
	if err = errors.Join(err, fresh.Close()); err == nil {
		err = publish(staging, temporary, dir, name, 0)
	}
	if err != nil {
		_ = removeOwnFile(staging, temporary)
		return err
	}
	return old.Truncate(0)
}

func sharedFolderNote(path string) string {
	return fmt.Sprintf("the access list of %s lets other accounts read files made there; OwnGit creates its log files privately and leaves the folder alone, but other files there stay readable; to remove the list run: chmod -N %s", path, shellQuote(operandPath(path)))
}
