package recovery

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// A check policy saved under raised check ceilings is backed up in format
// 11 and restores on a computer with the default ceilings, where it stays
// readable but admits no job until the owner raises the ceiling there.
func TestCheckPolicyAboveTheDefaultCeilingsRestoresAndWaits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	now := time.Unix(1_800_000_000, 0)
	raised := state.DefaultCheckCeilings
	raised.TimeoutMS, raised.QueueLimit = 2*24*60*60*1000, 5000
	noErr(t, store.SavePolicies(ctx, state.PolicyChange{CheckCeilings: &raised}))
	policy, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: raised.TimeoutMS, MaxOutputLimitBytes: 65536, QueueLimit: 5000, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, now)
	noErr(t, err)
	_, err = store.GrantCheckConsent(ctx, "project", now)
	noErr(t, err)
	request := state.CheckJobRequest{
		RepositoryID: "project", Trigger: "push", EventKey: "refs/heads/main@" + strings.Repeat("a", 40),
		SourceOID: strings.Repeat("a", 40), TriggerRef: "main", WorkflowDigest: strings.Repeat("b", 64),
		Checks:    []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
		TimeoutMS: raised.TimeoutMS,
	}
	job, _, err := store.AdmitCheckJob(ctx, request, now)
	noErr(t, err)
	if job.Limits.TimeoutMS != raised.TimeoutMS {
		t.Fatalf("admitted time limit %d, want %d", job.Limits.TimeoutMS, raised.TimeoutMS)
	}

	backup := filepath.Join(root, "backup")
	_, err = CreateWithReport(ctx, store, manager, backup)
	noErr(t, err)
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if manifest.Version != backupVersion {
		t.Fatalf("backup version %d, want %d", manifest.Version, backupVersion)
	}

	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	noErr(t, Restore(ctx, backup, restoredState, canonicalTestTarget(t, filepath.Join(root, "restored-repositories")), ""))
	restored, err := state.Open(ctx, restoredState)
	noErr(t, err)
	defer restored.Close()
	if ceilings, err := restored.CheckCeilings(ctx); err != nil || ceilings != state.DefaultCheckCeilings {
		t.Fatalf("restored ceilings %+v, %v; want the defaults", ceilings, err)
	}
	restoredPolicy, exists, err := restored.CheckPolicy(ctx, "project")
	if err != nil || !exists || restoredPolicy.Digest != policy.Digest || restoredPolicy.ConsentActive {
		t.Fatalf("restored policy %+v, %v, %v", restoredPolicy, exists, err)
	}
	restoredJob, exists, err := restored.CheckJob(ctx, "project", job.ID)
	if err != nil || !exists || restoredJob.Limits != job.Limits {
		t.Fatalf("restored job %+v, %v, %v", restoredJob, exists, err)
	}
	_, err = restored.GrantCheckConsent(ctx, "project", now)
	noErr(t, err)
	request.EventKey, request.SourceOID = "refs/heads/main@"+strings.Repeat("c", 40), strings.Repeat("c", 40)
	if _, _, err := restored.AdmitCheckJob(ctx, request, now); !errors.Is(err, state.ErrCheckCeilingExceeded) {
		t.Fatalf("admission under the default ceilings: %v", err)
	}
	listed, err := restored.RepositoriesAboveCheckCeilings(ctx)
	if err != nil || len(listed) != 1 || listed[0] != "project" {
		t.Fatalf("repositories above the ceilings: %v, %v", listed, err)
	}
}
