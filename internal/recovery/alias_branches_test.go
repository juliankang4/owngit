package recovery

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupReportsAliasBranchesAsOrdinaryBranches(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	remote, err := manager.Path("project")
	noErr(t, err)
	runGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	oid := gitOutput(t, remote, "--git-dir", ".", "rev-parse", "refs/heads/alias")
	backup := filepath.Join(root, "backup")
	report, err := CreateWhileServing(ctx, store, manager, backup)
	noErr(t, err)
	t.Logf("backup report: %+v", report)
	verification, err := Verify(ctx, backup, root, "")
	noErr(t, err)
	if !verification.Verified {
		t.Fatalf("verification: %+v", verification)
	}
	restoredRoot := filepath.Join(root, "restored-repositories")
	noErr(t, Restore(ctx, backup, filepath.Join(root, "restored-state"), restoredRoot, ""))
	restored := filepath.Join(restoredRoot, "project.git")
	assertRef(t, restored, "refs/heads/alias", oid)
	if output, err := gitCombined(restored, "--git-dir", ".", "symbolic-ref", "refs/heads/alias"); err == nil {
		t.Fatalf("restored alias must be ordinary: %s", output)
	}
	if got := gitOutput(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias"); got != "refs/heads/main" {
		t.Fatalf("backup changed the source alias: %s", got)
	}
	content, err := json.Marshal(report)
	noErr(t, err)
	var result struct {
		Aliases []struct {
			Repository string `json:"repository"`
			Name       string `json:"name"`
			Target     string `json:"target"`
		} `json:"alias_branches"`
	}
	noErr(t, json.Unmarshal(content, &result))
	if len(result.Aliases) != 1 || result.Aliases[0].Repository != "project" || result.Aliases[0].Name != "refs/heads/alias" || result.Aliases[0].Target != "refs/heads/main" {
		t.Fatalf("successful backup did not report the alias and target: %s", content)
	}
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	encoded, err := json.Marshal(manifest)
	noErr(t, err)
	if strings.Contains(string(encoded), "alias_branches") || strings.Contains(string(encoded), "\"target\"") {
		t.Fatalf("alias notices must not change the portable manifest: %s", encoded)
	}
}
