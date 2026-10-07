package state

import (
	"context"
	"errors"
	"testing"
)

// An approval can never attach to a policy generation nobody read, whether the
// replacement came before the approval or after it.

func TestGrantCheckConsentForRejectsAnotherGeneration(t *testing.T) {
	fixture := newCheckJobFixture(t)
	first, err := fixture.store.SetCheckPolicy(context.Background(), defaultPolicyInput(), fixture.now)
	noErr(t, err)

	changed := defaultPolicyInput()
	changed.MaxTimeoutMS = 300000
	second, err := fixture.store.SetCheckPolicy(context.Background(), changed, fixture.now)
	noErr(t, err)
	if second.Version == first.Version || second.Digest == first.Digest {
		t.Fatalf("the policy did not change: v%d/%s to v%d/%s", first.Version, first.Digest, second.Version, second.Digest)
	}

	stale := ExpectedCheckPolicy{Version: first.Version, Digest: first.Digest}
	if _, err := fixture.store.GrantCheckConsentFor(context.Background(), "project", stale, fixture.now); !errors.Is(err, ErrCheckPolicyStale) {
		t.Fatalf("stale approval error=%v, want ErrCheckPolicyStale", err)
	}
	current, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	if current.ConsentActive {
		t.Fatal("a stale approval granted consent")
	}

	// A generation number alone cannot identify what was read.
	mixed := ExpectedCheckPolicy{Version: second.Version, Digest: first.Digest}
	if _, err := fixture.store.GrantCheckConsentFor(context.Background(), "project", mixed, fixture.now); !errors.Is(err, ErrCheckPolicyStale) {
		t.Fatalf("digest mismatch error=%v, want ErrCheckPolicyStale", err)
	}

	matching := ExpectedCheckPolicy{Version: second.Version, Digest: second.Digest}
	granted, err := fixture.store.GrantCheckConsentFor(context.Background(), "project", matching, fixture.now)
	if err != nil {
		t.Fatalf("matching approval error=%v", err)
	}
	if !granted.ConsentActive || granted.ConsentDigest != second.Digest {
		t.Fatalf("consent digest=%q active=%v, want %q active", granted.ConsentDigest, granted.ConsentActive, second.Digest)
	}

	// The idempotent-success path must not answer a stale approval.
	if _, err := fixture.store.GrantCheckConsentFor(context.Background(), "project", stale, fixture.now); !errors.Is(err, ErrCheckPolicyStale) {
		t.Fatalf("stale approval against active consent error=%v, want ErrCheckPolicyStale", err)
	}
}

// Either order of an approval and a replacement ends with the replacement
// stored and consent inactive. An approval that lands first returns the
// generation it named with active consent; one that lands second is refused as
// stale. The store opens one connection, so these two orders are all there is.
func TestApprovalAndReplacementInEitherOrderNeverInheritConsent(t *testing.T) {
	replacementInput := func() CheckPolicyInput {
		input := defaultPolicyInput()
		input.MaxLeaseMS = 90000
		return input
	}
	for _, approvalFirst := range []bool{true, false} {
		name := "replacement lands before the approval"
		if approvalFirst {
			name = "approval lands before the replacement"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCheckJobFixture(t)
			first, err := fixture.store.SetCheckPolicy(context.Background(), defaultPolicyInput(), fixture.now)
			noErr(t, err)
			approve := func() (CheckPolicy, error) {
				return fixture.store.GrantCheckConsentFor(context.Background(), "project",
					ExpectedCheckPolicy{Version: first.Version, Digest: first.Digest}, fixture.now)
			}
			var granted CheckPolicy
			var grantErr error
			if approvalFirst {
				granted, grantErr = approve()
			}
			second, err := fixture.store.SetCheckPolicy(context.Background(), replacementInput(), fixture.now)
			noErr(t, err)
			if second.ConsentActive {
				t.Fatal("a replacement inherited the consent granted for the previous generation")
			}
			if !approvalFirst {
				granted, grantErr = approve()
			}

			if approvalFirst {
				if grantErr != nil || !granted.ConsentActive || granted.Digest != first.Digest || granted.ConsentDigest != first.Digest {
					t.Fatalf("approval on the current generation: %+v err=%v", granted, grantErr)
				}
			} else if !errors.Is(grantErr, ErrCheckPolicyStale) {
				t.Fatalf("an approval for a replaced generation returned %v, want ErrCheckPolicyStale", grantErr)
			}
			final, exists, err := fixture.store.CheckPolicy(context.Background(), "project")
			if err != nil || !exists || final.Digest != second.Digest || final.Version != second.Version || final.ConsentActive {
				t.Fatalf("final policy=%+v exists=%v err=%v, want the replacement with consent inactive", final, exists, err)
			}
		})
	}
}

