package state

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// raiseCheckCeilings sets every check ceiling to the largest value it may
// have, the record bound of its policy field.
func raiseCheckCeilings(t *testing.T, store *Store) {
	t.Helper()
	ceilings := DefaultCheckCeilings
	for _, bound := range ceilings.bounded() {
		_, *bound.ceiling = CheckCeilingRange(bound.field)
	}
	noErr(t, store.SavePolicies(context.Background(), PolicyChange{CheckCeilings: &ceilings}))
}

func TestCheckCeilingsAreReadSavedAndRefused(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if ceilings, err := store.CheckCeilings(ctx); err != nil || ceilings != DefaultCheckCeilings {
		t.Fatalf("nothing saved: %+v, %v; want the defaults", ceilings, err)
	}

	// A partial change keeps the other ceilings.
	queue, timeout := int64(5000), int64(3*24*60*60)
	current, err := store.CheckCeilings(ctx)
	noErr(t, err)
	changed, err := CheckCeilingFields{QueueLimit: &queue, TimeoutSeconds: &timeout}.Apply(current)
	noErr(t, err)
	noErr(t, store.SavePolicies(ctx, PolicyChange{CheckCeilings: &changed}))
	want := DefaultCheckCeilings
	want.QueueLimit, want.TimeoutMS = 5000, 3*24*60*60*1000
	if got, err := store.CheckCeilings(ctx); err != nil || got != want {
		t.Fatalf("saved %+v, %v; want %+v", got, err, want)
	}
	if !want.Looser() || DefaultCheckCeilings.Looser() {
		t.Fatal("Looser does not report a raised ceiling")
	}

	// Beyond the record bound, below the field's minimum, or a time that is
	// not whole seconds: refused, and the saved ceilings stay.
	for name, ceilings := range map[string]CheckCeilings{
		"queue above its record bound": func() CheckCeilings { c := DefaultCheckCeilings; c.QueueLimit = MaximumCheckQueueLimit + 1; return c }(),
		"no active job":                func() CheckCeilings { c := DefaultCheckCeilings; c.ActiveJobs = 0; return c }(),
		"part of a second":             func() CheckCeilings { c := DefaultCheckCeilings; c.TimeoutMS = 1500; return c }(),
	} {
		if err := store.SavePolicies(ctx, PolicyChange{CheckCeilings: &ceilings}); err == nil {
			t.Fatalf("%s was saved", name)
		}
	}
	if got, err := store.CheckCeilings(ctx); err != nil || got != want {
		t.Fatalf("after refusals %+v, %v; want %+v", got, err, want)
	}

	// A stored row that does not parse or is out of range is an error that
	// names the setting, never the defaults.
	for _, raw := range []string{`not json`, `{"queue_limit":0}`, `{"timeout_seconds":null}`} {
		err := store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('check_ceilings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, raw)
		noErr(t, err)
		var policyErr *PolicyError
		if _, err := store.CheckCeilings(ctx); !errors.As(err, &policyErr) || policyErr.Setting() != "check_ceilings" {
			t.Fatalf("stored %s: err=%v, want a PolicyError for check_ceilings", raw, err)
		}
	}
}

func TestCheckCeilingsBoundPoliciesAtSave(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	ceilings := DefaultCheckCeilings
	for _, bound := range ceilings.bounded() {
		t.Run(bound.field, func(t *testing.T) {
			ceiling := *bound.ceiling
			published, _ := DefaultCheckCeilings.Bounds(bound.field)
			if published.Max != ceiling {
				t.Fatalf("published maximum %d, want the ceiling %d", published.Max, ceiling)
			}
			input := containerPolicyInput()
			input.Execution.Source = DefaultCheckSourceLimits()
			set := func(value int64) {
				switch bound.field {
				case FieldMaxTimeoutMS:
					input.MaxTimeoutMS = value
				case FieldMaxOutputLimitBytes:
					input.MaxOutputLimitBytes = value
				case FieldQueueLimit:
					input.QueueLimit = int(value)
				case FieldMaxActiveJobs:
					input.MaxActiveJobs = int(value)
				case FieldContainerCPUMillis:
					input.Execution.ContainerCPUMillis = value
				case FieldContainerMemoryBytes:
					input.Execution.ContainerMemoryBytes = value
				case FieldContainerPIDs:
					input.Execution.ContainerPIDs = value
				case FieldContainerScratchBytes:
					input.Execution.ContainerScratchBytes = value
				case FieldSourceMaxTotalBytes:
					input.Execution.Source.MaxTotalBytes = value
				default:
					t.Fatalf("no case for %s", bound.field)
				}
			}
			set(ceiling)
			if _, err := fixture.store.SetCheckPolicy(ctx, input, fixture.now); err != nil {
				t.Fatalf("at the ceiling: %v", err)
			}
			set(ceiling + 1)
			_, err := fixture.store.SetCheckPolicy(ctx, input, fixture.now)
			refusals := PolicyFieldErrors(err)
			if !errors.Is(err, ErrInvalidCheckPolicy) || len(refusals) != 1 || refusals[0].Field != bound.field ||
				refusals[0].Rule != RuleCeiling || refusals[0].Max != ceiling {
				t.Fatalf("above the ceiling: err=%v refusals=%v, want one ceiling refusal quoting %d", err, refusals, ceiling)
			}
		})
	}

	// Once the ceilings are raised, the same value saves.
	raiseCheckCeilings(t, fixture.store)
	input := containerPolicyInput()
	input.QueueLimit = MaximumCheckQueueLimit
	input.Execution.ContainerMemoryBytes = 1 << 40
	if _, err := fixture.store.SetCheckPolicy(ctx, input, fixture.now); err != nil {
		t.Fatalf("under raised ceilings: %v", err)
	}
}

func TestLoweredCeilingStopsNewJobsOnly(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, func(input *CheckPolicyInput) { input.QueueLimit = 8 })
	policy := fixture.grantConsent(t)
	admitted := fixture.admit(t, pushJobRequest())

	lowered := DefaultCheckCeilings
	lowered.QueueLimit = 4
	noErr(t, fixture.store.SavePolicies(ctx, PolicyChange{CheckCeilings: &lowered}))
	listed, err := fixture.store.RepositoriesAboveCheckCeilings(ctx)
	if err != nil || !reflect.DeepEqual(listed, []string{"project"}) {
		t.Fatalf("repositories above the ceilings: %v, %v", listed, err)
	}

	// The policy, its consent and the admitted job are unchanged; only a
	// new admission is refused.
	stored, _, err := fixture.store.CheckPolicy(ctx, "project")
	noErr(t, err)
	if !samePolicyAndConsent(stored, policy) {
		t.Fatalf("lowering a ceiling changed the policy:\n%+v\n%+v", stored, policy)
	}
	job, exists, err := fixture.store.CheckJob(ctx, "project", admitted.ID)
	if err != nil || !exists || job.Status != CheckJobPending || job.Limits != admitted.Limits {
		t.Fatalf("admitted job after lowering: %+v, %v, %v", job, exists, err)
	}
	request := pullRequestJobRequest()
	if _, _, err := fixture.store.AdmitCheckJob(ctx, request, fixture.now); !errors.Is(err, ErrCheckCeilingExceeded) {
		t.Fatalf("admission above a lowered ceiling: %v", err)
	}
	if _, _, err := fixture.store.RerunCheckJob(ctx, "project", admitted.ID, fixture.now); err == nil {
		t.Fatal("a pending job was rerun")
	}

	// Raising the ceiling again admits without a new consent, and never
	// changes the policy.
	noErr(t, fixture.store.SavePolicies(ctx, PolicyChange{CheckCeilings: &DefaultCheckCeilings}))
	fixture.admit(t, request)
	stored, _, err = fixture.store.CheckPolicy(ctx, "project")
	noErr(t, err)
	if !samePolicyAndConsent(stored, policy) {
		t.Fatalf("raising a ceiling changed the policy:\n%+v\n%+v", stored, policy)
	}
}

func TestUnreadableCeilingsStopSavesAndAdmissions(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	err := fixture.store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('check_ceilings','{"pids":1}')`)
	noErr(t, err)
	var policyErr *PolicyError
	if _, _, err := fixture.store.AdmitCheckJob(ctx, pushJobRequest(), fixture.now); !errors.As(err, &policyErr) {
		t.Fatalf("admission with unreadable ceilings: %v", err)
	}
	if _, err := fixture.store.SetCheckPolicy(ctx, defaultPolicyInput(), fixture.now); !errors.As(err, &policyErr) {
		t.Fatalf("save with unreadable ceilings: %v", err)
	}
	if _, err := fixture.store.RepositoriesAboveCheckCeilings(ctx); !errors.As(err, &policyErr) {
		t.Fatalf("listing with unreadable ceilings: %v", err)
	}
}

func samePolicyAndConsent(a, b CheckPolicy) bool {
	return a.Version == b.Version && a.Digest == b.Digest && a.QueueLimit == b.QueueLimit &&
		a.ConsentActive == b.ConsentActive && a.ConsentVersion == b.ConsentVersion && a.ConsentDigest == b.ConsentDigest &&
		a.UpdatedAt.Equal(b.UpdatedAt)
}
