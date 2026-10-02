//go:build darwin

package repository

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreatedRepositoryRemovesInheritedWriteACL(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	root := manager.RepositoryRoot()
	noErr(t, exec.Command("chmod", "+a", "everyone allow add_file,add_subdirectory,delete_child,file_inherit,directory_inherit", root).Run())
	inspectedStaging := false
	manager.creationDirectoryHook = func(path string) {
		inspectedStaging = true
		listing, err := exec.Command("ls", "-lde", path).CombinedOutput()
		noErr(t, err)
		if strings.Contains(string(listing), " allow ") {
			t.Fatalf("creation staging was visible with an inherited permit entry before verification:\n%s", listing)
		}
	}

	created, err := manager.Create(context.Background(), "private-created", "")
	noErr(t, err)
	path, err := manager.Path(created.ID)
	noErr(t, err)
	listing, err := exec.Command("ls", "-lde", path).CombinedOutput()
	noErr(t, err)
	if strings.Contains(string(listing), " allow ") {
		t.Fatalf("created repository kept an inherited permit entry:\n%s", listing)
	}
	if filepath.Dir(path) != root {
		t.Fatalf("repository path %q is outside %q", path, root)
	}
	if !inspectedStaging {
		t.Fatal("creation staging was not inspected before verification")
	}
}