func TestGrantCheckConsentForRequiresAWholeIdentity(t *testing.T) {
	// A half-stated expectation is not a weaker expectation, it is an unusable
	// one. Treating it as "no expectation" would turn a malformed submission
	// into a granted consent, which is what this function exists to prevent.
	fixture := newCheckJobFixture(t)
	stored, err := fixture.store.SetCheckPolicy(context.Background(), defaultPolicyInput(), fixture.now)
	noErr(t, err)
	for name, expected := range map[string]ExpectedCheckPolicy{
		"nothing at all":   {},
		"version only":     {Version: stored.Version},
		"digest only":      {Digest: stored.Digest},
		"zero version":     {Version: 0, Digest: stored.Digest},
		"negative version": {Version: -1, Digest: stored.Digest},
	} {
		if expected.Complete() {
			t.Fatalf("%s: a partial identity reports itself complete", name)
		}
		if _, err := fixture.store.GrantCheckConsentFor(context.Background(), "project", expected, fixture.now); !errors.Is(err, ErrCheckPolicyStale) {
			t.Fatalf("%s: err=%v, want ErrCheckPolicyStale", name, err)
		}
		current, _, err := fixture.store.CheckPolicy(context.Background(), "project")
		noErr(t, err)
		if current.ConsentActive {
			t.Fatalf("%s: a partial identity granted consent", name)
		}
	}
	// The whole identity still works, so the strictness is about completeness
	// and not a blanket refusal.
	whole := ExpectedCheckPolicy{Version: stored.Version, Digest: stored.Digest}
	if !whole.Complete() {
		t.Fatal("a whole identity reports itself incomplete")
	}
	if _, err := fixture.store.GrantCheckConsentFor(context.Background(), "project", whole, fixture.now); err != nil {
		t.Fatalf("a whole identity was refused: %v", err)
	}
}

func TestGrantCheckConsentWithoutAnExpectationKeepsItsContract(t *testing.T) {
	// Callers with no rendered identity to quote, such as the CLI acting on
	// the policy it just fetched, keep the original behaviour including
	// idempotent success.
	fixture := newCheckJobFixture(t)
	if _, err := fixture.store.SetCheckPolicy(context.Background(), defaultPolicyInput(), fixture.now); err != nil {
		t.Fatal(err)
	}
	granted, err := fixture.store.GrantCheckConsent(context.Background(), "project", fixture.now)
	if err != nil || !granted.ConsentActive {
		t.Fatalf("grant active=%v err=%v", granted.ConsentActive, err)
	}
	again, err := fixture.store.GrantCheckConsent(context.Background(), "project", fixture.now)
	if err != nil {
		t.Fatalf("repeated grant err=%v", err)
	}
	if again.ConsentVersion != granted.ConsentVersion {
		t.Fatalf("repeated grant consumed a generation: %d then %d", granted.ConsentVersion, again.ConsentVersion)
	}
	if _, err := fixture.store.GrantCheckConsent(context.Background(), "other", fixture.now); !errors.Is(err, ErrCheckPolicyMissing) {
		t.Fatalf("grant without a policy err=%v, want ErrCheckPolicyMissing", err)
	}
}

