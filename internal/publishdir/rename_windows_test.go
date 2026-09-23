//go:build windows

package publishdir

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const holdEnvironment = "OWNGIT_PUBLISHDIR_TEST_HOLD"

// TestMain lets the test binary act as the other process that holds a file.
func TestMain(m *testing.M) {
	if path := os.Getenv(holdEnvironment); path != "" {
		os.Exit(holdFile(path))
	}
	os.Exit(m.Run())
}

// holdFile opens path with every share mode, as a scanner would, reports
// readiness, and keeps the handle until stdin closes.
func holdFile(path string) int {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	defer windows.CloseHandle(handle)
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// holdInAnotherProcess holds path from a child process. The returned release
// closes the handle by ending that process; cleanup also releases it.
func holdInAnotherProcess(t *testing.T, path string) func() {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^$")
	command.Env = append(os.Environ(), holdEnvironment+"="+path)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = stdin.Close()
			if err := command.Wait(); err != nil {
				t.Errorf("holder: %v", err)
			}
		})
	}
	t.Cleanup(release)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if strings.TrimSpace(line) != "ready" {
		release()
		t.Fatalf("holder did not become ready: %q %v", line, err)
	}
	return release
}

func TestRenameRetriesWhileAnotherProcessBrieflyHoldsAFile(t *testing.T) {
	root := t.TempDir()
	staged := stagedDirectory(t, root, "staged")
	published := filepath.Join(root, "published")
	release := holdInAnotherProcess(t, filepath.Join(staged, "hooks", "update"))
	if err := os.Rename(staged, published); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("control: os.Rename while a file is held err=%v, want access denied", err)
	}
	timer := time.AfterFunc(300*time.Millisecond, release)
	defer timer.Stop()
	started := time.Now()
	err := Rename(context.Background(), staged, published)
	elapsed := time.Since(started)
	t.Logf("publication succeeded after %v", elapsed)
	if err != nil {
		t.Fatalf("publication after the holder released: %v", err)
	}
	if elapsed < 250*time.Millisecond || elapsed >= RetryBound {
		t.Fatalf("publication took %v, want the retry to span the 300 ms hold", elapsed)
	}
	if content, err := os.ReadFile(filepath.Join(published, "hooks", "update")); err != nil || string(content) != "staged\n" {
		t.Fatalf("published content=%q err=%v", content, err)
	}
}

func TestRenameReturnsTheOriginalDenialAfterItsBound(t *testing.T) {
	root := t.TempDir()
	staged := stagedDirectory(t, root, "staged")
	published := filepath.Join(root, "published")
	holdInAnotherProcess(t, filepath.Join(staged, "hooks", "update"))
	started := time.Now()
	err := Rename(context.Background(), staged, published)
	elapsed := time.Since(started)
	t.Logf("gave up after %v: %v", elapsed, err)
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || linkErr.Op != "rename" || linkErr.Old != staged || linkErr.New != published {
		t.Fatalf("error is not the rename link error: %#v", err)
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("err=%v, want access denied", err)
	}
	if elapsed < RetryBound || elapsed > RetryBound+time.Second {
		t.Fatalf("gave up after %v, want about %v", elapsed, RetryBound)
	}
	if _, err := os.Lstat(staged); err != nil {
		t.Fatalf("staged directory after refused publication: %v", err)
	}
	if _, err := os.Lstat(published); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination after refused publication: %v", err)
	}
}

func TestRenameStopsRetryingWhenTheContextEnds(t *testing.T) {
	root := t.TempDir()
	staged := stagedDirectory(t, root, "staged")
	holdInAnotherProcess(t, filepath.Join(staged, "hooks", "update"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := Rename(ctx, staged, filepath.Join(root, "published"))
	elapsed := time.Since(started)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || elapsed > time.Second {
		t.Fatalf("cancelled publication err=%v after %v", err, elapsed)
	}
}

func TestRenameNeverReplacesAnExistingDestination(t *testing.T) {
	for _, kind := range []string{"non-empty directory", "empty directory", "file"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			staged := stagedDirectory(t, root, "staged")
			destination := filepath.Join(root, "destination")
			sentinel := destination
			switch kind {
			case "non-empty directory":
				stagedDirectory(t, root, "destination")
				sentinel = filepath.Join(destination, "hooks", "update")
			case "empty directory":
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				sentinel = ""
			case "file":
				if err := os.WriteFile(destination, []byte("destination\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now()
			err := Rename(context.Background(), staged, destination)
			elapsed := time.Since(started)
			if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) || !os.IsExist(err) {
				t.Fatalf("publication onto an existing %s err=%v", kind, err)
			}
			if elapsed > 200*time.Millisecond {
				t.Fatalf("refusal took %v, want no retry", elapsed)
			}
			if sentinel != "" {
				if content, err := os.ReadFile(sentinel); err != nil || string(content) != "destination\n" {
					t.Fatalf("existing destination changed: content=%q err=%v", content, err)
				}
			}
			if content, err := os.ReadFile(filepath.Join(staged, "hooks", "update")); err != nil || string(content) != "staged\n" {
				t.Fatalf("staged directory changed: content=%q err=%v", content, err)
			}
		})
	}
}

func TestRenameSupportsExtendedLengthPaths(t *testing.T) {
	root := t.TempDir()
	parent := root
	for len(parent) < 280 {
		parent = filepath.Join(parent, strings.Repeat("p", 40))
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := stagedDirectory(t, parent, ".owngit-create-0123456789abcdef")
	published := filepath.Join(parent, "published.git")
	if err := Rename(context.Background(), staged, published); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(published, "hooks", "update")); err != nil {
		t.Fatal(err)
	}
	// A path that is already in the extended form is used as given.
	extended := filepath.Join(parent, "extended.git")
	if err := Rename(context.Background(), `\\?\`+published, `\\?\`+extended); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(extended, "hooks", "update")); err != nil {
		t.Fatal(err)
	}
}
