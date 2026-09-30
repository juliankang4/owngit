package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/backups"
	"owngit/internal/state"
)

type backupStatusJSON struct {
	OK       bool                 `json:"ok"`
	Schedule backups.ScheduleView `json:"schedule"`
	Running  *backups.RunView     `json:"running"`
	LastRun  *backups.RunView     `json:"last_run"`
	Verified *backups.RunView     `json:"last_verified"`
	NextRun  *time.Time           `json:"next_run"`
}

func decodeBackupJSON(t *testing.T, response *http.Response, destination any) int {
	t.Helper()
	defer response.Body.Close()
	noErr(t, json.NewDecoder(response.Body).Decode(destination))
	return response.StatusCode
}

// The owner API shows backups not configured, then off with the defaults,
// and starts a backup only once a folder is set. Everything needs the
// administrator except the summary, which general access reads too.
func TestBackupAPIStatesAndSchedule(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/api/v1/backups"

	var status backupStatusJSON
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodGet, base, nil, "admin-password"), &status); code != http.StatusOK ||
		!status.OK || status.Schedule.State != backups.ScheduleNotConfigured || status.LastRun != nil || status.NextRun != nil {
		t.Fatalf("status %d %+v", code, status)
	}
	if code, errorCode := checkStatus(t, adminAPIRequest(t, http.MethodPost, base+"/runs", nil, "admin-password")); code != http.StatusConflict || errorCode != "backup_not_configured" {
		t.Fatalf("back up now without a folder: %d %s", code, errorCode)
	}
	for _, path := range []string{"", "/runs", "/schedule"} {
		if code, errorCode := checkStatus(t, apiRequest(t, http.MethodGet, base+path, nil, "shared-password", "")); code != http.StatusUnauthorized || errorCode != "admin_authentication_required" {
			t.Fatalf("%s with general access: %d %s", path, code, errorCode)
		}
	}
	if code, errorCode := checkStatus(t, adminAPIRequest(t, http.MethodPatch, base+"/schedule", map[string]any{"keep": 3}, "admin-password")); code != http.StatusBadRequest || errorCode != "invalid_backup_schedule" {
		t.Fatalf("schedule without a folder: %d %s", code, errorCode)
	}
	if code, errorCode := checkStatus(t, adminAPIRequest(t, http.MethodPatch, base+"/schedule", map[string]any{"destination": t.TempDir(), "interval": "2d"}, "admin-password")); code != http.StatusBadRequest || errorCode != "invalid_backup_schedule" {
		t.Fatalf("unknown interval: %d %s", code, errorCode)
	}

	// Off from the start, so that no scheduled backup starts meanwhile.
	destination := filepath.Join(t.TempDir(), "backups")
	var saved struct {
		Schedule backups.ScheduleView `json:"schedule"`
		Warnings []string             `json:"warnings"`
	}
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodPatch, base+"/schedule", map[string]any{"destination": destination, "scheduled": "off"}, "admin-password"), &saved); code != http.StatusOK ||
		saved.Schedule.State != backups.ScheduleOff || saved.Schedule.Interval != "1d" || saved.Schedule.Keep != 7 || saved.Schedule.Verify == nil || !*saved.Schedule.Verify ||
		saved.Schedule.Destination != destination || len(saved.Warnings) != 1 || !strings.Contains(saved.Warnings[0], "scheduled recovery copies") {
		t.Fatalf("first schedule %d %+v", code, saved)
	}
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodGet, base, nil, "admin-password"), &status); code != http.StatusOK ||
		status.Schedule.State != backups.ScheduleOff || status.NextRun != nil {
		t.Fatalf("status while off %d %+v", code, status)
	}
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodPatch, base+"/schedule", map[string]any{"verify": "off", "interval": "7d", "keep": 3}, "admin-password"), &saved); code != http.StatusOK ||
		saved.Schedule.State != backups.ScheduleOff || saved.Schedule.Interval != "7d" || saved.Schedule.Keep != 3 || len(saved.Warnings) != 1 || !strings.Contains(saved.Warnings[0], "rehearsing a restore") {
		t.Fatalf("verification off %d %+v", code, saved)
	}

	var started struct {
		Run backups.RunView `json:"run"`
	}
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodPost, base+"/runs", nil, "admin-password"), &started); code != http.StatusAccepted ||
		started.Run.Status != state.BackupRunning || started.Run.Kind != state.BackupRunManual || started.Run.Path != filepath.Join(destination, started.Run.BackupName) {
		t.Fatalf("back up now %d %+v", code, started)
	}
	// The administrator reads the status too, and every run in the list.
	var runs struct {
		Runs []backups.RunView `json:"runs"`
	}
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodGet, base+"/runs", nil, "admin-password"), &runs); code != http.StatusOK ||
		len(runs.Runs) != 1 || runs.Runs[0].ID != started.Run.ID {
		t.Fatalf("runs %d %+v", code, runs)
	}
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodGet, base, nil, "admin-password"), &status); code != http.StatusOK {
		t.Fatalf("status as administrator %d", code)
	}
}

