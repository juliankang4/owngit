//go:build windows

package state

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsNetworkPathRecognizesUNCAndFinalUNCForms(t *testing.T) {
	for _, path := range []string{`\\server\share\state`, `//server/share/state`, `\\?\UNC\server\share\state`} {
		if !windowsNetworkPath(path) {
			t.Errorf("windowsNetworkPath(%q)=false, want true", path)
		}
	}
	if windowsNetworkPath(`C:\local\state`) {
		t.Fatal("local drive path was classified as a network path")
	}
}

func TestWindowsPrivateOwnerAcceptanceIsLimitedToProcessSIDs(t *testing.T) {
	user, defaultOwner, err := processIdentity()
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := windows.StringToSid("S-1-0-0")
	if err != nil {
		t.Fatal(err)
	}
	if !ownerMatchesProcess(user, user, defaultOwner) || !ownerMatchesProcess(defaultOwner, user, defaultOwner) {
		t.Fatal("current user or token owner was rejected")
	}
	if foreign.Equals(user) || foreign.Equals(defaultOwner) {
		t.Fatal("foreign-owner fixture unexpectedly belongs to the process token")
	}
	if ownerMatchesProcess(foreign, user, defaultOwner) {
		t.Fatal("an owner outside the process token was accepted")
	}
}

func TestWindowsFullFileAccessAcceptsCanonicalMasksOnly(t *testing.T) {
	if !hasFullFileAccess(fileAllAccess) || !hasFullFileAccess(windows.GENERIC_ALL) {
		t.Fatal("canonical full-access mask was rejected")
	}
	if hasFullFileAccess(fileAllAccess &^ windows.WRITE_DAC) {
		t.Fatal("partial file access was accepted as full control")
	}
}

func TestWindowsCreatePrivateFileProtectsTheHeldObject(t *testing.T) {
	directory := t.TempDir()
	ordinaryPath := filepath.Join(directory, "ordinary")
	ordinaryName, err := windows.UTF16PtrFromString(ordinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryHandle, err := windows.CreateFile(ordinaryName, windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := os.NewFile(uintptr(ordinaryHandle), ordinaryPath)
	ordinaryMoved := ordinaryPath + ".moved"
	if err := os.Rename(ordinaryPath, ordinaryMoved); err != nil {
		_ = ordinary.Close()
		t.Fatalf("replacement positive control failed: %v", err)
	}
	if err := ordinary.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(directory, "private")
	file, err := CreatePrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := processIdentity()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := validateOwnerOnlyHandle(windows.Handle(file.Fd()), user, false); err != nil {
		_ = file.Close()
		t.Fatalf("creation descriptor: %v", err)
	}
	if err := ProtectPrivateHandle(file, false); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := validateOwnerOnlyHandle(windows.Handle(file.Fd()), user, false); err != nil {
		_ = file.Close()
		t.Fatalf("protected descriptor: %v", err)
	}
	if err := os.Rename(path, path+".moved"); err == nil {
		_ = file.Close()
		t.Fatal("private handle allowed path replacement")
	}
	if _, err := file.WriteString("private\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateFile(path); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := CreatePrivateFile(path); !os.IsExist(err) {
		if duplicate != nil {
			_ = duplicate.Close()
		}
		t.Fatalf("exclusive create error=%v, want an existing-file error", err)
	}
}

func TestWindowsStateTargetResolutionAndOwnerOnlyACL(t *testing.T) {
	directory := t.TempDir()
	resolved, err := finalWindowsPath(directory)
	if err != nil {
		t.Fatal(err)
	}
	if windowsNetworkPath(resolved) {
		t.Fatalf("local temporary directory resolved to network path %q", resolved)
	}
	if err := ensureLocalStateFilesystem(directory); err != nil {
		t.Fatal(err)
	}
	if err := ProtectPrivatePath(directory, true); err != nil {
		t.Fatal(err)
	}
	user, defaultOwner, err := processIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOwnerOnly(directory, user, true); err != nil {
		t.Fatalf("protected directory: %v", err)
	}
	privateFile := filepath.Join(directory, "private")
	if err := os.WriteFile(privateFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !defaultOwner.Equals(user) {
		if err := ValidatePrivateFile(privateFile); err == nil {
			t.Fatal("strict validation accepted a file still owned by the token default owner")
		}
	}
	if err := ProtectPrivatePath(privateFile, false); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateFile(privateFile); err != nil {
		t.Fatal(err)
	}
}
