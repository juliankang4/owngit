package state

import (
	"context"
	"errors"
	"testing"
)

// Save and enable turns checks on for exactly the submitted policy, and only
// when the stored policy is still the one the change was shown against.
func TestSaveAndEnableBindsConsentToTheSubmittedPolicy(t *testing.T) {
	fixture := newCheckJobFixture(t)
	ctx := context.Background()
	first := defaultPolicyInput()
	none := &ExpectedCheckPolicy{}
	enabled, err := fixture.store.SaveCheckPolicyAndGrantConsent(ctx, first, none, fixture.now)
	noErr(t, err)
	if !enabled.ConsentActive || enabled.ConsentDigest != enabled.Digest || enabled.Version != 1 {
		t.Fatalf("first save and enable=%+v", enabled)
	}

	// Someone else stores another policy after the page was drawn.
	other := defaultPolicyInput()
	other.MaxTimeoutMS = 300000
	replaced, err := fixture.store.SetCheckPolicy(ctx, other, fixture.now)
	noErr(t, err)

	mine := defaultPolicyInput()
	mine.QueueLimit = 9
	base := &ExpectedCheckPolicy{Version: enabled.Version, Digest: enabled.Digest}
	if _, err := fixture.store.SaveCheckPolicyAndGrantConsent(ctx, mine, base, fixture.now); !errors.Is(err, ErrCheckPolicyStale) {
		t.Fatalf("save and enable over another change: %v", err)
	}
	current, _, err := fixture.store.CheckPolicy(ctx, "project")
	noErr(t, err)
	if current.Digest != replaced.Digest || current.ConsentActive {
		t.Fatalf("a refused save and enable changed the policy or consent: %+v", current)
	}
	if _, err := fixture.store.SaveCheckPolicyAndGrantConsent(ctx, mine, none, fixture.now); !errors.Is(err, ErrCheckPolicyStale) {
		t.Fatalf("save and enable expecting no policy: %v", err)
	}

	base = &ExpectedCheckPolicy{Version: replaced.Version, Digest: replaced.Digest}
	saved, err := fixture.store.SaveCheckPolicyAndGrantConsent(ctx, mine, base, fixture.now)
	noErr(t, err)
	candidate, err := CandidateCheckPolicy(mine)
	noErr(t, err)
	if saved.Digest != candidate.Digest || !saved.ConsentActive || saved.ConsentDigest != candidate.Digest || saved.Version != replaced.Version+1 {
		t.Fatalf("save and enable stored %+v, want the submitted policy %s enabled", saved, candidate.Digest)
	}

	// A refused policy writes nothing.
	bad := defaultPolicyInput()
	bad.QueueLimit = 0
	if _, err := fixture.store.SaveCheckPolicyAndGrantConsent(ctx, bad, nil, fixture.now); !errors.Is(err, ErrInvalidCheckPolicy) {
		t.Fatalf("invalid save and enable: %v", err)
	}
	after, _, err := fixture.store.CheckPolicy(ctx, "project")
	noErr(t, err)
	if after.Digest != saved.Digest || !after.ConsentActive {
		t.Fatalf("an invalid save and enable changed the stored policy: %+v", after)
	}
}
