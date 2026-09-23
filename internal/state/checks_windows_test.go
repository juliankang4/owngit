//go:build windows

package state

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsLegacyLogsJunctionRemainsUntouched(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	target := t.TempDir()
	legacyFiles := map[string][]byte{
		"sentinel":           []byte("unchanged"),
		".pending.staging-1": []byte("legacy staging bytes"),
	}
	for name, content := range legacyFiles {
		noErr(t, os.WriteFile(filepath.Join(target, name), content, 0o600))
	}
	junction := filepath.Join(store.dir, "logs")
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create logs junction: %v: %s", err, output)
	}
	junctionName, err := windows.UTF16PtrFromString(junction)
	noErr(t, err)
	attributes, err := windows.GetFileAttributes(junctionName)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatalf("logs junction attributes=%#x err=%v", attributes, err)
	}

	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}))
	task, err := store.CreateTask(ctx, "project", "Ignore a legacy logs junction", now)
	noErr(t, err)
	attempt := attemptFor(task, strings.Repeat("7", 40), now, AttemptFailed)
	_, stored := recordAttemptWithLog(t, store, attempt, "database bytes")
	if stored.LogID != attempt.ID || stored.LogError != "" {
		t.Fatalf("database raw log outcome=%+v", stored)
	}
	if removed, err := store.PruneCheckLogs(ctx, now.Add(100*365*24*time.Hour)); err != nil || removed != 1 {
		t.Fatalf("database prune removed=%d err=%v", removed, err)
	}

	for name, want := range legacyFiles {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("legacy junction file %q=%q err=%v", name, got, err)
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != len(legacyFiles) {
		t.Fatalf("legacy junction entries=%d err=%v", len(entries), err)
	}
	attributes, err = windows.GetFileAttributes(junctionName)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatalf("logs junction changed: attributes=%#x err=%v", attributes, err)
	}
}
