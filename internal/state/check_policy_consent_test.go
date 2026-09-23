package state

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// Consent names the generation it was granted for.
//
// The property under test is not "a stale form is rejected". It is that an
// approval can never attach to a policy generation nobody read, including when
// the replacement happens while the approval is in flight.

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

	// The identity the operator read is now history.
	stale := ExpectedCheckPolicy{Version: first.Version, Digest: first.Digest}
	if _, err := fixture.store.GrantCheckConsentFor(context.Background(), "project", stale, fixture.now); !errors.Is(err, ErrCheckPolicyStale) {
		t.Fatalf("stale approval error=%v, want ErrCheckPolicyStale", err)
	}
	current, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	if current.ConsentActive {
		t.Fatal("a stale approval granted consent")
	}

	// A version that matches while the digest does not is still refused: a
	// generation number alone cannot identify what was read.
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
}

func TestStaleApprovalIsRefusedEvenWhenConsentIsAlreadyActive(t *testing.T) {
	// The idempotent-success path must not answer a stale approval. Otherwise
	// approving an old generation would return success while the active
	// consent belongs to a policy the operator never read.
	fixture := newCheckJobFixture(t)
	first, err := fixture.store.SetCheckPolicy(context.Background(), defaultPolicyInput(), fixture.now)
	noErr(t, err)
	changed := defaultPolicyInput()
	changed.QueueLimit = 7
	second, err := fixture.store.SetCheckPolicy(context.Background(), changed, fixture.now)
	noErr(t, err)
	if _, err := fixture.store.GrantCheckConsentFor(context.Background(), "project",
		ExpectedCheckPolicy{Version: second.Version, Digest: second.Digest}, fixture.now); err != nil {
		t.Fatal(err)
	}

	_, err = fixture.store.GrantCheckConsentFor(context.Background(), "project",
		ExpectedCheckPolicy{Version: first.Version, Digest: first.Digest}, fixture.now)
	if !errors.Is(err, ErrCheckPolicyStale) {
		t.Fatalf("stale approval against active consent error=%v, want ErrCheckPolicyStale", err)
	}
}

// assertConsentOutcome checks the only two honest results of an approval that
// raced a policy replacement.
//
// Either the approval won and consent is active on the generation it named, or
// the replacement won and the approval was refused as stale with consent
// cleared. Anything else is a failure, including an unrelated error: a caller
// that cannot tell "you approved an old policy" from "the database was busy"
// has no basis for deciding whether to retry.
// consentRace is what one approval-against-replacement sequence produced: the
// policy the approval returned, its error, and the policy the replacement
// stored. Both results are kept rather than discarded, because the final row
// alone cannot distinguish the two orders.
type consentRace struct {
	granted     CheckPolicy
	grantErr    error
	replacement CheckPolicy
}

// assertConsentOutcome checks the two honest results of an approval racing a
// policy replacement, asserting both completed operations rather than only the
// row left behind.
//
// Either order ends with the replacement stored and consent inactive, because
// a replacement always clears consent. What differs is the approval:
//
//   - The approval landed first. It returns the generation it named, with
//     active consent on that generation. The replacement then clears it. This
//     is a valid sequence and the returned policy is the only evidence of it.
//   - The replacement landed first. The approval names a generation that is
//     gone and must fail with ErrCheckPolicyStale.
//
// Any other error fails: a caller that cannot tell "you approved an old
// policy" from "the database was busy" has no basis for deciding to retry.
func assertConsentOutcome(t *testing.T, where string, store *Store, approved CheckPolicy, race consentRace) {
	t.Helper()
	if race.grantErr != nil && !errors.Is(race.grantErr, ErrCheckPolicyStale) {
		t.Fatalf("%s: the approval failed with %v, want nil or ErrCheckPolicyStale", where, race.grantErr)
	}

	final, exists, err := store.CheckPolicy(context.Background(), "project")
	if err != nil || !exists {
		t.Fatalf("%s: policy read exists=%v err=%v", where, exists, err)
	}
	// The replacement completed, so the stored policy is the one it wrote and
	// nothing inherited the approval that was granted for the old generation.
	if final.Digest != race.replacement.Digest || final.Version != race.replacement.Version {
		t.Fatalf("%s: stored policy is v%d/%s, want the replacement v%d/%s",
			where, final.Version, final.Digest, race.replacement.Version, race.replacement.Digest)
	}
	if final.ConsentActive {
		t.Fatalf("%s: the replacement kept consent active on v%d/%s",
			where, final.Version, final.Digest)
	}

	if race.grantErr != nil {
		return // the approval lost and said so
	}
	// The approval succeeded, so it must have returned the exact generation it
	// named, carrying active consent for that same generation. Its later
	// removal by the replacement does not make the grant itself wrong.
	if race.granted.Digest != approved.Digest || race.granted.Version != approved.Version {
		t.Fatalf("%s: the approval returned v%d/%s, want the approved v%d/%s",
			where, race.granted.Version, race.granted.Digest, approved.Version, approved.Digest)
	}
	if !race.granted.ConsentActive {
		t.Fatalf("%s: the approval succeeded without active consent", where)
	}
	if race.granted.ConsentDigest != approved.Digest {
		t.Fatalf("%s: the approval recorded consent for %q, want %q",
			where, race.granted.ConsentDigest, approved.Digest)
	}
}

