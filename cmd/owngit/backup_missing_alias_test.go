package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/recovery"
)

func TestOfflineBackupReportsMissingAliasRecreation(t *testing.T) {
	root := t.TempDir()
	stateDir := aliasBackupState(t, root, false)
	remote := filepath.Join(root, "repositories", "project.git")
	runPRGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/missing-target", "refs/heads/future")
	runPRGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/chained-missing", "refs/heads/missing-target")
	output, err := captureStdout(func() error {
		return backupState([]string{"--state-dir", stateDir, "--output", filepath.Join(root, "backup"), "--json"})
	})
	noErr(t, err)
	var result offlineBackupResult
	noErr(t, json.Unmarshal([]byte(output), &result))
	if !result.OK || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], recovery.MissingAliasBranchNotice) || strings.Contains(result.Warnings[0], recovery.AliasBranchNotice) {
		t.Fatalf("missing aliases were misreported: %s", output)
	}
	for _, pair := range [][2]string{{"refs/heads/missing-target", "refs/heads/future"}, {"refs/heads/chained-missing", "refs/heads/missing-target"}} {
		if !strings.Contains(result.Warnings[0], "project: "+pair[0]+" -> "+pair[1]) || !strings.Contains(result.Warnings[0], "git symbolic-ref -- '"+pair[0]+"' '"+pair[1]+"'") {
			t.Fatalf("backup lacks the exact alias recreation instruction: %s", output)
		}
	}
}
