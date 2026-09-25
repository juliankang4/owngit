package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	databaseName                = "owngit.sqlite"
	IncompleteRestoreMarkerName = ".owngit-restore-pending"

	// The committed baseline wrote no schema version marker; numbered
	// schemas are defined by schemaSteps. This SHA-256 fingerprint covers
	// normalized, non-internal sqlite_master entries of the baseline emitted
	// by commit 8fdefd1bf10bd4a41b7261131efc119d46666260.
	committedBaselineSchemaFingerprint = "0b1acb0288e7a64da492d7a2a768538052492f887c3b5e6ec2efc7a42b465600"
)

var ErrSetupComplete = errors.New("setup is already complete")

type Store struct {
	db  *sql.DB
	dir string
	// schemaUpgrade describes the upgrade this Open applied, for SchemaUpgrade.
	schemaUpgrade string

	// Credential transitions are repository-scoped. credentialRestoreMu stops
	// all of them only while a whole-store restore replaces portable state.
	credentialRegistryMu sync.Mutex
	credentialLocks      map[string]*sync.Mutex
	credentialRestoreMu  sync.RWMutex
	credentialAuthority  sync.Map
}

type Settings struct {
	Initialized          bool
	RepositoryRoot       string
	AccessMode           string
	AccessSessionVersion int64
	AdminSessionVersion  int64
	InsecureHTTPAccepted bool
	// CheckLogRetentionDays bounds disposable raw check logs. Durable task and
	// attempt records are never removed by log retention.
	CheckLogRetentionDays int
	// UpdateCheck allows the daily new-release check. It is machine-local:
	// a missing key (older state, a restored backup) means on.
	UpdateCheck bool
}

type Session struct {
	Kind    string
	CSRF    string
	Version int64
	Expires time.Time
}

type Repository struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	// AttemptSequence is the repository-wide counter that issues check attempt
	// sequences. It is portable so a restore keeps issuing higher sequences.
	AttemptSequence int64
}

type BootstrapSnapshot struct {
	Present   bool   `json:"present"`
	TokenHash []byte `json:"token_hash,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

func Open(ctx context.Context, dir string) (result *Store, err error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	ancestor := absolute
	for {
		if _, err := os.Stat(ancestor); err == nil {
			if err := ensureLocalStateFilesystem(ancestor); err != nil {
				return nil, fmt.Errorf("validate state directory parent: %w", err)
			}
			break
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect state directory parent: %w", err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil, errors.New("state directory has no accessible parent")
		}
		ancestor = parent
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(absolute, IncompleteRestoreMarkerName)); err == nil {
		return nil, errors.New("state directory belongs to an incomplete offline restore; follow the interrupted-restore procedure before use")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect incomplete restore marker: %w", err)
	}
	if err := ensureLocalStateFilesystem(absolute); err != nil {
		return nil, fmt.Errorf("validate state directory: %w", err)
	}
	// An existing database is classified before any permission change or
	// read-write SQLite access, so a refused database keeps its bytes, entries
	// and modes. The inspection holds the source handles until acceptance and
	// releases them before SQLite opens the same files. A release failure
	// blocks the writable open because the bound objects are no longer
	// reliably known.
	inspected, err := inspectState(ctx, absolute)
	if err != nil {
		return nil, err
	}
	// Keep the release guard until acceptance finishes normally, so an
	// interrupted acceptance cannot leave source handles open.
	defer func() {
		if inspected != nil {
			err = errors.Join(err, inspected.release())
			if err != nil {
				result = nil
			}
		}
	}()
	if err := inspected.accept(ctx, absolute); err != nil {
		return nil, err
	}
	class := inspected.class
	if err := inspected.release(); err != nil {
		return nil, err
	}
	inspected = nil
	path := filepath.Join(absolute, databaseName)
	db, err := sql.Open("sqlite", sqliteFileURI(path))
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, dir: absolute}
	if err := store.initialize(ctx, class); err != nil {
		db.Close()
		return nil, err
	}
	for _, protectedPath := range []string{path, path + walSuffix, path + shmSuffix} {
		if err := ProtectPrivatePath(protectedPath, false); err != nil && !errors.Is(err, os.ErrNotExist) {
			db.Close()
			return nil, fmt.Errorf("protect state database file: %w", err)
		}
	}
	return store, nil
}

// SchemaUpgrade returns a one-line description of the schema upgrade that Open
// applied to an existing database, for the caller to log, for example "state
// database upgraded from schema 14 to 15". It returns "" when Open created a
// new database or found the schema current, including one that another opener
// had already upgraded.
func (s *Store) SchemaUpgrade() string { return s.schemaUpgrade }

func sqliteFileURI(path string) string {
	return sqliteURI(path, "_txlock=immediate")
}

func sqliteURI(path, query string) string {
	slashPath := filepath.ToSlash(path)
	if len(slashPath) >= 3 && slashPath[1] == ':' && slashPath[2] == '/' &&
		(('a' <= slashPath[0] && slashPath[0] <= 'z') || ('A' <= slashPath[0] && slashPath[0] <= 'Z')) {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath, RawQuery: query}).String()
}

// initialize prepares the accepted database. The inspection result selects
// the path, but the real connection reclassifies before every persistent
// write. A migration candidate is reclassified again inside the immediate
// write transaction that applies the authorized schema steps.
func (s *Store) initialize(ctx context.Context, expected schemaClass) error {
	// Connection settings first, because they are not persistent writes.
	for _, pragma := range []string{
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("initialize state database: %w", err)
		}
	}
	class, err := classifySchema(ctx, s.db)
	if err != nil {
		return err
	}
	if class != schemaCurrent && class != expected {
		return unstable("state database changed between inspection and open")
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("initialize state database: %w", err)
	}
	if class == schemaCurrent {
		// Another accepted opener may have completed the migration first, so
		// a current database is served without repeating its statements.
		return nil
	}
	return s.migrate(ctx, expected)
}

// queryRower is the read surface shared by a connection and a transaction.
type queryRower interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// closeRows ends a row loop and reports why iteration stopped. When a step
// fails mid-scan, database/sql closes the rows itself and keeps the error only
// in Err, so a later Close returns nil and a truncated read would look
// complete.
func closeRows(rows *sql.Rows) error {
	return errors.Join(rows.Err(), rows.Close())
}

// classifySchema returns the accepted class of the visible schema or the
// compatibility error that refuses it.
func classifySchema(ctx context.Context, db queryRower) (schemaClass, error) {
	version, versioned, err := readSchemaVersion(ctx, db)
	if err != nil {
		return schemaClass{}, err
	}
	if !versioned {
		return validateUnversionedSchema(ctx, db)
	}
	current := currentSchemaVersion()
	switch {
	case version == current:
		return schemaCurrent, nil
	case version > current:
		return schemaClass{}, fmt.Errorf("state database schema version %d is newer than this OwnGit build supports (%d)", version, current)
	case isReleasedSchema(version):
		return schemaReleased(version), nil
	case version >= 1:
		return schemaClass{}, fmt.Errorf("state database uses the unreleased development schema %d; this build upgrades only the committed baseline (no schema version) and %s, and opens schema %d", version, describeReleasedSchemas(), current)
	default:
		return schemaClass{}, fmt.Errorf("unsupported state database schema version %d", version)
	}
}

// currentSchemaVersion is the schema this build writes.
func currentSchemaVersion() int {
	return schemaSteps[len(schemaSteps)-1].version
}

// isReleasedSchema reports whether version is a released schema below the
// current one, which upgrades in place.
func isReleasedSchema(version int) bool {
	for _, step := range schemaSteps[:len(schemaSteps)-1] {
		if step.version == version {
			return step.released
		}
	}
	return false
}

// describeReleasedSchemas names the released schemas below the current one,
// for example "released schema 14" or "released schemas 14 and 15".
func describeReleasedSchemas() string {
	var versions []string
	for _, step := range schemaSteps[:len(schemaSteps)-1] {
		if step.released {
			versions = append(versions, strconv.Itoa(step.version))
		}
	}
	switch len(versions) {
	case 0:
		return "no released schema"
	case 1:
		return "released schema " + versions[0]
	default:
		return "released schemas " + strings.Join(versions[:len(versions)-1], ", ") + " and " + versions[len(versions)-1]
	}
}

func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	version, _, err := readSchemaVersion(ctx, s.db)
	return version, err
}

func readSchemaVersion(ctx context.Context, db queryRower) (int, bool, error) {
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='metadata'`).Scan(&tables); err != nil {
		return 0, false, fmt.Errorf("inspect state database: %w", err)
	}
	if tables == 0 {
		return 0, false, nil
	}
	var value string
	err := db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='schema_version'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read state schema version: %w", err)
	}
	version, err := strconv.Atoi(value)
	if err != nil || version < 0 {
		return 0, false, fmt.Errorf("invalid state schema version %q", value)
	}
	return version, true, nil
}

// validateUnversionedSchema accepts an empty database or the exact committed
// baseline catalog.
func validateUnversionedSchema(ctx context.Context, db queryRower) (schemaClass, error) {
	fingerprint, objects, err := schemaFingerprint(ctx, db)
	if err != nil {
		return schemaClass{}, fmt.Errorf("inspect unversioned state database: %w", err)
	}
	if objects == 0 {
		return schemaEmpty, nil
	}
	if fingerprint == committedBaselineSchemaFingerprint {
		return schemaBaseline, nil
	}
	return schemaClass{}, errors.New("state database has no schema version and does not match the committed baseline")
}