func TestApprovalAndReplacementInEitherOrderNeverInheritConsent(t *testing.T) {
	// Both orders, stated explicitly rather than left to a scheduler. Each has
	// one correct result, and both end with the replacement stored and consent
	// inactive; what differs is whether the approval succeeded or was refused
	// as stale.
	replacementInput := func() CheckPolicyInput {
		input := defaultPolicyInput()
		input.MaxLeaseMS = 90000
		return input
	}

	t.Run("approval lands before the replacement", func(t *testing.T) {
		fixture := newCheckJobFixture(t)
		first, err := fixture.store.SetCheckPolicy(context.Background(), defaultPolicyInput(), fixture.now)
		noErr(t, err)
		// The approval wins. It returns the generation it named with active
		// consent, and that return value is the only record of the grant once
		// the replacement clears it.
		granted, grantErr := fixture.store.GrantCheckConsentFor(context.Background(), "project",
			ExpectedCheckPolicy{Version: first.Version, Digest: first.Digest}, fixture.now)
		if grantErr != nil {
			t.Fatalf("approval on the current generation failed: %v", grantErr)
		}
		second, err := fixture.store.SetCheckPolicy(context.Background(), replacementInput(), fixture.now)
		noErr(t, err)
		if second.ConsentActive {
			t.Fatal("a replacement inherited the consent granted for the previous generation")
		}
		assertConsentOutcome(t, "approval first", fixture.store, first,
			consentRace{granted: granted, grantErr: grantErr, replacement: second})
	})

	t.Run("replacement lands before the approval", func(t *testing.T) {
		fixture := newCheckJobFixture(t)
		first, err := fixture.store.SetCheckPolicy(context.Background(), defaultPolicyInput(), fixture.now)
		noErr(t, err)
		second, err := fixture.store.SetCheckPolicy(context.Background(), replacementInput(), fixture.now)
		noErr(t, err)
		// The approval loses. It names a generation that is gone, so it must
		// say so rather than land on the replacement.
		granted, grantErr := fixture.store.GrantCheckConsentFor(context.Background(), "project",
			ExpectedCheckPolicy{Version: first.Version, Digest: first.Digest}, fixture.now)
		if !errors.Is(grantErr, ErrCheckPolicyStale) {
			t.Fatalf("an approval for a replaced generation returned %v, want ErrCheckPolicyStale", grantErr)
		}
		assertConsentOutcome(t, "replacement first", fixture.store, first,
			consentRace{granted: granted, grantErr: grantErr, replacement: second})
	})
}

