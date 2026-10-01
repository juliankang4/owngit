package recovery

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if notice := report.AliasNotice(); !strings.Contains(notice, AliasBranchNotice) || !strings.Contains(notice, "project: refs/heads/alias -> refs/heads/main") || !strings.Contains(notice, "git symbolic-ref -- 'refs/heads/alias' 'refs/heads/main'") {
		t.Fatalf("alias notice lacks the conversion or reconnect instructions: %s", notice)
	}
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	encoded, err := json.Marshal(manifest)
	noErr(t, err)
	if strings.Contains(string(encoded), "alias_branches") || strings.Contains(string(encoded), "\"target\"") {
		t.Fatalf("alias notices must not change the portable manifest: %s", encoded)
	}
	entries, err := os.ReadDir(backup)
	noErr(t, err)
	if len(entries) != 2 {
		t.Fatalf("backup must contain only its manifest and repositories folder: %v", entries)
	}
}

func TestBackupAliasNoticeNamesImmediateTargets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	remote, err := manager.Path("project")
	noErr(t, err)
	for _, pair := range [][2]string{{"refs/heads/alias", "refs/heads/main"}, {"refs/heads/chained", "refs/heads/alias"}, {"refs/heads/quote';&$name", "refs/heads/main"}, {"refs/heads/through-head", "HEAD"}} {
		runGit(t, remote, "--git-dir", ".", "symbolic-ref", pair[0], pair[1])
	}
	backup := filepath.Join(root, "backup")
	report, err := CreateWithReport(ctx, store, manager, backup)
	noErr(t, err)
	if len(report.AliasBranches) != 4 || report.AliasBranches[1].Name != "refs/heads/chained" || report.AliasBranches[1].Target != "refs/heads/alias" || report.AliasBranches[3].Target != "HEAD" {
		t.Fatalf("immediate alias targets were not captured: %+v", report.AliasBranches)
	}
	if runtime.GOOS == "windows" {
		t.Skip("executing the POSIX reconnect command needs a POSIX shell")
	}
	restoredRoot := filepath.Join(root, "restored-repositories")
	noErr(t, Restore(ctx, backup, filepath.Join(root, "restored-state"), restoredRoot, ""))
	restored := filepath.Join(restoredRoot, "project.git")
	for _, alias := range report.AliasBranches {
		command := exec.Command("sh", "-c", alias.ReconnectCommand())
		command.Dir = restored
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("reconnect command %s: %v %s", alias.ReconnectCommand(), err, output)
		}
		if got := gitOutput(t, restored, "--git-dir", ".", "symbolic-ref", "--no-recurse", alias.Name); got != alias.Target {
			t.Fatalf("reconnected %s to %s, want %s", alias.Name, got, alias.Target)
		}
	}
}

func TestBackupWithoutAliasesHasNoNotice(t *testing.T) {
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	report, err := CreateWithReport(context.Background(), store, manager, filepath.Join(root, "backup"))
	noErr(t, err)
	content, err := json.Marshal(report)
	noErr(t, err)
	if report.AliasNotice() != "" || strings.Contains(string(content), "alias_branches") {
		t.Fatalf("ordinary branches gained an alias notice: %s", content)
	}
}