func schemaFingerprint(ctx context.Context, db queryRower) (string, int, error) {
	rows, err := db.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE name NOT GLOB 'sqlite_*' ORDER BY type,name`)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	digest := sha256.New()
	objects := 0
	for rows.Next() {
		var objectType, name string
		var statement sql.NullString
		if err := rows.Scan(&objectType, &name, &statement); err != nil {
			return "", 0, err
		}
		normalized := ""
		if statement.Valid {
			normalized = strings.Join(strings.Fields(statement.String), " ")
		}
		for _, field := range []string{objectType, name, normalized} {
			_, _ = fmt.Fprintf(digest, "%d:", len(field))
			_, _ = digest.Write([]byte(field))
		}
		objects++
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), objects, nil
}

// migrate applies every authorized step in one immediate transaction. The
// classification inside that transaction is the migration authority. A
// completed concurrent migration is accepted, while any other owner change is
// refused before a migration statement runs.
func (s *Store) migrate(ctx context.Context, expected schemaClass) (err error) {
	// A step that rebuilds a parent table drops it, and with foreign keys on
	// SQLite would first delete every child row through ON DELETE CASCADE.
	// Enforcement is therefore off for the migration connection, which is the
	// only one, and is checked before commit and restored afterwards. The
	// pragma has no effect inside a transaction, so it is set around it.
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("prepare state schema migration: %w", err)
	}
	defer func() {
		if _, restoreErr := s.db.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys=ON`); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore foreign key enforcement after schema migration: %w", restoreErr))
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	class, err := classifySchema(ctx, tx)
	if err != nil {
		return err
	}
	if class == schemaCurrent {
		return nil
	}
	if class != expected {
		return unstable("state database changed before schema migration")
	}
	// Only the committed baseline, an empty database and a released schema
	// reach this point. The first two run every step, a released schema every
	// step after its own version.
	previous := schemaSteps[0].version - 1
	if class.kind == kindReleased {
		previous = class.version
	}
	for _, step := range schemaSteps {
		if step.version <= previous {
			continue
		}
		if step.version != previous+1 {
			return fmt.Errorf("missing state schema migration %d", previous+1)
		}
		for _, statement := range step.statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply state schema migration %d: %w", step.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(step.version)); err != nil {
			return fmt.Errorf("record state schema migration %d: %w", step.version, err)
		}
		previous = step.version
	}
	violations, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check foreign keys after schema migration: %w", err)
	}
	violated := violations.Next()
	if err := closeRows(violations); err != nil {
		return fmt.Errorf("check foreign keys after schema migration: %w", err)
	}
	if violated {
		return errors.New("state schema migration left a foreign key violation")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	switch class.kind {
	case kindReleased:
		s.schemaUpgrade = fmt.Sprintf("state database upgraded from schema %d to %d", class.version, previous)
	case kindBaseline:
		s.schemaUpgrade = fmt.Sprintf("state database upgraded from the committed baseline (no schema version) to schema %d", previous)
	}
	return nil
}

// schemaStep upgrades the schema from version-1 to version.
type schemaStep struct {
	version int
	// released marks a version that an OwnGit release wrote. A database at a
	// released version below the current one upgrades in place through every
	// later step. Any other numbered version below the current one came from
	// an unreleased development build and is refused.
	released   bool
	statements []string
}