// Range refusals away from zero are covered by TestThePublishedBoundsAreTheEnforcedBounds,
// which maps a zero below the minimum to the default.
func TestPolicyRefusalNamesTheField(t *testing.T) {
	fixture := newCheckJobFixture(t)
	cases := map[string]struct {
		mutate func(*CheckPolicyInput)
		field  string
		rule   string
	}{
		"unknown executor": {func(i *CheckPolicyInput) { i.Executor = "vm" }, FieldExecutor, RuleUnknown},
		"no event":         {func(i *CheckPolicyInput) { i.AllowedEvents = nil }, FieldAllowedEvents, RuleRequired},
		"unknown event":    {func(i *CheckPolicyInput) { i.AllowedEvents = []string{"tag"} }, FieldAllowedEvents, RuleUnknown},
		"repeated event":   {func(i *CheckPolicyInput) { i.AllowedEvents = []string{"push", "push"} }, FieldAllowedEvents, RuleDuplicate},
		"queue zero":       {func(i *CheckPolicyInput) { i.QueueLimit = 0 }, FieldQueueLimit, RuleRange},
		"active zero":      {func(i *CheckPolicyInput) { i.MaxActiveJobs = 0 }, FieldMaxActiveJobs, RuleRange},
		"container field on host executor": {func(i *CheckPolicyInput) {
			i.Executor = CheckExecutorHost
			i.Execution.ContainerPIDs = 32
		}, FieldContainerPIDs, RuleNotApplicable},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			input := defaultPolicyInput()
			input.RepositoryID = "other"
			test.mutate(&input)
			_, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)

			// Existing callers keep working: every refusal is still this.
			if !errors.Is(err, ErrInvalidCheckPolicy) {
				t.Fatalf("error=%v, want ErrInvalidCheckPolicy", err)
			}
			refusals := PolicyFieldErrors(err)
			if len(refusals) != 1 {
				t.Fatalf("%d structured refusals, want one: %v", len(refusals), err)
			}
			if refusals[0].Field != test.field || refusals[0].Rule != test.rule {
				t.Fatalf("refused %s by %s, want %s by %s",
					refusals[0].Field, refusals[0].Rule, test.field, test.rule)
			}
			if _, exists, err := fixture.store.CheckPolicy(context.Background(), "other"); err != nil || exists {
				t.Fatalf("a refused policy was stored: exists=%v err=%v", exists, err)
			}
		})
	}
}

// containerPolicyInput is a valid container policy, used for the container
// bounds. They only apply to that executor, so testing them on the default
// external-runner policy would produce a "does not apply" refusal instead of
// the range refusal under test.
func containerPolicyInput() CheckPolicyInput {
	input := defaultPolicyInput()
	input.RepositoryID = "other"
	input.Executor = CheckExecutorContainer
	input.Execution.ContainerImage = "registry.example/checks@sha256:" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return input
}

