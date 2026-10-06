//go:build darwin

package state

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func inheritingFolder(t *testing.T) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("chmod", "+a", "nobody allow read,list,file_inherit,directory_inherit", path).CombinedOutput(); err != nil {
		t.Fatalf("add inherited access entry: %v: %s", err, output)
	}
	dir, err := OpenDirectory(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	return dir
}

// watchName opens the name over and over until stop is closed, and counts the
// descriptors that carried an access list entry when they were opened.
func watchName(dir *os.File, name string, stop <-chan struct{}, exposed *int) *sync.WaitGroup {
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			descriptor, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY, 0)
			if err != nil {
				continue
			}
			file := os.NewFile(uintptr(descriptor), filepath.Join(dir.Name(), name))
			if permits, _ := accessListPermits(file); permits {
				*exposed++
			}
			file.Close()
		}
	}()
	return &done
}

// No descriptor that another account could use is obtainable on a log name:
// a new file and a replacement are published without the inherited entry.
func TestLogFilesArePublishedWithoutTheInheritedEntry(t *testing.T) {
	dir := inheritingFolder(t)
	stop, exposed := make(chan struct{}), 0
	watcher := watchName(dir, "owngit.log", stop, &exposed)
	for range 150 {
		file, err := OpenLogFile(dir, "owngit.log")
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		// A private file that OwnGit did not publish, such as an earlier version's.
		if err := unix.Unlinkat(int(dir.Fd()), "owngit.log", 0); err != nil {
			t.Fatal(err)
		}
		legacy, err := OpenOwnFile(dir, "legacy", os.O_CREATE|os.O_WRONLY)
		if err != nil {
			t.Fatal(err)
		}
		if err := ProtectPrivateHandle(legacy, false); err != nil {
			t.Fatal(err)
		}
		legacy.Close()
		if err := RenameOwnFile(dir, "legacy", "owngit.log"); err != nil {
			t.Fatal(err)
		}
		if err := replaceWithPrivateCopy(dir, "owngit.log"); err != nil {
			t.Fatal(err)
		}
		if err := unix.Unlinkat(int(dir.Fd()), "owngit.log", 0); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	watcher.Wait()
	if exposed != 0 {
		t.Errorf("%d descriptors with an access list entry were opened on the log name", exposed)
	}
}

// A private log that this version did not publish is replaced once, and a
// descriptor opened earlier stops seeing later lines; a published log stays.
func TestPrivateUnpublishedLogIsReplacedOnce(t *testing.T) {
	dir := inheritingFolder(t)
	path := filepath.Join(dir.Name(), "owngit.log")
	if err := os.WriteFile(path, []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := replaceWithPrivateCopy(dir, "owngit.log"); err != nil {
		t.Fatal(err)
	}
	file, err := OpenLogFile(dir, "owngit.log")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	file.WriteString("later\n")
	if rest, _ := io.ReadAll(old); len(rest) != 0 {
		t.Errorf("the descriptor from before the replacement reads %q", rest)
	}
	before, _ := file.Stat()
	if err := replaceWithPrivateCopy(dir, "owngit.log"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("a published log was replaced again: %v", err)
	}
	if content, _ := os.ReadFile(path); string(content) != "earlier\nlater\n" {
		t.Errorf("log content: %q", content)
	}

	// A published log that is opened to others again is replaced again.
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceWithPrivateCopy(dir, "owngit.log"); err != nil {
		t.Fatal(err)
	}
	held, _ := reader.Stat()
	if current, err := os.Stat(path); err != nil || os.SameFile(held, current) || current.Mode().Perm() != 0o600 {
		t.Errorf("the opened-up log was not replaced by a private file: %v", err)
	}
	if rest, _ := io.ReadAll(reader); len(rest) != 0 {
		t.Errorf("a descriptor taken after the mode change reads %q", rest)
	}
}
