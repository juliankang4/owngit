package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLstatIdentityIsFixedAtTheCheck replaces an inspected path and then
// compares the recorded identity with the replacement first.
//
// The order matters on Windows. A path-based os.Lstat there records only the
// path, and os.SameFile resolves that path at its first comparison, so it would
// describe the replacement and the check would pass. LstatIdentity must keep
// describing the object it inspected, which later comparisons also confirm by
// matching the moved original.
func TestLstatIdentityIsFixedAtTheCheck(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "entry")
			moved := filepath.Join(dir, "moved")
			create := func(name string) {
				t.Helper()
				var err error
				if kind == "file" {
					err = os.WriteFile(name, []byte("same size\n"), 0o600)
				} else {
					err = os.Mkdir(name, 0o700)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			create(path)
			inspected, err := LstatIdentity(path)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "file" && !inspected.Mode().IsRegular() || kind == "directory" && !inspected.IsDir() {
				t.Fatalf("inspected mode is %v", inspected.Mode())
			}
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			create(path)

			replacement, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(inspected, replacement) {
				t.Fatal("the replacement was compared as the inspected object")
			}
			original, err := os.Lstat(moved)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(inspected, original) {
				t.Fatal("the recorded identity does not describe the inspected object")
			}
		})
	}

	t.Run("a final link is not followed", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("target\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symbolic links are unavailable: %v", err)
		}
		info, err := LstatIdentity(link)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link mode is %v, want a symbolic link", info.Mode())
		}
		targetInfo, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(info, targetInfo) {
			t.Fatal("the link was resolved to its target")
		}
	})

	t.Run("a missing path reports not exist", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing")
		_, err := LstatIdentity(path)
		if !errors.Is(err, os.ErrNotExist) || !os.IsNotExist(err) {
			t.Fatalf("LstatIdentity returned %v, want not exist", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("error %q does not name %s", err, path)
		}
	})
}
