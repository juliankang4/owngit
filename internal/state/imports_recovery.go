package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Portable import recovery.
//
// Source identity, mode, Git-only consent, refresh history, reference
// observations, and publication intents travel in a backup. Transport consent
// and schedules are machine-local: the snapshot clears them, and a restore
// refuses to revive them. Credentials live in a file outside the database and
// are therefore absent after restore. Active runs and unconfirmed intents
// become visible recoverable states instead of being treated as finished.

// readImportRecovery reads portable import state inside the snapshot
// transaction.
func readImportRecovery(ctx context.Context, tx *sql.Tx, snapshot *RecoveryState) error {
	rows, err := tx.QueryContext(ctx, `SELECT repository_id,url,source_generation,authority_revision,mode,git_only_consent,created_at,updated_at
		FROM import_sources ORDER BY repository_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var record ImportSource
		var created, updated int64
		if err := rows.Scan(&record.RepositoryID, &record.URL, &record.SourceGeneration, &record.AuthorityRevision, &record.Mode, &record.GitOnlyConsent, &created, &updated); err != nil {
			rows.Close()
			return err
		}
		record.CreatedAt = unixTime(created)
		record.UpdatedAt = unixTime(updated)
		// Transport consent is machine-local.
		record.AllowPrivateNetwork = false
		snapshot.ImportSources = append(snapshot.ImportSources, record)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	// Preserve admission order across repositories so current snapshots restore
	// the rowid chronology used by history and accepted-snapshot projection.
	rows, err = tx.QueryContext(ctx, importRunSelect+` ORDER BY rowid`)
	if err != nil {
		return err
	}
	for rows.Next() {
		record, err := scanImportRun(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.ImportRuns = append(snapshot.ImportRuns, record)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `SELECT repository_id,source_generation,ref_name,oid,symref_target,observed_at,run_id
		FROM import_ref_observations ORDER BY repository_id,source_generation,ref_name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var record ImportObservation
		var observed int64
		if err := rows.Scan(&record.RepositoryID, &record.SourceGeneration, &record.RefName, &record.OID, &record.SymrefTarget, &observed, &record.RunID); err != nil {
			rows.Close()
			return err
		}
		record.ObservedAt = unixTime(observed)
		snapshot.ImportObservations = append(snapshot.ImportObservations, record)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, importIntentSelect+` ORDER BY repository_id,created_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		record, err := scanImportIntent(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.ImportIntents = append(snapshot.ImportIntents, record)
	}
	return closeRows(rows)
}

// restoreImportRecovery inserts portable import state into a new store. Every
// machine-bound field is invalidated, never inherited.
func restoreImportRecovery(ctx context.Context, tx *sql.Tx, snapshot RecoveryState) error {
	for _, source := range snapshot.ImportSources {
		// Restore creates a new machine-local execution authority while retaining
		// source identity and observation ownership.
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_sources(repository_id,url,source_generation,authority_revision,credential_generation,mode,git_only_consent,allow_private_network,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,0,?,?)`,
			source.RepositoryID, source.URL, source.SourceGeneration, source.AuthorityRevision+1, "", source.Mode, boolInt(source.GitOnlyConsent), source.CreatedAt.Unix(), source.UpdatedAt.Unix()); err != nil {
			return fmt.Errorf("restore import source %q: %w", source.RepositoryID, err)
		}
	}
	for _, run := range snapshot.ImportRuns {
		status := run.Status
		finished := run.FinishedAt
		message := run.Message
		if importRunActive(status) {
			status = ImportRunInterrupted
			if finished.IsZero() {
				finished = run.StartedAt
			}
			if message == "" {
				message = "interrupted before restore"
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_runs(
			id,repository_id,source_generation,authority_revision,kind,status,started_at,finished_at,cancel_requested_at,object_format,
			refs_seen,refs_created,refs_updated,refs_unchanged,refs_divergent,refs_deleted_upstream,refs_skipped,
			pack_bytes,http_body_bytes,head_advertised,head_symref,error_class,message,
			lfs_detected,lfs_inspection_complete,lfs_scanned_blobs,lfs_scanned_bytes,staging_name,cleanup_error,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			run.ID, run.RepositoryID, run.SourceGeneration, run.AuthorityRevision, run.Kind, status, run.StartedAt.Unix(), finished.Unix(), nullableUnix(run.CancelRequestedAt), run.ObjectFormat,
			run.RefsSeen, run.RefsCreated, run.RefsUpdated, run.RefsUnchanged, run.RefsDivergent, run.RefsDeletedUpstream, run.RefsSkipped,
			run.PackBytes, run.HTTPBodyBytes, boolInt(run.HeadAdvertised), run.HeadSymref, run.ErrorClass, message,
			run.LFSDetected, boolInt(run.LFSInspectionDone), run.LFSScannedBlobs, run.LFSScannedBytes, run.StagingName, run.CleanupError, run.CreatedAt.Unix()); err != nil {
			return fmt.Errorf("restore import run %q: %w", run.ID, err)
		}
	}
	for _, observation := range snapshot.ImportObservations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_ref_observations(repository_id,source_generation,ref_name,oid,symref_target,observed_at,run_id)
			VALUES(?,?,?,?,?,?,?)`,
			observation.RepositoryID, observation.SourceGeneration, observation.RefName, observation.OID, observation.SymrefTarget, observation.ObservedAt.Unix(), observation.RunID); err != nil {
			return fmt.Errorf("restore import observation %q: %w", observation.RefName, err)
		}
	}
	for _, intent := range snapshot.ImportIntents {
		expected, err := encodeImportMap(intent.Expected)
		if err != nil {
			return err
		}
		desired, err := encodeImportMap(intent.Desired)
		if err != nil {
			return err
		}
		observed, err := encodeImportMap(intent.Observed)
		if err != nil {
			return err
		}
		retained, err := encodeImportMap(intent.Retained)
		if err != nil {
			return err
		}
		status := intent.Status
		reason := intent.Reason
		headOwned := intent.HeadOwned
		if status == ImportIntentPlanning || status == ImportIntentApplied || status == ImportIntentInvalidated {
			status = ImportIntentInvalidated
			headOwned = false
			if reason == "" {
				reason = "unfinished publication authority invalidated by restore"
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_publication_intents(
			id,repository_id,run_id,source_generation,authority_revision,status,expected_json,desired_json,observed_json,retained_json,head_symref,head_detach,head_owned,
			receipt_json,receipt_digest,reason,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			intent.ID, intent.RepositoryID, intent.RunID, intent.SourceGeneration, intent.AuthorityRevision, status, expected, desired, observed, retained,
			intent.HeadSymref, intent.HeadDetach, headOwned, intent.ReceiptJSON, intent.ReceiptDigest, reason, intent.CreatedAt.Unix(), intent.UpdatedAt.Unix()); err != nil {
			return fmt.Errorf("restore import intent %q: %w", intent.ID, err)
		}
	}
	return nil
}

// ValidateImportRecovery checks a complete snapshot's import state, including
// cross-record identity, before anything is written.
func ValidateImportRecovery(snapshot RecoveryState) error {
	sources := make(map[string]bool)
	for _, source := range snapshot.ImportSources {
		if err := validateImportSourceRecord(source); err != nil {
			return err
		}
		if source.CredentialGeneration != "" {
			return errors.New("portable import source contains machine-local credential authority")
		}
		if sources[source.RepositoryID] {
			return errors.New("duplicate import source repository")
		}
		sources[source.RepositoryID] = true
	}
	runs := make(map[string]ImportRun)
	for _, run := range snapshot.ImportRuns {
		if err := validateImportRunRecord(run, importRunActive(run.Status)); err != nil {
			return err
		}
		if _, exists := runs[run.ID]; exists {
			return errors.New("duplicate import run identity")
		}
		runs[run.ID] = run
		if source, exists := importRecoverySource(snapshot, run.RepositoryID); exists {
			if run.SourceGeneration > source.SourceGeneration {
				return errors.New("import run belongs to a newer source generation than the snapshot source")
			}
			if run.AuthorityRevision > source.AuthorityRevision {
				return errors.New("import run belongs to a newer authority than the snapshot source")
			}
		}
	}
	observations := make(map[string]bool)
	for _, observation := range snapshot.ImportObservations {
		if err := validateImportObservationRecord(observation); err != nil {
			return err
		}
		key := fmt.Sprintf("%s/%d/%s", observation.RepositoryID, observation.SourceGeneration, observation.RefName)
		if observations[key] {
			return errors.New("duplicate import observation")
		}
		observations[key] = true
		if err := importRecoveryCheckRef(observation.RefName); err != nil {
			return err
		}
		if observation.RunID != "" {
			run, exists := runs[observation.RunID]
			if !exists {
				return errors.New("import observation refers to a missing run")
			}
			if run.RepositoryID != observation.RepositoryID || run.SourceGeneration != observation.SourceGeneration {
				return errors.New("import observation identity does not match its run")
			}
		} else if err := importRecoveryObservationSource(snapshot, observation); err != nil {
			return err
		}
	}
	intents := make(map[string]bool)
	intentRuns := make(map[string]bool)
	for _, intent := range snapshot.ImportIntents {
		if err := validateImportIntentRecord(intent); err != nil {
			return err
		}
		if intents[intent.ID] {
			return errors.New("duplicate import intent identity")
		}
		intents[intent.ID] = true
		if intentRuns[intent.RunID] {
			return errors.New("import run has multiple publication intents")
		}
		intentRuns[intent.RunID] = true
		run, exists := runs[intent.RunID]
		if !exists {
			return errors.New("import intent refers to a missing run")
		}
		if run.RepositoryID != intent.RepositoryID || run.SourceGeneration != intent.SourceGeneration || run.AuthorityRevision != intent.AuthorityRevision {
			return errors.New("import intent authority does not match its run")
		}
		if !importRecoveryRepositoryExists(snapshot, run.RepositoryID) && !importIntentSettledWithoutRepository(intent.Status) {
			return fmt.Errorf("import publication intent for repository %q is %s, but the repository is not recorded. "+
				"Start and stop OwnGit once so startup reconciliation can settle it, then create the backup again. "+
				"If this message remains, reconciliation preserved an unpublished .owngit-create-* directory of that import in the repository root "+
				"because it could not prove the directory is its own or incomplete; move that directory out of the repository root, then repeat these steps",
				run.RepositoryID, intent.Status)
		}
		_, hasExactHEAD := intent.Expected[ImportHeadRef]
		for ref, oid := range intent.Retained {
			if !hasImportRefPrefix(ref, "refs/owngit/") {
				return errors.New("import intent retention is outside protected refs")
			}
			if !hasExactHEAD {
				// Legacy intents did not place retention expectations in the ref maps.
				continue
			}
			if intent.Desired[ref] != oid {
				return errors.New("import intent retention is not a protected desired ref")
			}
			if _, exists := intent.Expected[ref]; !exists {
				return errors.New("import intent retention has no expected ref fact")
			}
		}
		for ref := range intent.Desired {
			if _, retained := intent.Retained[ref]; retained {
				continue
			}
			if err := importRecoveryCheckRef(ref); err != nil {
				return err
			}
		}
		for ref := range intent.Expected {
			if _, retained := intent.Retained[ref]; retained {
				continue
			}
			if err := importRecoveryCheckRef(ref); err != nil {
				return err
			}
		}
	}
	if len(snapshot.ImportInitialDestinations) > 0 {
		return errors.New("portable import snapshot contains machine-local initial destinations")
	}
	for _, schedule := range snapshot.ImportSchedules {
		if !sources[schedule.RepositoryID] {
			return errors.New("import schedule does not reference an import source")
		}
	}
	return nil
}

// importIntentSettledWithoutRepository reports intents that may describe a
// repository that was never created, such as an initial import that stopped
// before publication. They are finished history: they authorize nothing and
// block nothing, so they travel as they are. An open, unresolved, or complete
// intent needs its repository, and a snapshot without it fails closed.
func importIntentSettledWithoutRepository(status string) bool {
	switch status {
	case ImportIntentNotApplied, ImportIntentAbandoned, ImportIntentInvalidated:
		return true
	default:
		return false
	}
}

func importRecoveryObservationSource(snapshot RecoveryState, observation ImportObservation) error {
	source, exists := importRecoverySource(snapshot, observation.RepositoryID)
	if !exists || observation.SourceGeneration <= 0 || observation.SourceGeneration > source.SourceGeneration {
		return errors.New("import observation without a run does not match a source generation")
	}
	return nil
}

func importRecoveryRepositoryExists(snapshot RecoveryState, repositoryID string) bool {
	for _, repository := range snapshot.Repositories {
		if repository.ID == repositoryID {
			return true
		}
	}
	return false
}

func importRecoverySource(snapshot RecoveryState, repositoryID string) (ImportSource, bool) {
	for _, source := range snapshot.ImportSources {
		if source.RepositoryID == repositoryID {
			return source, true
		}
	}
	return ImportSource{}, false
}

func importRecoveryCheckRef(name string) error {
	if name == ImportHeadRef {
		return nil
	}
	if !validImportRefName(name) {
		return fmt.Errorf("import recovery contains invalid ref %q", name)
	}
	if !hasImportRefPrefix(name, "refs/heads/") && !hasImportRefPrefix(name, "refs/tags/") {
		return fmt.Errorf("import recovery ref %q is outside the promised branch and tag namespaces", name)
	}
	return nil
}

func hasImportRefPrefix(name, prefix string) bool {
	return len(name) > len(prefix) && name[:len(prefix)] == prefix
}
