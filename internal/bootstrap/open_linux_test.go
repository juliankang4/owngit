package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Open starts xdg-open from PATH only when no other account can replace
// it: the file, every folder above it and every link on the way.
func TestOpenRunsOnlyAProtectedOpener(t *testing.T) {
	root := t.TempDir()
	mark := filepath.Join(root, "opened")
	folder := func(name string, mode os.FileMode) string {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	opener := func(dir string, mode os.FileMode) string {
		path := filepath.Join(dir, "xdg-open")
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho \"$1\" >> "+mark+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	own := folder("own", 0o755)
	opener(own, 0o755)
	shared := folder("shared", 0o777)
	opener(shared, 0o755)
	writable := folder("writable", 0o755)
	opener(writable, 0o757)
	linked := folder("linked", 0o755)
	if err := os.Symlink(filepath.Join(shared, "xdg-open"), filepath.Join(linked, "xdg-open")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, dir string
		safe      bool
	}{
		{"an opener in a folder of this account", own, true},
		{"a folder every account can write", shared, false},
		{"a file every account can write", writable, false},
		{"a link into a folder every account can write", linked, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			os.Remove(mark)
			t.Setenv("PATH", test.dir)
			err := Open("http://127.0.0.1:7654")
			if !test.safe {
				if err == nil || !strings.Contains(err.Error(), "another account can change") {
					t.Fatalf("accepted or unclear: %v", err)
				}
				time.Sleep(200 * time.Millisecond)
				if _, err := os.Stat(mark); !os.IsNotExist(err) {
					t.Fatal("the unsafe opener ran")
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				opened, _ := os.ReadFile(mark)
				if string(opened) == "http://127.0.0.1:7654\n" {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("the opener received %q", opened)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

// Open reports an xdg-open that ends with a failure, as when no browser
// takes the address, and takes one that keeps running as started.
func TestOpenReportsAFailedOpener(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "xdg-open")
	previous := openerWait
	t.Cleanup(func() { openerWait = previous })
	openerWait = 500 * time.Millisecond
	t.Setenv("PATH", dir)

	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Open("http://127.0.0.1:7654"); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("an opener that failed: %v", err)
	}

	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := Open("http://127.0.0.1:7654"); err != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("an opener still running: %v after %s", err, time.Since(start))
	}
}
