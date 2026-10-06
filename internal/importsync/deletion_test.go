package importsync

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// A new import cannot claim a name whose earlier deletion is unfinished; it is
// refused like an existing repository before any source is configured.
func TestInitialImportRefusesNameWithUnfinishedDeletion(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	root, err := f.manager.CanonicalStorageRoot()
	noErr(t, err)
	path := f.destinationPath()
	// The deletion stopped after its commit point. Its directory is moved
	// aside here, so only the recorded intent can refuse the name.
	noErr(t, f.store.BeginRepositoryDeletion(ctx, state.RepositoryDeletion{
		RepositoryID: "project", Mode: state.RepositoryDeletionKeepFiles, Root: root,
		Moved: ".owngit-removed/project-20270115T080000Z.git", Marker: strings.Repeat("c", 32), CreatedAt: time.Now(),
	}))
	noErr(t, os.Rename(path, path+".aside"))

	_, err = f.importProject(ImportInput{})
	require(t, problemCode(err) == CodeRepositoryTaken, "import during an unfinished deletion err=%v", err)
	_, exists, err := f.store.ImportSource(ctx, "project")
	require(t, err == nil && !exists, "refused import configured a source exists=%v err=%v", exists, err)
	_, err = os.Lstat(path)
	require(t, os.IsNotExist(err), "refused import created a directory: %v", err)
}

// A repository deleted after a refresh passed its admission checks, but before
// its run was recorded, stops that refresh as superseded and leaves no run.
func TestRefreshAdmittedBeforeDeletionIsSuperseded(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	deleted := false
	f.service.beforeRunRecord = func(context.Context) {
		f.service.beforeRunRecord = nil
		if _, err := f.manager.Delete(ctx, "project", repository.DeleteFiles); err != nil {
			t.Errorf("delete during admission: %v", err)
			return
		}
		deleted = true
	}
	_, err := f.refresh()
	require(t, problemCode(err) == CodeSuperseded, "refresh err=%v", err)
	require(t, deleted, "the deletion did not run inside the admission window")
	count, err := f.store.TableRowCount(ctx, "import_runs")
	require(t, err == nil && count == 0, "run rows after deletion=%d err=%v", count, err)
}
