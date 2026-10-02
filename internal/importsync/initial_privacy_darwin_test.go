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
	inspectedStaging := false
	f.service.afterInitialDirectoryCreated = func() {
		inspectedStaging = true
		directories := unpublishedInitialDirectories(t, f)
		if len(directories) != 1 {
			t.Fatalf("unpublished directories=%v", directories)
		}
		listing, err := exec.Command("ls", "-lde", directories[0]).CombinedOutput()
		noErr(t, err)
		if strings.Contains(string(listing), " allow ") {
			t.Fatalf("import staging was visible with an inherited permit entry before verification:\n%s", listing)
		}
	}

	f.mustImport(ImportInput{})
	path := repositoryFinalPath(t, f, "project")
	listing, err := exec.Command("ls", "-lde", path).CombinedOutput()
	noErr(t, err)
	if strings.Contains(string(listing), " allow ") {
		t.Fatalf("imported repository kept an inherited permit entry:\n%s", listing)
	}
	if !inspectedStaging {
		t.Fatal("import staging was not inspected before verification")
	}
}
