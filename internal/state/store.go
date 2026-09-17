package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

const databaseName = "owngit.sqlite"

var ErrSetupComplete = errors.New("setup is already complete")

type Store struct {
	db  *sql.DB
	dir string
}

type Settings struct {
	Initialized          bool
	RepositoryRoot       string
	AccessMode           string
	AccessSessionVersion int64
	AdminSessionVersion  int64
	InsecureHTTPAccepted bool
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
}

type BootstrapSnapshot struct {
	Present   bool   `json:"present"`
	TokenHash []byte `json:"token_hash,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

func Open(ctx context.Context, dir string) (*Store, error) {
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
	if err := ProtectPrivatePath(absolute, true); err != nil {
		return nil, fmt.Errorf("protect state directory: %w", err)
	}
	if err := ensureLocalStateFilesystem(absolute); err != nil {
		return nil, fmt.Errorf("validate state directory: %w", err)
	}
	path := filepath.Join(absolute, databaseName)
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, dir: absolute}
	if err := store.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	for _, protectedPath := range []string{path, path + "-wal", path + "-shm"} {
		if err := ProtectPrivatePath(protectedPath, false); err != nil && !os.IsNotExist(err) {
			db.Close()
			return nil, fmt.Errorf("protect state database file: %w", err)
		}
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
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
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize state database: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Dir() string  { return s.dir }

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
	return Settings{
		Initialized:          values["initialized"] == "true",
		RepositoryRoot:       values["repository_root"],
		AccessMode:           values["access_mode"],
		AccessSessionVersion: accessVersion,
		AdminSessionVersion:  adminVersion,
		InsecureHTTPAccepted: values["insecure_http_accepted"] == "true",
	}, nil
}

func (s *Store) CompleteSetup(ctx context.Context, repositoryRoot, accessMode, accessHash, adminHash string, insecureAccepted bool) error {
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM bootstrap WHERE singleton=1`); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE kind='setup'`); err != nil {
		return false, err
	}
	sessionHash := sha256.Sum256([]byte(sessionToken))
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(token_hash,kind,csrf,version,expires_at) VALUES(?,'setup',?,1,?)`, sessionHash[:], csrf, expires.Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
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

func (s *Store) CheckAttempt(ctx context.Context, kind, address string, now time.Time, maxAttempts int, window, block time.Duration) (bool, time.Duration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	var started, blocked int64
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT window_started_at,attempts,blocked_until FROM login_attempts WHERE kind=? AND address=?`, kind, address).Scan(&started, &attempts, &blocked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, 0, err
	}
	if blocked > now.Unix() {
		return false, time.Unix(blocked, 0).Sub(now), nil
	}
	if errors.Is(err, sql.ErrNoRows) || now.Sub(time.Unix(started, 0)) >= window {
		started, attempts, blocked = now.Unix(), 0, 0
	}
	attempts++
	if attempts >= maxAttempts {
		blocked = now.Add(block).Unix()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO login_attempts(kind,address,window_started_at,attempts,blocked_until) VALUES(?,?,?,?,?)
		ON CONFLICT(kind,address) DO UPDATE SET window_started_at=excluded.window_started_at,attempts=excluded.attempts,blocked_until=excluded.blocked_until`, kind, address, started, attempts, blocked); err != nil {
		return false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	if blocked > now.Unix() {
		return false, time.Unix(blocked, 0).Sub(now), nil
	}
	return true, 0, nil
}

func (s *Store) ClearAttempts(ctx context.Context, kind, address string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE kind=? AND address=?`, kind, address)
	return err
}

func (s *Store) AddRepository(ctx context.Context, repository Repository) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES(?,?,?,?)`, repository.ID, repository.Name, repository.Description, repository.CreatedAt.Unix())
	return err
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
