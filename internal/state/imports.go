package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Inbound import state.
//
// The import source, its refresh history, reference observations, publication
// intents, and staging ownership records are durable. Transport consent and
// schedules are machine-local and are cleared by an offline restore. Raw HTTP
// credentials never enter this database; they live in an owner-only file
// beside it (see import_credentials.go).
const (
	ImportModeStandalone  = "standalone"
	ImportModeCoexistence = "coexistence"

	ImportKindInitial   = "initial"
	ImportKindRefresh   = "refresh"
	ImportKindScheduled = "scheduled"

	ImportRunPreparing   = "preparing"
	ImportRunFetching    = "fetching"
	ImportRunIndexing    = "indexing"
	ImportRunInspecting  = "inspecting"
	ImportRunPublishing  = "publishing"
	ImportRunComplete    = "complete"
	ImportRunFailed      = "failed"
	ImportRunCancelled   = "cancelled"
	ImportRunSuperseded  = "superseded"
	ImportRunInterrupted = "interrupted"
	ImportRunUnresolved  = "unresolved"

	ImportIntentPlanning    = "planning"
	ImportIntentApplied     = "applied"
	ImportIntentComplete    = "complete"
	ImportIntentNotApplied  = "not_applied"
	ImportIntentAbandoned   = "abandoned"
	ImportIntentUnresolved  = "unresolved"
	ImportIntentInvalidated = "invalidated"
	// ImportIntentOwnerResolved is terminal. The owner accepted the
	// destination as found after an unresolved outcome. Its receipt records
	// what was read back, not a confirmed publication.
	ImportIntentOwnerResolved = "owner_resolved"

	ImportStagingActive        = "active"
	ImportStagingReleased      = "released"
	ImportStagingCleanupFailed = "cleanup_failed"
	ImportStagingUnknown       = "unknown"

	// Unpublished initial destination ownership. These rows are machine-local.
	// Unknown is informational and never authorizes removal or publication.
	ImportInitialPreparing     = "preparing"
	ImportInitialReady         = "ready"
	ImportInitialPublished     = "published"
	ImportInitialReleased      = "released"
	ImportInitialCleanupFailed = "cleanup_failed"
	ImportInitialUnknown       = "unknown"

	// ImportHeadRef is the observation key for HEAD facts.
	ImportHeadRef = "HEAD"

	maxImportURLBytes = 8192
	maxImportMessage  = 500
	// MaxImportRefNameBytes bounds every ref name recorded for an import,
	// including retention names derived from a published ref. The observation
	// tables enforce the same bound in their schema.
	MaxImportRefNameBytes = 500
	maxImportRefName      = MaxImportRefNameBytes
	// MaxImportIntentJSONBytes bounds one expected/desired/retained map or
	// receipt body.
	MaxImportIntentJSONBytes = 8 << 20
	maxImportIntentJSON      = MaxImportIntentJSONBytes
	maxImportScheduleStart   = 60
	maxImportScheduleEnd     = 7 * 24 * 60 * 60
)

// ErrImportActive reports an import run that is still in progress for the
// repository.
var ErrImportActive = errors.New("an import run is already active")

