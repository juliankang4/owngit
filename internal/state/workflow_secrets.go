package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"owngit/internal/statepath"
)

const (
	MaxWorkflowSecrets             = 100
	MaxWorkflowSecretValueBytes    = 48 << 10
	maxWorkflowSecretMetadataBytes = 4096
	// JSON can escape each value byte into six bytes.
	maxWorkflowSecretFileBytes = MaxWorkflowSecrets * (6*MaxWorkflowSecretValueBytes + maxWorkflowSecretMetadataBytes)
)

// WorkflowSecretInfo is safe to show to an administrator. It never has a value.
type WorkflowSecretInfo struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
	Actor     Actor     `json:"actor"`
}

type workflowSecret struct {
	WorkflowSecretInfo
	Value string `json:"value"`
}

func workflowSecretName(name string) (string, error) {
	for index, char := range []byte(name) {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char == '_' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return "", errors.New("workflow secret names must match [A-Za-z_][A-Za-z0-9_]*")
	}
	name = strings.ToUpper(name)
	if name == "" || strings.HasPrefix(name, "GITHUB_") {
		return "", errors.New("workflow secret name is empty or has the reserved GITHUB_ prefix")
	}
	return name, nil
}

func validateWorkflowSecretValue(value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return errors.New("workflow secret values must be UTF-8 without NUL")
	}
	if len(value) > MaxWorkflowSecretValueBytes {
		return errors.New("workflow secret value exceeds 48 KiB")
	}
	return nil
}

