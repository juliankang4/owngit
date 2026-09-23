//go:build !windows

package checksource

// The build tag matches the non-Windows split used elsewhere in this project
// (for example internal/state/private_unix.go), which targets macOS and Linux.
// The umask tests change process-wide state, so they must not be made parallel.

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// destinationIsPrivate reports whether the path denies group and other access.
func destinationIsPrivate(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()&0o077 == 0
}

// A permissive umask must not leave the destination group- or world-readable.
// The creating mkdir mode is subject to umask, so the protection call after it
// is what makes the result deterministic.
func TestMaterializeDestinationIgnoresAPermissiveUmask(t *testing.T) {
	previous := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previous) })

	source := newFakeSource(t, map[string]string{"nested/deep/file.txt": "private"}, nil)
	destination := destinationIn(t)
	if _, err := Materialize(context.Background(), source, destination, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		destination,
		filepath.Join(destination, "nested"),
		filepath.Join(destination, "nested", "deep"),
		filepath.Join(destination, "nested", "deep", "file.txt"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s is %v under a permissive umask, want owner-only", path, info.Mode())
		}
	}
}

// The destination must already be private when the first file is created, not
// only after the export finishes.
func TestMaterializeDestinationIsPrivateBeforeAnyContentIsWritten(t *testing.T) {
	previous := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previous) })

	source := newFakeSource(t, nil, nil)
	source.addBlob(t, "first.txt", ModeRegular, []byte("first"))
	destination := destinationIn(t)
	var observed os.FileMode
	source.beforeRead = func(string) error {
		info, err := os.Stat(destination)
		if err != nil {
			return err
		}
		observed = info.Mode()
		return nil
	}
	if _, err := Materialize(context.Background(), source, destination, Options{}); err != nil {
		t.Fatal(err)
	}
	if observed == 0 {
		t.Fatal("the destination did not exist before the first blob read")
	}
	if observed.Perm()&0o077 != 0 {
		t.Fatalf("the destination was %v before content was written, want owner-only", observed)
	}
}
