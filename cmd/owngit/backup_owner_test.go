package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// The backup commands of a running server print its JSON, and the MCP
// backup_status tool, with general access, returns the summary without the
// folder.
func TestBackupCommandsTalkToTheServer(t *testing.T) {
	serverURL, _ := startRepositoryCLIServer(t, "")
	adminFile := filepath.Join(t.TempDir(), "admin-password")
	noErr(t, os.WriteFile(adminFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminFile, false))
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", adminFile}
	command := func(arguments ...string) []string { return append(arguments, remote...) }

	status := cliOutput(t, backupState, command("status")...)
	if !strings.Contains(status, `"state":"not_configured"`) || !strings.Contains(status, `"last_run":null`) {
		t.Fatalf("status: %s", status)
	}
	session := startMCPSession(t, mcpOptions{server: serverURL, acceptInsecureHTTP: true})
	if text, isError := session.call("backup_status", nil); isError || text != `{"ok":true,"schedule":"not_configured","last_run":null,"last_verified_at":null,"next_run":null}` {
		t.Fatalf("backup_status isError=%v: %s", isError, text)
	}
	for _, refused := range []struct {
		arguments []string
		code      string
	}{
		{command("now"), "backup_not_configured"},
		{command("schedule", "set", "--keep", "3"), "invalid_backup_schedule"},
		{command("schedule", "set", "--destination", "relative"), "invalid_backup_schedule"},
	} {
		_, err := captureStdout(func() error { return backupState(refused.arguments) })
		var problem *apiclient.Error
		if !errors.As(err, &problem) || problem.Code != refused.code {
			t.Errorf("%v: %v", refused.arguments, err)
		}
	}
	if _, err := captureStdout(func() error { return backupState(command("schedule", "set", "--keep", "many")) }); err == nil {
		t.Error("--keep many was accepted")
	}

	destination := filepath.Join(t.TempDir(), "backups")
	var saved struct {
		Schedule struct {
			State       string `json:"state"`
			Destination string `json:"destination"`
			Interval    string `json:"interval"`
			Keep        int    `json:"keep"`
			Verify      bool   `json:"verify"`
		} `json:"schedule"`
		Warnings []string `json:"warnings"`
	}
	output := cliOutput(t, backupState, command("schedule", "set", "--destination", destination, "--interval", "12h", "--keep", "2", "--verify", "off")...)
	noErr(t, json.Unmarshal([]byte(output), &saved))
	if saved.Schedule.State != "on" || saved.Schedule.Destination != destination || saved.Schedule.Interval != "12h" || saved.Schedule.Keep != 2 ||
		saved.Schedule.Verify || len(saved.Warnings) != 1 {
		t.Fatalf("schedule set: %s", output)
	}
	if text, isError := session.call("backup_status", nil); isError || strings.Contains(text, "backups") || !strings.Contains(text, `"schedule":"on"`) {
		t.Fatalf("backup_status after set isError=%v: %s", isError, text)
	}
	output = cliOutput(t, backupState, command("schedule", "off")...)
	noErr(t, json.Unmarshal([]byte(output), &saved))
	if saved.Schedule.State != "off" || saved.Schedule.Keep != 2 || len(saved.Warnings) != 1 {
		t.Fatalf("schedule off: %s", output)
	}
	if shown := cliOutput(t, backupState, command("schedule", "show")...); !strings.Contains(shown, `"state":"off"`) {
		t.Fatalf("schedule show: %s", shown)
	}
	if runs := cliOutput(t, backupState, command("runs")...); !strings.HasPrefix(runs, `{"ok":true,"runs":[`) {
		t.Fatalf("runs: %s", runs)
	}
}