// The summary that general access reads names no folder, repository or
// error text, also after a backup failed; the administrator reads all of
// it in the status.
func TestBackupSummaryNamesNoFolderOrMessage(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/api/v1/backups"
	destination := filepath.Join(t.TempDir(), "private-backups")
	noErr(t, fixture.store.SaveBackupSchedule(context.Background(), state.BackupSchedule{Destination: destination, Interval: 24 * time.Hour, Keep: 7, Verify: true, UpdatedAt: time.Now()}))
	run := state.BackupRun{ID: strings.Repeat("e", 32), Kind: state.BackupRunManual, Destination: destination, BackupName: "owngit-backup-secret-name", StartedAt: time.Now().Add(-time.Minute)}
	noErr(t, fixture.store.StartBackupRun(context.Background(), run))
	run.Status, run.Verification, run.FinishedAt = state.BackupFailed, state.BackupVerifyNotRun, time.Now()
	run.Message, run.HoldKnown, run.LongestHoldRepository = "could not read repository project-secret", true, "project"
	noErr(t, fixture.store.FinishBackupRun(context.Background(), run))

	response := apiRequest(t, http.MethodGet, base+"/summary", nil, "shared-password", "")
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	noErr(t, err)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("summary %d %s", response.StatusCode, body)
	}
	for _, secret := range []string{"private-backups", "owngit-backup-secret-name", "project", "could not read", run.ID} {
		if strings.Contains(string(body), secret) {
			t.Errorf("the summary names %q: %s", secret, body)
		}
	}
	var summary struct {
		OK       bool   `json:"ok"`
		Schedule string `json:"schedule"`
		LastRun  *struct {
			Kind, Status, Verification string
			FinishedAt                 time.Time `json:"finished_at"`
		} `json:"last_run"`
		LastVerifiedAt *time.Time `json:"last_verified_at"`
		NextRun        *time.Time `json:"next_run"`
	}
	noErr(t, json.Unmarshal(body, &summary))
	if !summary.OK || summary.Schedule != backups.ScheduleOff || summary.LastRun == nil || summary.LastRun.Status != state.BackupFailed ||
		summary.LastRun.Kind != state.BackupRunManual || summary.LastRun.FinishedAt.IsZero() || summary.LastVerifiedAt != nil || summary.NextRun != nil {
		t.Fatalf("summary: %s", body)
	}
	if code, errorCode := checkStatus(t, apiRequest(t, http.MethodGet, base, nil, "shared-password", "")); code != http.StatusUnauthorized || errorCode != "admin_authentication_required" {
		t.Fatalf("full status with general access: %d %s", code, errorCode)
	}
	var status backupStatusJSON
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodGet, base, nil, "admin-password"), &status); code != http.StatusOK ||
		status.LastRun == nil || status.LastRun.Message != run.Message || status.LastRun.Destination != destination {
		t.Fatalf("status as administrator %d %+v", code, status)
	}
	if code := decodeBackupJSON(t, adminAPIRequest(t, http.MethodGet, base+"/summary", nil, "admin-password"), &summary); code != http.StatusOK || summary.LastRun == nil {
		t.Fatalf("summary as administrator %d", code)
	}
}