func TestThePublishedBoundsAreTheEnforcedBounds(t *testing.T) {
	// CheckPolicyBoundsFor is what an interface shows an operator. If it ever
	// disagreed with the rule that refuses a value, the screen would state a
	// range that is not true. Each bound is exercised at the edge: the bound
	// itself is accepted and one step outside it is refused, naming the field
	// and quoting the same numbers the table publishes. The check ceilings
	// are raised to their largest values, so the record bounds are the ones
	// that refuse; TestCheckCeilingsBoundPoliciesAtSave covers the ceilings.
	fixture := newCheckJobFixture(t)
	raiseCheckCeilings(t, fixture.store)

	// Each case builds a policy that is valid apart from the field under
	// test, so the refusal under test is the only one that can occur.
	sourceInput := func() CheckPolicyInput {
		input := defaultPolicyInput()
		input.RepositoryID = "other"
		input.Execution.Source = DefaultCheckSourceLimits()
		return input
	}
	cases := map[string]struct {
		base  func() CheckPolicyInput
		apply func(*CheckPolicyInput, int64)
	}{
		FieldMaxTimeoutMS: {sourceInput, func(i *CheckPolicyInput, v int64) { i.MaxTimeoutMS = v }},
		FieldMaxOutputLimitBytes: {sourceInput, func(i *CheckPolicyInput, v int64) {
			i.MaxOutputLimitBytes = v
		}},
		FieldQueueLimit:    {sourceInput, func(i *CheckPolicyInput, v int64) { i.QueueLimit = int(v) }},
		FieldMaxActiveJobs: {sourceInput, func(i *CheckPolicyInput, v int64) { i.MaxActiveJobs = int(v) }},
		FieldMaxLeaseMS:    {sourceInput, func(i *CheckPolicyInput, v int64) { i.MaxLeaseMS = v }},

		FieldSourceMaxEntries: {sourceInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.Source.MaxEntries = int(v)
		}},
		FieldSourceMaxFileBytes: {sourceInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.Source.MaxFileBytes = v
			// The total may not be smaller than one file, so it moves with
			// this value. Without that the file bound at its maximum would
			// trip the total's rule instead of its own.
			if total, _ := CheckPolicyBoundsFor(FieldSourceMaxTotalBytes); v > i.Execution.Source.MaxTotalBytes && v <= total.Max {
				i.Execution.Source.MaxTotalBytes = v
			}
		}},
		FieldSourceMaxPathDepth: {sourceInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.Source.MaxPathDepth = int(v)
		}},
		FieldSourceMaxPathBytes: {sourceInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.Source.MaxPathBytes = int(v)
		}},
		FieldSourceMaxNameBytes: {sourceInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.Source.MaxNameBytes = int(v)
		}},
		FieldSourceMetadataLimit: {sourceInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.Source.MetadataLimit = v
		}},

		FieldContainerCPUMillis: {containerPolicyInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.ContainerCPUMillis = v
		}},
		FieldContainerMemoryBytes: {containerPolicyInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.ContainerMemoryBytes = v
		}},
		FieldContainerPIDs: {containerPolicyInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.ContainerPIDs = v
		}},
		FieldContainerScratchBytes: {containerPolicyInput, func(i *CheckPolicyInput, v int64) {
			i.Execution.ContainerScratchBytes = v
		}},
	}
	for field, test := range cases {
		t.Run(field, func(t *testing.T) {
			bounds, known := CheckPolicyBoundsFor(field)
			if !known {
				t.Fatalf("%s publishes no range", field)
			}
			if !bounds.HasFixedMinimum() {
				t.Fatalf("%s has a moving floor and belongs in its own test", field)
			}
			if bounds.Min >= bounds.Max {
				t.Fatalf("%s publishes an unusable range %d to %d", field, bounds.Min, bounds.Max)
			}

			for name, value := range map[string]int64{"at the minimum": bounds.Min, "at the maximum": bounds.Max} {
				input := test.base()
				test.apply(&input, value)
				if _, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now); err != nil {
					t.Fatalf("%s %s (%d) was refused: %v", field, name, value, err)
				}
			}

			// A source or container limit of zero means "use the bounded
			// default", so it is not an out-of-range value. Where the minimum
			// is one, the value just outside the range is therefore negative.
			below := bounds.Min - 1
			if below == 0 {
				below = -1
			}
			for name, value := range map[string]int64{
				"below the minimum": below,
				"above the maximum": bounds.Max + 1,
			} {
				input := test.base()
				test.apply(&input, value)
				_, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
				if !errors.Is(err, ErrInvalidCheckPolicy) {
					t.Fatalf("%s %s (%d) was accepted: err=%v", field, name, value, err)
				}
				refusals := PolicyFieldErrors(err)
				if len(refusals) != 1 || refusals[0].Field != field || refusals[0].Rule != RuleRange {
					t.Fatalf("%s %s: refusals=%v, want one range refusal for this field", field, name, refusals)
				}
				// The refusal reports the same numbers the table publishes, so
				// a caller showing one and reading the other cannot present
				// two different ranges.
				if refusals[0].Min != bounds.Min || refusals[0].Max != bounds.Max {
					t.Fatalf("%s: refusal reports %d to %d, published range is %d to %d",
						field, refusals[0].Min, refusals[0].Max, bounds.Min, bounds.Max)
				}
			}
		})
	}
}

