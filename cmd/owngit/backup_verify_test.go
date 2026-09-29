package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/recovery"
	"owngit/internal/state"
)

// backup verify prints the result as JSON and exits 1 when the backup is
// not verified; restore --verify refuses such a backup before it creates
// anything at its targets, and restores a verified one into folders whose
// parents it creates.
func TestBackupVerifyAndRestoreVerify(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	adminHash, _ := auth.HashPassword("admin-password")
	err = store.CompleteSetup(context.Background(), repositoryRoot, "open", "", adminHash, false)
	store.Close()
	noErr(t, err)
	backup := filepath.Join(root, "backup")
	if _, err := captureStdout(func() error { return backupState([]string{"--state-dir", stateDir, "--output", backup}) }); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(root, "temporary")
	noErr(t, os.Mkdir(temporary, 0o700))

	output, err := captureStdout(func() error {
		return backupState([]string{"verify", backup, "--json", "--temp-dir", temporary})
	})
	var result recovery.Verification
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || !result.Verified || result.Database != recovery.VerifyPassed {
		t.Fatalf("verify output=%s err=%v", output, err)
	}

	broken := filepath.Join(root, "broken")
	noErr(t, os.Mkdir(broken, 0o700))
	noErr(t, os.WriteFile(filepath.Join(broken, "manifest.json"), []byte("{not json"), 0o600))
	output, err = captureStdout(func() error {
		return backupState([]string{"verify", "--json", "--temp-dir", temporary, broken})
	})
	var exit *checkExit
	result = recovery.Verification{}
	if !errors.As(err, &exit) || exit.code != 1 || json.Unmarshal([]byte(output), &result) != nil || result.Verified || !strings.Contains(result.Error, "decode backup manifest") {
		t.Fatalf("verify of a broken backup: output=%s err=%v", output, err)
	}

	restoredState := filepath.Join(root, "missing", "state")
	restoredRepositories := filepath.Join(root, "missing-too", "repositories")
	restore := func(input string) (string, error) {
		return captureStdout(func() error {
			return restoreState([]string{"--input", input, "--state-dir", restoredState, "--repository-root", restoredRepositories, "--verify", "--temp-dir", temporary})
		})
	}
	output, err = restore(broken)
	if err == nil || !strings.Contains(output, "Not verified: ") {
		t.Fatalf("restore --verify of a broken backup: output=%s err=%v", output, err)
	}
	for _, folder := range []string{filepath.Dir(restoredState), filepath.Dir(restoredRepositories)} {
		if _, err := os.Lstat(folder); !os.IsNotExist(err) {
			t.Fatalf("the refused restore created %s (%v)", folder, err)
		}
	}
	output, err = restore(backup)
	if err != nil || !strings.Contains(output, "Backup verified") {
		t.Fatalf("restore --verify: output=%s err=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(restoredState, "owngit.sqlite")); err != nil {
		t.Fatalf("restored state: %v", err)
	}
	if left, err := os.ReadDir(temporary); err != nil || len(left) != 0 {
		t.Fatalf("rehearsals left %v (%v)", left, err)
	}
}
