package recovery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
)

func importTestManifest(t *testing.T) Manifest {
	t.Helper()
	hash, err := auth.HashPassword("valid-password")
	if err != nil {
		t.Fatal(err)
	}
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
	older := valid
	older.Version = importBackupVersion - 1
	if err := validateManifest(older); err == nil || !strings.Contains(err.Error(), "unsupported import metadata") {
		t.Fatalf("version 8 manifest with import metadata error=%v", err)
	}
	empty := older
	empty.ImportSources, empty.ImportRuns, empty.ImportObservations, empty.ImportIntents = nil, nil, nil, nil
	empty.ImportRunOrderKnown = false
	empty.ImportHEADOwnershipVersion = 0
	if err := validateManifest(empty); err != nil {
		t.Fatalf("version 8 manifest without import metadata rejected: %v", err)
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

func TestImportManifestRejectsUnsupportedHEADOwnershipVersions(t *testing.T) {
	unsupported := importTestManifest(t)
	unsupported.ImportHEADOwnershipVersion = importHEADOwnershipVersion + 1
	if err := validateImportManifest(unsupported); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported ownership version error=%v", err)
	}
	unversioned := importTestManifest(t)
	unversioned.ImportHEADOwnershipVersion = 0
	unversioned.ImportIntents[0].HeadOwned = true
	if err := validateImportManifest(unversioned); err == nil || !strings.Contains(err.Error(), "format version") {
		t.Fatalf("unversioned ownership error=%v", err)
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

func TestLegacyImportManifestOmitsRunOrderEvidence(t *testing.T) {
	manifest := importTestManifest(t)
	manifest.ImportRunOrderKnown = false
	manifest.ImportHEADOwnershipVersion = 0
	manifest.ImportIntents[0].HeadOwned = false
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "import_run_order_known") || strings.Contains(string(encoded), "head_owned") || strings.Contains(string(encoded), "import_head_ownership_version") {
		t.Fatalf("legacy fixture falsely retained new evidence: %s", encoded)
	}
	var decoded Manifest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var snapshot state.RecoveryState
	attachImportState(&snapshot, decoded)
	if snapshot.ImportRunOrderKnown || snapshot.ImportIntents[0].HeadOwned {
		t.Fatal("legacy archive absence became new authority")
	}
}

func TestImportManifestRoundTripClearsMachineLocalConsent(t *testing.T) {
	snapshot := state.RecoveryState{
		ImportRunOrderKnown: true,
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
	if len(restored.ImportSources) != 1 || !restored.ImportRunOrderKnown {
		t.Fatalf("restored sources=%+v order_known=%v", restored.ImportSources, restored.ImportRunOrderKnown)
	}
	source := restored.ImportSources[0]
	if source.AllowPrivateNetwork || !source.GitOnlyConsent || source.SourceGeneration != 3 || source.AuthorityRevision != 5 || source.URL != "https://example.invalid/team/project.git" {
		t.Fatalf("restored source=%+v", source)
	}
	if err := state.ValidateImportRecovery(restored); err != nil {
		t.Fatalf("round-tripped import state invalid: %v", err)
	}
}
