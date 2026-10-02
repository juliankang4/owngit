//go:build darwin

package importsync

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInitialImportRemovesInheritedWriteACL(t *testing.T) {
	f := newFixture(t)
	root := f.manager.RepositoryRoot()
	noErr(t, exec.Command("chmod", "+a", "everyone allow add_file,add_subdirectory,delete_child,file_inherit,directory_inherit", root).Run())

	f.mustImport(ImportInput{})
	path := repositoryFinalPath(t, f, "project")
	listing, err := exec.Command("ls", "-lde", path).CombinedOutput()
	noErr(t, err)
	if strings.Contains(string(listing), " allow ") {
		t.Fatalf("imported repository kept an inherited permit entry:\n%s", listing)
	}
}
