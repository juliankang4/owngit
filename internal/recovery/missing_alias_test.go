package recovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBackupReportsCyclicAliasesWithoutRecreatingTheCycle(t *testing.T) {
	for _, scenario := range []string{"two-branch-cycle", "self-reference"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			remote, err := manager.Path("project")
			noErr(t, err)
			pairs := map[string]string{"refs/heads/a": "refs/heads/a"}
			if scenario == "two-branch-cycle" {
				pairs = map[string]string{"refs/heads/a": "refs/heads/b", "refs/heads/b": "refs/heads/a"}
			}
			for name, target := range pairs {
				runGit(t, remote, "--git-dir", ".", "symbolic-ref", name, target)
			}
			backup := filepath.Join(root, "backup")
			report, err := CreateWithReport(context.Background(), store, manager, backup)
			noErr(t, err)
			if len(report.AliasBranches) != len(pairs) {
				t.Fatalf("cycle aliases were not discovered: %+v", report)
			}
			for _, alias := range report.AliasBranches {
				if alias.MissingTarget || !alias.UnresolvedTarget || alias.Target != pairs[alias.Name] {
					t.Fatalf("cycle misclassified: %+v", alias)
				}
				got, err := gitCombined(remote, "--git-dir", ".", "symbolic-ref", "--no-recurse", alias.Name)
				noErr(t, err)
				if strings.TrimRight(got, "\r\n") != alias.Target {
					t.Fatalf("source alias changed: %s", got)
				}
			}
			notice := report.AliasNotice()
			if !strings.Contains(notice, UnresolvedAliasBranchNotice) || strings.Contains(notice, MissingAliasBranchNotice) || strings.Contains(notice, "git symbolic-ref") {
				t.Fatalf("cycle notice recreates or misstates the cycle: %s", notice)
			}
			for name, target := range pairs {
				if !strings.Contains(notice, "project: "+name+" -> "+target) {
					t.Fatalf("cycle notice lacks the immediate target: %s", notice)
				}
			}
			verification, err := Verify(context.Background(), backup, root, "")
			noErr(t, err)
			if !verification.Verified {
				t.Fatalf("verification: %+v", verification)
			}
		})
	}
}

func TestMissingAliasScanIsBoundedAndDoesNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	heads := filepath.Join(root, "refs", "heads")
	noErr(t, os.MkdirAll(heads, 0o700))
	noErr(t, os.WriteFile(filepath.Join(heads, "too-large"), []byte("ref: "+strings.Repeat("x", 4096)), 0o600))
	if _, err := readMissingBranchAliases(context.Background(), nil, root, nil); err == nil || !strings.Contains(err.Error(), "exceeds 4096 bytes") {
		t.Fatalf("oversize loose ref was not refused before Git: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link fixture requires link privileges")
	}
	linked := t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(linked, "refs"), 0o700))
	noErr(t, os.Symlink(heads, filepath.Join(linked, "refs", "heads")))
	if _, err := readMissingBranchAliases(context.Background(), nil, linked, nil); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("scan followed a linked branch directory: %v", err)
	}
}

func TestBackupReportsMissingAliasTargetsWithoutInventingRefs(t *testing.T) {
	for _, scenario := range []string{"populated", "empty", "empty-symbolic-head"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			remote, err := manager.Path("project")
			noErr(t, err)
			if scenario != "populated" {
				runGit(t, remote, "--git-dir", ".", "update-ref", "-d", "refs/heads/main")
			}
			target := "refs/heads/not-yet-created\u00a0"
			runGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/missing-target", target)
			runGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/chained-missing", "refs/heads/missing-target")
			if scenario == "empty-symbolic-head" {
				runGit(t, remote, "--git-dir", ".", "symbolic-ref", "HEAD", "refs/heads/chained-missing")
			}
			backup := filepath.Join(root, "backup")
			report, err := CreateWithReport(ctx, store, manager, backup)
			noErr(t, err)
			content, err := json.Marshal(report)
			noErr(t, err)
			var result struct {
				Aliases []struct {
					Repository    string `json:"repository"`
					Name          string `json:"name"`
					Target        string `json:"target"`
					MissingTarget bool   `json:"missing_target"`
				} `json:"alias_branches"`
			}
			noErr(t, json.Unmarshal(content, &result))
			if len(result.Aliases) != 2 {
				t.Fatalf("missing direct and chained aliases were not reported: %s", content)
			}
			wantTargets := map[string]string{"refs/heads/missing-target": target, "refs/heads/chained-missing": "refs/heads/missing-target"}
			for _, alias := range result.Aliases {
				if alias.Repository != "project" || alias.Target != wantTargets[alias.Name] || !alias.MissingTarget {
					t.Fatalf("unresolved alias metadata: %+v", alias)
				}
			}
			notice := report.AliasNotice()
			if !strings.Contains(notice, "not included") || !strings.Contains(notice, "target did not exist") || strings.Contains(notice, AliasBranchNotice) {
				t.Fatalf("notice misstates an omitted alias as a captured branch: %s", notice)
			}
			for name, target := range wantTargets {
				command := (AliasBranch{Name: name, Target: target}).ReconnectCommand()
				if !strings.Contains(notice, "project: "+name+" -> "+target) || !strings.Contains(notice, command) {
					t.Fatalf("notice lacks the exact missing alias and recreation command: %s", notice)
				}
			}
			manifest, err := readManifest(filepath.Join(backup, manifestName))
			noErr(t, err)
			for _, ref := range manifest.Repositories[0].Refs {
				if _, omitted := wantTargets[ref.Name]; omitted {
					t.Fatalf("unresolved alias acquired a fabricated OID: %+v", ref)
				}
			}
			if scenario != "populated" && !manifest.Repositories[0].Empty {
				t.Fatal("unresolved aliases made an empty repository appear populated")
			}
			verification, err := Verify(ctx, backup, root, "")
			noErr(t, err)
			if !verification.Verified {
				t.Fatalf("verification: %+v", verification)
			}
			restored := filepath.Join(root, "restored-repositories")
			noErr(t, Restore(ctx, backup, filepath.Join(root, "restored-state"), restored, ""))
			for name := range wantTargets {
				if _, err := os.Lstat(filepath.Join(restored, "project.git", filepath.FromSlash(name))); !os.IsNotExist(err) {
					t.Fatalf("restore created the omitted alias %s: %v", name, err)
				}
			}
		})
	}
}