// schemaSteps is the ordered migration chain and the single source of truth
// for schema versions: its last step is the schema this build writes, and its
// released steps are the versions it upgrades. The committed baseline and an
// empty database run every step from the first. Together the steps produce the
// current schema catalog, so their text must not change. A new schema appends
// one step, and a release that writes it marks that step released. Version 6
// upgrades the committed baseline, version 7 adds local raw check logs, version
// 8 adds the unused direct-review tables, version 9 adds the automatic-check
// foundation, version 10 binds executable settings and executor roles, version
// 11 adds inbound import state, version 12 records structured HEAD ownership,
// version 13 records machine-local ownership of unpublished initial
// destinations, version 14 admits the owner_resolved intent status, and
// version 15 admits closed pull requests. The 1.0.0 to 1.0.2 releases wrote
// schema 14 and 1.0.3 wrote schema 15.
// Schema 12 has no ownership rows. A missing row never authorizes removal or
// publication of a look-alike directory.
var schemaSteps = []schemaStep{
	{version: 6, statements: []string{
		`CREATE TABLE IF NOT EXISTS metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS passwords (
			kind TEXT PRIMARY KEY CHECK (kind IN ('access','admin')),
			encoded TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash BLOB PRIMARY KEY,
			kind TEXT NOT NULL CHECK (kind IN ('setup','general','admin')),
			csrf TEXT NOT NULL,
			version INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS bootstrap (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			token_hash BLOB NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS login_attempts (
			kind TEXT NOT NULL,
			address TEXT NOT NULL,
			window_started_at INTEGER NOT NULL,
			attempts INTEGER NOT NULL,
			blocked_until INTEGER NOT NULL,
			PRIMARY KEY (kind, address)
		)`,
		`CREATE TABLE IF NOT EXISTS repositories (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS pull_requests (
			repository_id TEXT NOT NULL,
			number INTEGER NOT NULL CHECK (number > 0),
			title TEXT NOT NULL,
			source_branch TEXT NOT NULL,
			target_branch TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('creating','open','merged')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			merge_source_oid TEXT NOT NULL DEFAULT '',
			merge_target_oid TEXT NOT NULL DEFAULT '',
			merge_oid TEXT NOT NULL DEFAULT '',
			merge_receipt_ref TEXT NOT NULL DEFAULT '',
			merged_at INTEGER,
			PRIMARY KEY (repository_id, number),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS pull_request_revisions (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			recorded_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, pull_request_number, source_oid, target_oid),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS pull_request_reviews (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0),
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','approved','changes_requested','skipped','not_requested')),
			reviewer_label TEXT NOT NULL,
			provenance TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, pull_request_number, sequence),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS pull_request_merge_intents (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			mode TEXT NOT NULL,
			tree_oid TEXT NOT NULL,
			result_oid TEXT NOT NULL,
			receipt_ref TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('preparing','planned','ready','complete')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, pull_request_number, source_oid, target_oid),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS trusted_hosts (
			host TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL
		)`,
		`INSERT OR IGNORE INTO metadata(key,value) VALUES
			('initialized','false'),
			('access_mode','open'),
			('access_session_version','1'),
			('admin_session_version','1'),
			('insecure_http_accepted','false')`,
		// Tasks keep only authoritative identity, title, and server-observed
		// timestamps. Status, budget, applied attempt, and pending attempt are
		// derived from attempts and reservations.
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			title TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		// One repository-wide counter issues attempt sequences. It advances
		// only with a new accepted registration.
		`CREATE TABLE IF NOT EXISTS repository_attempt_counters (
			repository_id TEXT PRIMARY KEY,
			attempt_sequence INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS check_configurations (
			repository_id TEXT NOT NULL,
			version INTEGER NOT NULL CHECK (version > 0),
			config_hash TEXT NOT NULL,
			checks_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, version),
			UNIQUE (repository_id, config_hash),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		// The registration worktree observation and the submitted completion
		// observation are stored separately, so a clean registration followed
		// by a dirty completion stays verifiable.
		`CREATE TABLE IF NOT EXISTS check_attempts (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			revision_oid TEXT NOT NULL,
			registration_worktree_state TEXT NOT NULL CHECK (registration_worktree_state IN ('clean','dirty','unknown')),
			submitted_worktree_state TEXT NOT NULL DEFAULT '' CHECK (submitted_worktree_state IN ('','clean','dirty','unknown')),
			configuration_version INTEGER NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','passed','failed','error','cancelled','incomplete','unavailable')),
			exit_code INTEGER,
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			summary TEXT NOT NULL DEFAULT '',
			protection TEXT NOT NULL DEFAULT 'unknown',
			execution_scope TEXT NOT NULL DEFAULT 'inherited',
			credential_id TEXT NOT NULL DEFAULT '',
			timeout_ms INTEGER NOT NULL DEFAULT 0,
			output_limit_bytes INTEGER NOT NULL DEFAULT 0,
			log_id TEXT NOT NULL DEFAULT '',
			log_expires_at INTEGER,
			log_truncated INTEGER NOT NULL DEFAULT 0,
			log_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			sequence INTEGER NOT NULL,
			cycle_id TEXT NOT NULL DEFAULT '',
			registration_digest TEXT NOT NULL,
			completion_digest TEXT NOT NULL DEFAULT '',
			submitted_log_digest TEXT NOT NULL DEFAULT '',
			submitted_truncated INTEGER NOT NULL DEFAULT 0,
			submitted_cancelled INTEGER NOT NULL DEFAULT 0,
			log_digest TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS check_results (
			attempt_id TEXT NOT NULL,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			command TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('passed','failed','error','cancelled','incomplete','unavailable')),
			exit_code INTEGER,
			duration_ms INTEGER NOT NULL,
			output_excerpt TEXT NOT NULL,
			truncated INTEGER NOT NULL DEFAULT 0,
			cleanup_error TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (attempt_id, position),
			FOREIGN KEY (attempt_id) REFERENCES check_attempts(id) ON DELETE CASCADE
		)`,
		// A cycle records the repository attempt counter observed inside its
		// reservation transaction. The boundary is immutable, so a
		// pre-reservation attempt finishing later cannot certify the round.
		`CREATE TABLE IF NOT EXISTS check_cycles (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0),
			reserved_at INTEGER NOT NULL,
			reserved_after_sequence INTEGER NOT NULL,
			UNIQUE (task_id, sequence),
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS helper_credentials (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			label TEXT NOT NULL,
			creation_id TEXT NOT NULL DEFAULT '',
			token_hash BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			revoked_at INTEGER,
			last_used_at INTEGER,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS helper_credentials_creation ON helper_credentials(repository_id, creation_id) WHERE creation_id != ''`,
		`CREATE INDEX IF NOT EXISTS check_attempts_revision ON check_attempts(repository_id, revision_oid, sequence)`,
		`CREATE INDEX IF NOT EXISTS check_attempts_task ON check_attempts(task_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS check_attempts_pending ON check_attempts(task_id, status, sequence)`,
		`CREATE INDEX IF NOT EXISTS check_cycles_task ON check_cycles(task_id, sequence)`,
		// The committed baseline review table predates the optional review
		// status, and SQLite cannot alter a CHECK constraint in place.
		`CREATE TABLE pull_request_reviews_v6 (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0),
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','approved','changes_requested','skipped','not_requested')),
			reviewer_label TEXT NOT NULL,
			provenance TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, pull_request_number, sequence),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		)`,
		`INSERT INTO pull_request_reviews_v6(repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,created_at)
			SELECT repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,created_at FROM pull_request_reviews`,
		`DROP TABLE pull_request_reviews`,
		`ALTER TABLE pull_request_reviews_v6 RENAME TO pull_request_reviews`,
	}},
	{version: 7, statements: []string{
		`CREATE TABLE check_raw_logs (
			attempt_id TEXT PRIMARY KEY,
			content BLOB NOT NULL CHECK (length(content) <= 262144),
			expires_at INTEGER NOT NULL,
			FOREIGN KEY (attempt_id) REFERENCES check_attempts(id) ON DELETE CASCADE
		) WITHOUT ROWID`,
		`CREATE INDEX check_raw_logs_expiry ON check_raw_logs(expires_at,attempt_id)`,
	}},
	{version: 8, statements: []string{
		`ALTER TABLE pull_request_reviews ADD COLUMN review_event_id TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX pull_request_reviews_event ON pull_request_reviews(repository_id,review_event_id) WHERE review_event_id != ''`,
		`CREATE TABLE direct_review_credentials (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL UNIQUE,
			label TEXT NOT NULL CHECK (length(CAST(label AS BLOB)) BETWEEN 1 AND 100),
			value TEXT NOT NULL CHECK (length(CAST(value AS BLOB)) BETWEEN 1 AND 16384),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE direct_review_repository_settings (
			repository_id TEXT PRIMARY KEY,
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			protocol TEXT NOT NULL CHECK (protocol IN ('openai_responses','anthropic_messages','compatible_chat_completions')),
			endpoint TEXT NOT NULL CHECK (length(CAST(endpoint AS BLOB)) BETWEEN 1 AND 2048),
			model TEXT NOT NULL CHECK (length(CAST(model AS BLOB)) BETWEEN 1 AND 200),
			authentication_mode TEXT NOT NULL CHECK (authentication_mode IN ('stored_credential','none')),
			provider_limits_json TEXT NOT NULL CHECK (length(CAST(provider_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			repository_limits_json TEXT NOT NULL CHECK (length(CAST(repository_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			instruction_version TEXT NOT NULL CHECK (length(CAST(instruction_version AS BLOB)) BETWEEN 1 AND 100),
			credential_id TEXT NOT NULL DEFAULT '',
			connection_version INTEGER NOT NULL DEFAULT 0 CHECK (connection_version >= 0),
			authority_epoch TEXT NOT NULL CHECK (length(authority_epoch) = 32),
			probe_fingerprint TEXT NOT NULL DEFAULT '',
			probe_request_id TEXT NOT NULL DEFAULT '',
			probe_updated_at INTEGER NOT NULL DEFAULT 0,
			automatic_pr_enabled INTEGER NOT NULL DEFAULT 0 CHECK (automatic_pr_enabled IN (0,1)),
			automatic_task_enabled INTEGER NOT NULL DEFAULT 0 CHECK (automatic_task_enabled IN (0,1)),
			automatic_consent_version INTEGER NOT NULL DEFAULT 0 CHECK (automatic_consent_version >= 0),
			automatic_consent_digest TEXT NOT NULL DEFAULT '',
			automatic_consent_active INTEGER NOT NULL DEFAULT 0 CHECK (automatic_consent_active IN (0,1)),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE direct_review_probes (
			request_id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			registration_digest TEXT NOT NULL,
			capability_fingerprint TEXT NOT NULL,
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			connection_version INTEGER NOT NULL CHECK (connection_version >= 0),
			connection_fingerprint TEXT NOT NULL,
			protocol TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			model TEXT NOT NULL,
			authentication_mode TEXT NOT NULL,
			provider_limits_json TEXT NOT NULL CHECK (length(CAST(provider_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			disclosure_version TEXT NOT NULL,
			disclosure_digest TEXT NOT NULL,
			phase TEXT NOT NULL CHECK (phase IN ('preparing','running','terminal')),
			cancel_requested_at INTEGER,
			created_at INTEGER NOT NULL,
			running_at INTEGER,
			terminal_at INTEGER,
			result_json TEXT NOT NULL DEFAULT '' CHECK (length(CAST(result_json AS BLOB)) <= 1048576),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX direct_review_probes_repository ON direct_review_probes(repository_id,created_at,request_id)`,
		`CREATE TABLE direct_review_task_contexts (
			context_id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL UNIQUE,
			credential_id TEXT NOT NULL,
			base_oid TEXT NOT NULL,
			head_oid TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL DEFAULT 0 CHECK (pull_request_number >= 0),
			observed_source_oid TEXT NOT NULL DEFAULT '',
			observed_target_oid TEXT NOT NULL DEFAULT '',
			registration_digest TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
			FOREIGN KEY (attempt_id) REFERENCES check_attempts(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX direct_review_task_contexts_task ON direct_review_task_contexts(repository_id,task_id,created_at,context_id)`,
		`CREATE TABLE direct_review_requests (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL UNIQUE,
			repository_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('manual','automatic_pr','automatic_task')),
			source_event_key TEXT NOT NULL DEFAULT '',
			pull_request_number INTEGER NOT NULL DEFAULT 0 CHECK (pull_request_number >= 0),
			task_id TEXT NOT NULL DEFAULT '',
			attempt_id TEXT NOT NULL DEFAULT '',
			observed_source_oid TEXT NOT NULL DEFAULT '',
			observed_target_oid TEXT NOT NULL DEFAULT '',
			effective_base_oid TEXT NOT NULL DEFAULT '',
			effective_head_oid TEXT NOT NULL DEFAULT '',
			diff_mode TEXT NOT NULL DEFAULT '' CHECK (diff_mode IN ('','two_commit')),
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			connection_version INTEGER NOT NULL CHECK (connection_version >= 0),
			connection_fingerprint TEXT NOT NULL,
			protocol TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			model TEXT NOT NULL,
			authentication_mode TEXT NOT NULL,
			provider_limits_json TEXT NOT NULL CHECK (length(CAST(provider_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			repository_limits_json TEXT NOT NULL CHECK (length(CAST(repository_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			instruction_version TEXT NOT NULL,
			consent_version TEXT NOT NULL DEFAULT '',
			consent_digest TEXT NOT NULL DEFAULT '',
			disclosure_version TEXT NOT NULL,
			disclosure_digest TEXT NOT NULL,
			registration_digest TEXT NOT NULL,
			phase TEXT NOT NULL CHECK (phase IN ('observed','preparing','running','terminal')),
			cancel_requested_at INTEGER,
			created_at INTEGER NOT NULL,
			preparing_at INTEGER,
			running_at INTEGER,
			terminal_at INTEGER,
			initial_context_bytes INTEGER NOT NULL DEFAULT 0 CHECK (initial_context_bytes >= 0),
			initial_context_truncated INTEGER NOT NULL DEFAULT 0 CHECK (initial_context_truncated IN (0,1)),
			result_json TEXT NOT NULL DEFAULT '' CHECK (length(CAST(result_json AS BLOB)) <= 1048576),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX direct_review_requests_source_event ON direct_review_requests(repository_id,trigger_kind,source_event_key) WHERE source_event_key != ''`,
		`CREATE INDEX direct_review_requests_pull_request ON direct_review_requests(repository_id,pull_request_number,sequence) WHERE pull_request_number > 0`,
		`CREATE INDEX direct_review_requests_task ON direct_review_requests(repository_id,task_id,sequence) WHERE task_id != ''`,
	}},
	{version: 9, statements: []string{
		// Attempts gain a server-owned job link. Empty keeps the exact helper
		// behavior and every historical digest shape.
		`ALTER TABLE check_attempts ADD COLUMN job_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX check_attempts_job ON check_attempts(job_id) WHERE job_id != ''`,
		// One current policy row per repository. It holds the operator-selected
		// executor, the limits that cap workflow requests, the local consent
		// generation, and the local authority epoch. Queue and lease management
		// are server-owned and are not visible to the committed workflow.
		`CREATE TABLE check_policies (
			repository_id TEXT PRIMARY KEY,
			policy_version INTEGER NOT NULL CHECK (policy_version > 0),
			policy_digest TEXT NOT NULL CHECK (length(policy_digest) = 64),
			executor TEXT NOT NULL CHECK (executor IN ('host','container','external_runner')),
			allowed_events TEXT NOT NULL CHECK (length(CAST(allowed_events AS BLOB)) BETWEEN 2 AND 100),
			max_timeout_ms INTEGER NOT NULL CHECK (max_timeout_ms > 0),
			max_output_limit_bytes INTEGER NOT NULL CHECK (max_output_limit_bytes > 0),
			queue_limit INTEGER NOT NULL CHECK (queue_limit > 0),
			max_active_jobs INTEGER NOT NULL CHECK (max_active_jobs > 0),
			max_lease_ms INTEGER NOT NULL CHECK (max_lease_ms > 0),
			consent_version INTEGER NOT NULL DEFAULT 0 CHECK (consent_version >= 0),
			consent_digest TEXT NOT NULL DEFAULT '' CHECK (consent_digest = '' OR length(consent_digest) = 64),
			consent_active INTEGER NOT NULL DEFAULT 0 CHECK (consent_active IN (0,1)),
			runner_generation INTEGER NOT NULL DEFAULT 0 CHECK (runner_generation >= 0),
			authority_epoch TEXT NOT NULL CHECK (length(authority_epoch) = 32),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		// One durable job per effective execution condition. The dedup digest
		// covers the source, workflow, checks, limits, executor, policy, consent,
		// and rerun generation, so a repeated observation cannot double-run and an
		// uncertain loss requires an explicit rerun.
		`CREATE TABLE check_jobs (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('push','pull_request')),
			event_key TEXT NOT NULL CHECK (length(CAST(event_key AS BLOB)) BETWEEN 1 AND 200),
			source_oid TEXT NOT NULL,
			base_oid TEXT NOT NULL DEFAULT '',
			pull_request_number INTEGER NOT NULL DEFAULT 0 CHECK (pull_request_number >= 0),
			trigger_ref TEXT NOT NULL CHECK (length(CAST(trigger_ref AS BLOB)) BETWEEN 1 AND 200),
			workflow_path TEXT NOT NULL,
			workflow_oid TEXT NOT NULL DEFAULT '',
			workflow_digest TEXT NOT NULL CHECK (length(workflow_digest) = 64),
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			executor TEXT NOT NULL CHECK (executor IN ('host','container','external_runner')),
			policy_version INTEGER NOT NULL CHECK (policy_version > 0),
			consent_version INTEGER NOT NULL CHECK (consent_version > 0),
			limits_json TEXT NOT NULL CHECK (length(CAST(limits_json AS BLOB)) BETWEEN 2 AND 4096),
			dedup_digest TEXT NOT NULL CHECK (length(dedup_digest) = 64),
			rerun_root TEXT NOT NULL DEFAULT '',
			rerun_generation INTEGER NOT NULL DEFAULT 0 CHECK (rerun_generation >= 0),
			status TEXT NOT NULL CHECK (status IN ('pending','claimed','started','passed','failed','error','cancelled','incomplete','unavailable','ambiguous','interrupted')),
			attempt_id TEXT NOT NULL DEFAULT '',
			lease_id TEXT NOT NULL DEFAULT '',
			lease_expires_at INTEGER,
			credential_id TEXT NOT NULL DEFAULT '',
			credential_generation INTEGER NOT NULL DEFAULT 0 CHECK (credential_generation >= 0),
			protection TEXT NOT NULL DEFAULT 'unknown' CHECK (protection IN ('unknown','host','container','runner_reported')),
			admitted_at INTEGER NOT NULL,
			claimed_at INTEGER,
			started_at INTEGER,
			finished_at INTEGER,
			lease_lost_at INTEGER,
			cancel_requested_at INTEGER,
			interrupted_at INTEGER,
			summary TEXT NOT NULL DEFAULT '' CHECK (length(CAST(summary AS BLOB)) <= 500),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX check_jobs_dedup ON check_jobs(repository_id,dedup_digest)`,
		`CREATE INDEX check_jobs_queue ON check_jobs(repository_id,status,admitted_at,id)`,
		`CREATE INDEX check_jobs_attempt ON check_jobs(attempt_id) WHERE attempt_id != ''`,
		// Dedicated revocable runner authority. Only the verifier is stored. The
		// issued token carries its local epoch, identity, and generation, so a
		// pre-restore bearer cannot become valid under new local authority.
		`CREATE TABLE check_runner_credentials (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			label TEXT NOT NULL CHECK (length(CAST(label AS BLOB)) BETWEEN 1 AND 100),
			creation_id TEXT NOT NULL DEFAULT '',
			generation INTEGER NOT NULL CHECK (generation > 0),
			token_hash BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			revoked_at INTEGER,
			last_used_at INTEGER,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX check_runner_credentials_creation ON check_runner_credentials(repository_id,creation_id) WHERE creation_id != ''`,
		`CREATE UNIQUE INDEX check_runner_credentials_hash ON check_runner_credentials(repository_id,token_hash) WHERE revoked_at IS NULL`,
		// Latest observed refs per repository. Observations are machine-local
		// capture evidence, so a restore starts without them.
		`CREATE TABLE check_observations (
			repository_id TEXT NOT NULL,
			ref_name TEXT NOT NULL CHECK (length(CAST(ref_name AS BLOB)) BETWEEN 1 AND 500),
			oid TEXT NOT NULL,
			observed_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id,ref_name),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX check_observations_recent ON check_observations(repository_id,observed_at,ref_name)`,
	}},
	{version: 10, statements: []string{
		// Schema 9 did not persist source/container bounds. Its rows remain valid
		// history under their original v1 digests, but consent is cleared and the
		// operator must save a complete v2 policy before new execution.
		`ALTER TABLE check_policies ADD COLUMN execution_json TEXT NOT NULL DEFAULT '{"legacy":true}' CHECK (length(CAST(execution_json AS BLOB)) BETWEEN 2 AND 4096)`,
		`UPDATE check_policies SET consent_active=0`,
		// Rebuild the job table to add captured execution authority and to widen
		// the accepted trigger set with the released merge event. Exact schema 9
		// classification makes this copy shape deterministic.
		`ALTER TABLE check_jobs RENAME TO check_jobs_v9`,
		`CREATE TABLE check_jobs (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('push','pull_request')),
			event_key TEXT NOT NULL CHECK (length(CAST(event_key AS BLOB)) BETWEEN 1 AND 200),
			source_oid TEXT NOT NULL,
			base_oid TEXT NOT NULL DEFAULT '',
			pull_request_number INTEGER NOT NULL DEFAULT 0 CHECK (pull_request_number >= 0),
			trigger_ref TEXT NOT NULL CHECK (length(CAST(trigger_ref AS BLOB)) BETWEEN 1 AND 200),
			workflow_path TEXT NOT NULL,
			workflow_oid TEXT NOT NULL DEFAULT '',
			workflow_digest TEXT NOT NULL CHECK (length(workflow_digest) = 64),
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			executor TEXT NOT NULL CHECK (executor IN ('host','container','external_runner')),
			policy_version INTEGER NOT NULL CHECK (policy_version > 0),
			consent_version INTEGER NOT NULL CHECK (consent_version > 0),
			limits_json TEXT NOT NULL CHECK (length(CAST(limits_json AS BLOB)) BETWEEN 2 AND 4096),
			execution_json TEXT NOT NULL CHECK (length(CAST(execution_json AS BLOB)) BETWEEN 2 AND 4096),
			dedup_digest TEXT NOT NULL CHECK (length(dedup_digest) = 64),
			rerun_root TEXT NOT NULL DEFAULT '',
			rerun_generation INTEGER NOT NULL DEFAULT 0 CHECK (rerun_generation >= 0),
			status TEXT NOT NULL CHECK (status IN ('pending','claimed','started','passed','failed','error','cancelled','incomplete','unavailable','ambiguous','interrupted')),
			attempt_id TEXT NOT NULL DEFAULT '',
			lease_id TEXT NOT NULL DEFAULT '',
			lease_expires_at INTEGER,
			credential_id TEXT NOT NULL DEFAULT '',
			credential_generation INTEGER NOT NULL DEFAULT 0 CHECK (credential_generation >= 0),
			credential_role TEXT NOT NULL DEFAULT '' CHECK (credential_role IN ('','server','external_runner')),
			protection TEXT NOT NULL DEFAULT 'unknown' CHECK (protection IN ('unknown','host','container','runner_reported')),
			admitted_at INTEGER NOT NULL,
			claimed_at INTEGER,
			started_at INTEGER,
			finished_at INTEGER,
			lease_lost_at INTEGER,
			cancel_requested_at INTEGER,
			interrupted_at INTEGER,
			summary TEXT NOT NULL DEFAULT '' CHECK (length(CAST(summary AS BLOB)) <= 500),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,
		`INSERT INTO check_jobs(
			id,repository_id,task_id,trigger_kind,event_key,source_oid,base_oid,pull_request_number,trigger_ref,workflow_path,
			workflow_oid,workflow_digest,configuration_version,executor,policy_version,consent_version,limits_json,execution_json,
			dedup_digest,rerun_root,rerun_generation,status,attempt_id,lease_id,lease_expires_at,credential_id,credential_generation,credential_role,
			protection,admitted_at,claimed_at,started_at,finished_at,lease_lost_at,cancel_requested_at,interrupted_at,summary
		) SELECT
			id,repository_id,task_id,trigger_kind,event_key,source_oid,base_oid,pull_request_number,trigger_ref,workflow_path,
			workflow_oid,workflow_digest,configuration_version,executor,policy_version,consent_version,limits_json,'{"legacy":true}',
			dedup_digest,rerun_root,rerun_generation,status,attempt_id,lease_id,lease_expires_at,credential_id,credential_generation,'',
			protection,admitted_at,claimed_at,started_at,finished_at,lease_lost_at,cancel_requested_at,interrupted_at,summary
		FROM check_jobs_v9`,
		`DROP TABLE check_jobs_v9`,
		`CREATE UNIQUE INDEX check_jobs_dedup ON check_jobs(repository_id,dedup_digest)`,
		`CREATE INDEX check_jobs_queue ON check_jobs(repository_id,status,admitted_at,id)`,
		`CREATE INDEX check_jobs_attempt ON check_jobs(attempt_id) WHERE attempt_id != ''`,
		// Every schema 9 runner credential was external authority. Server-local
		// execution uses the policy epoch directly and never receives a bearer.
		`ALTER TABLE check_runner_credentials ADD COLUMN role TEXT NOT NULL DEFAULT 'external_runner' CHECK (role IN ('server','external_runner'))`,
		// Active container identity is machine-local cleanup authority and never
		// enters backup manifests. The daemon ID prevents cleanup against a
		// different mutable local Docker context after restart.
		`CREATE TABLE check_job_runtime_ownership (
			job_id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			container_name TEXT NOT NULL CHECK (length(CAST(container_name AS BLOB)) BETWEEN 1 AND 200),
			container_id TEXT NOT NULL CHECK (length(container_id)=0 OR length(container_id) BETWEEN 12 AND 64),
			daemon_id TEXT NOT NULL CHECK (length(CAST(daemon_id AS BLOB)) BETWEEN 1 AND 200),
			created_at INTEGER NOT NULL,
			FOREIGN KEY (job_id) REFERENCES check_jobs(id) ON DELETE CASCADE,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
	}},
	{version: 11, statements: []string{
		// One inbound import source per repository. No foreign key is declared
		// because a configured source can precede the repository row: the
		// destination object format is only known after the first advertisement.
		// allow_private_network is machine-local transport consent, so a restore
		// clears it while source identity and mode stay portable.
		`CREATE TABLE import_sources (
			repository_id TEXT PRIMARY KEY,
			url TEXT NOT NULL CHECK (length(CAST(url AS BLOB)) BETWEEN 1 AND 8192),
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			authority_revision INTEGER NOT NULL CHECK (authority_revision > 0),
			credential_generation TEXT NOT NULL DEFAULT '' CHECK (length(credential_generation) IN (0,32)),
			mode TEXT NOT NULL CHECK (mode IN ('standalone','coexistence')),
			git_only_consent INTEGER NOT NULL DEFAULT 0 CHECK (git_only_consent IN (0,1)),
			allow_private_network INTEGER NOT NULL DEFAULT 0 CHECK (allow_private_network IN (0,1)),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		// Append-only refresh history. Counts and inspection facts describe what
		// actually happened, including divergence and truncation.
		`CREATE TABLE import_runs (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			authority_revision INTEGER NOT NULL CHECK (authority_revision > 0),
			kind TEXT NOT NULL CHECK (kind IN ('initial','refresh','scheduled')),
			status TEXT NOT NULL CHECK (status IN ('preparing','fetching','indexing','inspecting','publishing','complete','failed','cancelled','superseded','interrupted','unresolved')),
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL DEFAULT 0,
			cancel_requested_at INTEGER,
			object_format TEXT NOT NULL DEFAULT '' CHECK (object_format IN ('','sha1','sha256')),
			refs_seen INTEGER NOT NULL DEFAULT 0 CHECK (refs_seen >= 0),
			refs_created INTEGER NOT NULL DEFAULT 0 CHECK (refs_created >= 0),
			refs_updated INTEGER NOT NULL DEFAULT 0 CHECK (refs_updated >= 0),
			refs_unchanged INTEGER NOT NULL DEFAULT 0 CHECK (refs_unchanged >= 0),
			refs_divergent INTEGER NOT NULL DEFAULT 0 CHECK (refs_divergent >= 0),
			refs_deleted_upstream INTEGER NOT NULL DEFAULT 0 CHECK (refs_deleted_upstream >= 0),
			refs_skipped INTEGER NOT NULL DEFAULT 0 CHECK (refs_skipped >= 0),
			pack_bytes INTEGER NOT NULL DEFAULT 0 CHECK (pack_bytes >= 0),
			http_body_bytes INTEGER NOT NULL DEFAULT 0 CHECK (http_body_bytes >= 0),
			head_advertised INTEGER NOT NULL DEFAULT 0 CHECK (head_advertised IN (0,1)),
			head_symref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_symref AS BLOB)) <= 500),
			error_class TEXT NOT NULL DEFAULT '' CHECK (length(CAST(error_class AS BLOB)) <= 100),
			message TEXT NOT NULL DEFAULT '' CHECK (length(CAST(message AS BLOB)) <= 500),
			lfs_detected INTEGER NOT NULL DEFAULT 0 CHECK (lfs_detected >= 0),
			lfs_inspection_complete INTEGER NOT NULL DEFAULT 0 CHECK (lfs_inspection_complete IN (0,1)),
			lfs_scanned_blobs INTEGER NOT NULL DEFAULT 0 CHECK (lfs_scanned_blobs >= 0),
			lfs_scanned_bytes INTEGER NOT NULL DEFAULT 0 CHECK (lfs_scanned_bytes >= 0),
			staging_name TEXT NOT NULL DEFAULT '' CHECK (length(CAST(staging_name AS BLOB)) <= 100),
			cleanup_error TEXT NOT NULL DEFAULT '' CHECK (length(CAST(cleanup_error AS BLOB)) <= 500),
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX import_runs_repository ON import_runs(repository_id,started_at,id)`,
		// Last source fact per ref and generation. The observation baseline is
		// bound to the source generation so a new URL cannot inherit authority
		// over refs published by the previous URL.
		`CREATE TABLE import_ref_observations (
			repository_id TEXT NOT NULL,
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			ref_name TEXT NOT NULL CHECK (length(CAST(ref_name AS BLOB)) BETWEEN 1 AND 500),
			oid TEXT NOT NULL DEFAULT '' CHECK (oid = '' OR length(oid) IN (40,64)),
			symref_target TEXT NOT NULL DEFAULT '' CHECK (length(CAST(symref_target AS BLOB)) <= 500),
			observed_at INTEGER NOT NULL,
			run_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(run_id AS BLOB)) <= 64),
			PRIMARY KEY (repository_id, source_generation, ref_name)
		)`,
		`CREATE INDEX import_ref_observations_recent ON import_ref_observations(repository_id,observed_at,ref_name)`,
		// Durable publication intent and receipt. expected/desired/retained are
		// JSON objects of ref to object ID, so reconciliation can verify actual
		// refs instead of trusting a receipt.
		`CREATE TABLE import_publication_intents (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			authority_revision INTEGER NOT NULL CHECK (authority_revision > 0),
			status TEXT NOT NULL CHECK (status IN ('planning','applied','complete','not_applied','abandoned','unresolved','invalidated')),
			expected_json TEXT NOT NULL CHECK (length(CAST(expected_json AS BLOB)) BETWEEN 2 AND 8388608),
			desired_json TEXT NOT NULL CHECK (length(CAST(desired_json AS BLOB)) BETWEEN 2 AND 8388608),
			observed_json TEXT NOT NULL CHECK (length(CAST(observed_json AS BLOB)) BETWEEN 2 AND 8388608),
			retained_json TEXT NOT NULL CHECK (length(CAST(retained_json AS BLOB)) BETWEEN 2 AND 8388608),
			head_symref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_symref AS BLOB)) <= 500),
			head_detach TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_detach AS BLOB)) <= 64),
			receipt_json TEXT NOT NULL DEFAULT '' CHECK (length(CAST(receipt_json AS BLOB)) <= 8388608),
			receipt_digest TEXT NOT NULL DEFAULT '' CHECK (receipt_digest = '' OR length(receipt_digest) = 64),
			reason TEXT NOT NULL DEFAULT '' CHECK (length(CAST(reason AS BLOB)) <= 500),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX import_publication_intents_repository ON import_publication_intents(repository_id,created_at,id)`,
		// Task-owned staging ownership. The on-disk marker must agree with this
		// row before cleanup deletes anything, so a name alone is not proof.
		`CREATE TABLE import_stagings (
			name TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			token TEXT NOT NULL CHECK (length(token) BETWEEN 16 AND 64),
			state TEXT NOT NULL CHECK (state IN ('active','released','cleanup_failed','unknown')),
			issue TEXT NOT NULL DEFAULT '' CHECK (length(CAST(issue AS BLOB)) <= 500),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX import_stagings_run ON import_stagings(run_id)`,
		// Machine-local opt-in schedule. A restore drops these rows.
		`CREATE TABLE import_schedules (
			repository_id TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
			interval_seconds INTEGER NOT NULL CHECK (interval_seconds BETWEEN 60 AND 604800),
			last_started_at INTEGER,
			last_finished_at INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX import_schedules_due ON import_schedules(enabled,last_started_at,repository_id)`,
	}},
	{version: 12, statements: []string{
		`ALTER TABLE import_publication_intents ADD COLUMN head_owned INTEGER NOT NULL DEFAULT 0 CHECK (head_owned IN (0,1))`,
	}},
	{version: 13, statements: []string{
		`CREATE TABLE import_initial_destinations (
			name TEXT PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 80),
			repository_id TEXT NOT NULL CHECK (length(repository_id) <= 100),
			run_id TEXT NOT NULL CHECK (length(run_id) <= 64),
			root_id TEXT NOT NULL CHECK (length(root_id) = 32),
			token TEXT NOT NULL CHECK (length(token) BETWEEN 16 AND 64),
			display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 100),
			description TEXT NOT NULL DEFAULT '' CHECK (length(CAST(description AS BLOB)) <= 500),
			state TEXT NOT NULL CHECK (state IN ('preparing','ready','published','released','cleanup_failed','unknown')),
			issue TEXT NOT NULL DEFAULT '' CHECK (length(CAST(issue AS BLOB)) <= 500),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX import_initial_destinations_run ON import_initial_destinations(run_id)`,
	}},
	// SQLite cannot change a CHECK constraint in place, so the intent table is
	// rebuilt with every row and column kept. owner_resolved is terminal: the
	// owner accepted the destination as found after an unresolved outcome.
	{version: 14, released: true, statements: []string{
		`CREATE TABLE import_publication_intents_v14 (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			authority_revision INTEGER NOT NULL CHECK (authority_revision > 0),
			status TEXT NOT NULL CHECK (status IN ('planning','applied','complete','not_applied','abandoned','unresolved','invalidated','owner_resolved')),
			expected_json TEXT NOT NULL CHECK (length(CAST(expected_json AS BLOB)) BETWEEN 2 AND 8388608),
			desired_json TEXT NOT NULL CHECK (length(CAST(desired_json AS BLOB)) BETWEEN 2 AND 8388608),
			observed_json TEXT NOT NULL CHECK (length(CAST(observed_json AS BLOB)) BETWEEN 2 AND 8388608),
			retained_json TEXT NOT NULL CHECK (length(CAST(retained_json AS BLOB)) BETWEEN 2 AND 8388608),
			head_symref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_symref AS BLOB)) <= 500),
			head_detach TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_detach AS BLOB)) <= 64),
			receipt_json TEXT NOT NULL DEFAULT '' CHECK (length(CAST(receipt_json AS BLOB)) <= 8388608),
			receipt_digest TEXT NOT NULL DEFAULT '' CHECK (receipt_digest = '' OR length(receipt_digest) = 64),
			reason TEXT NOT NULL DEFAULT '' CHECK (length(CAST(reason AS BLOB)) <= 500),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			head_owned INTEGER NOT NULL DEFAULT 0 CHECK (head_owned IN (0,1))
		)`,
		// rowid order is the pending-intent page order, so it is copied too.
		`INSERT INTO import_publication_intents_v14(rowid,id,repository_id,run_id,source_generation,authority_revision,status,expected_json,desired_json,observed_json,retained_json,
			head_symref,head_detach,receipt_json,receipt_digest,reason,created_at,updated_at,head_owned)
			SELECT rowid,id,repository_id,run_id,source_generation,authority_revision,status,expected_json,desired_json,observed_json,retained_json,
			head_symref,head_detach,receipt_json,receipt_digest,reason,created_at,updated_at,head_owned FROM import_publication_intents`,
		`DROP TABLE import_publication_intents`,
		`ALTER TABLE import_publication_intents_v14 RENAME TO import_publication_intents`,
		`CREATE INDEX import_publication_intents_repository ON import_publication_intents(repository_id,created_at,id)`,
	}},
	// A pull request can be closed without merging. The table is rebuilt with
	// every row and column kept; its revisions, reviews and merge intents
	// refer to it by name and keep their rows (see migrate).
	{version: 15, released: true, statements: []string{
		`CREATE TABLE pull_requests_v15 (
			repository_id TEXT NOT NULL,
			number INTEGER NOT NULL CHECK (number > 0),
			title TEXT NOT NULL,
			source_branch TEXT NOT NULL,
			target_branch TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('creating','open','merged','closed')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			merge_source_oid TEXT NOT NULL DEFAULT '',
			merge_target_oid TEXT NOT NULL DEFAULT '',
			merge_oid TEXT NOT NULL DEFAULT '',
			merge_receipt_ref TEXT NOT NULL DEFAULT '',
			merged_at INTEGER,
			PRIMARY KEY (repository_id, number),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		)`,
		`INSERT INTO pull_requests_v15(repository_id,number,title,source_branch,target_branch,status,created_at,updated_at,
			merge_source_oid,merge_target_oid,merge_oid,merge_receipt_ref,merged_at)
			SELECT repository_id,number,title,source_branch,target_branch,status,created_at,updated_at,
			merge_source_oid,merge_target_oid,merge_oid,merge_receipt_ref,merged_at FROM pull_requests`,
		`DROP TABLE pull_requests`,
		`ALTER TABLE pull_requests_v15 RENAME TO pull_requests`,
	}},
}

// Exec runs one statement against the state database. It is used for targeted
// repair and for tests that need to break one read on purpose.
func (s *Store) Exec(ctx context.Context, statement string, args ...any) error {
	_, err := s.db.ExecContext(ctx, statement, args...)
	return err
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Dir() string  { return s.dir }

// TableRowCount reports the row count of an existing table. It covers narrow
// checks that have no dedicated accessor, such as confirming an unrelated
// feature left no state behind. A missing table is reported as an error so
// callers can skip tables from later migrations.
func (s *Store) TableRowCount(ctx context.Context, table string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+table+`"`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func RequireExisting(directory string) error {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(absolute, databaseName))
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("OwnGit state does not exist")
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("OwnGit state database must be a regular file")
	}
	return nil
}

func (s *Store) Settings(ctx context.Context) (Settings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM metadata`)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	values := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Settings{}, err
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return Settings{}, err
	}
	accessVersion, err := strconv.ParseInt(values["access_session_version"], 10, 64)
	if err != nil {
		return Settings{}, fmt.Errorf("invalid access session version: %w", err)
	}
	adminVersion, err := strconv.ParseInt(values["admin_session_version"], 10, 64)
	if err != nil {
		return Settings{}, fmt.Errorf("invalid admin session version: %w", err)
	}
	retentionDays := DefaultCheckLogRetentionDays
	if raw := values["check_log_retention_days"]; raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 3650 {
			return Settings{}, fmt.Errorf("invalid check log retention days %q", raw)
		}
		retentionDays = parsed
	}
	updateCheck := true
	switch raw := values[updateCheckKey]; raw {
	case "", "on":
	case "off":
		updateCheck = false
	default:
		return Settings{}, fmt.Errorf("invalid update check setting %q", raw)
	}
	return Settings{
		Initialized:           values["initialized"] == "true",
		RepositoryRoot:        values["repository_root"],
		AccessMode:            values["access_mode"],
		AccessSessionVersion:  accessVersion,
		AdminSessionVersion:   adminVersion,
		InsecureHTTPAccepted:  values["insecure_http_accepted"] == "true",
		CheckLogRetentionDays: retentionDays,
		UpdateCheck:           updateCheck,
	}, nil
}

// updateCheckKey is an optional metadata key, so the setting needs no schema
// change and older OwnGit builds ignore it.
const updateCheckKey = "update_check"

// SetUpdateCheck saves whether the new-release check may run.
func (s *Store) SetUpdateCheck(ctx context.Context, enabled bool) error {
	value := "off"
	if enabled {
		value = "on"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, updateCheckKey, value)
	return err
}

// Network settings are optional metadata keys like updateCheckKey, so they
// need no schema change and older OwnGit builds ignore them. They are
// machine-local: backups do not carry them. The allowed Host names are the
// trusted_hosts table.
const (
	networkListenKey  = "network_listen"
	networkBaseURLKey = "network_base_url"
	// networkTrustedProxiesKey holds the trusted reverse proxies as a JSON
	// list of canonical addresses and ranges.
	networkTrustedProxiesKey = "network_trusted_proxies"
	// networkRunningKey holds the RunningNetwork record of the server that
	// currently serves this state directory.
	networkRunningKey = "network_running"
)

// NetworkSettings are the saved network settings that serve applies at its
// next start when no flag overrides them. An empty value is not saved, so
// the default applies. The store does not validate them; callers do.
type NetworkSettings struct {
	Listen  string
	BaseURL string
}

// NetworkSettings returns the saved network settings.
func (s *Store) NetworkSettings(ctx context.Context) (NetworkSettings, error) {
	values, err := s.metadataValues(ctx, networkListenKey, networkBaseURLKey)
	if err != nil {
		return NetworkSettings{}, err
	}
	return NetworkSettings{Listen: values[networkListenKey], BaseURL: values[networkBaseURLKey]}, nil
}

// TrustedProxies returns the saved trusted reverse proxies, sorted.
func (s *Store) TrustedProxies(ctx context.Context) ([]string, error) {
	return trustedProxies(ctx, s.db)
}

func trustedProxies(ctx context.Context, db queryRower) ([]string, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, networkTrustedProxiesKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	proxies := []string{}
	if err := json.Unmarshal([]byte(raw), &proxies); err != nil {
		return nil, fmt.Errorf("invalid saved trusted proxies: %w", err)
	}
	return proxies, nil
}

// NetworkUpdate replaces the saved network settings and changes the allowed
// Host names and trusted proxies in one transaction. Host names and proxies
// are stored as given; callers pass normalized names and canonical proxies.
type NetworkUpdate struct {
	Settings      NetworkSettings
	AddHosts      []string
	RemoveHosts   []string
	AddProxies    []string
	RemoveProxies []string
	// Tailscale, when set, replaces the Tailscale sharing record in the same
	// transaction; ClearTailscale removes it.
	Tailscale      *TailscaleServe
	ClearTailscale bool
}

// tailscaleServeKey holds the TailscaleServe record. Like the network
// settings it is optional metadata and machine-local.
const tailscaleServeKey = "tailscale_serve"

// TailscaleServe records what OwnGit changed to share this computer's OwnGit
// on the tailnet: the Tailscale Serve endpoint it wrote and the OwnGit
// settings it changed. Turning sharing off removes the endpoint only when
// Tailscale still has exactly this endpoint, and takes back only these
// settings.
type TailscaleServe struct {
	// Name is the computer's MagicDNS name, such as box.tail1234.ts.net.
	Name string `json:"name"`
	// HTTPSPort is the port Tailscale answers HTTPS on, and Target the local
	// address it proxies to, such as http://127.0.0.1:7654.
	HTTPSPort int    `json:"https_port"`
	Target    string `json:"target"`
	// Created is false when Tailscale already had exactly this endpoint, so
	// turning sharing off leaves it in place.
	Created bool `json:"created"`
	// Confirmed is true once the endpoint was read back from Tailscale and
	// the settings below were saved.
	Confirmed bool  `json:"confirmed"`
	CreatedAt int64 `json:"created_at"`
	// BaseURL is the base URL OwnGit saved and PreviousBaseURL the one saved
	// before; turning off restores it while BaseURL is still saved.
	BaseURL         string `json:"base_url"`
	PreviousBaseURL string `json:"previous_base_url,omitempty"`
	// AddedProxy and AddedHost are the trusted proxy and allowed Host name
	// OwnGit added, or empty when they were already saved.
	AddedProxy string `json:"added_proxy,omitempty"`
	AddedHost  string `json:"added_host,omitempty"`
}

// TailscaleServe returns the Tailscale sharing record.
func (s *Store) TailscaleServe(ctx context.Context) (TailscaleServe, bool, error) {
	values, err := s.metadataValues(ctx, tailscaleServeKey)
	if err != nil {
		return TailscaleServe{}, false, err
	}
	raw, found := values[tailscaleServeKey]
	if !found {
		return TailscaleServe{}, false, nil
	}
	var record TailscaleServe
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return TailscaleServe{}, false, fmt.Errorf("invalid Tailscale sharing record: %w", err)
	}
	return record, true, nil
}

// SaveTailscaleServe replaces the Tailscale sharing record.
func (s *Store) SaveTailscaleServe(ctx context.Context, record TailscaleServe) error {
	return putTailscaleServe(ctx, s.db, record)
}

// ClearTailscaleServe removes the Tailscale sharing record.
func (s *Store) ClearTailscaleServe(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM metadata WHERE key=?`, tailscaleServeKey)
	return err
}

func putTailscaleServe(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, record TailscaleServe) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, tailscaleServeKey, string(encoded))
	return err
}

// UpdateNetwork applies update atomically.
func (s *Store) UpdateNetwork(ctx context.Context, update NetworkUpdate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{networkListenKey: update.Settings.Listen, networkBaseURLKey: update.Settings.BaseURL} {
		if value == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM metadata WHERE key=?`, key)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
		}
		if err != nil {
			return err
		}
	}
	for _, host := range update.RemoveHosts {
		if _, err := tx.ExecContext(ctx, `DELETE FROM trusted_hosts WHERE host=?`, host); err != nil {
			return err
		}
	}
	for _, host := range update.AddHosts {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO trusted_hosts(host,created_at) VALUES(?,?)`, host, time.Now().Unix()); err != nil {
			return err
		}
	}
	if len(update.AddProxies) > 0 || len(update.RemoveProxies) > 0 {
		proxies, err := trustedProxies(ctx, tx)
		if err != nil {
			return err
		}
		proxies = slices.DeleteFunc(proxies, func(proxy string) bool { return slices.Contains(update.RemoveProxies, proxy) })
		for _, proxy := range update.AddProxies {
			if !slices.Contains(proxies, proxy) {
				proxies = append(proxies, proxy)
			}
		}
		slices.Sort(proxies)
		if len(proxies) == 0 {
			_, err = tx.ExecContext(ctx, `DELETE FROM metadata WHERE key=?`, networkTrustedProxiesKey)
		} else {
			encoded, _ := json.Marshal(proxies)
			_, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, networkTrustedProxiesKey, string(encoded))
		}
		if err != nil {
			return err
		}
	}
	switch {
	case update.ClearTailscale:
		_, err = tx.ExecContext(ctx, `DELETE FROM metadata WHERE key=?`, tailscaleServeKey)
	case update.Tailscale != nil:
		err = putTailscaleServe(ctx, tx, *update.Tailscale)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RunningNetwork is what a serving process actually uses. serve publishes it
// after its listener is bound and clears it when it stops. A process that
// ends without stopping, such as after a crash, leaves the record behind, so
// the record is trustworthy only while the process that published it holds
// the running-record lock in the state directory; read it through
// ObserveRunningNetwork or OwnRunningNetwork, never directly. Holding the
// offline lock alone proves nothing: an older OwnGit or an offline backup
// holds it too. Saved settings (NetworkSettings and the allowed Hosts)
// differ from the running values until the next start.
type RunningNetwork struct {
	PID       int   `json:"pid"`
	StartedAt int64 `json:"started_at"`
	// Listen is the requested listen address and Address the bound one; they
	// differ when the port is 0.
	Listen  string `json:"listen"`
	Address string `json:"address"`
	// BaseURL is the configured base URL, or "" when the server derives the
	// address from each request. Origin is the owner-facing origin used for
	// setup links.
	BaseURL string `json:"base_url"`
	Origin  string `json:"origin"`
	// ListenSource and BaseURLSource name where each value came from:
	// "flag", "saved", or "default".
	ListenSource  string `json:"listen_source"`
	BaseURLSource string `json:"base_url_source"`
	// SavedHosts are the allowed Host names loaded from storage at start;
	// AcceptedHosts are every Host name the running Host check accepts,
	// loopback names included.
	SavedHosts    []string `json:"saved_hosts"`
	AcceptedHosts []string `json:"accepted_hosts"`
	// TrustedProxies are the reverse proxies whose forwarded headers the
	// server believes, and TrustedProxiesSource where the list came from.
	TrustedProxies       []string `json:"trusted_proxies"`
	TrustedProxiesSource string   `json:"trusted_proxies_source"`
}

// PublishRunningNetwork records what this serving process uses.
func (s *Store) PublishRunningNetwork(ctx context.Context, running RunningNetwork) error {
	encoded, err := json.Marshal(running)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, networkRunningKey, string(encoded))
	return err
}

// ClearRunningNetwork removes the running record when a server stops.
func (s *Store) ClearRunningNetwork(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM metadata WHERE key=?`, networkRunningKey)
	return err
}

// RunningNetwork returns the last published running record. It does not
// check whether that server still runs; ObserveRunningNetwork and
// OwnRunningNetwork do.
func (s *Store) RunningNetwork(ctx context.Context) (RunningNetwork, bool, error) {
	values, err := s.metadataValues(ctx, networkRunningKey)
	if err != nil {
		return RunningNetwork{}, false, err
	}
	raw, found := values[networkRunningKey]
	if !found {
		return RunningNetwork{}, false, nil
	}
	var running RunningNetwork
	if err := json.Unmarshal([]byte(raw), &running); err != nil {
		return RunningNetwork{}, false, fmt.Errorf("invalid running network record: %w", err)
	}
	return running, true, nil
}

// metadataValues reads the present values of keys.
func (s *Store) metadataValues(ctx context.Context, keys ...string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		var value string
		err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, key).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, nil
}

// CompleteSetup saves the first-run answers. trustedHosts, when given, are
// normalized Host names saved as allowed Hosts in the same transaction.
func (s *Store) CompleteSetup(ctx context.Context, repositoryRoot, accessMode, accessHash, adminHash string, insecureAccepted bool, trustedHosts ...string) error {
	if accessMode != "open" && accessMode != "password" {
		return errors.New("invalid access mode")
	}
	if accessMode == "password" && accessHash == "" {
		return errors.New("access password hash is required")
	}
	if adminHash == "" {
		return errors.New("administrator password hash is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var initialized string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='initialized'`).Scan(&initialized); err != nil {
		return err
	}
	if initialized == "true" {
		return ErrSetupComplete
	}
	updates := map[string]string{
		"repository_root":        repositoryRoot,
		"access_mode":            accessMode,
		"initialized":            "true",
		"insecure_http_accepted": strconv.FormatBool(insecureAccepted),
	}
	for key, value := range updates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO passwords(kind,encoded) VALUES('admin',?) ON CONFLICT(kind) DO UPDATE SET encoded=excluded.encoded`, adminHash); err != nil {
		return err
	}
	if accessMode == "password" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO passwords(kind,encoded) VALUES('access',?) ON CONFLICT(kind) DO UPDATE SET encoded=excluded.encoded`, accessHash); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM passwords WHERE kind='access'`); err != nil {
		return err
	}
	for _, host := range trustedHosts {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO trusted_hosts(host,created_at) VALUES(?,?)`, host, time.Now().Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bootstrap`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE kind='setup'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PasswordHash(ctx context.Context, kind string) (string, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT encoded FROM passwords WHERE kind=?`, kind).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return encoded, err
}

func (s *Store) SetAccessPassword(ctx context.Context, encoded string) error {
	if encoded == "" {
		return errors.New("password hash is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO passwords(kind,encoded) VALUES('access',?) ON CONFLICT(kind) DO UPDATE SET encoded=excluded.encoded`, encoded); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value='password' WHERE key='access_mode'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='access_session_version'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE kind='general'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DisableAccessPassword(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM passwords WHERE kind='access'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value='open' WHERE key='access_mode'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='access_session_version'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE kind='general'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcknowledgeInsecureHTTP(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE metadata SET value='true' WHERE key='insecure_http_accepted'`)
	return err
}

func (s *Store) SetAdminPassword(ctx context.Context, encoded string) error {
	if encoded == "" {
		return errors.New("password hash is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO passwords(kind,encoded) VALUES('admin',?) ON CONFLICT(kind) DO UPDATE SET encoded=excluded.encoded`, encoded); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='admin_session_version'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE kind='admin'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PutBootstrap(ctx context.Context, token string, expires time.Time) error {
	hash := sha256.Sum256([]byte(token))
	return s.putBootstrapHash(ctx, hash[:], expires.Unix())
}

func (s *Store) putBootstrapHash(ctx context.Context, hash []byte, expiresAt int64) error {
	var singleton int
	err := s.db.QueryRowContext(ctx, `INSERT INTO bootstrap(singleton,token_hash,expires_at)
		SELECT 1,?,? WHERE (SELECT value FROM metadata WHERE key='initialized')='false'
		ON CONFLICT(singleton) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at
		WHERE (SELECT value FROM metadata WHERE key='initialized')='false'
		RETURNING singleton`, hash, expiresAt).Scan(&singleton)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSetupComplete
	}
	return err
}

func (s *Store) BootstrapSnapshot(ctx context.Context) (BootstrapSnapshot, error) {
	var snapshot BootstrapSnapshot
	err := s.db.QueryRowContext(ctx, `SELECT token_hash,expires_at FROM bootstrap WHERE singleton=1`).Scan(&snapshot.TokenHash, &snapshot.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return BootstrapSnapshot{}, err
	}
	snapshot.Present = true
	return snapshot, nil
}

func (s *Store) RestoreBootstrap(ctx context.Context, snapshot BootstrapSnapshot) error {
	if !snapshot.Present {
		_, err := s.db.ExecContext(ctx, `DELETE FROM bootstrap WHERE singleton=1`)
		return err
	}
	if len(snapshot.TokenHash) != sha256.Size {
		return errors.New("invalid bootstrap snapshot")
	}
	return s.putBootstrapHash(ctx, snapshot.TokenHash, snapshot.ExpiresAt)
}

func BootstrapMatches(snapshot BootstrapSnapshot, token string) bool {
	if !snapshot.Present || len(snapshot.TokenHash) != sha256.Size {
		return false
	}
	hash := sha256.Sum256([]byte(token))
	return equalHash(snapshot.TokenHash, hash[:])
}

// RedeemBootstrap consumes a valid token and creates the only setup session atomically.
func (s *Store) RedeemBootstrap(ctx context.Context, token, sessionToken, csrf string, now, expires time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var expected []byte
	var expiresAt int64
	if err := tx.QueryRowContext(ctx, `SELECT token_hash,expires_at FROM bootstrap WHERE singleton=1`).Scan(&expected, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	actual := sha256.Sum256([]byte(token))
	if expiresAt < now.Unix() || !equalHash(expected, actual[:]) {
		return false, nil
	}
	if err := replaceSetupSession(ctx, tx, sessionToken, csrf, expires); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// StartApprovedSetupSession creates the only setup session for a browser the
// owner approved from the terminal running OwnGit. It grants exactly what
// redeeming the setup capability grants: any unredeemed capability and any
// other setup session end. It fails with ErrSetupComplete after setup.
func (s *Store) StartApprovedSetupSession(ctx context.Context, sessionToken, csrf string, expires time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var initialized string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='initialized'`).Scan(&initialized); err != nil {
		return err
	}
	if initialized == "true" {
		return ErrSetupComplete
	}
	if err := replaceSetupSession(ctx, tx, sessionToken, csrf, expires); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceSetupSession consumes the setup capability and makes sessionToken
// the only setup session.
func replaceSetupSession(ctx context.Context, tx *sql.Tx, sessionToken, csrf string, expires time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM bootstrap WHERE singleton=1`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE kind='setup'`); err != nil {
		return err
	}
	sessionHash := sha256.Sum256([]byte(sessionToken))
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions(token_hash,kind,csrf,version,expires_at) VALUES(?,'setup',?,1,?)`, sessionHash[:], csrf, expires.Unix())
	return err
}

// EndSetupSessions ends every browser setup session, for example when the
// owner switches to setup in the terminal.
func (s *Store) EndSetupSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE kind='setup'`)
	return err
}

func (s *Store) CreateSession(ctx context.Context, token, kind, csrf string, version int64, expires time.Time) error {
	if kind != "general" && kind != "admin" {
		return errors.New("invalid session kind")
	}
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,kind,csrf,version,expires_at) VALUES(?,?,?,?,?)`, hash[:], kind, csrf, version, expires.Unix())
	return err
}

func (s *Store) Session(ctx context.Context, token, kind string, now time.Time) (Session, bool, error) {
	hash := sha256.Sum256([]byte(token))
	var session Session
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT kind,csrf,version,expires_at FROM sessions WHERE token_hash=? AND kind=?`, hash[:], kind).Scan(&session.Kind, &session.CSRF, &session.Version, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	session.Expires = time.Unix(expiresAt, 0)
	if !session.Expires.After(now) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, hash[:])
		return Session{}, false, nil
	}
	return session, true, nil
}

func (s *Store) DeleteSession(ctx context.Context, token, kind string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=? AND kind=?`, hash[:], kind)
	return err
}

// AttemptBlocked reports whether authentication of kind from address is
// refused at now because of earlier failures.
func (s *Store) AttemptBlocked(ctx context.Context, kind, address string, now time.Time) (bool, error) {
	var blocked int64
	err := s.db.QueryRowContext(ctx, `SELECT blocked_until FROM login_attempts WHERE kind=? AND address=?`, kind, address).Scan(&blocked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return blocked > now.Unix(), nil
}

// RecordFailedAttempt counts one wrong password from address. The failure
// that reaches maxFailures within window blocks the address for block.
func (s *Store) RecordFailedAttempt(ctx context.Context, kind, address string, now time.Time, maxFailures int, window, block time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var started, blocked int64
	var failures int
	err = tx.QueryRowContext(ctx, `SELECT window_started_at,attempts,blocked_until FROM login_attempts WHERE kind=? AND address=?`, kind, address).Scan(&started, &failures, &blocked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) || now.Sub(time.Unix(started, 0)) >= window {
		started, failures, blocked = now.Unix(), 0, 0
	}
	failures++
	if failures >= maxFailures {
		blocked = now.Add(block).Unix()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO login_attempts(kind,address,window_started_at,attempts,blocked_until) VALUES(?,?,?,?,?)
		ON CONFLICT(kind,address) DO UPDATE SET window_started_at=excluded.window_started_at,attempts=excluded.attempts,blocked_until=excluded.blocked_until`, kind, address, started, failures, blocked); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClearAttempts(ctx context.Context, kind, address string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE kind=? AND address=?`, kind, address)
	return err
}

// AddRepository records a repository. It refuses an ID whose earlier deletion
// is unfinished, so no path can claim a name before that deletion completes.
func (s *Store) AddRepository(ctx context.Context, repository Repository) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO repositories(id,name,description,created_at) SELECT ?,?,?,?
		WHERE NOT EXISTS(SELECT 1 FROM metadata WHERE key=?)`,
		repository.ID, repository.Name, repository.Description, repository.CreatedAt.Unix(), repositoryDeletionKey(repository.ID))
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrRepositoryDeletionPending
	}
	return nil
}

func (s *Store) Repository(ctx context.Context, id string) (Repository, bool, error) {
	var repository Repository
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id,name,description,created_at FROM repositories WHERE id=?`, id).Scan(&repository.ID, &repository.Name, &repository.Description, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Repository{}, false, nil
	}
	if err != nil {
		return Repository{}, false, err
	}
	repository.CreatedAt = time.Unix(created, 0)
	return repository, true, nil
}

func (s *Store) Repositories(ctx context.Context) ([]Repository, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,created_at FROM repositories ORDER BY lower(name),name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repositories []Repository
	for rows.Next() {
		var repository Repository
		var created int64
		if err := rows.Scan(&repository.ID, &repository.Name, &repository.Description, &created); err != nil {
			return nil, err
		}
		repository.CreatedAt = time.Unix(created, 0)
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func (s *Store) AddTrustedHost(ctx context.Context, host string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO trusted_hosts(host,created_at) VALUES(?,?)`, host, time.Now().Unix())
	return err
}

func (s *Store) TrustedHosts(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host FROM trusted_hosts ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

func equalHash(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}