// ImportSource is one repository's persisted inbound source configuration.
// SourceGeneration advances only when the URL identity changes, preserving
// same-source observations. AuthorityRevision advances for every effective
// execution-authority change and invalidates work admitted under an older
// configuration. CredentialGeneration is a nonsecret machine-local binding
// identifier and is deliberately omitted from portable recovery state.
type ImportSource struct {
	RepositoryID         string
	URL                  string
	SourceGeneration     int64
	AuthorityRevision    int64
	CredentialGeneration string
	Mode                 string
	GitOnlyConsent       bool
	AllowPrivateNetwork  bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ImportSourceInput is one explicit source configuration mutation.
type ImportSourceInput struct {
	RepositoryID        string
	URL                 string
	Mode                string
	GitOnlyConsent      bool
	AllowPrivateNetwork bool
	Now                 time.Time
}

// ImportRun is one append-only initial import, refresh, or scheduled refresh.
type ImportRun struct {
	ID                  string
	RepositoryID        string
	SourceGeneration    int64
	AuthorityRevision   int64
	Kind                string
	Status              string
	StartedAt           time.Time
	FinishedAt          time.Time
	CancelRequestedAt   *time.Time
	ObjectFormat        string
	RefsSeen            int64
	RefsCreated         int64
	RefsUpdated         int64
	RefsUnchanged       int64
	RefsDivergent       int64
	RefsDeletedUpstream int64
	RefsSkipped         int64
	PackBytes           int64
	HTTPBodyBytes       int64
	HeadAdvertised      bool
	HeadSymref          string
	ErrorClass          string
	Message             string
	LFSDetected         int64
	LFSInspectionDone   bool
	LFSScannedBlobs     int64
	LFSScannedBytes     int64
	StagingName         string
	CleanupError        string
	CreatedAt           time.Time
	// RowID is the SQLite insertion order used as a history cursor. It is not
	// portable backup state.
	RowID int64
}

// ImportObservation is the last observed source fact for one ref in one source
// generation. The observation, not the local ref, decides whether an upstream
// move may replace a previously imported tip.
type ImportObservation struct {
	RepositoryID     string
	SourceGeneration int64
	RefName          string
	OID              string
	SymrefTarget     string
	ObservedAt       time.Time
	RunID            string
}

// ImportIntent is a durable publication intent and its verifying receipt.
// Expected and Desired describe the destination before and after publication;
// Observed records what the source advertised so source facts are not lost
// when a divergent ref stays local.
type ImportIntent struct {
	ID                string
	RepositoryID      string
	RunID             string
	SourceGeneration  int64
	AuthorityRevision int64
	Status            string
	Expected          map[string]string
	Desired           map[string]string
	Observed          map[string]string
	Retained          map[string]string
	HeadSymref        string
	HeadDetach        string
	HeadOwned         bool
	ReceiptJSON       string
	ReceiptDigest     string
	Reason            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	// RowID is the SQLite insertion order used as a reconciliation cursor.
	RowID int64
}

// ImportInitialDestination records ownership of one unpublished initial
// repository directory. A directory name or marker alone is not this record.
type ImportInitialDestination struct {
	Name         string
	RepositoryID string
	RunID        string
	RootID       string
	Token        string
	DisplayName  string
	Description  string
	State        string
	Issue        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ImportStaging records ownership of one unpublished staging directory.
type ImportStaging struct {
	Name         string
	RepositoryID string
	RunID        string
	Token        string
	State        string
	Issue        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ImportSchedule is a machine-local opt-in refresh schedule.
type ImportSchedule struct {
	RepositoryID    string
	Enabled         bool
	IntervalSeconds int64
	LastStartedAt   *time.Time
	LastFinishedAt  *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (s *Store) ConfigureImportSource(ctx context.Context, input ImportSourceInput) (ImportSource, error) {
	if !validText(input.RepositoryID, 100) {
		return ImportSource{}, errors.New("invalid import repository identifier")
	}
	if !validText(input.URL, maxImportURLBytes) {
		return ImportSource{}, errors.New("invalid import source URL")
	}
	if input.Mode != ImportModeStandalone && input.Mode != ImportModeCoexistence {
		return ImportSource{}, errors.New("invalid import mode")
	}
	if input.Now.IsZero() {
		return ImportSource{}, errors.New("import source time is required")
	}
	releaseCredentialAuthority := s.LockImportCredentialAuthority(input.RepositoryID)
	defer releaseCredentialAuthority()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportSource{}, err
	}
	defer tx.Rollback()
	var current ImportSource
	var created, updated int64
	err = tx.QueryRowContext(ctx, `SELECT repository_id,url,source_generation,authority_revision,credential_generation,mode,git_only_consent,allow_private_network,created_at,updated_at FROM import_sources WHERE repository_id=?`, input.RepositoryID).
		Scan(&current.RepositoryID, &current.URL, &current.SourceGeneration, &current.AuthorityRevision, &current.CredentialGeneration, &current.Mode, &current.GitOnlyConsent, &current.AllowPrivateNetwork, &created, &updated)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A deleted and re-created source must not collide with authority stamped
		// on retained run history.
		var priorGeneration, priorAuthority int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(source_generation),0),COALESCE(MAX(authority_revision),0) FROM import_runs WHERE repository_id=?`, input.RepositoryID).Scan(&priorGeneration, &priorAuthority); err != nil {
			return ImportSource{}, err
		}
		current = ImportSource{
			RepositoryID: input.RepositoryID, URL: input.URL, SourceGeneration: priorGeneration + 1, AuthorityRevision: priorAuthority + 1,
			CredentialGeneration: "", Mode: input.Mode, GitOnlyConsent: input.GitOnlyConsent, AllowPrivateNetwork: input.AllowPrivateNetwork,
			CreatedAt: input.Now, UpdatedAt: input.Now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_sources(repository_id,url,source_generation,authority_revision,credential_generation,mode,git_only_consent,allow_private_network,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			current.RepositoryID, current.URL, current.SourceGeneration, current.AuthorityRevision, current.CredentialGeneration, current.Mode, boolInt(current.GitOnlyConsent), boolInt(current.AllowPrivateNetwork), current.CreatedAt.Unix(), current.UpdatedAt.Unix()); err != nil {
			return ImportSource{}, err
		}
	case err != nil:
		return ImportSource{}, err
	default:
		current.CreatedAt = unixTime(created)
		current.UpdatedAt = unixTime(updated)
		changed := current.URL != input.URL || current.Mode != input.Mode || current.GitOnlyConsent != input.GitOnlyConsent || current.AllowPrivateNetwork != input.AllowPrivateNetwork
		if changed {
			if current.URL != input.URL {
				current.SourceGeneration++
				current.CredentialGeneration = ""
			}
			current.URL = input.URL
			current.AuthorityRevision++
			current.Mode = input.Mode
			current.GitOnlyConsent = input.GitOnlyConsent
			current.AllowPrivateNetwork = input.AllowPrivateNetwork
			current.UpdatedAt = input.Now
			if _, err := tx.ExecContext(ctx, `UPDATE import_sources
				SET url=?,source_generation=?,authority_revision=?,credential_generation=?,mode=?,git_only_consent=?,allow_private_network=?,updated_at=?
				WHERE repository_id=? AND authority_revision=?`,
				current.URL, current.SourceGeneration, current.AuthorityRevision, current.CredentialGeneration, current.Mode, boolInt(current.GitOnlyConsent), boolInt(current.AllowPrivateNetwork), current.UpdatedAt.Unix(), input.RepositoryID, current.AuthorityRevision-1); err != nil {
				return ImportSource{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return ImportSource{}, err
	}
	return current, nil
}

func (s *Store) ImportSource(ctx context.Context, repositoryID string) (ImportSource, bool, error) {
	var record ImportSource
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT repository_id,url,source_generation,authority_revision,credential_generation,mode,git_only_consent,allow_private_network,created_at,updated_at FROM import_sources WHERE repository_id=?`, repositoryID).
		Scan(&record.RepositoryID, &record.URL, &record.SourceGeneration, &record.AuthorityRevision, &record.CredentialGeneration, &record.Mode, &record.GitOnlyConsent, &record.AllowPrivateNetwork, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return ImportSource{}, false, nil
	}
	if err != nil {
		return ImportSource{}, false, err
	}
	record.CreatedAt = unixTime(created)
	record.UpdatedAt = unixTime(updated)
	return record, true, nil
}

func (s *Store) ImportSources(ctx context.Context) ([]ImportSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository_id,url,source_generation,authority_revision,credential_generation,mode,git_only_consent,allow_private_network,created_at,updated_at FROM import_sources ORDER BY repository_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportSource
	for rows.Next() {
		var record ImportSource
		var created, updated int64
		if err := rows.Scan(&record.RepositoryID, &record.URL, &record.SourceGeneration, &record.AuthorityRevision, &record.CredentialGeneration, &record.Mode, &record.GitOnlyConsent, &record.AllowPrivateNetwork, &created, &updated); err != nil {
			return nil, err
		}
		record.CreatedAt = unixTime(created)
		record.UpdatedAt = unixTime(updated)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) SetImportGitOnlyConsent(ctx context.Context, repositoryID string, consent bool, now time.Time) (ImportSource, error) {
	return s.setImportConsent(ctx, repositoryID, "git_only_consent", consent, now)
}

func (s *Store) SetImportTransportConsent(ctx context.Context, repositoryID string, consent bool, now time.Time) (ImportSource, error) {
	return s.setImportConsent(ctx, repositoryID, "allow_private_network", consent, now)
}

func (s *Store) setImportConsent(ctx context.Context, repositoryID, column string, consent bool, now time.Time) (ImportSource, error) {
	if now.IsZero() {
		return ImportSource{}, errors.New("import source time is required")
	}
	releaseCredentialAuthority := s.LockImportCredentialAuthority(repositoryID)
	defer releaseCredentialAuthority()
	query := `UPDATE import_sources SET ` + column + `=?,authority_revision=authority_revision+1,updated_at=? WHERE repository_id=? AND ` + column + `!=?`
	result, err := s.db.ExecContext(ctx, query, boolInt(consent), now.Unix(), repositoryID, boolInt(consent))
	if err != nil {
		return ImportSource{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ImportSource{}, err
	} else if affected == 0 {
		if _, exists, readErr := s.ImportSource(ctx, repositoryID); readErr != nil {
			return ImportSource{}, readErr
		} else if !exists {
			return ImportSource{}, errors.New("import source is not configured")
		}
	}
	record, _, err := s.ImportSource(ctx, repositoryID)
	return record, err
}

func (s *Store) activateImportCredential(ctx context.Context, source ImportSource, credentialGeneration string, now time.Time) (ImportSource, error) {
	if source.AuthorityRevision <= 0 || now.IsZero() || (credentialGeneration != "" && !isLowerHex(credentialGeneration, 32)) {
		return ImportSource{}, errors.New("invalid import credential authority transition")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE import_sources
		SET authority_revision=authority_revision+1,credential_generation=?,updated_at=?
		WHERE repository_id=? AND url=? AND source_generation=? AND authority_revision=? AND credential_generation=?`,
		credentialGeneration, now.Unix(), source.RepositoryID, source.URL, source.SourceGeneration, source.AuthorityRevision, source.CredentialGeneration)
	if err != nil {
		return ImportSource{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ImportSource{}, err
	}
	if affected != 1 {
		return ImportSource{}, errors.New("import authority changed before credential mutation completed")
	}
	source.AuthorityRevision++
	source.CredentialGeneration = credentialGeneration
	source.UpdatedAt = now
	return source, nil
}

// DeleteImportSource removes the source and its observations. Refresh history
// and publication intents stay as history of that source.
func (s *Store) DeleteImportSource(ctx context.Context, repositoryID string) error {
	releaseCredentialAuthority := s.LockImportCredentialAuthority(repositoryID)
	defer releaseCredentialAuthority()
	// Invalidate captured credentials before touching the private file. A failed
	// deletion remains blocked until an exact credential retry or successful
	// source deletion establishes a safe state.
	s.beginImportCredentialMutationLocked(repositoryID)
	if err := s.removeImportCredentialFile(repositoryID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM import_ref_observations WHERE repository_id=?`, repositoryID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM import_sources WHERE repository_id=?`, repositoryID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.completeImportCredentialMutationLocked(repositoryID)
	return nil
}

// BeginImportRun atomically refuses a second active run for the repository and
// records the run. A scheduled run also stamps its schedule's start so a slow
// repository cannot starve later ones.
func (s *Store) BeginImportRun(ctx context.Context, run ImportRun) error {
	if err := validateImportRunRecord(run, true); err != nil {
		return err
	}
	run.CreatedAt = run.StartedAt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_runs WHERE repository_id=? AND status IN (?,?,?,?,?)`,
		run.RepositoryID, ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return ErrImportActive
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_runs(
		id,repository_id,source_generation,authority_revision,kind,status,started_at,finished_at,cancel_requested_at,object_format,
		refs_seen,refs_created,refs_updated,refs_unchanged,refs_divergent,refs_deleted_upstream,refs_skipped,
		pack_bytes,http_body_bytes,head_advertised,head_symref,error_class,message,
		lfs_detected,lfs_inspection_complete,lfs_scanned_blobs,lfs_scanned_bytes,staging_name,cleanup_error,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.RepositoryID, run.SourceGeneration, run.AuthorityRevision, run.Kind, run.Status, run.StartedAt.Unix(), int64(0), nil, run.ObjectFormat,
		run.RefsSeen, run.RefsCreated, run.RefsUpdated, run.RefsUnchanged, run.RefsDivergent, run.RefsDeletedUpstream, run.RefsSkipped,
		run.PackBytes, run.HTTPBodyBytes, boolInt(run.HeadAdvertised), run.HeadSymref, run.ErrorClass, run.Message,
		run.LFSDetected, boolInt(run.LFSInspectionDone), run.LFSScannedBlobs, run.LFSScannedBytes, run.StagingName, run.CleanupError, run.CreatedAt.Unix()); err != nil {
		return err
	}
	if run.Kind == ImportKindScheduled {
		if _, err := tx.ExecContext(ctx, `UPDATE import_schedules SET last_started_at=?,updated_at=? WHERE repository_id=? AND enabled=1`,
			run.StartedAt.Unix(), run.StartedAt.Unix(), run.RepositoryID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FinishImportRun records the terminal facts of a run. A run that a concurrent
// reconciliation already finished is accepted unchanged.
func (s *Store) FinishImportRun(ctx context.Context, run ImportRun) error {
	if err := validateImportRunRecord(run, false); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := finishImportRunTx(ctx, tx, run); err != nil {
		return err
	}
	return tx.Commit()
}

func finishImportRunTx(ctx context.Context, tx *sql.Tx, run ImportRun) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM import_runs WHERE id=?`, run.ID).Scan(&status); err != nil {
		return err
	}
	// A late failing caller cannot downgrade a run atomically completed by
	// publication reconciliation. A complete retry may still fill exact facts.
	if status != ImportRunComplete || run.Status == ImportRunComplete {
		result, err := tx.ExecContext(ctx, `UPDATE import_runs SET
			status=?,finished_at=?,cancel_requested_at=COALESCE(cancel_requested_at,?),object_format=?,refs_seen=?,refs_created=?,refs_updated=?,refs_unchanged=?,
			refs_divergent=?,refs_deleted_upstream=?,refs_skipped=?,pack_bytes=?,http_body_bytes=?,head_advertised=?,head_symref=?,
			error_class=?,message=?,lfs_detected=?,lfs_inspection_complete=?,lfs_scanned_blobs=?,lfs_scanned_bytes=?,cleanup_error=?
			WHERE id=?`,
			run.Status, run.FinishedAt.Unix(), nullableUnix(run.CancelRequestedAt), run.ObjectFormat, run.RefsSeen, run.RefsCreated, run.RefsUpdated, run.RefsUnchanged,
			run.RefsDivergent, run.RefsDeletedUpstream, run.RefsSkipped, run.PackBytes, run.HTTPBodyBytes, boolInt(run.HeadAdvertised), run.HeadSymref,
			run.ErrorClass, run.Message, run.LFSDetected, boolInt(run.LFSInspectionDone), run.LFSScannedBlobs, run.LFSScannedBytes, run.CleanupError,
			run.ID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return errors.New("import run outcome did not update exactly one row")
		}
	}
	if run.Kind == ImportKindScheduled {
		finished := run.FinishedAt
		if finished.IsZero() {
			finished = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE import_schedules SET last_finished_at=?,updated_at=? WHERE repository_id=?`,
			finished.Unix(), finished.Unix(), run.RepositoryID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ImportRun(ctx context.Context, id string) (ImportRun, bool, error) {
	rows, err := s.db.QueryContext(ctx, importRunSelect+` WHERE id=?`, id)
	if err != nil {
		return ImportRun{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ImportRun{}, false, rows.Err()
	}
	record, err := scanImportRun(rows)
	if err != nil {
		return ImportRun{}, false, err
	}
	return record, true, rows.Err()
}

func (s *Store) ActiveImportRun(ctx context.Context, repositoryID string) (ImportRun, bool, error) {
	rows, err := s.db.QueryContext(ctx, importRunSelect+` WHERE repository_id=? AND status IN (?,?,?,?,?) ORDER BY rowid DESC LIMIT 1`,
		repositoryID, ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing)
	if err != nil {
		return ImportRun{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ImportRun{}, false, rows.Err()
	}
	record, err := scanImportRun(rows)
	if err != nil {
		return ImportRun{}, false, err
	}
	return record, true, rows.Err()
}

// ImportRuns returns newest admissions first. SQLite rowid is the durable
// insertion order for this append-only table, so equal caller clock values do
// not let random run IDs reorder history.
func (s *Store) ImportRuns(ctx context.Context, repositoryID string, limit int) ([]ImportRun, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, importRunSelect+` WHERE repository_id=? ORDER BY rowid DESC LIMIT ?`, repositoryID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var records []ImportRun
	for rows.Next() {
		record, err := scanImportRun(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(records) > limit {
		return records[:limit], true, nil
	}
	return records, false, nil
}

// ImportRunsBefore returns runs older than afterRowID, newest first. A zero
// cursor starts at the newest row. rowid is the durable admission order.
func (s *Store) ImportRunsBefore(ctx context.Context, repositoryID string, limit int, afterRowID int64) ([]ImportRun, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, importRunSelect+` WHERE repository_id=? AND (?=0 OR rowid < ?) ORDER BY rowid DESC LIMIT ?`, repositoryID, afterRowID, afterRowID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var records []ImportRun
	for rows.Next() {
		record, err := scanImportRun(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(records) > limit {
		return records[:limit], true, nil
	}
	return records, false, nil
}

// LatestCompletedImportRun returns the newest run that actually completed
// publication. Failed later attempts do not describe the accepted snapshot.
func (s *Store) LatestCompletedImportRun(ctx context.Context, repositoryID string) (ImportRun, bool, error) {
	rows, err := s.db.QueryContext(ctx, importRunSelect+` WHERE repository_id=? AND status=? ORDER BY rowid DESC LIMIT 1`, repositoryID, ImportRunComplete)
	if err != nil {
		return ImportRun{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ImportRun{}, false, rows.Err()
	}
	record, err := scanImportRun(rows)
	if err != nil {
		return ImportRun{}, false, err
	}
	return record, true, rows.Err()
}

const importRunSelect = `SELECT id,repository_id,source_generation,authority_revision,kind,status,started_at,finished_at,cancel_requested_at,object_format,
	refs_seen,refs_created,refs_updated,refs_unchanged,refs_divergent,refs_deleted_upstream,refs_skipped,
	pack_bytes,http_body_bytes,head_advertised,head_symref,error_class,message,
	lfs_detected,lfs_inspection_complete,lfs_scanned_blobs,lfs_scanned_bytes,staging_name,cleanup_error,created_at,rowid FROM import_runs`

func scanImportRun(scanner rowScanner) (ImportRun, error) {
	var record ImportRun
	var started, finished, created int64
	var cancel sql.NullInt64
	var headAdvertised, lfsDone int
	if err := scanner.Scan(&record.ID, &record.RepositoryID, &record.SourceGeneration, &record.AuthorityRevision, &record.Kind, &record.Status, &started, &finished, &cancel, &record.ObjectFormat,
		&record.RefsSeen, &record.RefsCreated, &record.RefsUpdated, &record.RefsUnchanged, &record.RefsDivergent, &record.RefsDeletedUpstream, &record.RefsSkipped,
		&record.PackBytes, &record.HTTPBodyBytes, &headAdvertised, &record.HeadSymref, &record.ErrorClass, &record.Message,
		&record.LFSDetected, &lfsDone, &record.LFSScannedBlobs, &record.LFSScannedBytes, &record.StagingName, &record.CleanupError, &created, &record.RowID); err != nil {
		return ImportRun{}, err
	}
	record.StartedAt = unixTime(started)
	record.FinishedAt = unixTime(finished)
	if cancel.Valid {
		value := unixTime(cancel.Int64)
		record.CancelRequestedAt = &value
	}
	record.HeadAdvertised = headAdvertised != 0
	record.LFSInspectionDone = lfsDone != 0
	record.CreatedAt = unixTime(created)
	return record, nil
}

// RequestImportCancel marks the active run for cancellation. The caller that
// owns the run cancels its context; a run observed after the fact still shows
// the request.
func (s *Store) RequestImportCancel(ctx context.Context, repositoryID string, now time.Time) (ImportRun, bool, error) {
	return s.requestImportCancel(ctx, repositoryID, 0, now)
}

// RequestImportCancelBeforeAuthority cancels only a run admitted under an
// older authority revision. A newly admitted current run is never selected.
func (s *Store) RequestImportCancelBeforeAuthority(ctx context.Context, repositoryID string, authorityRevision int64, now time.Time) (ImportRun, bool, error) {
	if authorityRevision <= 0 {
		return ImportRun{}, false, errors.New("invalid current import authority")
	}
	return s.requestImportCancel(ctx, repositoryID, authorityRevision, now)
}

// RequestImportRunCancel marks one exact active run. It is used under the
// service lifecycle barrier after process-local credential authority changes.
func (s *Store) RequestImportRunCancel(ctx context.Context, runID string, now time.Time) (ImportRun, bool, error) {
	run, exists, err := s.ImportRun(ctx, runID)
	if err != nil || !exists || !importRunActive(run.Status) {
		return ImportRun{}, false, err
	}
	if run.CancelRequestedAt == nil {
		result, err := s.db.ExecContext(ctx, `UPDATE import_runs SET cancel_requested_at=? WHERE id=? AND cancel_requested_at IS NULL AND status IN (?,?,?,?,?)`,
			now.Unix(), run.ID, ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing)
		if err != nil {
			return ImportRun{}, false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return ImportRun{}, false, err
		}
		if affected != 1 {
			return ImportRun{}, false, nil
		}
		value := now
		run.CancelRequestedAt = &value
	}
	return run, true, nil
}

func (s *Store) requestImportCancel(ctx context.Context, repositoryID string, currentAuthority int64, now time.Time) (ImportRun, bool, error) {
	query := importRunSelect + ` WHERE repository_id=? AND status IN (?,?,?,?,?)`
	arguments := []any{repositoryID, ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing}
	if currentAuthority > 0 {
		query += ` AND authority_revision<?`
		arguments = append(arguments, currentAuthority)
	}
	query += ` ORDER BY rowid DESC LIMIT 1`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return ImportRun{}, false, err
	}
	if !rows.Next() {
		err := rows.Err()
		rows.Close()
		return ImportRun{}, false, err
	}
	run, err := scanImportRun(rows)
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return ImportRun{}, false, errors.Join(err, closeErr)
	}
	if run.CancelRequestedAt == nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE import_runs SET cancel_requested_at=? WHERE id=? AND cancel_requested_at IS NULL`, now.Unix(), run.ID); err != nil {
			return ImportRun{}, false, err
		}
		value := now
		run.CancelRequestedAt = &value
	}
	return run, true, nil
}

// InterruptImportAuthority marks what a stopped process left unfinished. Active
// runs become interrupted and open publication intents become invalidated;
// reconciliation later verifies actual refs instead of assuming an outcome.
// liveRunIDs names runs the calling process is still executing, so a
// reconciliation inside a live server never interrupts its own work.
func (s *Store) InterruptImportAuthority(ctx context.Context, now time.Time, liveRunIDs ...string) (int64, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	live := make([]string, 0, len(liveRunIDs))
	for _, id := range liveRunIDs {
		if id != "" {
			live = append(live, id)
		}
	}
	placeholders := ""
	liveArguments := make([]any, 0, len(live))
	if len(live) > 0 {
		placeholders = " (?" + strings.Repeat(",?", len(live)-1) + ")"
		for _, id := range live {
			liveArguments = append(liveArguments, id)
		}
	}
	runQuery := `UPDATE import_runs SET status=?,finished_at=?,message=CASE WHEN message='' THEN 'interrupted by restart' ELSE message END
		WHERE status IN (?,?,?,?,?)`
	runArguments := []any{ImportRunInterrupted, now.Unix(), ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing}
	if len(live) > 0 {
		runQuery += ` AND id NOT IN` + placeholders
		runArguments = append(runArguments, liveArguments...)
	}
	runs, err := tx.ExecContext(ctx, runQuery, runArguments...)
	if err != nil {
		return 0, 0, err
	}
	intentQuery := `UPDATE import_publication_intents SET status=?,head_owned=0,reason=CASE WHEN reason='' THEN 'interrupted by restart' ELSE reason END,updated_at=?
		WHERE status IN (?,?)`
	intentArguments := []any{ImportIntentInvalidated, now.Unix(), ImportIntentPlanning, ImportIntentApplied}
	if len(live) > 0 {
		intentQuery += ` AND run_id NOT IN` + placeholders
		intentArguments = append(intentArguments, liveArguments...)
	}
	intents, err := tx.ExecContext(ctx, intentQuery, intentArguments...)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	runCount, _ := runs.RowsAffected()
	intentCount, _ := intents.RowsAffected()
	return runCount, intentCount, nil
}

func (s *Store) RecordImportObservations(ctx context.Context, observations []ImportObservation) error {
	if len(observations) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, observation := range observations {
		if err := recordImportObservationTx(ctx, tx, observation); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func recordImportObservationTx(ctx context.Context, tx *sql.Tx, observation ImportObservation) error {
	if err := validateImportObservationRecord(observation); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO import_ref_observations(repository_id,source_generation,ref_name,oid,symref_target,observed_at,run_id)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(repository_id,source_generation,ref_name) DO UPDATE SET
		oid=excluded.oid,symref_target=excluded.symref_target,observed_at=excluded.observed_at,run_id=excluded.run_id`,
		observation.RepositoryID, observation.SourceGeneration, observation.RefName, observation.OID, observation.SymrefTarget, observation.ObservedAt.Unix(), observation.RunID)
	return err
}

// FinalizeImportPublication commits the receipt, source observations, and run
// outcome together. A failed transaction leaves the intent recoverable and can
// never expose a complete run with missing publication facts.
func (s *Store) FinalizeImportPublication(ctx context.Context, intentID, receiptJSON, receiptDigest, reason string, observations []ImportObservation, run ImportRun) error {
	if run.Status != ImportRunComplete {
		return errors.New("finalized import publication requires a complete run")
	}
	if err := validateImportRunRecord(run, false); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, importIntentSelect+` WHERE id=?`, intentID)
	intent, err := scanImportIntent(row)
	if err != nil {
		return err
	}
	if intent.RepositoryID != run.RepositoryID || intent.RunID != run.ID || intent.SourceGeneration != run.SourceGeneration || intent.AuthorityRevision != run.AuthorityRevision {
		return errors.New("import publication intent does not match its run")
	}
	intent.Status = ImportIntentComplete
	intent.ReceiptJSON = receiptJSON
	intent.ReceiptDigest = receiptDigest
	intent.Reason = reason
	if err := validateImportIntentRecord(intent); err != nil {
		return err
	}
	if err := validateCompleteImportIntentReceipt(intent); err != nil {
		return err
	}
	if len(observations) != len(intent.Observed) {
		return errors.New("import publication observations are incomplete")
	}
	seenObservations := make(map[string]bool, len(observations))
	for _, observation := range observations {
		if observation.RepositoryID != intent.RepositoryID || observation.SourceGeneration != intent.SourceGeneration || observation.RunID != intent.RunID {
			return errors.New("import publication observation does not match its intent")
		}
		fact, exists := intent.Observed[observation.RefName]
		if !exists || seenObservations[observation.RefName] {
			return errors.New("import publication observation is unrelated or duplicated")
		}
		observedFact := observation.OID
		if observation.RefName == ImportHeadRef {
			switch {
			case observation.SymrefTarget != "":
				observedFact = "symbolic " + observation.SymrefTarget + " " + observation.OID
			case observation.OID != "":
				observedFact = "detached " + observation.OID
			default:
				observedFact = "absent"
			}
		}
		if observedFact != fact {
			return errors.New("import publication observation does not match its source fact")
		}
		seenObservations[observation.RefName] = true
		if err := recordImportObservationTx(ctx, tx, observation); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE import_publication_intents SET status=?,receipt_json=?,receipt_digest=?,reason=?,updated_at=? WHERE id=?`,
		ImportIntentComplete, receiptJSON, receiptDigest, reason, run.FinishedAt.Unix(), intentID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return errors.New("import publication receipt did not update exactly one row")
	}
	if err := finishImportRunTx(ctx, tx, run); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ImportObservations(ctx context.Context, repositoryID string, generation int64) ([]ImportObservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository_id,source_generation,ref_name,oid,symref_target,observed_at,run_id
		FROM import_ref_observations WHERE repository_id=? AND source_generation=? ORDER BY ref_name`, repositoryID, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportObservation
	for rows.Next() {
		var record ImportObservation
		var observed int64
		if err := rows.Scan(&record.RepositoryID, &record.SourceGeneration, &record.RefName, &record.OID, &record.SymrefTarget, &observed, &record.RunID); err != nil {
			return nil, err
		}
		record.ObservedAt = unixTime(observed)
		records = append(records, record)
	}
	return records, rows.Err()
}

// ImportObservationsBounded returns newest source generation first, then ref
// name, up to limit rows.
func (s *Store) ImportObservationsBounded(ctx context.Context, repositoryID string, limit int) ([]ImportObservation, error) {
	if limit <= 0 {
		return nil, errors.New("import observation page size is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT repository_id,source_generation,ref_name,oid,symref_target,observed_at,run_id
		FROM import_ref_observations WHERE repository_id=? ORDER BY source_generation DESC, ref_name LIMIT ?`, repositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportObservation
	for rows.Next() {
		var record ImportObservation
		var observed int64
		if err := rows.Scan(&record.RepositoryID, &record.SourceGeneration, &record.RefName, &record.OID, &record.SymrefTarget, &observed, &record.RunID); err != nil {
			return nil, err
		}
		record.ObservedAt = unixTime(observed)
		records = append(records, record)
	}
	return records, rows.Err()
}

// ImportObservationsAll returns every generation's observations so status can
// report refs published under an earlier source.
func (s *Store) ImportObservationsAll(ctx context.Context, repositoryID string) ([]ImportObservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository_id,source_generation,ref_name,oid,symref_target,observed_at,run_id
		FROM import_ref_observations WHERE repository_id=? ORDER BY source_generation,ref_name`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportObservation
	for rows.Next() {
		var record ImportObservation
		var observed int64
		if err := rows.Scan(&record.RepositoryID, &record.SourceGeneration, &record.RefName, &record.OID, &record.SymrefTarget, &observed, &record.RunID); err != nil {
			return nil, err
		}
		record.ObservedAt = unixTime(observed)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) CreateImportIntent(ctx context.Context, intent ImportIntent) error {
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
	if intent.CreatedAt.IsZero() {
		return errors.New("import intent time is required")
	}
	if err := validateImportIntentRecord(intent); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO import_publication_intents(
		id,repository_id,run_id,source_generation,authority_revision,status,expected_json,desired_json,observed_json,retained_json,head_symref,head_detach,head_owned,
		receipt_json,receipt_digest,reason,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		intent.ID, intent.RepositoryID, intent.RunID, intent.SourceGeneration, intent.AuthorityRevision, intent.Status, expected, desired, observed, retained,
		intent.HeadSymref, intent.HeadDetach, intent.HeadOwned, intent.ReceiptJSON, intent.ReceiptDigest, intent.Reason, intent.CreatedAt.Unix(), intent.CreatedAt.Unix())
	return err
}

func (s *Store) ImportIntent(ctx context.Context, id string) (ImportIntent, bool, error) {
	rows, err := s.db.QueryContext(ctx, importIntentSelect+` WHERE id=?`, id)
	if err != nil {
		return ImportIntent{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ImportIntent{}, false, rows.Err()
	}
	record, err := scanImportIntent(rows)
	if err != nil {
		return ImportIntent{}, false, err
	}
	return record, true, rows.Err()
}

// CompletedImportIntentForRun returns the single complete receipt for a run.
// More than one complete intent is invalid because ownership evidence must be
// attributable to one exact publication.
func (s *Store) CompletedImportIntentForRun(ctx context.Context, runID string) (ImportIntent, bool, error) {
	rows, err := s.db.QueryContext(ctx, importIntentSelect+` WHERE run_id=? AND status=? ORDER BY created_at,id`, runID, ImportIntentComplete)
	if err != nil {
		return ImportIntent{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ImportIntent{}, false, rows.Err()
	}
	record, err := scanImportIntent(rows)
	if err != nil {
		return ImportIntent{}, false, err
	}
	if err := validateImportIntentRecord(record); err != nil {
		return ImportIntent{}, false, err
	}
	if err := validateCompleteImportIntentReceipt(record); err != nil {
		return ImportIntent{}, false, err
	}
	if rows.Next() {
		return ImportIntent{}, false, errors.New("import run has more than one complete publication intent")
	}
	if err := rows.Err(); err != nil {
		return ImportIntent{}, false, err
	}
	return record, true, nil
}

// PendingImportIntents returns intents that were never confirmed or were
// interrupted before confirmation.
func (s *Store) PendingImportIntents(ctx context.Context, repositoryID string) ([]ImportIntent, error) {
	rows, err := s.db.QueryContext(ctx, importIntentSelect+` WHERE repository_id=? AND (
		status IN (?,?,?,?) OR (status=? AND EXISTS (SELECT 1 FROM import_runs r WHERE r.id=import_publication_intents.run_id AND r.status!=?))
		) ORDER BY created_at,id`, repositoryID, ImportIntentPlanning, ImportIntentApplied, ImportIntentInvalidated, ImportIntentUnresolved,
		ImportIntentComplete, ImportRunComplete)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportIntent
	for rows.Next() {
		record, err := scanImportIntent(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// SetImportRunStatus advances an active run's visible stage.
func (s *Store) SetImportRunStatus(ctx context.Context, id, status string) error {
	if !importRunActive(status) {
		return errors.New("import run stage must be active")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE import_runs SET status=? WHERE id=? AND status IN (?,?,?,?,?)`,
		status, id, ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing)
	return err
}

// HasCompletedImportRun reports whether the source generation already produced
// a completed import, which distinguishes a retry from a later refresh.
func (s *Store) HasCompletedImportRun(ctx context.Context, repositoryID string, generation int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_runs WHERE repository_id=? AND source_generation=? AND status=?`,
		repositoryID, generation, ImportRunComplete).Scan(&count)
	return count > 0, err
}

// PendingImportIntentsAll returns every pending intent for startup
// reconciliation, oldest first.
func (s *Store) PendingImportIntentsAll(ctx context.Context) ([]ImportIntent, error) {
	rows, err := s.db.QueryContext(ctx, importIntentSelect+` WHERE status IN (?,?,?,?) OR (
		status=? AND EXISTS (SELECT 1 FROM import_runs r WHERE r.id=import_publication_intents.run_id AND r.status!=?)
		) ORDER BY repository_id,created_at,id`, ImportIntentPlanning, ImportIntentApplied, ImportIntentInvalidated, ImportIntentUnresolved,
		ImportIntentComplete, ImportRunComplete)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportIntent
	for rows.Next() {
		record, err := scanImportIntent(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// PendingImportIntentsPage returns pending intents after afterRowID, oldest
// rowid first. A zero cursor starts at the first row. Resolved intents leave
// the predicate, so the next page does not re-read them.
func (s *Store) PendingImportIntentsPage(ctx context.Context, afterRowID int64, limit int) ([]ImportIntent, error) {
	if limit <= 0 {
		return nil, errors.New("import intent page size is required")
	}
	rows, err := s.db.QueryContext(ctx, importIntentSelect+` WHERE rowid>? AND (
		status IN (?,?,?,?) OR (status=? AND EXISTS (SELECT 1 FROM import_runs r WHERE r.id=import_publication_intents.run_id AND r.status!=?))
		) ORDER BY rowid LIMIT ?`, afterRowID, ImportIntentPlanning, ImportIntentApplied, ImportIntentInvalidated, ImportIntentUnresolved,
		ImportIntentComplete, ImportRunComplete, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportIntent
	for rows.Next() {
		record, err := scanImportIntent(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// UnresolvedImportIntentCount counts intents whose Git outcome needs owner
// attention. The count is bounded by the caller's own limit.
func (s *Store) UnresolvedImportIntentCount(ctx context.Context, repositoryID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_publication_intents WHERE repository_id=? AND status=?`, repositoryID, ImportIntentUnresolved).Scan(&count)
	return count, err
}

func validateCompleteImportIntentReceipt(intent ImportIntent) error {
	if intent.Status != ImportIntentComplete {
		return errors.New("import publication intent is not complete")
	}
	receipt, err := decodeImportMap(intent.ReceiptJSON)
	if err != nil || len(receipt) != len(intent.Desired) {
		return errors.New("import publication receipt is incomplete")
	}
	for ref, desired := range intent.Desired {
		if receipt[ref] != desired {
			return errors.New("import publication receipt does not match every desired fact")
		}
	}
	return nil
}

func (s *Store) UpdateImportIntent(ctx context.Context, id, status, receiptJSON, receiptDigest, reason string, now time.Time) error {
	if !validImportIntentStatus(status) {
		return errors.New("invalid import intent status")
	}
	if receiptDigest != "" && !isLowerHex(receiptDigest, 64) {
		return errors.New("invalid import receipt digest")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	intent, err := scanImportIntent(tx.QueryRowContext(ctx, importIntentSelect+` WHERE id=?`, id))
	if err != nil {
		return err
	}
	intent.Status = status
	intent.ReceiptJSON = receiptJSON
	intent.ReceiptDigest = receiptDigest
	intent.Reason = reason
	intent.UpdatedAt = now
	if status != ImportIntentApplied && status != ImportIntentComplete {
		intent.HeadOwned = false
	}
	if err := validateImportIntentRecord(intent); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE import_publication_intents SET status=?,receipt_json=?,receipt_digest=?,reason=?,
		head_owned=CASE WHEN ? IN ('applied','complete') THEN head_owned ELSE 0 END,updated_at=? WHERE id=?`,
		status, receiptJSON, receiptDigest, reason, status, now.Unix(), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("import intent update did not affect exactly one row")
	}
	return tx.Commit()
}

// UpdateImportIntentHEADOwnership records structured ownership only after an
// applied exact HEAD write or for a validated complete synthetic fixture.
func (s *Store) UpdateImportIntentHEADOwnership(ctx context.Context, id, status, reason string, now time.Time) error {
	if status != ImportIntentApplied && status != ImportIntentComplete {
		return errors.New("HEAD ownership requires an applied or complete intent")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	intent, err := scanImportIntent(tx.QueryRowContext(ctx, importIntentSelect+` WHERE id=?`, id))
	if err != nil {
		return err
	}
	intent.Status = status
	intent.HeadOwned = true
	intent.Reason = reason
	intent.UpdatedAt = now
	if err := validateImportIntentRecord(intent); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE import_publication_intents SET status=?,head_owned=1,reason=?,updated_at=? WHERE id=?`,
		status, reason, now.Unix(), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("import HEAD ownership update did not affect exactly one row")
	}
	return tx.Commit()
}

const importIntentSelect = `SELECT id,repository_id,run_id,source_generation,authority_revision,status,expected_json,desired_json,observed_json,retained_json,head_symref,head_detach,head_owned,
	receipt_json,receipt_digest,reason,created_at,updated_at,rowid FROM import_publication_intents`

func scanImportIntent(scanner rowScanner) (ImportIntent, error) {
	var record ImportIntent
	var expected, desired, observed, retained string
	var created, updated int64
	if err := scanner.Scan(&record.ID, &record.RepositoryID, &record.RunID, &record.SourceGeneration, &record.AuthorityRevision, &record.Status, &expected, &desired, &observed, &retained,
		&record.HeadSymref, &record.HeadDetach, &record.HeadOwned, &record.ReceiptJSON, &record.ReceiptDigest, &record.Reason, &created, &updated, &record.RowID); err != nil {
		return ImportIntent{}, err
	}
	var err error
	if record.Expected, err = decodeImportMap(expected); err != nil {
		return ImportIntent{}, err
	}
	if record.Desired, err = decodeImportMap(desired); err != nil {
		return ImportIntent{}, err
	}
	if record.Observed, err = decodeImportMap(observed); err != nil {
		return ImportIntent{}, err
	}
	if record.Retained, err = decodeImportMap(retained); err != nil {
		return ImportIntent{}, err
	}
	record.CreatedAt = unixTime(created)
	record.UpdatedAt = unixTime(updated)
	return record, nil
}

func (s *Store) RegisterImportStaging(ctx context.Context, item ImportStaging) error {
	// Repository and run identifiers stay empty for a directory whose marker and
	// ownership record are both missing; unknown content is preserved, not
	// claimed.
	if err := validateImportStagingRecord(item, ImportStagingActive, ImportStagingUnknown); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO import_stagings(name,repository_id,run_id,token,state,issue,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		item.Name, item.RepositoryID, item.RunID, item.Token, item.State, item.Issue, item.CreatedAt.Unix(), item.CreatedAt.Unix())
	return err
}

// ClaimImportStaging makes the named row belong to one run. A row registered by
// reconciliation while the run was creating its directory is informational and
// is replaced; a row already authorized by the same run is refreshed; anything
// else keeps its identity and reports applied=false, so this path never
// silently overwrites another run's authorization.
func (s *Store) ClaimImportStaging(ctx context.Context, item ImportStaging) (bool, error) {
	if err := validateImportStagingRecord(item, ImportStagingActive); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO import_stagings(name,repository_id,run_id,token,state,issue,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET repository_id=excluded.repository_id,run_id=excluded.run_id,
		token=excluded.token,state=excluded.state,issue=excluded.issue,updated_at=excluded.updated_at
		WHERE import_stagings.state=? OR import_stagings.run_id=excluded.run_id`,
		item.Name, item.RepositoryID, item.RunID, item.Token, item.State, item.Issue, item.CreatedAt.Unix(), item.CreatedAt.Unix(), ImportStagingUnknown)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func validateImportStagingRecord(item ImportStaging, states ...string) error {
	if item.Name == "" || len(item.RepositoryID) > 100 || len(item.RunID) > 64 || len(item.Token) < 16 || len(item.Token) > 64 {
		return errors.New("invalid import staging record")
	}
	valid := false
	for _, accepted := range states {
		if item.State == accepted {
			valid = true
			break
		}
	}
	if !valid {
		return errors.New("invalid import staging state")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("import staging time is required")
	}
	return nil
}

func (s *Store) ImportStaging(ctx context.Context, name string) (ImportStaging, bool, error) {
	rows, err := s.db.QueryContext(ctx, importStagingSelect+` WHERE name=?`, name)
	if err != nil {
		return ImportStaging{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ImportStaging{}, false, rows.Err()
	}
	record, err := scanImportStaging(rows)
	if err != nil {
		return ImportStaging{}, false, err
	}
	return record, true, rows.Err()
}

func (s *Store) ImportStagingIssueCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_stagings WHERE state!=?`, ImportStagingReleased).Scan(&count)
	return count, err
}

func (s *Store) ImportStagingsPage(ctx context.Context, afterName string, limit int) ([]ImportStaging, error) {
	if limit <= 0 {
		return nil, errors.New("import staging page size is required")
	}
	rows, err := s.db.QueryContext(ctx, importStagingSelect+` WHERE name>? ORDER BY name LIMIT ?`, afterName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportStaging
	for rows.Next() {
		record, err := scanImportStaging(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ImportSchedulesPage(ctx context.Context, afterID string, limit int) ([]ImportSchedule, error) {
	if limit <= 0 {
		return nil, errors.New("import schedule page size is required")
	}
	rows, err := s.db.QueryContext(ctx, importScheduleSelect+` WHERE repository_id>? ORDER BY repository_id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportSchedule
	for rows.Next() {
		record, err := scanImportSchedule(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ImportInitialDestinationsPage(ctx context.Context, afterName string, limit int) ([]ImportInitialDestination, error) {
	if limit <= 0 {
		return nil, errors.New("initial destination page size is required")
	}
	rows, err := s.db.QueryContext(ctx, importInitialDestinationSelect+` WHERE name>? ORDER BY name LIMIT ?`, afterName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportInitialDestination
	for rows.Next() {
		record, err := scanImportInitialDestination(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ImportStagings(ctx context.Context) ([]ImportStaging, error) {
	rows, err := s.db.QueryContext(ctx, importStagingSelect+` ORDER BY created_at,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportStaging
	for rows.Next() {
		record, err := scanImportStaging(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ReleaseImportStaging(ctx context.Context, name, stagingState, issue string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE import_stagings SET state=?,issue=?,updated_at=? WHERE name=?`, stagingState, issue, now.Unix(), name)
	return err
}

const importStagingSelect = `SELECT name,repository_id,run_id,token,state,issue,created_at,updated_at FROM import_stagings`

func scanImportStaging(scanner rowScanner) (ImportStaging, error) {
	var record ImportStaging
	var created, updated int64
	if err := scanner.Scan(&record.Name, &record.RepositoryID, &record.RunID, &record.Token, &record.State, &record.Issue, &created, &updated); err != nil {
		return ImportStaging{}, err
	}
	record.CreatedAt = unixTime(created)
	record.UpdatedAt = unixTime(updated)
	return record, nil
}

func (s *Store) SetImportSchedule(ctx context.Context, repositoryID string, enabled bool, interval time.Duration, now time.Time) (ImportSchedule, error) {
	seconds := int64(interval / time.Second)
	if seconds < maxImportScheduleStart || seconds > maxImportScheduleEnd {
		return ImportSchedule{}, fmt.Errorf("import schedule interval must be between %ds and %ds", maxImportScheduleStart, maxImportScheduleEnd)
	}
	if now.IsZero() {
		return ImportSchedule{}, errors.New("import schedule time is required")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO import_schedules(repository_id,enabled,interval_seconds,created_at,updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(repository_id) DO UPDATE SET enabled=excluded.enabled,interval_seconds=excluded.interval_seconds,updated_at=excluded.updated_at`,
		repositoryID, boolInt(enabled), seconds, now.Unix(), now.Unix()); err != nil {
		return ImportSchedule{}, err
	}
	record, exists, err := s.ImportSchedule(ctx, repositoryID)
	if err != nil {
		return ImportSchedule{}, err
	}
	if !exists {
		return ImportSchedule{}, errors.New("import schedule disappeared")
	}
	return record, nil
}

func (s *Store) ImportSchedule(ctx context.Context, repositoryID string) (ImportSchedule, bool, error) {
	rows, err := s.db.QueryContext(ctx, importScheduleSelect+` WHERE repository_id=?`, repositoryID)
	if err != nil {
		return ImportSchedule{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ImportSchedule{}, false, rows.Err()
	}
	record, err := scanImportSchedule(rows)
	if err != nil {
		return ImportSchedule{}, false, err
	}
	return record, true, rows.Err()
}

// ClaimDueImportSchedule stamps last_started_at only while the schedule is
// still due. A false result means another worker already claimed it.
func (s *Store) ClaimDueImportSchedule(ctx context.Context, repositoryID string, now time.Time) (bool, error) {
	if repositoryID == "" || now.IsZero() {
		return false, errors.New("import schedule claim is incomplete")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE import_schedules SET last_started_at=?,updated_at=?
		WHERE repository_id=? AND enabled=1 AND (last_started_at IS NULL OR last_started_at + interval_seconds <= ?)`,
		now.Unix(), now.Unix(), repositoryID, now.Unix())
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}

// DueImportSchedules returns enabled schedules whose interval elapsed, oldest
// start first so every repository is served in turn.
func (s *Store) DueImportSchedules(ctx context.Context, now time.Time, limit int) ([]ImportSchedule, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.QueryContext(ctx, importScheduleSelect+`
		WHERE enabled=1 AND (last_started_at IS NULL OR last_started_at + interval_seconds <= ?)
		ORDER BY COALESCE(last_started_at,0),repository_id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportSchedule
	for rows.Next() {
		record, err := scanImportSchedule(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) TouchImportScheduleFinished(ctx context.Context, repositoryID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE import_schedules SET last_finished_at=?,updated_at=? WHERE repository_id=?`, now.Unix(), now.Unix(), repositoryID)
	return err
}

const importScheduleSelect = `SELECT repository_id,enabled,interval_seconds,last_started_at,last_finished_at,created_at,updated_at FROM import_schedules`

func scanImportSchedule(scanner rowScanner) (ImportSchedule, error) {
	var record ImportSchedule
	var enabled int
	var started, finished sql.NullInt64
	var created, updated int64
	if err := scanner.Scan(&record.RepositoryID, &enabled, &record.IntervalSeconds, &started, &finished, &created, &updated); err != nil {
		return ImportSchedule{}, err
	}
	record.Enabled = enabled != 0
	record.LastStartedAt = nullableTimePointer(started)
	record.LastFinishedAt = nullableTimePointer(finished)
	record.CreatedAt = unixTime(created)
	record.UpdatedAt = unixTime(updated)
	return record, nil
}

func encodeImportMap(values map[string]string) (string, error) {
	if values == nil {
		values = map[string]string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxImportIntentJSON {
		return "", errors.New("import intent JSON exceeds its bound")
	}
	return string(encoded), nil
}

func decodeImportMap(value string) (map[string]string, error) {
	if len(value) > maxImportIntentJSON {
		return nil, errors.New("import intent JSON exceeds its bound")
	}
	decoded := map[string]string{}
	if value == "" {
		return decoded, nil
	}
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, fmt.Errorf("decode import intent JSON: %w", err)
	}
	return decoded, nil
}

// ImportReceiptDigest is the canonical SHA-256 over a receipt JSON body.
// Receipts are written with encoding/json, which sorts map keys, so the digest
// is deterministic for the same claims.
func ImportReceiptDigest(receiptJSON string) string {
	sum := sha256.Sum256([]byte(receiptJSON))
	return hex.EncodeToString(sum[:])
}

func validImportIntentStatus(status string) bool {
	switch status {
	case ImportIntentPlanning, ImportIntentApplied, ImportIntentComplete, ImportIntentNotApplied, ImportIntentAbandoned, ImportIntentUnresolved, ImportIntentInvalidated,
		ImportIntentOwnerResolved:
		return true
	}
	return false
}

func validImportRunStatus(status string) bool {
	switch status {
	case ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing,
		ImportRunComplete, ImportRunFailed, ImportRunCancelled, ImportRunSuperseded, ImportRunInterrupted, ImportRunUnresolved:
		return true
	}
	return false
}

func importRunActive(status string) bool {
	switch status {
	case ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing:
		return true
	}
	return false
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func validImportRefName(name string) bool {
	if name == ImportHeadRef {
		return true
	}
	if !strings.HasPrefix(name, "refs/") || len(name) > maxImportRefName {
		return false
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") ||
		strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character < 0x20 || character == 0x7f || character == ' ' || strings.ContainsRune("\\~^:?*[", rune(character)) {
			return false
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validateImportSourceRecord(record ImportSource) error {
	if !validText(record.RepositoryID, 100) || !validText(record.URL, maxImportURLBytes) {
		return errors.New("invalid import source record")
	}
	if record.SourceGeneration <= 0 || record.AuthorityRevision <= 0 ||
		(record.CredentialGeneration != "" && !isLowerHex(record.CredentialGeneration, 32)) ||
		(record.Mode != ImportModeStandalone && record.Mode != ImportModeCoexistence) {
		return errors.New("invalid import source generation, authority, credential generation, or mode")
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return errors.New("invalid import source times")
	}
	return nil
}

func validateImportRunRecord(record ImportRun, requireActive bool) error {
	if !isLowerHex(record.ID, 32) || !validText(record.RepositoryID, 100) {
		return errors.New("invalid import run identity")
	}
	if record.SourceGeneration <= 0 || record.AuthorityRevision <= 0 || !validImportRunStatus(record.Status) {
		return errors.New("invalid import run generation, authority, or status")
	}
	if record.Kind != ImportKindInitial && record.Kind != ImportKindRefresh && record.Kind != ImportKindScheduled {
		return errors.New("invalid import run kind")
	}
	if record.StartedAt.IsZero() {
		return errors.New("import run start time is required")
	}
	if requireActive && !importRunActive(record.Status) {
		return errors.New("import run must start active")
	}
	if !requireActive && record.FinishedAt.IsZero() {
		return errors.New("import run finish time is required")
	}
	if record.ObjectFormat != "" && record.ObjectFormat != "sha1" && record.ObjectFormat != "sha256" {
		return errors.New("invalid import run object format")
	}
	if len(record.ErrorClass) > 100 || len(record.Message) > maxImportMessage || len(record.HeadSymref) > maxImportRefName || len(record.StagingName) > 100 || len(record.CleanupError) > maxImportMessage {
		return errors.New("import run text exceeds its bound")
	}
	for _, count := range []int64{record.RefsSeen, record.RefsCreated, record.RefsUpdated, record.RefsUnchanged, record.RefsDivergent, record.RefsDeletedUpstream, record.RefsSkipped, record.PackBytes, record.HTTPBodyBytes, record.LFSDetected, record.LFSScannedBlobs, record.LFSScannedBytes} {
		if count < 0 {
			return errors.New("import run count is negative")
		}
	}
	if record.HeadSymref != "" && !validImportRefName(record.HeadSymref) {
		return errors.New("import run HEAD symref is invalid")
	}
	return nil
}

func validateImportObservationRecord(record ImportObservation) error {
	if !validText(record.RepositoryID, 100) || record.SourceGeneration <= 0 {
		return errors.New("invalid import observation identity")
	}
	if !validImportRefName(record.RefName) {
		return errors.New("invalid import observation ref")
	}
	if record.RefName == ImportHeadRef {
		if record.OID != "" && !validObjectID(record.OID) {
			return errors.New("invalid import observation HEAD object")
		}
	} else if !validObjectID(record.OID) {
		return errors.New("invalid import observation object")
	}
	if record.SymrefTarget != "" && (!strings.HasPrefix(record.SymrefTarget, "refs/heads/") || !validImportRefName(record.SymrefTarget)) {
		return errors.New("invalid import observation symref target")
	}
	if record.ObservedAt.IsZero() {
		return errors.New("import observation time is required")
	}
	return nil
}

func validateImportIntentRecord(record ImportIntent) error {
	if !isLowerHex(record.ID, 32) || !validText(record.RepositoryID, 100) || !validText(record.RunID, 64) {
		return errors.New("invalid import intent identity")
	}
	if record.SourceGeneration <= 0 || record.AuthorityRevision <= 0 || !validImportIntentStatus(record.Status) {
		return errors.New("invalid import intent generation, authority, or status")
	}
	if err := validateImportIntentMap(record.Expected, true); err != nil {
		return err
	}
	if err := validateImportIntentMap(record.Desired, false); err != nil {
		return err
	}
	if err := validateImportObservedMap(record.Observed); err != nil {
		return err
	}
	if err := validateImportIntentMap(record.Retained, false); err != nil {
		return err
	}
	for ref := range record.Desired {
		if _, exists := record.Expected[ref]; !exists {
			return errors.New("import intent desired ref has no expected value")
		}
	}
	_, hasExpectedHEAD := record.Expected[ImportHeadRef]
	_, hasDesiredHEAD := record.Desired[ImportHeadRef]
	if hasExpectedHEAD != hasDesiredHEAD {
		return errors.New("import intent has incomplete HEAD identity facts")
	}
	if hasExpectedHEAD {
		observedHEAD, exists := record.Observed[ImportHeadRef]
		if !exists {
			return errors.New("import intent with destination HEAD facts lacks a source HEAD fact")
		}
		if !validImportHeadIdentity(observedHEAD) {
			return errors.New("import intent has an invalid exact source HEAD fact")
		}
	}
	if record.HeadSymref != "" && (!strings.HasPrefix(record.HeadSymref, "refs/heads/") || !validImportRefName(record.HeadSymref)) {
		return errors.New("invalid import intent HEAD symref")
	}
	if record.HeadDetach != "" && !validObjectID(record.HeadDetach) {
		return errors.New("invalid import intent detached HEAD")
	}
	if record.ReceiptDigest != "" && !isLowerHex(record.ReceiptDigest, 64) {
		return errors.New("invalid import intent receipt digest")
	}
	if record.HeadOwned {
		if record.Status != ImportIntentPlanning && record.Status != ImportIntentApplied && record.Status != ImportIntentComplete {
			return errors.New("HEAD ownership is attached to an ineligible intent status")
		}
		desiredHEAD, desiredExists := record.Desired[ImportHeadRef]
		observedHEAD, observedExists := record.Observed[ImportHeadRef]
		if !desiredExists || !observedExists || !sameImportHeadOwnership(desiredHEAD, observedHEAD) {
			return errors.New("HEAD ownership lacks matching desired and observed identity")
		}
	}
	if record.Status == ImportIntentOwnerResolved && record.ReceiptJSON == "" {
		return errors.New("owner-resolved import intent has no accepted destination receipt")
	}
	if record.ReceiptJSON != "" {
		receipt, err := decodeImportMap(record.ReceiptJSON)
		if err != nil {
			return err
		}
		if !isLowerHex(record.ReceiptDigest, 64) {
			return errors.New("import intent has a receipt without a digest")
		}
		if ImportReceiptDigest(record.ReceiptJSON) != record.ReceiptDigest {
			return errors.New("import intent receipt digest does not match its body")
		}
		if record.Status == ImportIntentOwnerResolved {
			if err := validateOwnerResolvedReceipt(record, receipt); err != nil {
				return err
			}
			receipt = nil
		}
		for ref, oid := range receipt {
			if ref == ImportHeadRef {
				if desired, exists := record.Desired[ref]; exists {
					if desired != oid || !validImportHeadIdentity(oid) {
						return errors.New("import intent receipt has an invalid HEAD identity")
					}
				} else if !validObjectID(oid) {
					// Legacy receipts recorded only the resolved HEAD object.
					return errors.New("import intent receipt has an invalid legacy HEAD object")
				}
				continue
			}
			if desired, exists := record.Desired[ref]; !exists || desired != oid {
				return errors.New("import intent receipt claims an unplanned ref value")
			}
		}
	}
	if len(record.ReceiptJSON) > maxImportIntentJSON || len(record.Reason) > maxImportMessage {
		return errors.New("import intent text exceeds its bound")
	}
	if record.Status == ImportIntentComplete {
		if err := validateCompleteImportIntentReceipt(record); err != nil {
			return err
		}
	}
	return nil
}

// validateOwnerResolvedReceipt checks the destination facts an owner
// accepted. Keys are the refs the intent named plus HEAD. Values are what was
// read back by exact kind: "" for absent, an object ID for a direct ref,
// "symbolic <target> <object ID>" for an alias, and an exact HEAD identity.
func validateOwnerResolvedReceipt(record ImportIntent, receipt map[string]string) error {
	if _, exists := receipt[ImportHeadRef]; !exists {
		return errors.New("owner-resolved receipt has no HEAD identity")
	}
	for ref, value := range receipt {
		if ref == ImportHeadRef {
			if !validImportHeadIdentity(value) {
				return errors.New("owner-resolved receipt has an invalid HEAD identity")
			}
			continue
		}
		_, expected := record.Expected[ref]
		_, desired := record.Desired[ref]
		_, retained := record.Retained[ref]
		if !expected && !desired && !retained {
			return errors.New("owner-resolved receipt names a ref outside its intent")
		}
		if !validOwnerResolvedRefValue(value) {
			return errors.New("owner-resolved receipt has an invalid ref value")
		}
	}
	return nil
}

func validOwnerResolvedRefValue(value string) bool {
	if value == "" || validObjectID(value) {
		return true
	}
	fields := strings.Split(value, " ")
	return len(fields) == 3 && fields[0] == "symbolic" && validImportRefName(fields[1]) && (fields[2] == "" || validObjectID(fields[2]))
}

// ImportIntentResolution is the owner's acceptance of one unresolved intent.
type ImportIntentResolution struct {
	ID          string
	ReceiptJSON string
	Reason      string
}

// ErrImportIntentNotUnresolved reports that an intent chosen for owner
// resolution is no longer unresolved.
var ErrImportIntentNotUnresolved = errors.New("import intent is not unresolved")

// ErrImportRunActive reports an admitted import run for the repository.
var ErrImportRunActive = errors.New("an import run is active for the repository")

// ResolveImportIntents moves unresolved intents of one repository to
// owner_resolved in one transaction. Every intent must still be unresolved,
// and no import run may be active for the repository. Runs are not changed.
func (s *Store) ResolveImportIntents(ctx context.Context, repositoryID string, resolutions []ImportIntentResolution, now time.Time) error {
	if len(resolutions) == 0 {
		return ErrImportIntentNotUnresolved
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_runs WHERE repository_id=? AND status IN (?,?,?,?,?)`,
		repositoryID, ImportRunPreparing, ImportRunFetching, ImportRunIndexing, ImportRunInspecting, ImportRunPublishing).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrImportRunActive
	}
	for _, resolution := range resolutions {
		intent, err := scanImportIntent(tx.QueryRowContext(ctx, importIntentSelect+` WHERE id=?`, resolution.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrImportIntentNotUnresolved
		}
		if err != nil {
			return err
		}
		if intent.RepositoryID != repositoryID || intent.Status != ImportIntentUnresolved {
			return ErrImportIntentNotUnresolved
		}
		intent.Status = ImportIntentOwnerResolved
		intent.HeadOwned = false
		intent.ReceiptJSON = resolution.ReceiptJSON
		intent.ReceiptDigest = ImportReceiptDigest(resolution.ReceiptJSON)
		intent.Reason = resolution.Reason
		intent.UpdatedAt = now
		if err := validateImportIntentRecord(intent); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE import_publication_intents SET status=?,head_owned=0,receipt_json=?,receipt_digest=?,reason=?,updated_at=?
			WHERE id=? AND repository_id=? AND status=?`,
			intent.Status, intent.ReceiptJSON, intent.ReceiptDigest, intent.Reason, now.Unix(), intent.ID, repositoryID, ImportIntentUnresolved)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return err
		} else if changed != 1 {
			return ErrImportIntentNotUnresolved
		}
	}
	return tx.Commit()
}

// UnresolvedImportIntents returns the repository's unresolved intents,
// oldest first.
func (s *Store) UnresolvedImportIntents(ctx context.Context, repositoryID string) ([]ImportIntent, error) {
	rows, err := s.db.QueryContext(ctx, importIntentSelect+` WHERE repository_id=? AND status=? ORDER BY created_at,id`, repositoryID, ImportIntentUnresolved)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportIntent
	for rows.Next() {
		record, err := scanImportIntent(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func sameImportHeadOwnership(left, right string) bool {
	leftFields := strings.Fields(left)
	rightFields := strings.Fields(right)
	if len(leftFields) == 0 || len(rightFields) == 0 || leftFields[0] != rightFields[0] {
		return false
	}
	switch leftFields[0] {
	case "absent":
		return len(leftFields) == 1 && len(rightFields) == 1
	case "detached":
		return len(leftFields) == 2 && len(rightFields) == 2 && leftFields[1] == rightFields[1]
	case "symbolic":
		return len(leftFields) >= 2 && len(rightFields) >= 2 && leftFields[1] == rightFields[1]
	default:
		return false
	}
}

func validateImportIntentMap(values map[string]string, allowEmptyValue bool) error {
	for ref, oid := range values {
		if !validImportRefName(ref) {
			return errors.New("invalid import intent ref")
		}
		if ref == ImportHeadRef {
			if !validImportHeadIdentity(oid) {
				return errors.New("invalid import intent HEAD identity")
			}
			continue
		}
		if oid == "" && allowEmptyValue {
			continue
		}
		if !validObjectID(oid) {
			return errors.New("invalid import intent object")
		}
	}
	return nil
}

// validateImportObservedMap keeps the source facts separate from destination
// targets. HEAD is recorded here when the source advertised HEAD facts.
func validImportHeadIdentity(value string) bool {
	if value == "absent" {
		return true
	}
	fields := strings.Split(value, " ")
	switch {
	case len(fields) == 3 && fields[0] == "symbolic":
		return strings.HasPrefix(fields[1], "refs/heads/") && validImportRefName(fields[1]) && (fields[2] == "" || validObjectID(fields[2]))
	case len(fields) == 2 && fields[0] == "detached":
		return validObjectID(fields[1])
	default:
		return false
	}
}

func validateImportObservedMap(values map[string]string) error {
	for ref, oid := range values {
		if !validImportRefName(ref) {
			return errors.New("invalid import observed ref")
		}
		if ref == ImportHeadRef {
			if oid != "" && !validObjectID(oid) && !validImportHeadIdentity(oid) {
				return errors.New("invalid import observed HEAD fact")
			}
			continue
		}
		if !validObjectID(oid) {
			return errors.New("invalid import observed object")
		}
	}
	return nil
}

func validInitialDestinationState(value string) bool {
	switch value {
	case ImportInitialPreparing, ImportInitialReady, ImportInitialPublished, ImportInitialReleased, ImportInitialCleanupFailed, ImportInitialUnknown:
		return true
	default:
		return false
	}
}

func validOwnedInitialDestinationName(name string) bool {
	const prefix = ".owngit-create-"
	if !strings.HasPrefix(name, prefix) || strings.ContainsAny(name, `/\`) {
		return false
	}
	return isLowerHex(name[len(prefix):], 32)
}

func validateImportInitialDestination(item ImportInitialDestination, states ...string) error {
	if item.Name == "" || len(item.Name) > 80 || strings.ContainsAny(item.Name, `/\`) || !strings.HasPrefix(item.Name, ".owngit-create-") {
		return errors.New("invalid initial destination name")
	}
	if len(item.RepositoryID) > 100 || len(item.RunID) > 64 || len(item.DisplayName) > 100 || len(item.Description) > 500 || len(item.Issue) > 500 {
		return errors.New("invalid initial destination record")
	}
	if !isLowerHex(item.RootID, 32) || len(item.Token) < 16 || len(item.Token) > 64 || item.CreatedAt.IsZero() {
		return errors.New("invalid initial destination identity")
	}
	valid := false
	for _, accepted := range states {
		if item.State == accepted {
			valid = true
			break
		}
	}
	if !valid || !validInitialDestinationState(item.State) {
		return errors.New("invalid initial destination state")
	}
	if item.State != ImportInitialUnknown {
		if !validOwnedInitialDestinationName(item.Name) || item.RepositoryID == "" || item.RunID == "" || item.DisplayName == "" {
			return errors.New("initial destination identity is incomplete")
		}
	}
	return nil
}

// RegisterImportInitialDestination inserts one ownership row. It does not
// replace an existing row, so reconciliation cannot adopt a name that a run
// already authorized.
func (s *Store) RegisterImportInitialDestination(ctx context.Context, item ImportInitialDestination) error {
	if err := validateImportInitialDestination(item, item.State); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO import_initial_destinations(
		name,repository_id,run_id,root_id,token,display_name,description,state,issue,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		item.Name, item.RepositoryID, item.RunID, item.RootID, item.Token, item.DisplayName, item.Description,
		item.State, item.Issue, item.CreatedAt.Unix(), item.CreatedAt.Unix())
	return err
}

func (s *Store) ImportInitialDestination(ctx context.Context, name string) (ImportInitialDestination, bool, error) {
	rows, err := s.db.QueryContext(ctx, importInitialDestinationSelect+` WHERE name=?`, name)
	if err != nil {
		return ImportInitialDestination{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ImportInitialDestination{}, false, rows.Err()
	}
	record, err := scanImportInitialDestination(rows)
	if err != nil {
		return ImportInitialDestination{}, false, err
	}
	return record, true, rows.Err()
}

func (s *Store) ImportInitialDestinationsForRun(ctx context.Context, runID string) ([]ImportInitialDestination, error) {
	rows, err := s.db.QueryContext(ctx, importInitialDestinationSelect+` WHERE run_id=? ORDER BY created_at,name`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportInitialDestination
	for rows.Next() {
		record, err := scanImportInitialDestination(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ImportInitialDestinations(ctx context.Context) ([]ImportInitialDestination, error) {
	rows, err := s.db.QueryContext(ctx, importInitialDestinationSelect+` ORDER BY created_at,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ImportInitialDestination
	for rows.Next() {
		record, err := scanImportInitialDestination(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) SetImportInitialDestinationState(ctx context.Context, name, destinationState, issue string, now time.Time) error {
	if !validInitialDestinationState(destinationState) || now.IsZero() || len(name) == 0 || len(name) > 80 {
		return errors.New("invalid initial destination state update")
	}
	if len(issue) > 500 {
		issue = issue[:500]
	}
	result, err := s.db.ExecContext(ctx, `UPDATE import_initial_destinations SET state=?,issue=?,updated_at=? WHERE name=?`,
		destinationState, issue, now.Unix(), name)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("initial destination state update did not affect exactly one row")
	}
	return nil
}

const importInitialDestinationSelect = `SELECT name,repository_id,run_id,root_id,token,display_name,description,state,issue,created_at,updated_at FROM import_initial_destinations`

func scanImportInitialDestination(scanner rowScanner) (ImportInitialDestination, error) {
	var record ImportInitialDestination
	var created, updated int64
	if err := scanner.Scan(&record.Name, &record.RepositoryID, &record.RunID, &record.RootID, &record.Token, &record.DisplayName, &record.Description, &record.State, &record.Issue, &created, &updated); err != nil {
		return ImportInitialDestination{}, err
	}
	record.CreatedAt = unixTime(created)
	record.UpdatedAt = unixTime(updated)
	return record, nil
}