// A serving OwnGit records a backup that a stopped process left running as
// interrupted, does not repeat its scheduled slot, and makes a verified
// backup when asked.
func TestServeRunsBackups(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositoriesRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoriesRoot, 0o700))
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoriesRoot, "open", "", adminHash, true))
	runner, err := gitexec.New("", filepath.Join(stateDir, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	_, err = manager.Create(ctx, "project", "")
	noErr(t, err)
	destination := filepath.Join(root, "backups")
	noErr(t, store.SaveBackupSchedule(ctx, state.BackupSchedule{Enabled: true, Interval: 24 * time.Hour, Destination: destination, Keep: 7, Verify: true, UpdatedAt: time.Now()}))
	left := state.BackupRun{ID: strings.Repeat("d", 32), Kind: state.BackupRunScheduled, Destination: destination, BackupName: "owngit-backup-left", StartedAt: time.Now().Add(-time.Hour)}
	noErr(t, store.StartBackupRun(ctx, left))
	noErr(t, store.Close())

	instance := startServed(t, stateDir)
	instance.waitForLog("recorded as interrupted", time.Minute)
	adminFile := filepath.Join(root, "admin-password")
	noErr(t, os.WriteFile(adminFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminFile, false))
	remote := []string{"--server", instance.url, "--accept-insecure-http", "--password-file", adminFile}
	var started struct {
		Run struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"run"`
	}
	noErr(t, json.Unmarshal([]byte(cliOutput(t, backupState, append([]string{"now"}, remote...)...)), &started))
	instance.waitForLog("backup "+started.Run.Path+" ", 5*time.Minute)

	var status struct {
		LastRun struct {
			ID, Status, Verification string
			LongestHoldMS            *int64 `json:"longest_hold_ms"`
		} `json:"last_run"`
		LastVerified *struct{ ID string } `json:"last_verified"`
		NextRun      time.Time            `json:"next_run"`
	}
	output := cliOutput(t, backupState, append([]string{"status"}, remote...)...)
	noErr(t, json.Unmarshal([]byte(output), &status))
	if status.LastRun.ID != started.Run.ID || status.LastRun.Status != "succeeded" || status.LastRun.Verification != "passed" ||
		status.LastRun.LongestHoldMS == nil || status.LastVerified == nil || status.LastVerified.ID != started.Run.ID ||
		!status.NextRun.Equal(left.StartedAt.Truncate(time.Second).Add(24*time.Hour)) {
		t.Fatalf("status: %s", output)
	}
	var runs struct {
		Runs []struct{ ID, Status, Message string } `json:"runs"`
	}
	noErr(t, json.Unmarshal([]byte(cliOutput(t, backupState, append([]string{"runs"}, remote...)...)), &runs))
	if len(runs.Runs) != 2 || runs.Runs[1].ID != left.ID || runs.Runs[1].Status != "interrupted" {
		t.Fatalf("runs: %+v", runs)
	}

	// The backup is verified again on request.
	if output := cliOutput(t, backupState, append([]string{"check", "--run", started.Run.ID}, remote...)...); !strings.Contains(output, `"status":"running"`) {
		t.Fatalf("check: %s", output)
	}
	instance.waitForLog("verification of backup "+started.Run.Path+" passed", 5*time.Minute)

	// A downloaded backup, unpacked with tar, passes owngit backup verify
	// on this computer, and uploaded again it is verified and comes with
	// the command that restores it.
	archive := filepath.Join(root, "backup.tar")
	var downloaded struct {
		Path  string `json:"path"`
		Bytes int64  `json:"bytes"`
	}
	noErr(t, json.Unmarshal([]byte(cliOutput(t, backupState, append([]string{"download", "--run", started.Run.ID, "--output", archive}, remote...)...)), &downloaded))
	if downloaded.Path != archive || downloaded.Bytes == 0 {
		t.Fatalf("download: %+v", downloaded)
	}
	if _, err := captureStdout(func() error {
		return backupState(append([]string{"download", "--run", started.Run.ID, "--output", archive}, remote...))
	}); err == nil {
		t.Fatal("a download replaced a file that exists")
	}
	unpacked := filepath.Join(root, "unpacked")
	noErr(t, os.Mkdir(unpacked, 0o700))
	untar, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(untar, "tar", "-xf", archive, "-C", unpacked).CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, output)
	}
	if output := cliOutput(t, backupState, "verify", filepath.Join(unpacked, filepath.Base(started.Run.Path))); !strings.Contains(output, "passed") {
		t.Fatalf("backup verify of the unpacked download: %s", output)
	}
	if output := cliOutput(t, backupState, append([]string{"upload", "--input", archive}, remote...)...); !strings.Contains(output, `"status":"verifying"`) {
		t.Fatalf("upload: %s", output)
	}
	instance.waitForLog("the uploaded backup "+filepath.Base(started.Run.Path)+" passed verification", 5*time.Minute)
	var uploaded struct {
		Upload struct {
			Status, Path string
		} `json:"upload"`
		Command string `json:"upload_restore_command"`
	}
	output = cliOutput(t, backupState, append([]string{"status"}, remote...)...)
	noErr(t, json.Unmarshal([]byte(output), &uploaded))
	moved := filepath.Join(stateDir+".before-restore", "backup-uploads", filepath.Base(started.Run.Path))
	if uploaded.Upload.Status != "passed" || !strings.Contains(uploaded.Command, "owngit restore --input "+commandWord(moved)+" --state-dir "+commandWord(stateDir)) ||
		!strings.HasSuffix(uploaded.Command, " --verify") {
		t.Fatalf("status after the upload: %s", output)
	}
}

// The restore steps name the folders that move aside, a restore command
// that reads an uploaded backup where it is after that move, and the
// service commands only for a service.
func TestRestoreGuideMovesTheCurrentFoldersAside(t *testing.T) {
	stateDir, repositories := filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "repositories")
	elsewhere := filepath.Join(t.TempDir(), "backups", "owngit-backup-1")
	guide := restoreGuide(stateDir, false)(elsewhere, repositories)
	if guide.Stop != "" || guide.Start != "" || guide.MovedState != stateDir+".before-restore" || guide.MovedRepositories != repositories+".before-restore" ||
		guide.Command != "owngit restore --input "+commandWord(elsewhere)+" --state-dir "+commandWord(stateDir)+" --repository-root "+commandWord(repositories)+" --verify" {
		t.Fatalf("guide: %+v", guide)
	}
	uploaded := restoreGuide(stateDir, true)(filepath.Join(stateDir, "backup-uploads", "b"), repositories)
	if uploaded.Stop != "owngit service stop" || uploaded.Start != "owngit service start" ||
		!strings.Contains(uploaded.Command, "--input "+commandWord(filepath.Join(stateDir+".before-restore", "backup-uploads", "b"))) {
		t.Fatalf("uploaded guide: %+v", uploaded)
	}
}

// Every path in a restore command stays one literal argument: nothing in
// it is expanded by PowerShell on Windows or by a POSIX shell elsewhere.
func TestRestoreCommandWordsAreLiteral(t *testing.T) {
	path := `D:\Data\o'wner\$(Invoke-Evil) ` + "`" + `%TEMP% & x`
	if got := shellWord("windows", path); got != `'D:\Data\o''wner\$(Invoke-Evil) `+"`"+`%TEMP% & x'` {
		t.Fatalf("PowerShell word: %s", got)
	}
	// PowerShell also ends a literal string at the single quotation marks.
	for _, mark := range []string{"\u2018", "\u2019", "\u201a", "\u201b"} {
		path := `D:\Data\a` + mark + `; Invoke-Evil; ` + mark + `b`
		want := `'D:\Data\a` + mark + mark + `; Invoke-Evil; ` + mark + mark + `b'`
		if got := shellWord("windows", path); got != want {
			t.Fatalf("PowerShell word with %U: %s", []rune(mark)[0], got)
		}
	}
	if got := shellWord("linux", "/srv/o'wner/$(evil) `x`"); got != `'/srv/o'\''wner/$(evil) `+"`x`'" {
		t.Fatalf("POSIX word: %s", got)
	}
	if commandShell("windows") != "PowerShell" || commandShell("darwin") != "" {
		t.Fatal("the shell is not named for Windows only")
	}
}
