//go:build windows

package importsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// A junction is a reparse point that Go reports without ModeSymlink. Every
// HEAD-related guard must refuse it through the platform attribute check.
// Native execution on Windows is required; cross-compilation is not evidence.
func makeJunction(t *testing.T, junction, target string) {
	t.Helper()
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Skipf("junction creation unavailable: %v: %s", err, output)
	}
	name, err := windows.UTF16PtrFromString(junction)
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatalf("junction attributes=%#x err=%v", attributes, err)
	}
}

func TestWindowsJunctionRepositoryRootIsRefusedForHEADLock(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	junction := filepath.Join(f.root, "root-junction")
	makeJunction(t, junction, path)
	info, err := os.Lstat(junction)
	if err != nil {
		t.Fatal(err)
	}
	if directDirectory(junction, info) {
		t.Fatal("junction root passed the direct directory guard")
	}
	expected := headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: oid}
	if _, err := f.service.acquireHEADLock(context.Background(), &runState{limits: DefaultLimits()}, junction, expected); err == nil || problemCode(err) != CodeRepositoryMissing {
		t.Fatalf("junction root accepted: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(path, "HEAD.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("lock created through junction root: %v", statErr)
	}
}

func TestWindowsJunctionHEADLockPathIsPreserved(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	target := filepath.Join(f.root, "lock-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(path, "HEAD.lock")
	makeJunction(t, lockPath, target)
	expected := headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: oid}
	if _, err := f.service.acquireHEADLock(context.Background(), &runState{limits: DefaultLimits()}, path, expected); err == nil || problemCode(err) != CodeDestinationChanged {
		t.Fatalf("junction HEAD.lock accepted: %v", err)
	}
	if err := removeOwnedHEADLock(lockPath, nil); err == nil {
		t.Fatal("cleanup removed a junction it never created")
	}
	name, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatalf("junction lock changed: attributes=%#x err=%v", attributes, err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("junction target removed: %v", err)
	}
}
