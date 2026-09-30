package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Nothing saved means the defaults: browsing limits as before, maintenance
// on from 03:00 to 05:00, cleanup off. A saved row that does not parse or
// is out of bounds is a PolicyError naming its group, never the default,
// and a saved value reads back as it was saved.
func TestGitStoragePoliciesReadDefaultsAndRefuseWhatTheyCannotUse(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if got, err := store.BrowseLimits(ctx); err != nil || got != DefaultBrowseLimits {
		t.Fatalf("browse=%+v err=%v", got, err)
	}
	if got, err := store.Maintenance(ctx); err != nil || got != DefaultMaintenance || !got.Enabled || got.WindowHours() != 2 {
		t.Fatalf("maintenance=%+v err=%v", got, err)
	}
	if got, err := store.UnusedObjectCleanup(ctx); err != nil || got.Enabled || got.Grace != 14*24*time.Hour {
		t.Fatalf("cleanup=%+v err=%v", got, err)
	}
	read := map[string]func() error{
		"browse_limits":         func() error { _, err := store.BrowseLimits(ctx); return err },
		"maintenance":           func() error { _, err := store.Maintenance(ctx); return err },
		"unused_object_cleanup": func() error { _, err := store.UnusedObjectCleanup(ctx); return err },
	}
	for _, stored := range []struct{ key, value string }{
		{"browse_limits", `{"raw_bytes":1048575}`}, {"browse_limits", `{"file_bytes":67108865}`}, {"browse_limits", `{"compare_seconds":4}`},
		{"browse_limits", `{"compare_seconds":9223372036854775807}`}, {"browse_limits", `{"markdown_bytes":1}`}, {"browse_limits", `[]`},
		{"maintenance", `{"window_start_hour":24}`}, {"maintenance", `{"window_start_hour":5}`}, {"maintenance", `{"idle_seconds":59}`},
		{"maintenance", `{"pack_threshold":1001}`}, {"maintenance", `{"enabled":null}`}, {"maintenance", `{"retry_seconds":60}`},
		{"unused_object_cleanup", `{"grace_days":1}`}, {"unused_object_cleanup", `{"grace_days":-9223372036854775808}`}, {"unused_object_cleanup", `{"enabled":1}`},
	} {
		noErr(t, store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, stored.key, stored.value))
		var policyErr *PolicyError
		if err := read[stored.key](); !errors.As(err, &policyErr) || policyErr.Setting() != stored.key {
			t.Fatalf("%s=%s read with err=%v, want a PolicyError", stored.key, stored.value, err)
		}
	}
	browse := BrowseLimits{RawBytes: MaximumRawBytes, FileBytes: MinimumBrowseBytes, CommitPatchBytes: MaximumBrowseBytes, FilePatchBytes: MaximumBrowseBytes,
		CommitFileBytes: MinimumCommitFileBytes, CompareBytes: MaximumBrowseBytes, CompareTime: MaximumCompareTime}
	maintenance := Maintenance{WindowStart: 22, WindowEnd: 6, Idle: MaximumMaintenanceTime, CommandTime: MinimumMaintenanceTime, FullRepackTime: MaximumMaintenanceTime, PackThreshold: MinimumPackThreshold}
	cleanup := UnusedObjectCleanup{Enabled: true, Grace: MaximumCleanupGrace}
	noErr(t, store.SavePolicies(ctx, PolicyChange{Browse: &browse, Maintenance: &maintenance, Cleanup: &cleanup}))
	if got, err := store.BrowseLimits(ctx); err != nil || got != browse || !got.Looser() {
		t.Fatalf("browse=%+v err=%v", got, err)
	}
	if got, err := store.Maintenance(ctx); err != nil || got != maintenance || got.WindowHours() != 8 || !got.Looser() {
		t.Fatalf("maintenance=%+v err=%v", got, err)
	}
	if got, err := store.UnusedObjectCleanup(ctx); err != nil || got != cleanup {
		t.Fatalf("cleanup=%+v err=%v", got, err)
	}
	tooWide := DefaultBrowseLimits
	tooWide.CompareTime = 61 * time.Second
	if err := store.SavePolicies(ctx, PolicyChange{Browse: &tooWide, Cleanup: &DefaultUnusedObjectCleanup}); err == nil {
		t.Fatal("a comparison time over a minute was saved")
	}
	if got, err := store.UnusedObjectCleanup(ctx); err != nil || got != cleanup {
		t.Fatalf("a refused change saved cleanup: %+v err=%v", got, err)
	}
}
