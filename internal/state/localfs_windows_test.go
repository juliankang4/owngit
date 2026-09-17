//go:build windows

package state

import (
	"os"
	"path/filepath"
	"testing"
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
	privateFile := filepath.Join(directory, "private")
	if err := os.WriteFile(privateFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProtectPrivatePath(privateFile, false); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateFile(privateFile); err != nil {
		t.Fatal(err)
	}
}
