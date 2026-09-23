package recovery

import (
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
)

func importTestManifest(t *testing.T) Manifest {
	t.Helper()
	hash, err := auth.HashPassword("valid-password")
	noErr(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	return Manifest{
		Format: backupFormat, Version: backupVersion, CreatedAt: now,
		AccessMode: "password", AccessHash: hash, AdminHash: hash,
		Repositories: []RepositoryManifest{{ID: "project", Name: "Project", CreatedAt: now, Empty: true}},
		ImportSources: []ImportSourceManifest{{
			RepositoryID: "project", URL: "https://example.invalid/team/project.git",
			SourceGeneration: 1, AuthorityRevision: 2, Mode: state.ImportModeCoexistence, GitOnlyConsent: true,
			CreatedAt: now, UpdatedAt: now,
		}},
		ImportRunOrderKnown:        true,
		ImportHEADOwnershipVersion: importHEADOwnershipVersion,
		ImportRuns: []ImportRunManifest{{
			ID: strings.Repeat("a", 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 2,
			Kind: state.ImportKindInitial, Status: state.ImportRunComplete,
			StartedAt: now, FinishedAt: now, CreatedAt: now, RefsCreated: 1,
		}},
		ImportObservations: []ImportObservationManifest{{
			RepositoryID: "project", SourceGeneration: 1, RefName: "refs/heads/main",
			OID: strings.Repeat("b", 40), ObservedAt: now, RunID: strings.Repeat("a", 32),
		}},
		ImportIntents: []ImportIntentManifest{{
			ID: strings.Repeat("c", 32), RepositoryID: "project", RunID: strings.Repeat("a", 32),
			SourceGeneration: 1, AuthorityRevision: 2, Status: state.ImportIntentComplete,
			Expected:      map[string]string{"refs/heads/main": ""},
			Desired:       map[string]string{"refs/heads/main": strings.Repeat("b", 40)},
			Observed:      map[string]string{"refs/heads/main": strings.Repeat("b", 40)},
			Retained:      map[string]string{},
			ReceiptJSON:   `{"refs/heads/main":"` + strings.Repeat("b", 40) + `"}`,
			ReceiptDigest: state.ImportReceiptDigest(`{"refs/heads/main":"` + strings.Repeat("b", 40) + `"}`),
			CreatedAt:     now, UpdatedAt: now,
		}},
	}
}

func TestManifestImportMetadataVersionGates(t *testing.T) {
	valid := importTestManifest(t)
	if err := validateManifest(valid); err != nil {
		t.Fatalf("valid version 9 import metadata rejected: %v", err)
	}
	dangling := importTestManifest(t)
	dangling.ImportIntents[0].RunID = strings.Repeat("d", 32)
	if err := validateManifest(dangling); err == nil {
		t.Fatal("dangling import intent run was accepted")
	}
	forged := importTestManifest(t)
	forged.ImportIntents[0].ReceiptDigest = strings.Repeat("0", 64)
	if err := validateManifest(forged); err == nil {
		t.Fatal("forged import receipt digest was accepted")
	}
}

// A version 9 manifest from the current build records run order whenever runs
// exist and the HEAD ownership version whenever intents exist. Development
// builds that omitted them are refused.
func TestImportManifestRefusesMissingFormatEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"unsupported ownership version", func(m *Manifest) { m.ImportHEADOwnershipVersion = importHEADOwnershipVersion + 1 }, "backup import HEAD ownership version is unsupported"},
		{"runs without order", func(m *Manifest) { m.ImportRunOrderKnown = false }, "backup import runs have no recorded order; it was written by an unreleased development build"},
		{"intents without ownership version", func(m *Manifest) { m.ImportHEADOwnershipVersion = 0 }, "backup import intents have no HEAD ownership version; it was written by an unreleased development build"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := importTestManifest(t)
			test.mutate(&manifest)
			if err := validateManifest(manifest); err == nil || err.Error() != test.want {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
	// Without runs or intents the evidence is absent in current manifests too.
	empty := importTestManifest(t)
	empty.ImportRuns, empty.ImportObservations, empty.ImportIntents = nil, nil, nil
	empty.ImportRunOrderKnown, empty.ImportHEADOwnershipVersion = false, 0
	if err := validateManifest(empty); err != nil {
		t.Fatalf("current manifest without runs or intents rejected: %v", err)
	}
}

func TestImportManifestRoundTripsExactHEADFacts(t *testing.T) {
	manifest := importTestManifest(t)
	oid := strings.Repeat("b", 40)
	head := "symbolic refs/heads/main " + oid
	manifest.ImportIntents[0].Expected[state.ImportHeadRef] = "symbolic refs/heads/main "
	manifest.ImportIntents[0].Desired[state.ImportHeadRef] = head
	manifest.ImportIntents[0].Observed[state.ImportHeadRef] = head
	manifest.ImportIntents[0].HeadSymref = "refs/heads/main"
	manifest.ImportIntents[0].HeadOwned = true
	manifest.ImportIntents[0].ReceiptJSON = `{"HEAD":"` + head + `","refs/heads/main":"` + oid + `"}`
	manifest.ImportIntents[0].ReceiptDigest = state.ImportReceiptDigest(manifest.ImportIntents[0].ReceiptJSON)
	if err := validateImportManifest(manifest); err != nil {
		t.Fatalf("exact HEAD manifest rejected: %v", err)
	}
	var snapshot state.RecoveryState
	attachImportState(&snapshot, manifest)
	var rebuilt Manifest
	addImportState(&rebuilt, snapshot)
	got := rebuilt.ImportIntents[0]
	if got.Expected[state.ImportHeadRef] != "symbolic refs/heads/main " || got.Desired[state.ImportHeadRef] != head || got.Observed[state.ImportHeadRef] != head || !got.HeadOwned {
		t.Fatalf("HEAD facts or ownership changed across portable round trip: %+v", got)
	}
}

func TestImportManifestRoundTripClearsMachineLocalConsent(t *testing.T) {
	snapshot := state.RecoveryState{
		ImportSources: []state.ImportSource{{
			RepositoryID: "project", URL: "https://example.invalid/team/project.git",
			SourceGeneration: 3, AuthorityRevision: 5, Mode: state.ImportModeStandalone, GitOnlyConsent: true,
			AllowPrivateNetwork: true, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
		}},
		ImportRuns: []state.ImportRun{{
			ID: strings.Repeat("e", 32), RepositoryID: "project", SourceGeneration: 3, AuthorityRevision: 5,
			Kind: state.ImportKindScheduled, Status: state.ImportRunInterrupted,
			StartedAt: time.Unix(3, 0).UTC(), FinishedAt: time.Unix(4, 0).UTC(), CreatedAt: time.Unix(3, 0).UTC(),
		}},
	}
	var manifest Manifest
	addImportState(&manifest, snapshot)
	if len(manifest.ImportSources) != 1 || !manifest.ImportSources[0].GitOnlyConsent || !manifest.ImportRunOrderKnown {
		t.Fatalf("manifest import metadata=%+v order_known=%v", manifest.ImportSources, manifest.ImportRunOrderKnown)
	}
	var restored state.RecoveryState
	attachImportState(&restored, manifest)
	if len(restored.ImportSources) != 1 {
		t.Fatalf("restored sources=%+v", restored.ImportSources)
	}
	source := restored.ImportSources[0]
	if source.AllowPrivateNetwork || !source.GitOnlyConsent || source.SourceGeneration != 3 || source.AuthorityRevision != 5 || source.URL != "https://example.invalid/team/project.git" {
		t.Fatalf("restored source=%+v", source)
	}
	if err := state.ValidateImportRecovery(restored); err != nil {
		t.Fatalf("round-tripped import state invalid: %v", err)
	}
}
