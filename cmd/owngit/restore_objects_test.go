package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/gitexec"
)

// Real Git bundles exercise both loose and packed fsck diagnostics and the
// CLI's text and JSON results without changing the restored history.
func TestRestoreReportsMalformedTrees(t *testing.T) {
	for _, test := range []struct {
		name   string
		asJSON bool
		packed bool
		trees  int
	}{{name: "loose-text"}, {name: "packed-json", asJSON: true, packed: true}, {name: "many-trees-json", asJSON: true, trees: 1000}} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			stateDir := aliasBackupState(t, root, false)
			remote := filepath.Join(root, "repositories", "project.git")
			runner, err := gitexec.New("", filepath.Join(root, "git-runtime"))
			noErr(t, err)
			write := func(kind, content string) string {
				t.Helper()
				result, err := runner.Run(context.Background(), remote, strings.NewReader(content), "--git-dir", ".", "hash-object", "--literally", "-t", kind, "-w", "--stdin")
				noErr(t, err)
				return strings.TrimSpace(string(result.Stdout))
			}
			entry := func(mode, name, oid string) string {
				t.Helper()
				raw, err := hex.DecodeString(oid)
				noErr(t, err)
				return mode + " " + name + "\x00" + string(raw)
			}
			var treeData strings.Builder
			if test.packed {
				// More than 100 objects keep the fetched bundle in a pack.
				for index := range 110 {
					name := fmt.Sprintf("a%03d", index)
					treeData.WriteString(entry("100644", name, write("blob", name)))
				}
			}
			blob := write("blob", "data")
			treeData.WriteString(entry("100644", "collision", blob))
			treeData.WriteString(entry("40000", "collision", write("tree", "")))
			if test.trees != 0 {
				// Batch writing keeps a long malformed history cheap to build.
				directory := t.TempDir()
				var paths strings.Builder
				for index := range test.trees {
					name := fmt.Sprintf("f%05d", index)
					path := filepath.Join(directory, name)
					noErr(t, os.WriteFile(path, []byte(treeData.String()+entry("100644", name, blob)), 0o600))
					fmt.Fprintln(&paths, path)
				}
				result, err := runner.Run(context.Background(), remote, strings.NewReader(paths.String()), "--git-dir", ".", "hash-object", "--literally", "-t", "tree", "-w", "--stdin-paths")
				noErr(t, err)
				trees := strings.Fields(string(result.Stdout))
				if len(trees) != test.trees {
					t.Fatalf("wrote %d trees, want %d", len(trees), test.trees)
				}
				treeData.Reset()
				for index, oid := range trees {
					treeData.WriteString(entry("40000", fmt.Sprintf("d%05d", index), oid))
				}
			}
			tree := write("tree", treeData.String())
			oid := prGitOutput(t, remote, "--git-dir", ".", "-c", "user.name=Backup Test", "-c", "user.email=backup@example.invalid", "commit-tree", tree, "-m", "stored history")
			runPRGit(t, remote, "--git-dir", ".", "update-ref", "refs/tags/legacy", oid)
			backup := filepath.Join(root, "backup")
			_, err = captureStdout(func() error { return backupState([]string{"--state-dir", stateDir, "--output", backup}) })
			noErr(t, err)
			stateTarget, repoTarget := filepath.Join(root, "restored-state"), filepath.Join(root, "restored-repositories")
			args := []string{"--input", backup, "--state-dir", stateTarget, "--repository-root", repoTarget, "--verify", "--temp-dir", root}
			if test.asJSON {
				args = append(args, "--json")
			}
			output, err := captureStdout(func() error { return restoreState(args) })
			noErr(t, err)
			message := output
			if test.asJSON {
				var result struct {
					OK             bool `json:"ok"`
					ObjectWarnings []struct {
						ID      string `json:"id"`
						Message string `json:"message"`
					} `json:"object_warnings"`
				}
				noErr(t, json.Unmarshal([]byte(output), &result))
				if !result.OK || len(result.ObjectWarnings) != 1 || result.ObjectWarnings[0].ID != "project" {
					t.Fatalf("restore warnings: %s", output)
				}
				message = result.ObjectWarnings[0].Message
			} else if strings.Count(output, "Repository project:") != 1 {
				t.Fatalf("restore warning must name the repository once: %s", output)
			}
			if !strings.Contains(message, "malformed Git objects") || !strings.Contains(message, "git fsck --strict") || !strings.Contains(message, "repair") {
				t.Fatalf("restore lacks the meaning or repair instruction: %s", output)
			}
			restored := filepath.Join(repoTarget, "project.git")
			if got := prGitOutput(t, restored, "--git-dir", ".", "rev-parse", "refs/tags/legacy"); got != oid {
				t.Fatalf("restored history=%s, want %s", got, oid)
			}
			result, err := runner.Run(context.Background(), restored, nil, "--git-dir", ".", "cat-file", "tree", tree)
			noErr(t, err)
			if !bytes.Equal(result.Stdout, []byte(treeData.String())) {
				t.Fatal("restore changed the malformed tree")
			}
		})
	}
}