func TestEveryPublishedNumericFieldIsCoveredByTheBoundaryTest(t *testing.T) {
	// A bound added to the table without a boundary case would be published to
	// operators and never checked against the rule that enforces it.
	covered := map[string]bool{
		FieldMaxTimeoutMS: true, FieldMaxOutputLimitBytes: true, FieldQueueLimit: true,
		FieldMaxActiveJobs: true, FieldMaxLeaseMS: true,
		FieldSourceMaxEntries: true, FieldSourceMaxFileBytes: true,
		FieldSourceMaxPathDepth: true, FieldSourceMaxPathBytes: true,
		FieldSourceMaxNameBytes: true, FieldSourceMetadataLimit: true,
		FieldContainerCPUMillis: true, FieldContainerMemoryBytes: true,
		FieldContainerPIDs: true, FieldContainerScratchBytes: true,
		// Covered by its own test, because its floor moves.
		FieldSourceMaxTotalBytes: true,
	}
	for field := range checkPolicyBounds {
		if !covered[field] {
			t.Errorf("%s publishes bounds with no boundary case", field)
		}
	}
}

func TestTheSourceTotalPublishesAMovingFloorAndAFixedCeiling(t *testing.T) {
	// This limit has both: it cannot be smaller than a single file, and it
	// cannot exceed 1 TiB. A moving floor is not a reason to leave the ceiling
	// enforced in private, so both are published and both are tested.
	bounds, known := CheckPolicyBoundsFor(FieldSourceMaxTotalBytes)
	if !known {
		t.Fatal("the source total publishes no bounds at all")
	}
	if bounds.HasFixedMinimum() {
		t.Fatal("the source total claims a fixed floor; its floor is the file limit")
	}
	if bounds.MinField != FieldSourceMaxFileBytes {
		t.Fatalf("the floor names %q, want the per-file limit", bounds.MinField)
	}
	if bounds.Max != 1<<40 {
		t.Fatalf("the published ceiling is %d, want 1 TiB", bounds.Max)
	}

	fixture := newCheckJobFixture(t)
	raiseCheckCeilings(t, fixture.store)
	source := func(file, total int64) CheckPolicyInput {
		input := defaultPolicyInput()
		input.RepositoryID = "other"
		input.Execution.Source = DefaultCheckSourceLimits()
		input.Execution.Source.MaxFileBytes = file
		input.Execution.Source.MaxTotalBytes = total
		return input
	}

	// The relationship: one byte below the file limit is refused, and exactly
	// the file limit is accepted.
	const file = 4 << 20
	_, err := fixture.store.SetCheckPolicy(context.Background(), source(file, file-1), fixture.now)
	refusals := PolicyFieldErrors(err)
	if len(refusals) != 1 || refusals[0].Field != FieldSourceMaxTotalBytes || refusals[0].Rule != RuleRange {
		t.Fatalf("below the file limit: refusals=%v, want one range refusal for the total", refusals)
	}
	if refusals[0].Min != file {
		t.Fatalf("the refusal floor is %d, want the operator's file limit %d", refusals[0].Min, file)
	}
	if refusals[0].Max != bounds.Max {
		t.Fatalf("the refusal ceiling is %d, want the published %d", refusals[0].Max, bounds.Max)
	}
	if _, err := fixture.store.SetCheckPolicy(context.Background(), source(file, file), fixture.now); err != nil {
		t.Fatalf("a total equal to the file limit was refused: %v", err)
	}

	// The ceiling: exactly the maximum is accepted, one byte past it is not.
	if _, err := fixture.store.SetCheckPolicy(context.Background(), source(file, bounds.Max), fixture.now); err != nil {
		t.Fatalf("the published maximum was refused: %v", err)
	}
	_, err = fixture.store.SetCheckPolicy(context.Background(), source(file, bounds.Max+1), fixture.now)
	if !errors.Is(err, ErrInvalidCheckPolicy) {
		t.Fatalf("one byte past the published maximum was accepted: err=%v", err)
	}
	refusals = PolicyFieldErrors(err)
	if len(refusals) != 1 || refusals[0].Field != FieldSourceMaxTotalBytes || refusals[0].Max != bounds.Max {
		t.Fatalf("above the ceiling: refusals=%v, want a range refusal quoting %d", refusals, bounds.Max)
	}
}

