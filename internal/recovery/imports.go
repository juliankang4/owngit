package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"owngit/internal/state"
)

const importHEADOwnershipVersion = 1

// Portable import metadata.
//
// Only facts that must survive a restore travel here: source identity and mode,
// Git-only consent, refresh history, reference observations, and publication
// intents with their receipts. Transport consent, schedules, and raw
// credentials are machine-local and absent by construction.

type ImportSourceManifest struct {
	RepositoryID      string    `json:"repository_id"`
	URL               string    `json:"url"`
	SourceGeneration  int64     `json:"source_generation"`
	AuthorityRevision int64     `json:"authority_revision"`
	Mode              string    `json:"mode"`
	GitOnlyConsent    bool      `json:"git_only_consent,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ImportRunManifest struct {
	ID                  string     `json:"id"`
	RepositoryID        string     `json:"repository_id"`
	SourceGeneration    int64      `json:"source_generation"`
	AuthorityRevision   int64      `json:"authority_revision"`
	Kind                string     `json:"kind"`
	Status              string     `json:"status"`
	StartedAt           time.Time  `json:"started_at"`
	FinishedAt          time.Time  `json:"finished_at,omitempty"`
	CancelRequestedAt   *time.Time `json:"cancel_requested_at,omitempty"`
	ObjectFormat        string     `json:"object_format,omitempty"`
	RefsSeen            int64      `json:"refs_seen,omitempty"`
	RefsCreated         int64      `json:"refs_created,omitempty"`
	RefsUpdated         int64      `json:"refs_updated,omitempty"`
	RefsUnchanged       int64      `json:"refs_unchanged,omitempty"`
	RefsDivergent       int64      `json:"refs_divergent,omitempty"`
	RefsDeletedUpstream int64      `json:"refs_deleted_upstream,omitempty"`
	RefsSkipped         int64      `json:"refs_skipped,omitempty"`
	PackBytes           int64      `json:"pack_bytes,omitempty"`
	HTTPBodyBytes       int64      `json:"http_body_bytes,omitempty"`
	HeadAdvertised      bool       `json:"head_advertised,omitempty"`
	HeadSymref          string     `json:"head_symref,omitempty"`
	ErrorClass          string     `json:"error_class,omitempty"`
	Message             string     `json:"message,omitempty"`
	LFSDetected         int64      `json:"lfs_detected,omitempty"`
	LFSInspectionDone   bool       `json:"lfs_inspection_complete,omitempty"`
	LFSScannedBlobs     int64      `json:"lfs_scanned_blobs,omitempty"`
	LFSScannedBytes     int64      `json:"lfs_scanned_bytes,omitempty"`
	StagingName         string     `json:"staging_name,omitempty"`
	CleanupError        string     `json:"cleanup_error,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

type ImportObservationManifest struct {
	RepositoryID     string    `json:"repository_id"`
	SourceGeneration int64     `json:"source_generation"`
	RefName          string    `json:"ref_name"`
	OID              string    `json:"oid,omitempty"`
	SymrefTarget     string    `json:"symref_target,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
	RunID            string    `json:"run_id,omitempty"`
}

type ImportIntentManifest struct {
	ID                string            `json:"id"`
	RepositoryID      string            `json:"repository_id"`
	RunID             string            `json:"run_id"`
	SourceGeneration  int64             `json:"source_generation"`
	AuthorityRevision int64             `json:"authority_revision"`
	Status            string            `json:"status"`
	Expected          map[string]string `json:"expected"`
	Desired           map[string]string `json:"desired"`
	Observed          map[string]string `json:"observed,omitempty"`
	Retained          map[string]string `json:"retained"`
	HeadSymref        string            `json:"head_symref,omitempty"`
	HeadDetach        string            `json:"head_detach,omitempty"`
	HeadOwned         bool              `json:"head_owned,omitempty"`
	ReceiptJSON       string            `json:"receipt_json,omitempty"`
	ReceiptDigest     string            `json:"receipt_digest,omitempty"`
	Reason            string            `json:"reason,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

func addImportState(manifest *Manifest, snapshot state.RecoveryState) {
	manifest.ImportRunOrderKnown = len(snapshot.ImportRuns) > 0
	if len(snapshot.ImportIntents) > 0 {
		manifest.ImportHEADOwnershipVersion = importHEADOwnershipVersion
	}
	for _, source := range snapshot.ImportSources {
		manifest.ImportSources = append(manifest.ImportSources, ImportSourceManifest{
			RepositoryID: source.RepositoryID, URL: source.URL, SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
			Mode: source.Mode, GitOnlyConsent: source.GitOnlyConsent, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
		})
	}
	for _, run := range snapshot.ImportRuns {
		manifest.ImportRuns = append(manifest.ImportRuns, ImportRunManifest{
			ID: run.ID, RepositoryID: run.RepositoryID, SourceGeneration: run.SourceGeneration, AuthorityRevision: run.AuthorityRevision,
			Kind: run.Kind, Status: run.Status, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
			CancelRequestedAt: run.CancelRequestedAt, ObjectFormat: run.ObjectFormat,
			RefsSeen: run.RefsSeen, RefsCreated: run.RefsCreated, RefsUpdated: run.RefsUpdated, RefsUnchanged: run.RefsUnchanged,
			RefsDivergent: run.RefsDivergent, RefsDeletedUpstream: run.RefsDeletedUpstream, RefsSkipped: run.RefsSkipped,
			PackBytes: run.PackBytes, HTTPBodyBytes: run.HTTPBodyBytes, HeadAdvertised: run.HeadAdvertised,
			HeadSymref: run.HeadSymref, ErrorClass: run.ErrorClass, Message: run.Message,
			LFSDetected: run.LFSDetected, LFSInspectionDone: run.LFSInspectionDone,
			LFSScannedBlobs: run.LFSScannedBlobs, LFSScannedBytes: run.LFSScannedBytes,
			StagingName: run.StagingName, CleanupError: run.CleanupError, CreatedAt: run.CreatedAt,
		})
	}
	for _, observation := range snapshot.ImportObservations {
		manifest.ImportObservations = append(manifest.ImportObservations, ImportObservationManifest{
			RepositoryID: observation.RepositoryID, SourceGeneration: observation.SourceGeneration,
			RefName: observation.RefName, OID: observation.OID, SymrefTarget: observation.SymrefTarget,
			ObservedAt: observation.ObservedAt, RunID: observation.RunID,
		})
	}
	for _, intent := range snapshot.ImportIntents {
		manifest.ImportIntents = append(manifest.ImportIntents, ImportIntentManifest{
			ID: intent.ID, RepositoryID: intent.RepositoryID, RunID: intent.RunID,
			SourceGeneration: intent.SourceGeneration, AuthorityRevision: intent.AuthorityRevision, Status: intent.Status,
			Expected: intent.Expected, Desired: intent.Desired, Observed: intent.Observed, Retained: intent.Retained,
			HeadSymref: intent.HeadSymref, HeadDetach: intent.HeadDetach, HeadOwned: intent.HeadOwned,
			ReceiptJSON: intent.ReceiptJSON, ReceiptDigest: intent.ReceiptDigest, Reason: intent.Reason,
			CreatedAt: intent.CreatedAt, UpdatedAt: intent.UpdatedAt,
		})
	}
}

func attachImportState(snapshot *state.RecoveryState, manifest Manifest) {
	for _, source := range manifest.ImportSources {
		// Transport consent is machine-local and not part of the manifest.
		snapshot.ImportSources = append(snapshot.ImportSources, state.ImportSource{
			RepositoryID: source.RepositoryID, URL: source.URL, SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
			Mode: source.Mode, GitOnlyConsent: source.GitOnlyConsent, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
		})
	}
	for _, run := range manifest.ImportRuns {
		snapshot.ImportRuns = append(snapshot.ImportRuns, state.ImportRun{
			ID: run.ID, RepositoryID: run.RepositoryID, SourceGeneration: run.SourceGeneration, AuthorityRevision: run.AuthorityRevision,
			Kind: run.Kind, Status: run.Status, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
			CancelRequestedAt: run.CancelRequestedAt, ObjectFormat: run.ObjectFormat,
			RefsSeen: run.RefsSeen, RefsCreated: run.RefsCreated, RefsUpdated: run.RefsUpdated, RefsUnchanged: run.RefsUnchanged,
			RefsDivergent: run.RefsDivergent, RefsDeletedUpstream: run.RefsDeletedUpstream, RefsSkipped: run.RefsSkipped,
			PackBytes: run.PackBytes, HTTPBodyBytes: run.HTTPBodyBytes, HeadAdvertised: run.HeadAdvertised,
			HeadSymref: run.HeadSymref, ErrorClass: run.ErrorClass, Message: run.Message,
			LFSDetected: run.LFSDetected, LFSInspectionDone: run.LFSInspectionDone,
			LFSScannedBlobs: run.LFSScannedBlobs, LFSScannedBytes: run.LFSScannedBytes,
			StagingName: run.StagingName, CleanupError: run.CleanupError, CreatedAt: run.CreatedAt,
		})
	}
	for _, observation := range manifest.ImportObservations {
		snapshot.ImportObservations = append(snapshot.ImportObservations, state.ImportObservation{
			RepositoryID: observation.RepositoryID, SourceGeneration: observation.SourceGeneration,
			RefName: observation.RefName, OID: observation.OID, SymrefTarget: observation.SymrefTarget,
			ObservedAt: observation.ObservedAt, RunID: observation.RunID,
		})
	}
	for _, intent := range manifest.ImportIntents {
		snapshot.ImportIntents = append(snapshot.ImportIntents, state.ImportIntent{
			ID: intent.ID, RepositoryID: intent.RepositoryID, RunID: intent.RunID,
			SourceGeneration: intent.SourceGeneration, AuthorityRevision: intent.AuthorityRevision, Status: intent.Status,
			Expected: intent.Expected, Desired: intent.Desired, Observed: intent.Observed, Retained: intent.Retained,
			HeadSymref: intent.HeadSymref, HeadDetach: intent.HeadDetach,
			HeadOwned:   intent.HeadOwned,
			ReceiptJSON: intent.ReceiptJSON, ReceiptDigest: intent.ReceiptDigest, Reason: intent.Reason,
			CreatedAt: intent.CreatedAt, UpdatedAt: intent.UpdatedAt,
		})
	}
}

// validateImportManifest validates decoded import metadata and its internal
// consistency before a backup is published or restored.
// The current format records run order whenever runs exist and the HEAD
// ownership version whenever intents exist. Only unreleased development
// builds omitted them.
func validateImportManifest(manifest Manifest) error {
	if manifest.ImportHEADOwnershipVersion != 0 && manifest.ImportHEADOwnershipVersion != importHEADOwnershipVersion {
		return errors.New("backup import HEAD ownership version is unsupported")
	}
	if len(manifest.ImportRuns) > 0 && !manifest.ImportRunOrderKnown {
		return errors.New("backup import runs have no recorded order; it was written by an unreleased development build")
	}
	if len(manifest.ImportIntents) > 0 && manifest.ImportHEADOwnershipVersion != importHEADOwnershipVersion {
		return errors.New("backup import intents have no HEAD ownership version; it was written by an unreleased development build")
	}
	var snapshot state.RecoveryState
	for _, item := range manifest.Repositories {
		snapshot.Repositories = append(snapshot.Repositories, state.Repository{ID: item.ID, Name: item.Name, CreatedAt: item.CreatedAt})
	}
	attachImportState(&snapshot, manifest)
	if err := state.ValidateImportRecovery(snapshot); err != nil {
		return fmt.Errorf("backup import metadata is invalid: %w", err)
	}
	for _, intent := range manifest.ImportIntents {
		for name, values := range map[string]map[string]string{"expected": intent.Expected, "desired": intent.Desired, "observed": intent.Observed, "retained": intent.Retained} {
			encoded, err := json.Marshal(values)
			if err != nil {
				return fmt.Errorf("backup import intent %s %s is not encodable: %w", intent.ID, name, err)
			}
			if len(encoded) > state.MaxImportIntentJSONBytes {
				return fmt.Errorf("backup import intent %s %s exceeds its bound", intent.ID, name)
			}
		}
	}
	return nil
}
