package recovery

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"owngit/internal/state"
)

// The server-wide policies of Settings belong to the installation, like the
// administrator confirmation: a backup does not carry them, and a restored
// installation starts with every default. A repository's own kept history
// choice and default branch protection describe the repository and come
// back with it.
func TestServerPoliciesStayWithTheirInstallation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	session, branch, logs, off := state.Session30Days, "trunk", state.KeepCheckLogs, false
	noErr(t, store.SavePolicies(ctx, state.PolicyChange{
		Session: &session, InitialBranch: &branch, CheckLogs: &logs,
		GitTransfer: &state.GitTransferLimits{MaximumBytes: 64 << 30, Operation: 24 * time.Hour, PerRepository: 4, ExtraSlots: 1, Idle: time.Minute, QueueWait: 90 * time.Second}, KeptHistory: &off,
		DeleteRequiresName: &off, CrossSiteLinks: pointerTo(state.CrossSiteLax),
		LoginLimits: &state.LoginLimits{Attempts: 100, Window: time.Minute, Pause: time.Minute},
		Browse:      &state.BrowseLimits{RawBytes: 256 << 20, FileBytes: 64 << 20, CommitPatchBytes: 64 << 20, FilePatchBytes: 64 << 20, CommitFileBytes: 16 << 20, CompareBytes: 64 << 20, CompareTime: time.Minute},
		Maintenance: &state.Maintenance{WindowStart: 0, WindowEnd: 23, Idle: time.Minute, CommandTime: time.Minute, FullRepackTime: time.Minute, PackThreshold: 2},
		Cleanup:     &state.UnusedObjectCleanup{Enabled: true, Grace: 2 * 24 * time.Hour},
	}))
	on, protect := state.KeptHistoryOn, true
	_, err := store.SaveRepositoryRefPolicy(ctx, "project", state.RepositoryRefPolicyChange{KeptHistory: &on, ProtectDefaultBranch: &protect, ExtraRefPrefixes: &[]string{"refs/notes/"}})
	noErr(t, err)
	backup := filepath.Join(root, "backup")
	noErr(t, Create(ctx, store, manager, backup))
	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	noErr(t, Restore(ctx, backup, restoredState, canonicalTestTarget(t, filepath.Join(root, "restored-repositories")), ""))
	restored, err := state.Open(ctx, restoredState)
	noErr(t, err)
	defer restored.Close()
	if got, err := restored.GeneralSession(ctx); err != nil || got != state.DefaultGeneralSession {
		t.Fatalf("session=%q err=%v", got, err)
	}
	if got, err := restored.InitialBranch(ctx); err != nil || got != state.DefaultInitialBranch {
		t.Fatalf("initial branch=%q err=%v", got, err)
	}
	if got, err := restored.GitTransferLimits(ctx); err != nil || got != state.DefaultGitTransferLimits {
		t.Fatalf("transfer limits=%+v err=%v", got, err)
	}
	if got, err := restored.CheckLogRetention(ctx); err != nil || got != state.DefaultCheckLogRetention {
		t.Fatalf("raw log retention=%q err=%v", got, err)
	}
	if got, err := restored.KeptHistory(ctx); err != nil || !got {
		t.Fatalf("server kept history=%v err=%v", got, err)
	}
	if got, err := restored.DeleteRequiresName(ctx); err != nil || !got {
		t.Fatalf("delete choice=%v err=%v", got, err)
	}
	if got, err := restored.LoginLimits(ctx); err != nil || got != state.DefaultLoginLimits {
		t.Fatalf("login limits=%+v err=%v", got, err)
	}
	if got, err := restored.CrossSiteLinks(ctx); err != nil || got != state.DefaultCrossSiteLinks {
		t.Fatalf("cross-site choice=%q err=%v", got, err)
	}
	if got, err := restored.BrowseLimits(ctx); err != nil || got != state.DefaultBrowseLimits {
		t.Fatalf("browsing limits=%+v err=%v", got, err)
	}
	if got, err := restored.Maintenance(ctx); err != nil || got != state.DefaultMaintenance {
		t.Fatalf("maintenance=%+v err=%v", got, err)
	}
	if got, err := restored.UnusedObjectCleanup(ctx); err != nil || got != state.DefaultUnusedObjectCleanup {
		t.Fatalf("cleanup=%+v err=%v", got, err)
	}
	if got, err := restored.RepositoryExtraRefPrefixes(ctx, "project"); err != nil || !reflect.DeepEqual(got, []string{"refs/notes/"}) {
		t.Fatalf("repository ref namespaces=%q err=%v", got, err)
	}
	if got, err := restored.RepositoryRefPolicy(ctx, "project"); err != nil || !reflect.DeepEqual(got, state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryOn, ProtectDefaultBranch: true}) {
		t.Fatalf("repository choices=%+v err=%v", got, err)
	}
}

func pointerTo[T any](value T) *T { return &value }
