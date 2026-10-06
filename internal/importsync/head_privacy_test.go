package importsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedRepositoryRootDoesNotAuthorizeCrashLockRemoval(t *testing.T) {
	t.Setenv("OWNGIT_IMPORT_SHARED_ROOT", "1")
	f := killedPublicationFixture(t, "head-locked")
	assertRecoveryPrivacyRefusal(t, f)
}

func assertRecoveryPrivacyRefusal(t *testing.T, f *fixture) {
	t.Helper()
	path := filepath.Join(f.destinationPath(), "HEAD.lock")
	before, err := os.ReadFile(path)
	noErr(t, err)
	err = f.service.Reconcile(context.Background())
	require(t, problemCode(err) == CodeUnresolved &&
		strings.Contains(err.Error(), "repository folder is not private to this account"),
		"missing privacy fallback: %v", err)
	fileIs(t, path, string(before))
	records, err := f.store.ImportRefLocksPage(context.Background(), "", 100)
	noErr(t, err)
	require(t, len(records) == 1, "non-private recovery discarded evidence: %+v", records)
}

func assertNormalHEADPublication(t *testing.T, f *fixture) {
	t.Helper()
	f.commit("next", "next\n")
	f.git(f.source, "branch", "release")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	_, err := f.refresh()
	noErr(t, err)
	eq(t, "HEAD after normal publication on shared storage",
		f.git(f.destinationPath(), "symbolic-ref", "HEAD"), "refs/heads/release")
	absent(t, filepath.Join(f.destinationPath(), "HEAD.lock"))
}
