package state

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Machine-local import credentials.
//
// Raw Basic/Bearer secrets and the optional source CA bundle live in an
// owner-only file beside the state database, so a portable backup manifest
// never contains them and an offline restore cannot revive stale authority.
// The file records source identity and an unpredictable credential generation.
// Replacement publishes a new generation before the database binds that exact
// generation, so unrelated authority revisions can never authorize a failed
// write.
const (
	importCredentialDir          = "import-credentials"
	maxImportCredentialBytes     = 16 << 10
	maxImportCredentialFileBytes = maxImportCredentialBytes + 2*MaxImportCABytes + 8192
)

// MaxImportCABytes bounds a stored source CA bundle. The browser form, the
// API, and the command line share this one bound.
const MaxImportCABytes = 1 << 20

var ErrImportCredentialMissing = errors.New("import credential is not stored")

type importCredentialAuthority struct {
	revision uint64
	blocked  bool
}

// ImportCredentialAuthority returns process-local credential authority. A run
// pins the revision and refuses admission while a credential mutation is
// incomplete. This state is deliberately absent from backup and process
// restart because no request carrying captured credentials survives restart.
func (s *Store) ImportCredentialAuthority(repositoryID string) (uint64, bool) {
	release := s.LockImportCredentialAuthority(repositoryID)
	defer release()
	return s.importCredentialAuthorityLocked(repositoryID)
}

func (s *Store) importCredentialLock(repositoryID string) *sync.Mutex {
	s.credentialRegistryMu.Lock()
	defer s.credentialRegistryMu.Unlock()
	if s.credentialLocks == nil {
		s.credentialLocks = make(map[string]*sync.Mutex)
	}
	lock := s.credentialLocks[repositoryID]
	if lock == nil {
		lock = new(sync.Mutex)
		s.credentialLocks[repositoryID] = lock
	}
	return lock
}

// LockImportCredentialAuthority serializes one repository's final local
// publication window with its source and credential mutations. The returned
// release function must be invoked by the caller.
func (s *Store) LockImportCredentialAuthority(repositoryID string) func() {
	s.credentialRestoreMu.RLock()
	lock := s.importCredentialLock(repositoryID)
	lock.Lock()
	return func() {
		lock.Unlock()
		s.credentialRestoreMu.RUnlock()
	}
}

// ImportCredentialAuthorityLocked reads authority while the caller holds the
// lock acquired by LockImportCredentialAuthority.
func (s *Store) ImportCredentialAuthorityLocked(repositoryID string) (uint64, bool) {
	return s.importCredentialAuthorityLocked(repositoryID)
}

func (s *Store) importCredentialAuthorityLocked(repositoryID string) (uint64, bool) {
	value, _ := s.credentialAuthority.Load(repositoryID)
	state, _ := value.(importCredentialAuthority)
	return state.revision, state.blocked
}

func (s *Store) beginImportCredentialMutationLocked(repositoryID string) uint64 {
	value, _ := s.credentialAuthority.Load(repositoryID)
	state, _ := value.(importCredentialAuthority)
	state.revision++
	state.blocked = true
	s.credentialAuthority.Store(repositoryID, state)
	return state.revision
}

func (s *Store) completeImportCredentialMutationLocked(repositoryID string) {
	value, _ := s.credentialAuthority.Load(repositoryID)
	state, _ := value.(importCredentialAuthority)
	state.blocked = false
	s.credentialAuthority.Store(repositoryID, state)
}

// ImportBasicAuth is an in-memory HTTP Basic credential.
type ImportBasicAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ImportCredentials is one source's machine-local secret and trust material.
// At most one of Basic or BearerToken is set.
type ImportCredentials struct {
	RepositoryID              string           `json:"repository_id"`
	URL                       string           `json:"url"`
	SourceGeneration          int64            `json:"source_generation"`
	CredentialGeneration      string           `json:"credential_generation"`
	ExpectedAuthorityRevision int64            `json:"-"`
	Basic                     *ImportBasicAuth `json:"basic,omitempty"`
	BearerToken               string           `json:"bearer_token,omitempty"`
	RootCAPEM                 []byte           `json:"root_ca_pem,omitempty"`
}

