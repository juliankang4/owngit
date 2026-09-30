package state

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Each container option is off when absent, accepted only for the container
// executor, validated the same way on save and on restore, and part of the
// policy digest.
func TestContainerOptionsAreValidatedOneFieldAtATime(t *testing.T) {
	fixture := newCheckJobFixture(t)
	refused := func(name string, mutate func(*CheckPolicyInput), field, rule string) {
		t.Helper()
		input := containerPolicyInput()
		mutate(&input)
		_, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
		refusals := PolicyFieldErrors(err)
		if !errors.Is(err, ErrInvalidCheckPolicy) || len(refusals) != 1 || refusals[0].Field != field || refusals[0].Rule != rule {
			t.Errorf("%s: error=%v, want %s by %s", name, err, field, rule)
		}
	}
	refused("tag without allowing tags", func(input *CheckPolicyInput) { input.Execution.ContainerImage = "registry.example/checks:1" },
		FieldContainerImage, RuleFormat)
	refused("option-like image", func(input *CheckPolicyInput) {
		input.Execution.ContainerAllowTags = true
		input.Execution.ContainerImage = "--privileged"
	}, FieldContainerImage, RuleFormat)
	refused("pulling a bare image ID", func(input *CheckPolicyInput) {
		input.Execution.ContainerImage = "sha256:" + strings.Repeat("a", 64)
		input.Execution.ContainerPullMissing = true
	}, FieldContainerPullMissing, RuleNeedsRepository)
	refused("host network", func(input *CheckPolicyInput) { input.Execution.ContainerNetwork = "host" }, FieldContainerNetwork, RuleForbidden)
	for _, network := range []string{"default", "container:other", "-net", "a b", strings.Repeat("n", 129)} {
		refused("network "+network, func(input *CheckPolicyInput) { input.Execution.ContainerNetwork = network }, FieldContainerNetwork, RuleUnknown)
	}
	refused("unknown limit", func(input *CheckPolicyInput) { input.Execution.ContainerMissingEnforcement = []string{"disk"} },
		FieldContainerMissingEnforcement, RuleUnknown)
	refused("repeated limit", func(input *CheckPolicyInput) { input.Execution.ContainerMissingEnforcement = []string{"swap", "swap"} },
		FieldContainerMissingEnforcement, RuleDuplicate)

	for field, mutate := range map[string]func(*CheckExecutionSettings){
		FieldContainerAllowTags:          func(settings *CheckExecutionSettings) { settings.ContainerAllowTags = true },
		FieldContainerPullMissing:        func(settings *CheckExecutionSettings) { settings.ContainerPullMissing = true },
		FieldContainerMissingEnforcement: func(settings *CheckExecutionSettings) { settings.ContainerMissingEnforcement = []string{"swap"} },
		FieldContainerImageVolumes:       func(settings *CheckExecutionSettings) { settings.ContainerImageVolumes = true },
		FieldContainerWritableRoot:       func(settings *CheckExecutionSettings) { settings.ContainerWritableRoot = true },
	} {
		for _, executor := range []string{CheckExecutorHost, CheckExecutorExternalRunner} {
			refused(field+" for "+executor, func(input *CheckPolicyInput) {
				input.Executor = executor
				input.Execution = CheckExecutionSettings{}
				mutate(&input.Execution)
			}, field, RuleNotApplicable)
		}
	}
}

func TestContainerOptionsAreStoredCanonicallyAndChangeTheDigest(t *testing.T) {
	fixture := newCheckJobFixture(t)
	plain, err := fixture.store.SetCheckPolicy(context.Background(), containerPolicyInput(), fixture.now)
	noErr(t, err)
	if plain.Execution.HasContainerOptions() {
		t.Fatalf("a policy without options reports options: %+v", plain.Execution)
	}
	_, err = fixture.store.GrantCheckConsentFor(context.Background(), plain.RepositoryID, ExpectedCheckPolicy{Version: plain.Version, Digest: plain.Digest}, fixture.now)
	noErr(t, err)

	input := containerPolicyInput()
	input.Execution.ContainerImage = "registry.example:5000/team/checks:1.2"
	input.Execution.ContainerAllowTags = true
	input.Execution.ContainerPullMissing = true
	input.Execution.ContainerMissingEnforcement = []string{"swap", "cpu"}
	input.Execution.ContainerImageVolumes = true
	input.Execution.ContainerWritableRoot = true
	input.Execution.ContainerNetwork = "checks-net"
	stored, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
	noErr(t, err)
	if stored.Digest == plain.Digest || stored.ConsentActive {
		t.Fatalf("changing options kept the digest or consent: %+v", stored)
	}
	if !reflect.DeepEqual(stored.Execution.ContainerMissingEnforcement, []string{"cpu", "swap"}) || !stored.Execution.HasContainerOptions() {
		t.Fatalf("stored options are not canonical: %+v", stored.Execution)
	}
	read, _, err := fixture.store.CheckPolicy(context.Background(), stored.RepositoryID)
	noErr(t, err)
	if !reflect.DeepEqual(read.Execution, stored.Execution) {
		t.Fatalf("read %+v, stored %+v", read.Execution, stored.Execution)
	}
	// Restore applies the same normalization and requires it to be a no-op.
	normalized, err := normalizeCheckExecutionSettings(read.Executor, read.Execution)
	if err != nil || !reflect.DeepEqual(normalized, read.Execution) {
		t.Fatalf("stored options do not survive restore validation: %+v %v", normalized, err)
	}
	if registry := ContainerImageRegistry(input.Execution.ContainerImage); registry != "registry.example:5000" {
		t.Fatalf("registry=%q", registry)
	}
	if registry := ContainerImageRegistry("checks:1"); registry != "docker.io" {
		t.Fatalf("registry of a short name=%q", registry)
	}
}