func TestConcurrentPolicyChangeNeverInheritsConsent(t *testing.T) {
	// A policy replacement and an approval for the previous generation are
	// submitted from two goroutines.
	//
	// The store opens at most one connection, so the two transactions are
	// serialized rather than interleaved. What this adds over the two
	// deterministic orders above is that neither caller chooses which one
	// lands first, and that both results stay honest under the race detector.
	//
	// Both outcomes are legitimate here, including a successful approval whose
	// consent the replacement then clears, so both operations are captured and
	// checked rather than only the row left behind.
	for attempt := 0; attempt < 24; attempt++ {
		fixture := newCheckJobFixture(t)
		first, err := fixture.store.SetCheckPolicy(context.Background(), defaultPolicyInput(), fixture.now)
		noErr(t, err)
		replacementInput := defaultPolicyInput()
		replacementInput.MaxLeaseMS = 90000

		var wait sync.WaitGroup
		wait.Add(2)
		// Separate locals per goroutine, assembled after both finish, so the
		// test itself shares nothing between them.
		var granted, replaced CheckPolicy
		var grantErr, replaceErr error
		go func() {
			defer wait.Done()
			granted, grantErr = fixture.store.GrantCheckConsentFor(context.Background(), "project",
				ExpectedCheckPolicy{Version: first.Version, Digest: first.Digest}, fixture.now)
		}()
		go func() {
			defer wait.Done()
			replaced, replaceErr = fixture.store.SetCheckPolicy(context.Background(), replacementInput, fixture.now)
		}()
		wait.Wait()
		if replaceErr != nil {
			t.Fatalf("attempt %d: the replacement failed: %v", attempt, replaceErr)
		}

		assertConsentOutcome(t, fmt.Sprintf("attempt %d", attempt), fixture.store, first,
			consentRace{granted: granted, grantErr: grantErr, replacement: replaced})
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

// ---------------------------------------------------------------------------
// structured field refusals
// ---------------------------------------------------------------------------

func TestPolicyRefusalNamesTheFieldAndItsBounds(t *testing.T) {
	fixture := newCheckJobFixture(t)
	cases := map[string]struct {
		mutate func(*CheckPolicyInput)
		field  string
		rule   string
	}{
		"unknown executor": {func(i *CheckPolicyInput) { i.Executor = "vm" }, FieldExecutor, RuleUnknown},
		// The event set is the earliest validation a policy meets. Its
		// refusals are structured like every later one, so the first refusal a
		// reader can hit is not the one with no field attached.
		"no event":       {func(i *CheckPolicyInput) { i.AllowedEvents = nil }, FieldAllowedEvents, RuleRequired},
		"unknown event":  {func(i *CheckPolicyInput) { i.AllowedEvents = []string{"tag"} }, FieldAllowedEvents, RuleUnknown},
		"repeated event": {func(i *CheckPolicyInput) { i.AllowedEvents = []string{"push", "push"} }, FieldAllowedEvents, RuleDuplicate},
		"timeout low":    {func(i *CheckPolicyInput) { i.MaxTimeoutMS = 10 }, FieldMaxTimeoutMS, RuleRange},
		"output high":    {func(i *CheckPolicyInput) { i.MaxOutputLimitBytes = 1 << 30 }, FieldMaxOutputLimitBytes, RuleRange},
		"queue zero":     {func(i *CheckPolicyInput) { i.QueueLimit = 0 }, FieldQueueLimit, RuleRange},
		"active zero":    {func(i *CheckPolicyInput) { i.MaxActiveJobs = 0 }, FieldMaxActiveJobs, RuleRange},
		"lease low":      {func(i *CheckPolicyInput) { i.MaxLeaseMS = 1 }, FieldMaxLeaseMS, RuleRange},
		"source entries": {func(i *CheckPolicyInput) { i.Execution.Source.MaxEntries = 1 << 30 },
			FieldSourceMaxEntries, RuleRange},
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
			// A range refusal carries its bounds as numbers, so a caller never
			// has to parse the sentence and never keeps a second copy.
			if test.rule == RuleRange && refusals[0].Max <= refusals[0].Min {
				t.Fatalf("range refusal has no usable bounds: min=%d max=%d", refusals[0].Min, refusals[0].Max)
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
	// and quoting the same numbers the table publishes.
	fixture := newCheckJobFixture(t)

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
	for _, field := range PublishedPolicyFields() {
		if !covered[field] {
			t.Errorf("%s publishes bounds with no boundary case", field)
		}
	}
}

func TestTheSourceTotalPublishesAMovingFloorAndAFixedCeiling(t *testing.T) {
	// This limit has both: it cannot be smaller than a single file, and it
	// cannot exceed 4 GiB. A moving floor is not a reason to leave the ceiling
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
	if bounds.Max != 4<<30 {
		t.Fatalf("the published ceiling is %d, want 4 GiB", bounds.Max)
	}

	fixture := newCheckJobFixture(t)
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