// Validate bounds and normalizes one credential record.
func (c ImportCredentials) Validate() error {
	if !validText(c.RepositoryID, 100) || !validText(c.URL, maxImportURLBytes) || c.SourceGeneration <= 0 {
		return errors.New("invalid import credential identity")
	}
	if c.Basic != nil && c.BearerToken != "" {
		return errors.New("import credential selects both Basic and Bearer")
	}
	if c.Basic != nil {
		if strings.Contains(c.Basic.Username, ":") || len(c.Basic.Username)+len(c.Basic.Password) > maxImportCredentialBytes {
			return errors.New("invalid import Basic credential")
		}
	}
	if len(c.BearerToken) > maxImportCredentialBytes {
		return errors.New("import Bearer credential exceeds its bound")
	}
	if c.CredentialGeneration != "" && !isLowerHex(c.CredentialGeneration, 32) {
		return errors.New("invalid import credential generation")
	}
	if len(c.RootCAPEM) > MaxImportCABytes {
		return errors.New("import CA bundle exceeds its bound")
	}
	return nil
}

// Bound reports whether this credential belongs to the given source
// configuration. An unbound credential is not sent.
func (c ImportCredentials) Bound(source ImportSource) bool {
	return c.RepositoryID == source.RepositoryID && c.URL == source.URL && c.SourceGeneration == source.SourceGeneration &&
		c.CredentialGeneration != "" && c.CredentialGeneration == source.CredentialGeneration
}