func TestContainerImageRefusalSeparatesMissingFromMalformed(t *testing.T) {
	// "You left it empty" and "that is not an immutable reference" are
	// different problems with different fixes.
	fixture := newCheckJobFixture(t)
	for name, test := range map[string]struct {
		image string
		rule  string
	}{
		"empty":     {"", RuleRequired},
		"mutable":   {"registry.example/checks:latest", RuleFormat},
		"truncated": {"sha256:abc", RuleFormat},
	} {
		t.Run(name, func(t *testing.T) {
			input := defaultPolicyInput()
			input.RepositoryID = "other"
			input.Executor = CheckExecutorContainer
			input.Execution.ContainerImage = test.image
			_, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
			if !errors.Is(err, ErrInvalidCheckPolicy) {
				t.Fatalf("error=%v, want ErrInvalidCheckPolicy", err)
			}
			refusals := PolicyFieldErrors(err)
			if len(refusals) != 1 || refusals[0].Field != FieldContainerImage || refusals[0].Rule != test.rule {
				t.Fatalf("refusals=%v, want %s by %s", refusals, FieldContainerImage, test.rule)
			}
		})
	}
}

func TestStructuredRefusalsDoNotChangeAcceptedPolicies(t *testing.T) {
	// The bounds and defaults are unchanged. A policy that was accepted before
	// is still accepted, and still normalizes to the same stored facts.
	fixture := newCheckJobFixture(t)
	input := defaultPolicyInput()
	input.Executor = CheckExecutorContainer
	input.Execution.ContainerImage = "registry.example/checks@sha256:" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	stored, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
	if err != nil {
		t.Fatalf("a valid container policy was refused: %v", err)
	}
	defaults := DefaultCheckSourceLimits()
	if stored.Execution.Source != defaults {
		t.Fatalf("source defaults changed: %+v, want %+v", stored.Execution.Source, defaults)
	}
	if stored.Execution.ContainerRuntime != "docker-local" || stored.Execution.ContainerNetwork != ContainerNetworkNone {
		t.Fatalf("container defaults changed: runtime=%q network=%q",
			stored.Execution.ContainerRuntime, stored.Execution.ContainerNetwork)
	}
	if stored.Execution.ContainerCPUMillis != 1000 || stored.Execution.ContainerMemoryBytes != 512<<20 ||
		stored.Execution.ContainerPIDs != 256 || stored.Execution.ContainerScratchBytes != 512<<20 {
		t.Fatalf("container resource defaults changed: %+v", stored.Execution)
	}
	// The digest is computed from the normalized facts, so an unchanged
	// resubmission must be recognised as the same policy.
	same, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
	noErr(t, err)
	if same.Version != stored.Version || same.Digest != stored.Digest {
		t.Fatalf("an unchanged policy produced a new generation: v%d/%s then v%d/%s",
			stored.Version, stored.Digest, same.Version, same.Digest)
	}
}
