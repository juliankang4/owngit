package state

import (
	"context"
	"errors"
	"testing"
)

// A repository follows the server's kept history unless it chooses for
// itself, and protects its default branch only when it says so. A stored
// value that cannot be used is a PolicyError wherever it is read, never the
// default, and saving both choices replaces it.
func TestRefWritesFollowTheServerDefaultAndTheRepositoryChoice(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	noErr(t, store.Exec(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES('project','project','',1)`))
	writes := func() RefWrites {
		t.Helper()
		got, err := store.RefWrites(ctx, "project")
		noErr(t, err)
		return got
	}
	if got := writes(); got != (RefWrites{KeepHistory: true}) {
		t.Fatalf("defaults = %+v", got)
	}
	off, on, follow := false, KeptHistoryOn, KeptHistoryDefault
	noErr(t, store.SavePolicies(ctx, PolicyChange{KeptHistory: &off}))
	if got := writes(); got.KeepHistory {
		t.Fatal("a repository that follows the server kept history while the server does not")
	}
	protect := true
	saved, err := store.SaveRepositoryRefPolicy(ctx, "project", RepositoryRefPolicyChange{KeptHistory: &on, ProtectDefaultBranch: &protect})
	noErr(t, err)
	if saved != (RefPolicySave{Saved: RepositoryRefPolicy{KeptHistory: KeptHistoryOn, ProtectDefaultBranch: true}, Now: RefWrites{KeepHistory: true, ProtectDefaultBranch: true}}) {
		t.Fatalf("saved = %+v", saved)
	}
	if got := writes(); got != (RefWrites{KeepHistory: true, ProtectDefaultBranch: true}) {
		t.Fatalf("repository on, server off = %+v", got)
	}
	// A change that names one choice keeps the other.
	saved, err = store.SaveRepositoryRefPolicy(ctx, "project", RepositoryRefPolicyChange{KeptHistory: &follow})
	noErr(t, err)
	if saved != (RefPolicySave{Saved: RepositoryRefPolicy{KeptHistory: KeptHistoryDefault, ProtectDefaultBranch: true}, Now: RefWrites{ProtectDefaultBranch: true}, KeptHistoryOff: true}) {
		t.Fatalf("saved = %+v", saved)
	}
	var retain any
	noErr(t, store.db.QueryRowContext(ctx, `SELECT retain_history FROM repository_policies WHERE repository_id='project'`).Scan(&retain))
	if retain != nil {
		t.Fatalf("following the server stored %v, want NULL", retain)
	}

	var policyErr *PolicyError
	noErr(t, store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('retain_history','maybe') ON CONFLICT(key) DO UPDATE SET value=excluded.value`))
	if _, err := store.RefWrites(ctx, "project"); !errors.As(err, &policyErr) || policyErr.Setting() != "kept_history" {
		t.Fatalf("an unreadable server default read with err=%v", err)
	}
	// A change whose result would follow the unreadable server default
	// saves nothing.
	if _, err := store.SaveRepositoryRefPolicy(ctx, "project", RepositoryRefPolicyChange{ProtectDefaultBranch: &off}); !errors.As(err, &policyErr) || policyErr.Setting() != "kept_history" {
		t.Fatalf("a change following an unreadable server default: err=%v", err)
	}
	if got, err := store.RepositoryRefPolicy(ctx, "project"); err != nil || !got.ProtectDefaultBranch {
		t.Fatalf("a refused change was saved: %+v err=%v", got, err)
	}
	// A repository with its own choice does not need the server default,
	// and turning protection off with it says so; kept history, which was
	// unknown before, counts as turned off.
	offChoice := KeptHistoryOff
	saved, err = store.SaveRepositoryRefPolicy(ctx, "project", RepositoryRefPolicyChange{KeptHistory: &offChoice, ProtectDefaultBranch: &off})
	noErr(t, err)
	if !saved.KeptHistoryOff || !saved.ProtectionOff || saved.Now != (RefWrites{}) {
		t.Fatalf("explicit choices with an unreadable server default = %+v", saved)
	}
	noErr(t, store.Exec(ctx, `UPDATE repository_policies SET protect_default_branch=1 WHERE repository_id='project'`))
	if got := writes(); got != (RefWrites{ProtectDefaultBranch: true}) {
		t.Fatalf("repository off with an unreadable server default = %+v", got)
	}
	keep := true
	noErr(t, store.SavePolicies(ctx, PolicyChange{KeptHistory: &keep}))

	for _, stored := range []string{"protect_default_branch=5", "protect_default_branch=1.5", "retain_history='yes'"} {
		noErr(t, store.Exec(ctx, `PRAGMA ignore_check_constraints=ON; UPDATE repository_policies SET retain_history=NULL,protect_default_branch=1,`+stored+` WHERE repository_id='project'; PRAGMA ignore_check_constraints=OFF`))
		if _, err := store.RefWrites(ctx, "project"); !errors.As(err, &policyErr) || policyErr.Setting() != "repository_policy" {
			t.Fatalf("%s read with err=%v", stored, err)
		}
	}
	if _, err := store.SaveRepositoryRefPolicy(ctx, "project", RepositoryRefPolicyChange{KeptHistory: &on}); !errors.As(err, &policyErr) {
		t.Fatalf("a change of one choice replaced an unreadable row: err=%v", err)
	}
	unprotected := false
	if _, err := store.SaveRepositoryRefPolicy(ctx, "project", RepositoryRefPolicyChange{KeptHistory: &on, ProtectDefaultBranch: &unprotected}); err != nil {
		t.Fatalf("a change of both choices did not replace an unreadable row: %v", err)
	}
	if got := writes(); got != (RefWrites{KeepHistory: true}) {
		t.Fatalf("after replacing the unreadable row = %+v", got)
	}
	invalid := KeptHistoryChoice("sometimes")
	if _, err := store.SaveRepositoryRefPolicy(ctx, "project", RepositoryRefPolicyChange{KeptHistory: &invalid}); err == nil {
		t.Fatal("an unknown kept history choice was saved")
	}
}
