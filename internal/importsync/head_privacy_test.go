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
	if problemCode(err) != CodeUnresolved || !strings.Contains(err.Error(), "repository folder is not private to this account") {
		t.Errorf("missing privacy fallback: %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("non-private recovery changed the lock: %q err=%v", after, readErr)
	}
	records, err := f.store.ImportRefLocksPage(context.Background(), "", 100)
	noErr(t, err)
	if len(records) != 1 {
		t.Fatalf("non-private recovery discarded evidence: %+v", records)
	}
}

func assertNormalHEADPublication(t *testing.T, f *fixture) {
	t.Helper()
	f.commit("next", "next\n")
	f.git(f.source, "branch", "release")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	_, err := f.refresh()
	noErr(t, err)
	if got := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); got != "refs/heads/release" {
		t.Fatalf("normal publication on shared storage did not complete: %s", got)
	}
	if _, err := os.Lstat(filepath.Join(f.destinationPath(), "HEAD.lock")); !os.IsNotExist(err) {
		t.Fatalf("normal publication retained its own lock: %v", err)
	}
}
