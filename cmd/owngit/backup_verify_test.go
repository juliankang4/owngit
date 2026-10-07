package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
	"owngit/internal/recovery"
	"owngit/internal/service"
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

// With --json the offline backup and restore print their result as JSON,
// and every refusal is a coded JSON error with the same exit status as
// without it; a verification that ran is the details of a refused restore.
func TestOfflineBackupAndRestorePrintJSON(t *testing.T) {
	root := t.TempDir()
	stateDir := aliasBackupState(t, root, false)
	temporary := filepath.Join(root, "temporary")
	noErr(t, os.Mkdir(temporary, 0o700))
	// refused runs command, which must fail, and returns what the error
	// report writes and its exit status; nothing else may be printed.
	refused := func(command func() error) (map[string]any, int) {
		t.Helper()
		output, err := captureStdout(command)
		var reported strings.Builder
		code := reportError(&reported, err)
		var answer struct {
			OK    bool           `json:"ok"`
			Error map[string]any `json:"error"`
		}
		if err == nil || output != "" || json.Unmarshal([]byte(reported.String()), &answer) != nil || answer.OK {
			t.Fatalf("refusal output=%q reported=%q err=%v", output, reported.String(), err)
		}
		return answer.Error, code
	}

	backup := filepath.Join(root, "backup")
	output, err := captureStdout(func() error { return backupState([]string{"--state-dir", stateDir, "--output", backup, "--json"}) })
	var written offlineBackupResult
	if err != nil || json.Unmarshal([]byte(output), &written) != nil || !written.OK || written.Backup != backup || !strings.Contains(written.Note, "SHA-256") {
		t.Fatalf("backup output=%q err=%v", output, err)
	}
	for _, test := range []struct {
		arguments []string
		code      string
	}{
		{[]string{"--state-dir", stateDir, "--json"}, "invalid_arguments"},
		{[]string{"--state-dir", stateDir, "--output", backup, "--unknown", "--json"}, "invalid_arguments"},
		{[]string{"--state-dir", filepath.Join(root, "absent"), "--output", filepath.Join(root, "other"), "--json"}, "state_missing"},
		{[]string{"--state-dir", stateDir, "--output", backup, "--json"}, "backup_failed"},
	} {
		problem, exit := refused(func() error { return backupState(test.arguments) })
		if problem["code"] != test.code || exit != 1 {
			t.Fatalf("backup %v error=%v exit=%d", test.arguments, problem, exit)
		}
	}

	broken := filepath.Join(root, "broken")
	noErr(t, os.Mkdir(broken, 0o700))
	noErr(t, os.WriteFile(filepath.Join(broken, "manifest.json"), []byte("{not json"), 0o600))
	restoredState := filepath.Join(root, "restored", "state")
	restoredRepositories := filepath.Join(root, "restored", "repositories")
	restore := func(input string) func() error {
		return func() error {
			return restoreState([]string{"--input", input, "--state-dir", restoredState, "--repository-root", restoredRepositories, "--verify", "--temp-dir", temporary, "--json"})
		}
	}
	problem, exit := refused(restore(broken))
	details, _ := problem["details"].(map[string]any)
	if problem["code"] != "backup_not_verified" || exit != 1 || details["verified"] != false || !strings.Contains(details["error"].(string), "decode backup manifest") {
		t.Fatalf("restore of a broken backup error=%v exit=%d", problem, exit)
	}
	if _, err := os.Lstat(filepath.Dir(restoredState)); !os.IsNotExist(err) {
		t.Fatalf("the refused restore created its folder (%v)", err)
	}
	if problem, exit := refused(func() error { return restoreState([]string{"--input", backup, "--json"}) }); problem["code"] != "invalid_arguments" || exit != 1 {
		t.Fatalf("restore without targets error=%v exit=%d", problem, exit)
	}

	output, err = captureStdout(restore(backup))
	var restored restoreResult
	if err != nil || json.Unmarshal([]byte(output), &restored) != nil || !restored.OK || restored.StateDir != restoredState || restored.RepositoryRoot != restoredRepositories ||
		restored.Verification == nil || !restored.Verification.Verified || !reflect.DeepEqual(restored.Notes, recovery.RestoreNotes()) ||
		len(restored.ObjectWarnings) != 0 || strings.Contains(output, "object_warnings") {
		t.Fatalf("restore output=%q err=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(restoredState, "owngit.sqlite")); err != nil {
		t.Fatalf("restored state: %v", err)
	}
	problem, exit = refused(func() error {
		return restoreState([]string{"--input", backup, "--state-dir", restoredState, "--repository-root", restoredRepositories, "--json"})
	})
	if problem["code"] != "restore_failed" || exit != 1 {
		t.Fatalf("restore over a restored state error=%v exit=%d", problem, exit)
	}
}

// An interrupted command with a coded error writes it as JSON and still
// exits as a process an interrupt stopped.
func TestInterruptedCodedErrorIsWrittenAndExits130(t *testing.T) {
	err := &apiclient.Error{Code: "interrupted", Message: "the restore was interrupted", Cause: &recovery.Interrupted{What: "restore"}}
	var reported strings.Builder
	if code := reportError(&reported, err); code != 130 || !strings.Contains(reported.String(), `"code":"interrupted"`) {
		t.Fatalf("exit=%d reported=%q", code, reported.String())
	}
}

// When an elevated Windows command cannot start its copy without
// administrator rights, a command given --json still prints a coded JSON
// error, and a text command still gets the plain error; the exit status of
// a copy that ran is passed on unchanged.
func TestFailedStartWithoutAdminRightsIsAJSONError(t *testing.T) {
	previousProbe, previousRun := probeEnvironment, runWithoutAdminRights
	t.Cleanup(func() { probeEnvironment, runWithoutAdminRights = previousProbe, previousRun })
	probeEnvironment = func() service.Environment { return service.Environment{Windows: true, Elevated: true} }
	runWithoutAdminRights = func([]string) (int, error) { return 0, errors.New("synthetic launch failure") }
	for _, command := range []string{"backup", "restore"} {
		arguments := []string{command, "--state-dir", filepath.Join(t.TempDir(), "state")}
		printed, err := captureStdout(func() error { return run(append(arguments, "--json")) })
		var reported strings.Builder
		var answer struct {
			OK    bool `json:"ok"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if exit := reportError(&reported, err); exit != 1 || printed != "" || json.Unmarshal([]byte(reported.String()), &answer) != nil ||
			answer.OK || answer.Error.Code != "privilege_drop_failed" || !strings.Contains(answer.Error.Message, "synthetic launch failure") {
			t.Fatalf("%s --json: printed=%q reported=%q exit=%d err=%v", command, printed, reported.String(), exit, err)
		}
		err = run(arguments)
		var coded interface{ ErrorCode() string }
		if err == nil || errors.As(err, &coded) {
			t.Fatalf("%s without --json err=%v", command, err)
		}
	}
	runWithoutAdminRights = func([]string) (int, error) { return 3, nil }
	var reported strings.Builder
	if exit := reportError(&reported, run([]string{"restore", "--json"})); exit != 3 || reported.String() != "" {
		t.Fatalf("copy exit=%d reported=%q", exit, reported.String())
	}
}

// A JSON error keeps the context that wraps its coded error, such as the
// command that runs it as the state's owner.
func TestJSONErrorKeepsWrappedContext(t *testing.T) {
	var reported strings.Builder
	err := fmt.Errorf("%w: sudo -u owngit owngit backup", cliProblem("state_unavailable", "the state belongs to another account"))
	if !writeStructuredCommandError(&reported, err) || !strings.Contains(reported.String(), "the state belongs to another account: sudo -u owngit owngit backup") {
		t.Fatalf("reported %q", reported.String())
	}
}
