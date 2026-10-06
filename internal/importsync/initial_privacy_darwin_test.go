//go:build darwin

package importsync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"owngit/internal/state"
)

func TestInitialImportPrivateCreationFailureIsNotCollision(t *testing.T) {
	f := newFixture(t)
	root := f.manager.RepositoryRoot()
	noErr(t, exec.Command("chmod",
		"+a", "everyone allow add_file,add_subdirectory,delete_child,file_inherit,directory_inherit", root).Run())
	f.service.mkdirInitialDirectory = func(path string) error {
		noErr(t, os.Mkdir(path, 0o700))
		listing, err := exec.Command("ls", "-lde", path).CombinedOutput()
		noErr(t, err)
		require(t, strings.Contains(string(listing), " allow "),
			"fallback fixture did not inherit its permit entry:\n%s", listing)
		noErr(t, os.Remove(path))
		return fmt.Errorf("OwnGit could not create the unpublished folder privately: %w", unix.EINVAL)
	}

	result, err := f.importProject(ImportInput{})
	require(t, err != nil && problemCode(err) == CodeRepositoryCreateFailed && problemCode(err) != CodeRepositoryTaken,
		"private creation result=%+v code=%s err=%v", result, problemCode(err), err)
	finalPath := repositoryFinalPath(t, f, "project")
	_, statErr := os.Lstat(finalPath)
	require(t, os.IsNotExist(statErr), "private creation refusal left final repository: %v", statErr)
	directories := unpublishedInitialDirectories(t, f)
	require(t, len(directories) == 0, "private creation refusal left unpublished folders: %v", directories)
	rows, readErr := f.store.ImportInitialDestinationsForRun(context.Background(), result.Run.ID)
	noErr(t, readErr)
	require(t, len(rows) == 1 && rows[0].State == state.ImportInitialUnknown &&
		rows[0].Issue == initialDestinationCreateIssue, "durable private creation issue=%+v", rows)
}

func TestInitialImportRemovesInheritedWriteACL(t *testing.T) {
	f := newFixture(t)
	root := f.manager.RepositoryRoot()
	noErr(t, exec.Command("chmod",
		"+a", "everyone allow add_file,add_subdirectory,delete_child,file_inherit,directory_inherit", root).Run())
	inspectedStaging := false
	f.service.afterInitialDirectoryCreated = func() {
		inspectedStaging = true
		directories := unpublishedInitialDirectories(t, f)
		require(t, len(directories) == 1, "unpublished directories=%v", directories)
		listing, err := exec.Command("ls", "-lde", directories[0]).CombinedOutput()
		noErr(t, err)
		require(t, !strings.Contains(string(listing), " allow "),
			"import staging was visible with an inherited permit entry before verification:\n%s", listing)
	}

	f.mustImport(ImportInput{})
	path := repositoryFinalPath(t, f, "project")
	listing, err := exec.Command("ls", "-lde", path).CombinedOutput()
	noErr(t, err)
	require(t, !strings.Contains(string(listing), " allow "),
		"imported repository kept an inherited permit entry:\n%s", listing)
	require(t, inspectedStaging, "import staging was not inspected before verification")
}