func (s *Store) workflowSecretPath(repositoryID string) (string, error) {
	if !validText(repositoryID, 100) || strings.ContainsAny(repositoryID, `/\`) {
		return "", errors.New("invalid workflow secret repository identifier")
	}
	return filepath.Join(s.dir, statepath.WorkflowSecrets, repositoryID+statepath.CredentialSuffix), nil
}

func (s *Store) lockWorkflowSecrets(repositoryID string) func() {
	value, _ := s.workflowSecretLocks.LoadOrStore(repositoryID, new(sync.Mutex))
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func (s *Store) requireWorkflowSecretRepository(ctx context.Context, repositoryID string) error {
	if _, exists, err := s.Repository(ctx, repositoryID); err != nil {
		return err
	} else if !exists {
		return ErrRepositoryNotFound
	}
	if _, pending, err := s.RepositoryDeletion(ctx, repositoryID); err != nil {
		return err
	} else if pending {
		return ErrRepositoryDeletionPending
	}
	return nil
}

func (s *Store) loadWorkflowSecrets(ctx context.Context, repositoryID string) ([]workflowSecret, error) {
	path, err := s.workflowSecretPath(repositoryID)
	if err != nil {
		return nil, err
	}
	if err := s.requireWorkflowSecretRepository(ctx, repositoryID); err != nil {
		return nil, err
	}
	content, exists, err := readPrivateBytes(ctx, path, maxWorkflowSecretFileBytes, "workflow secret file exceeds its bound", OpenPrivateInputFile)
	if err != nil {
		return nil, err
	}
	secrets := []workflowSecret{}
	if !exists {
		return secrets, nil
	}
	// Decode errors can include input bytes, so never return them for secrets.
	if !utf8.Valid(content) || json.Unmarshal(content, &secrets) != nil || secrets == nil {
		return nil, errors.New("invalid workflow secret file")
	}
	if len(secrets) > MaxWorkflowSecrets {
		return nil, errors.New("workflow secret file exceeds 100 secrets")
	}
	seen := make(map[string]bool, len(secrets))
	for _, secret := range secrets {
		name, err := workflowSecretName(secret.Name)
		if err != nil || name != secret.Name || seen[name] || secret.UpdatedAt.IsZero() || secret.Actor.Validate() != nil || validateWorkflowSecretValue(secret.Value) != nil {
			return nil, errors.New("invalid workflow secret record")
		}
		seen[name] = true
	}
	slices.SortFunc(secrets, func(left, right workflowSecret) int { return strings.Compare(left.Name, right.Name) })
	return secrets, nil
}

func (s *Store) saveWorkflowSecrets(ctx context.Context, repositoryID string, secrets []workflowSecret) error {
	path, err := s.workflowSecretPath(repositoryID)
	if err != nil {
		return err
	}
	content, err := json.Marshal(secrets)
	if err != nil {
		return errors.New("encode workflow secret file")
	}
	if len(content) > maxWorkflowSecretFileBytes {
		return errors.New("workflow secret file exceeds its bound")
	}
	return writePrivateBytesLocked(ctx, path, repositoryID, statepath.CredentialWrite, content, "workflow secret")
}

// ListWorkflowSecrets returns only names, update times and actors, sorted by name.
func (s *Store) ListWorkflowSecrets(ctx context.Context, repositoryID string) ([]WorkflowSecretInfo, error) {
	if _, err := s.workflowSecretPath(repositoryID); err != nil {
		return nil, err
	}
	release := s.lockWorkflowSecrets(repositoryID)
	defer release()
	secrets, err := s.loadWorkflowSecrets(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	infos := make([]WorkflowSecretInfo, len(secrets))
	for index, secret := range secrets {
		infos[index] = secret.WorkflowSecretInfo
	}
	return infos, nil
}

// SetWorkflowSecret adds or replaces one case-insensitive name in an owner-only
// file. Values never enter the database, returned metadata or errors.
func (s *Store) SetWorkflowSecret(ctx context.Context, repositoryID, name, value string, actor Actor, now time.Time) (WorkflowSecretInfo, error) {
	name, err := workflowSecretName(name)
	if err != nil {
		return WorkflowSecretInfo{}, err
	}
	if err := validateWorkflowSecretValue(value); err != nil {
		return WorkflowSecretInfo{}, err
	}
	if now.IsZero() || actor.Validate() != nil {
		return WorkflowSecretInfo{}, errors.New("invalid workflow secret update time or actor")
	}
	if _, err := s.workflowSecretPath(repositoryID); err != nil {
		return WorkflowSecretInfo{}, err
	}
	release := s.lockWorkflowSecrets(repositoryID)
	defer release()
	secrets, err := s.loadWorkflowSecrets(ctx, repositoryID)
	if err != nil {
		return WorkflowSecretInfo{}, err
	}
	info := WorkflowSecretInfo{Name: name, UpdatedAt: now.UTC(), Actor: actor}
	record := workflowSecret{WorkflowSecretInfo: info, Value: value}
	index := slices.IndexFunc(secrets, func(secret workflowSecret) bool { return secret.Name == name })
	if index >= 0 {
		secrets[index] = record
	} else {
		if len(secrets) == MaxWorkflowSecrets {
			return WorkflowSecretInfo{}, errors.New("a repository can have at most 100 workflow secrets")
		}
		secrets = append(secrets, record)
	}
	if err := s.saveWorkflowSecrets(ctx, repositoryID, secrets); err != nil {
		return WorkflowSecretInfo{}, err
	}
	return info, nil
}

// RemoveWorkflowSecret is idempotent for a missing name in an existing repository.
func (s *Store) RemoveWorkflowSecret(ctx context.Context, repositoryID, name string) error {
	name, err := workflowSecretName(name)
	if err != nil {
		return err
	}
	if _, err := s.workflowSecretPath(repositoryID); err != nil {
		return err
	}
	release := s.lockWorkflowSecrets(repositoryID)
	defer release()
	secrets, err := s.loadWorkflowSecrets(ctx, repositoryID)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(secrets, func(secret workflowSecret) bool { return secret.Name == name })
	if index >= 0 {
		secrets = slices.Delete(secrets, index, index+1)
	}
	if len(secrets) == 0 {
		return s.removeWorkflowSecretFileLocked(repositoryID)
	}
	if index < 0 {
		return nil
	}
	return s.saveWorkflowSecrets(ctx, repositoryID, secrets)
}

// ReadWorkflowSecrets reads fresh values for one job. It returns only requested
// upper-case names; a name not set in this repository has an empty value. A read
// failure must stop the job, never become an empty collection of secrets.
func (s *Store) ReadWorkflowSecrets(ctx context.Context, repositoryID string, names []string) (map[string]string, error) {
	requested := make(map[string]string, len(names))
	for _, name := range names {
		name, err := workflowSecretName(name)
		if err != nil {
			return nil, err
		}
		requested[name] = ""
	}
	if _, err := s.workflowSecretPath(repositoryID); err != nil {
		return nil, err
	}
	release := s.lockWorkflowSecrets(repositoryID)
	defer release()
	secrets, err := s.loadWorkflowSecrets(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	for _, secret := range secrets {
		if _, wanted := requested[secret.Name]; wanted {
			requested[secret.Name] = secret.Value
		}
	}
	return requested, nil
}

func (s *Store) removeWorkflowSecretFileLocked(repositoryID string) error {
	path, err := s.workflowSecretPath(repositoryID)
	if err != nil {
		return err
	}
	directory, err := OpenDirectory(filepath.Dir(path), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := removeWorkflowSecretTemporariesLocked(directory, repositoryID); err != nil {
		return err
	}
	return removePrivateFileLocked(path)
}

func removeWorkflowSecretTemporariesLocked(directory *os.File, repositoryID string) error {
	entries, err := readStateDirectory(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, temporary := statepath.CredentialTemporaryID(entry.Name())
		if !temporary || repositoryID != "" && id != repositoryID {
			continue
		}
		if err := removeOwnFile(directory, entry.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncPrivateDirectory(directory.Name())
}
