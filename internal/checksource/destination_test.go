package checksource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The destination and everything materialized inside it must be unreachable by
// other users. This asserts the portable consequence, which is the access
// actually granted. The mechanism differs by platform (Unix permission bits,
// Windows ACL inheritance from the protected root), so destinationIsPrivate is
// defined per platform and the platform test files assert the mechanism.
func TestMaterializeCreatesAPrivateDestination(t *testing.T) {
	source := newFakeSource(t, map[string]string{"nested/secret.txt": "private"}, nil)
	destination := destinationIn(t)
	if _, err := Materialize(context.Background(), source, destination, Options{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("the destination is not a directory")
	}
	for _, path := range []string{
		destination,
		filepath.Join(destination, "nested"),
		filepath.Join(destination, "nested", "secret.txt"),
	} {
		if !destinationIsPrivate(t, path) {
			t.Fatalf("%s is reachable by other users", path)
		}
	}
}

// Protection is fail-closed: when owner-only access cannot be established the
// export stops and never writes blob content into an unprotected directory.
func TestMaterializeStopsWhenDestinationProtectionFails(t *testing.T) {
	original := protectDestinationHook
	t.Cleanup(func() { protectDestinationHook = original })
	failure := errors.New("synthetic private-access failure")
	protectDestinationHook = func(*os.Root, string) error { return failure }

	source := newFakeSource(t, map[string]string{"secret.txt": "private"}, nil)
	destination := destinationIn(t)
	result, err := Materialize(context.Background(), source, destination, Options{})
	if result != nil || !errors.Is(err, failure) {
		t.Fatalf("result=%+v err=%v, want the protection failure", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(destination, "secret.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("content was written into an unprotected destination: %v", statErr)
	}
}

// Anything present inside the newly protected directory was planted before
// protection took effect, so the export must fail rather than continue.
func TestMaterializeFailsWhenTheNewDestinationIsNotEmpty(t *testing.T) {
	original := protectDestinationHook
	t.Cleanup(func() { protectDestinationHook = original })
	protectDestinationHook = func(root *os.Root, destination string) error {
		if err := original(root, destination); err != nil {
			return err
		}
		// Simulate a race in the creation window.
		return os.WriteFile(filepath.Join(destination, "planted.txt"), []byte("planted"), 0o600)
	}

	source := newFakeSource(t, map[string]string{"a.txt": "a"}, nil)
	result, err := Materialize(context.Background(), source, destinationIn(t), Options{})
	if result != nil || !errors.Is(err, ErrPathConflict) {
		t.Fatalf("result=%+v err=%v, want a path conflict", result, err)
	}
}

// An existing destination must be refused before any protection change, so a
// directory this call did not create is never re-permissioned.
//
// The Unix mode carries no ACL information on Windows, so
// TestMaterializeLeavesAnExistingDestinationSecurityDescriptorUnchanged in
// destination_windows_test.go snapshots and compares the actual descriptor
// there.
func TestMaterializeDoesNotProtectADestinationItDidNotCreate(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "existing")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	source := newFakeSource(t, map[string]string{"a.txt": "a"}, nil)
	if _, err := Materialize(context.Background(), source, destination, Options{}); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("err=%v, want existing destination", err)
	}
	after, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() {
		t.Fatalf("the existing destination was re-permissioned from %v to %v", before.Mode(), after.Mode())
	}
}
