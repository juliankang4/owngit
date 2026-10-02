package recovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupReportsAliasesToDirectoryTargets(t *testing.T) {
	for _, scenario := range []string{"missing-directory", "existing-loose-ref", "existing-packed-ref-with-directory"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			remote, err := manager.Path("project")
			noErr(t, err)
			missing := scenario == "missing-directory"
			target := "refs/heads/future"
			if missing {
				runGit(t, remote, "--git-dir", ".", "update-ref", target+"/topic", "refs/heads/main")
			} else {
				runGit(t, remote, "--git-dir", ".", "update-ref", target, "refs/heads/main")
				if scenario == "existing-packed-ref-with-directory" {
					runGit(t, remote, "--git-dir", ".", "pack-refs", "--all", "--prune")
					noErr(t, os.MkdirAll(filepath.Join(remote, filepath.FromSlash(target)), 0o700))
				}
			}
			pairs := map[string]string{"refs/heads/directory-target": target, "refs/heads/chained-directory": "refs/heads/directory-target"}
			for name, target := range pairs {
				runGit(t, remote, "--git-dir", ".", "symbolic-ref", name, target)
			}
			backup := filepath.Join(root, "backup")
			report, err := CreateWithReport(context.Background(), store, manager, backup)
			noErr(t, err)
			if len(report.AliasBranches) != len(pairs) {
				t.Fatalf("alias capture: %+v", report)
			}
			for _, alias := range report.AliasBranches {
				if alias.Repository != "project" || alias.Target != pairs[alias.Name] || alias.MissingTarget != missing || alias.UnresolvedTarget {
					t.Fatalf("directory target classified incorrectly: %+v", alias)
				}
				got, err := gitCombined(remote, "--git-dir", ".", "symbolic-ref", "--no-recurse", alias.Name)
				noErr(t, err)
				if strings.TrimRight(got, "\r\n") != alias.Target {
					t.Fatalf("source alias changed: %q", got)
				}
			}
			heading := AliasBranchNotice
			if missing {
				heading = MissingAliasBranchNotice
			}
			notice := report.AliasNotice()
			if !strings.HasPrefix(notice, heading+"\n") || strings.Contains(notice, UnresolvedAliasBranchNotice) {
				t.Fatalf("wrong target guidance: %s", notice)
			}
			for name, target := range pairs {
				entry := "project: " + name + " -> " + target + ". " + (AliasBranch{Name: name, Target: target}).ReconnectCommand()
				if !strings.Contains(notice, entry) {
					t.Fatalf("complete recreation entry missing: %s", notice)
				}
			}
			verification, err := Verify(context.Background(), backup, root, "")
			noErr(t, err)
			if !verification.Verified {
				t.Fatalf("verification: %+v", verification)
			}
			restored := filepath.Join(root, "restored-repositories")
			noErr(t, Restore(context.Background(), backup, filepath.Join(root, "restored-state"), restored, ""))
			manifest, err := readManifest(filepath.Join(backup, manifestName))
			noErr(t, err)
			for _, ref := range manifest.Repositories[0].Refs {
				if missing {
					if _, omitted := pairs[ref.Name]; omitted {
						t.Fatalf("missing alias acquired a fabricated ref: %+v", ref)
					}
				}
				oid, err := gitCombined(filepath.Join(restored, "project.git"), "--git-dir", ".", "rev-parse", ref.Name)
				noErr(t, err)
				if strings.TrimRight(oid, "\r\n") != ref.OID {
					t.Fatalf("restored ref changed: %+v, got %q", ref, oid)
				}
			}
			if missing {
				for name := range pairs {
					if _, err := os.Lstat(filepath.Join(restored, "project.git", filepath.FromSlash(name))); !os.IsNotExist(err) {
						t.Fatalf("restore invented missing alias %s: %v", name, err)
					}
				}
			}
		})
	}
}