func (s *Store) importCredentialPath(repositoryID string) (string, error) {
	if !validText(repositoryID, 100) || strings.ContainsAny(repositoryID, `/\`) {
		return "", errors.New("invalid import credential repository identifier")
	}
	return filepath.Join(s.dir, importCredentialDir, repositoryID+".json"), nil
}

// SaveImportCredentials atomically replaces credentials and advances execution
// authority only after the new-generation private file is durable. Repeating
// an identical credential is a no-op and does not revoke admitted work.
func (s *Store) SaveImportCredentials(ctx context.Context, credential ImportCredentials, now time.Time) (ImportSource, error) {
	if err := credential.Validate(); err != nil {
		return ImportSource{}, err
	}
	if now.IsZero() {
		return ImportSource{}, errors.New("import credential time is required")
	}
	release := s.LockImportCredentialAuthority(credential.RepositoryID)
	defer release()

	source, exists, err := s.ImportSource(ctx, credential.RepositoryID)
	if err != nil {
		return ImportSource{}, err
	}
	if !exists {
		return ImportSource{}, errors.New("import source is not configured")
	}
	if credential.RepositoryID != source.RepositoryID || credential.URL != source.URL || credential.SourceGeneration != source.SourceGeneration ||
		credential.ExpectedAuthorityRevision != source.AuthorityRevision {
		return ImportSource{}, errors.New("import credential was prepared for stale source authority")
	}
	current, stored, err := s.LoadImportCredentials(ctx, credential.RepositoryID)
	if err != nil {
		return ImportSource{}, err
	}
	if stored && current.Bound(source) && sameImportCredential(current, credential) {
		// An identical explicit retry may release a block left by a failed file
		// operation because the current private file was revalidated as exact.
		s.completeImportCredentialMutationLocked(source.RepositoryID)
		return source, nil
	}
	s.beginImportCredentialMutationLocked(source.RepositoryID)
	generation, err := newImportCredentialGeneration(source.CredentialGeneration)
	if err != nil {
		return ImportSource{}, err
	}
	credential.CredentialGeneration = generation
	if err := s.writeImportCredentials(ctx, credential); err != nil {
		return ImportSource{}, err
	}
	changed, err := s.activateImportCredential(ctx, source, generation, now)
	if err != nil {
		return ImportSource{}, err
	}
	s.completeImportCredentialMutationLocked(source.RepositoryID)
	return changed, nil
}

func newImportCredentialGeneration(exclude string) (string, error) {
	for {
		value := make([]byte, 16)
		if _, err := rand.Read(value); err != nil {
			return "", err
		}
		generation := hex.EncodeToString(value)
		if generation != exclude {
			return generation, nil
		}
	}
}

func sameImportCredential(left, right ImportCredentials) bool {
	if left.RepositoryID != right.RepositoryID || left.URL != right.URL || left.SourceGeneration != right.SourceGeneration || left.BearerToken != right.BearerToken || !bytes.Equal(left.RootCAPEM, right.RootCAPEM) {
		return false
	}
	if left.Basic == nil || right.Basic == nil {
		return left.Basic == nil && right.Basic == nil
	}
	return left.Basic.Username == right.Basic.Username && left.Basic.Password == right.Basic.Password
}

func (s *Store) writeImportCredentials(ctx context.Context, credential ImportCredentials) error {
	path, err := s.importCredentialPath(credential.RepositoryID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create import credential directory: %w", err)
	}
	if err := ProtectPrivatePath(directory, true); err != nil {
		return fmt.Errorf("protect import credential directory: %w", err)
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	if len(encoded) > maxImportCredentialFileBytes {
		return errors.New("import credential file exceeds its bound")
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(directory, "."+credential.RepositoryID+".tmp-"+hex.EncodeToString(suffix))
	file, err := CreatePrivateFile(temporary)
	if err != nil {
		return fmt.Errorf("create import credential file: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return fmt.Errorf("write import credential file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync import credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close import credential file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish import credential file: %w", err)
	}
	removeTemporary = false
	if err := ProtectPrivatePath(path, false); err != nil {
		return fmt.Errorf("protect import credential file: %w", err)
	}
	return nil
}

// LoadImportCredentials reads one stored credential. A bounded read refuses a
// file that grew beyond its expected size.
func (s *Store) LoadImportCredentials(ctx context.Context, repositoryID string) (ImportCredentials, bool, error) {
	if err := ctx.Err(); err != nil {
		return ImportCredentials{}, false, err
	}
	path, err := s.importCredentialPath(repositoryID)
	if err != nil {
		return ImportCredentials{}, false, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ImportCredentials{}, false, nil
	}
	if err != nil {
		return ImportCredentials{}, false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxImportCredentialFileBytes+1))
	if err != nil {
		return ImportCredentials{}, false, err
	}
	if len(content) > maxImportCredentialFileBytes {
		return ImportCredentials{}, false, errors.New("import credential file exceeds its bound")
	}
	var credential ImportCredentials
	if err := json.Unmarshal(content, &credential); err != nil {
		return ImportCredentials{}, false, fmt.Errorf("decode import credential file: %w", err)
	}
	if err := credential.Validate(); err != nil {
		return ImportCredentials{}, false, err
	}
	if credential.CredentialGeneration == "" {
		return ImportCredentials{}, false, errors.New("import credential file has no generation")
	}
	if credential.RepositoryID != repositoryID {
		return ImportCredentials{}, false, errors.New("import credential file belongs to another repository")
	}
	return credential, true, nil
}

// DeleteImportCredentials revokes credentials before advancing authority. A
// failed transition remains locally blocked until an explicit retry succeeds.
func (s *Store) DeleteImportCredentials(ctx context.Context, repositoryID string, now time.Time) (ImportSource, error) {
	if now.IsZero() {
		return ImportSource{}, errors.New("import credential time is required")
	}
	release := s.LockImportCredentialAuthority(repositoryID)
	defer release()
	if err := ctx.Err(); err != nil {
		return ImportSource{}, err
	}
	source, exists, err := s.ImportSource(ctx, repositoryID)
	if err != nil {
		return ImportSource{}, err
	}
	if !exists {
		return ImportSource{}, errors.New("import source is not configured")
	}
	s.beginImportCredentialMutationLocked(repositoryID)
	if err := s.removeImportCredentialFile(repositoryID); err != nil {
		return ImportSource{}, err
	}
	changed, err := s.activateImportCredential(ctx, source, "", now)
	if err != nil {
		return ImportSource{}, err
	}
	s.completeImportCredentialMutationLocked(repositoryID)
	return changed, nil
}

func (s *Store) removeImportCredentialFile(repositoryID string) error {
	path, err := s.importCredentialPath(repositoryID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// OrphanImportBindings lists names that have an import source or a stored
// import credential file but no repository row, in name order.
func (s *Store) OrphanImportBindings(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository_id FROM import_sources WHERE repository_id NOT IN (SELECT id FROM repositories)`)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names[name] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, importCredentialDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		name, isJSON := strings.CutSuffix(entry.Name(), ".json")
		if !isJSON || strings.HasPrefix(name, ".") || !entry.Type().IsRegular() {
			continue
		}
		if _, err := s.importCredentialPath(name); err != nil {
			continue
		}
		if _, exists, err := s.Repository(ctx, name); err != nil {
			return nil, err
		} else if !exists {
			names[name] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	return sorted, nil
}

// ImportBindingSnapshot is the source row and credential file that existed
// before an initial import wrote its own binding. It is not portable backup
// state. CredentialJSON is secret and must not be logged.
type ImportBindingSnapshot struct {
	SourceExists     bool
	Source           ImportSource
	CredentialExists bool
	CredentialJSON   []byte
}

// ReadImportBinding snapshots the source row and credential file. The caller
// holds the repository lock so the snapshot matches the following write.
func (s *Store) ReadImportBinding(ctx context.Context, repositoryID string) (ImportBindingSnapshot, error) {
	var snapshot ImportBindingSnapshot
	source, exists, err := s.ImportSource(ctx, repositoryID)
	if err != nil {
		return snapshot, err
	}
	snapshot.SourceExists = exists
	snapshot.Source = source
	content, credentialExists, err := s.readImportCredentialFile(ctx, repositoryID)
	if err != nil {
		return snapshot, err
	}
	snapshot.CredentialExists = credentialExists
	snapshot.CredentialJSON = content
	return snapshot, nil
}

// RestoreImportBinding puts back a snapshot of the source row and credential
// file. It does not take the repository or credential locks and does not delete
// observations or destination content. The caller holds those locks.
func (s *Store) RestoreImportBinding(ctx context.Context, repositoryID string, snapshot ImportBindingSnapshot) error {
	if snapshot.SourceExists {
		if snapshot.Source.RepositoryID != repositoryID {
			return errors.New("import binding snapshot belongs to another repository")
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO import_sources(
			repository_id,url,source_generation,authority_revision,credential_generation,mode,git_only_consent,allow_private_network,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(repository_id) DO UPDATE SET
			url=excluded.url,source_generation=excluded.source_generation,authority_revision=excluded.authority_revision,
			credential_generation=excluded.credential_generation,mode=excluded.mode,git_only_consent=excluded.git_only_consent,
			allow_private_network=excluded.allow_private_network,created_at=excluded.created_at,updated_at=excluded.updated_at`,
			snapshot.Source.RepositoryID, snapshot.Source.URL, snapshot.Source.SourceGeneration, snapshot.Source.AuthorityRevision,
			snapshot.Source.CredentialGeneration, snapshot.Source.Mode, boolInt(snapshot.Source.GitOnlyConsent), boolInt(snapshot.Source.AllowPrivateNetwork),
			snapshot.Source.CreatedAt.Unix(), snapshot.Source.UpdatedAt.Unix()); err != nil {
			return err
		}
	} else if _, err := s.db.ExecContext(ctx, `DELETE FROM import_sources WHERE repository_id=?`, repositoryID); err != nil {
		return err
	}
	if snapshot.CredentialExists {
		if err := s.writeImportCredentialBytes(ctx, repositoryID, snapshot.CredentialJSON); err != nil {
			return err
		}
	} else if err := s.removeImportCredentialFile(repositoryID); err != nil {
		return err
	}
	source, exists, err := s.ImportSource(ctx, repositoryID)
	if err != nil {
		return err
	}
	if snapshot.SourceExists {
		if !exists || source.URL != snapshot.Source.URL || source.SourceGeneration != snapshot.Source.SourceGeneration ||
			source.AuthorityRevision != snapshot.Source.AuthorityRevision || source.CredentialGeneration != snapshot.Source.CredentialGeneration ||
			source.Mode != snapshot.Source.Mode || source.GitOnlyConsent != snapshot.Source.GitOnlyConsent || source.AllowPrivateNetwork != snapshot.Source.AllowPrivateNetwork {
			return errors.New("import source snapshot could not be read back")
		}
	} else if exists {
		return errors.New("import source remained after binding restore")
	}
	after, afterExists, err := s.readImportCredentialFile(ctx, repositoryID)
	if err != nil {
		return err
	}
	if afterExists != snapshot.CredentialExists || (afterExists && !bytes.Equal(after, snapshot.CredentialJSON)) {
		return errors.New("import credential snapshot could not be read back")
	}
	s.completeImportCredentialMutationLocked(repositoryID)
	return nil
}

func (s *Store) readImportCredentialFile(ctx context.Context, repositoryID string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	path, err := s.importCredentialPath(repositoryID)
	if err != nil {
		return nil, false, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxImportCredentialFileBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(content) > maxImportCredentialFileBytes {
		return nil, false, errors.New("import credential file exceeds its bound")
	}
	return content, true, nil
}

func (s *Store) writeImportCredentialBytes(ctx context.Context, repositoryID string, content []byte) error {
	if len(content) == 0 || len(content) > maxImportCredentialFileBytes {
		return errors.New("import credential snapshot has an unexpected size")
	}
	path, err := s.importCredentialPath(repositoryID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := ProtectPrivatePath(directory, true); err != nil {
		return err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(directory, "."+repositoryID+".restore-"+hex.EncodeToString(suffix))
	file, err := CreatePrivateFile(temporary)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	removeTemporary = false
	return ProtectPrivatePath(path, false)
}
