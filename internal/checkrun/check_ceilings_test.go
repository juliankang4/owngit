package checkrun

import (
	"testing"

	"owngit/internal/state"
)

// A policy above a lowered check ceiling queues nothing; the pushes that
// arrive meanwhile are queued once the ceiling is raised again.
func TestPolicyAboveTheCeilingsWaitsForThem(t *testing.T) {
	fixture := newPushFixture(t, 1000)
	fixture.setPolicyVersion(60_000)
	lowered := state.DefaultCheckCeilings
	lowered.QueueLimit = 10
	noErr(t, fixture.store.SavePolicies(fixture.ctx, state.PolicyChange{CheckCeilings: &lowered}))
	fixture.pushWorkflow("main", `{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"exit 0"}]}`)
	if jobs := fixture.settle(); jobs != 0 {
		t.Fatalf("a policy above the ceilings queued %d jobs", jobs)
	}
	noErr(t, fixture.store.SavePolicies(fixture.ctx, state.PolicyChange{CheckCeilings: &state.DefaultCheckCeilings}))
	if jobs := fixture.settle(); jobs != 1 {
		t.Fatalf("after raising the ceiling %d jobs, want the waiting push", jobs)
	}
}
