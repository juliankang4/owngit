//go:build windows

package publishdir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const maxRetryDelay = 100 * time.Millisecond

// Rename moves oldPath to newPath with MoveFileEx and no flags, so an existing
// destination of any kind fails with ERROR_ALREADY_EXISTS and is never
// replaced.
//
// A handle that another process holds on any file inside the directory makes
// the move fail with ERROR_ACCESS_DENIED, and a handle on the directory itself
// with ERROR_SHARING_VIOLATION. Rename retries only those two errors, with a
// growing delay, until RetryBound has passed or ctx ends. Any other error
// returns at once. The returned error is the last *os.LinkError, as os.Rename
// would report it.
func Rename(ctx context.Context, oldPath, newPath string) error {
	from, err := windowsPath(oldPath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
	to, err := windowsPath(newPath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
	deadline := time.Now().Add(RetryBound)
	delay := time.Millisecond
	for {
		err := windows.MoveFileEx(from, to, 0)
		if err == nil {
			return nil
		}
		linkErr := &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return linkErr
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return linkErr
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return linkErr
		case <-timer.C:
		}
		delay = min(2*delay, maxRetryDelay)
	}
}

// windowsPath converts path for MoveFileEx. Like os.Rename, it keeps paths that
// are already extended (\\?\ or \??\) or device paths (\\.\) as given, and
// otherwise uses the extended-length form only when the absolute path is too
// long for the ordinary form. Unlike os.Rename it adds that form even when
// the process could use long paths without it, which is equally valid.
func windowsPath(path string) (*uint16, error) {
	separator := func(c byte) bool { return c == '\\' || c == '/' }
	if strings.HasPrefix(path, `\??\`) || len(path) >= 4 && separator(path[0]) && separator(path[1]) && (path[2] == '?' || path[2] == '.') && separator(path[3]) {
		return windows.UTF16PtrFromString(path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if len(absolute) >= 248 {
		if strings.HasPrefix(absolute, `\\`) {
			path = `\\?\UNC\` + absolute[2:]
		} else {
			path = `\\?\` + absolute
		}
	}
	return windows.UTF16PtrFromString(path)
}
