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

	if _, err := f.importProject(ImportInput{}); problemCode(err) != CodeRepositoryTaken {
		t.Fatalf("import during an unfinished deletion err=%v", err)
	}
	if _, exists, err := f.store.ImportSource(ctx, "project"); err != nil || exists {
		t.Fatalf("refused import configured a source exists=%v err=%v", exists, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("refused import created a directory: %v", err)
	}
}

// A repository deleted after a refresh passed its admission checks, but before
// its run was recorded, stops that refresh as superseded and leaves no run.
func TestRefreshAdmittedBeforeDeletionIsSuperseded(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	deleted := false
	f.service.beforeRunRecord = func() {
		f.service.beforeRunRecord = nil
		if _, err := f.manager.Delete(ctx, "project", repository.DeleteFiles); err != nil {
			t.Errorf("delete during admission: %v", err)
			return
		}
		deleted = true
	}
	if _, err := f.refresh(); problemCode(err) != CodeSuperseded {
		t.Fatalf("refresh err=%v", err)
	}
	if !deleted {
		t.Fatal("the deletion did not run inside the admission window")
	}
	if count, err := f.store.TableRowCount(ctx, "import_runs"); err != nil || count != 0 {
		t.Fatalf("run rows after deletion=%d err=%v", count, err)
	}
}
