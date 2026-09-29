package recovery

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/state"
)

// The server-wide policies of Settings belong to the installation, like the
// administrator confirmation: a backup does not carry them, and a restored
// installation starts with every default.
func TestServerPoliciesStayWithTheirInstallation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	session, branch, logs := state.Session30Days, "trunk", state.KeepCheckLogs
	noErr(t, store.SavePolicies(ctx, state.PolicyChange{
		Session: &session, InitialBranch: &branch, CheckLogs: &logs,
		GitTransfer: &state.GitTransferLimits{MaximumBytes: 64 << 30, Operation: 24 * time.Hour},
	}))
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
}
