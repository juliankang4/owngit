package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/publishdir"
	"owngit/internal/state"
)

var (
	validName             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	ErrInvalidName        = errors.New("invalid repository name")
	ErrInvalidDescription = errors.New("invalid repository description")
	ErrNameTaken          = errors.New("repository name is already in use")
	ErrUnsupportedFormat  = errors.New("unsupported repository object format")
)

type Manager struct {
	Store            *state.Store
	Git              *gitexec.Runner
	Locks            *gitexec.Locks
	Root             string
	restorePublisher func(context.Context, string, string, string, string) error
	// OnChange wakes advisory check reconciliation after a repository ref write.
	// It must be nonblocking and must not execute repository commands.
	OnChange func(string)
	mu       sync.RWMutex
}

func (m *Manager) SetRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Root = root
}

func (m *Manager) RepositoryRoot() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Root
}

// CreateOptions configure repository creation. ObjectFormat selects the
// repository hash algorithm: an empty value keeps the Git default (SHA-1),
// and "sha256" requires a Git that supports it.
type CreateOptions struct {
	ObjectFormat string
}

// ValidateName applies the repository name and description rules used by
// Create, so an importer can reject a name before starting network work.
func ValidateName(name, description string) error {
	trimmed := strings.TrimSpace(name)
	if !validName.MatchString(trimmed) || trimmed == "." || trimmed == ".." || strings.HasSuffix(strings.ToLower(trimmed), ".git") {
		return fmt.Errorf("%w: use 1-100 letters, numbers, dots, underscores, or hyphens and do not end in .git", ErrInvalidName)
	}
	switch strings.ToLower(trimmed) {
	case "new", "new-import":
		return fmt.Errorf("%w: %q is reserved for a repository form", ErrInvalidName, trimmed)
	}
	if len(description) > 500 {
		return ErrInvalidDescription
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, name, description string) (state.Repository, error) {
	return m.CreateWithOptions(ctx, name, description, CreateOptions{})
}

// CreateWithOptions creates, configures and records a new bare repository.
// The directory is visible at its final path before this method returns, so an
// initial import uses InitBareRepository and records the repository row only
// after refs and HEAD are ready.
func (m *Manager) CreateWithOptions(ctx context.Context, name, description string, options CreateOptions) (state.Repository, error) {
	if options.ObjectFormat != "" && options.ObjectFormat != ObjectFormatSHA1 && options.ObjectFormat != ObjectFormatSHA256 {
		return state.Repository{}, fmt.Errorf("%w: %q", ErrUnsupportedFormat, options.ObjectFormat)
	}
	name = strings.TrimSpace(name)
	if err := ValidateName(name, description); err != nil {
		return state.Repository{}, err
	}
	id := strings.ToLower(name)
	if err := ValidateID(id); err != nil {
		return state.Repository{}, fmt.Errorf("%w: %v", ErrInvalidName, err)
	}
	if m.Locks == nil {
		return state.Repository{}, errors.New("repository locks are unavailable")
	}
	// Hold the repository lock from the existence check through the row write so
	// an initial import cannot configure this id between those steps.
	lock := m.Locks.For(id)
	lock.Lock()
	defer lock.Unlock()
	if _, exists, err := m.Store.Repository(ctx, id); err != nil {
		return state.Repository{}, err
	} else if exists {
		return state.Repository{}, ErrNameTaken
	}
	root, err := canonicalRoot(m.RepositoryRoot())
	if err != nil {
		return state.Repository{}, err
	}
	finalPath := filepath.Join(root, id+".git")
	if info, err := os.Lstat(finalPath); err == nil {
		return state.Repository{}, fmt.Errorf("%w: repository path already exists (%s)", ErrNameTaken, info.Name())
	} else if !os.IsNotExist(err) {
		return state.Repository{}, fmt.Errorf("inspect repository path: %w", err)
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return state.Repository{}, err
	}
	temporaryPath := filepath.Join(root, ".owngit-create-"+hex.EncodeToString(suffix))
	if err := os.Mkdir(temporaryPath, 0o700); err != nil {
		return state.Repository{}, fmt.Errorf("create temporary repository directory: %w", err)
	}
	created := false
	defer func() {
		if !created {
			_ = os.RemoveAll(temporaryPath)
		}
	}()

	if err := m.InitBareRepository(ctx, temporaryPath, options); err != nil {
		return state.Repository{}, err
	}
	if err := publishdir.Rename(ctx, temporaryPath, finalPath); err != nil {
		return state.Repository{}, fmt.Errorf("publish repository directory: %w", err)
	}
	created = true
	repository := state.Repository{ID: id, Name: name, Description: strings.TrimSpace(description), CreatedAt: time.Now()}
	if err := m.Store.AddRepository(ctx, repository); err != nil {
		return state.Repository{}, fmt.Errorf("record repository (the new bare repository remains at %s for owner recovery): %w", finalPath, err)
	}
	return repository, nil
}

// InitBareRepository initializes a bare repository at directory. It does not
// rename the directory or record a repository row.
func (m *Manager) InitBareRepository(ctx context.Context, directory string, options CreateOptions) error {
	if options.ObjectFormat != "" && options.ObjectFormat != ObjectFormatSHA1 && options.ObjectFormat != ObjectFormatSHA256 {
		return fmt.Errorf("%w: %q", ErrUnsupportedFormat, options.ObjectFormat)
	}
	initArguments := []string{"init", "--bare", "--initial-branch=main"}
	if options.ObjectFormat == ObjectFormatSHA256 {
		initArguments = append(initArguments, "--object-format=sha256")
	}
	initArguments = append(initArguments, ".")
	if _, err := m.Git.Run(ctx, directory, nil, initArguments...); err != nil {
		return err
	}
	if options.ObjectFormat != "" {
		format, err := m.ObjectFormat(ctx, directory)
		if err != nil {
			return err
		}
		if format != options.ObjectFormat {
			return fmt.Errorf("Git created a %s repository instead of %s", format, options.ObjectFormat)
		}
	}
	for _, setting := range repositoryConfig() {
		if _, err := m.Git.Run(ctx, directory, nil, "--git-dir", ".", "config", "--local", setting[0], setting[1]); err != nil {
			return err
		}
	}
	return writeRetentionHook(directory, m.Git)
}

// CanonicalStorageRoot returns the cleaned repository storage root.
func (m *Manager) CanonicalStorageRoot() (string, error) {
	return canonicalRoot(m.RepositoryRoot())
}

func (m *Manager) Path(id string) (string, error) {
	if err := ValidateID(id); err != nil {
		return "", err
	}
	root, err := canonicalRoot(m.RepositoryRoot())
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, id+".git")
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("repository path escapes the storage root")
	}
	return path, nil
}

func (m *Manager) ExistingPath(ctx context.Context, id string) (string, state.Repository, bool, error) {
	repository, exists, err := m.Store.Repository(ctx, id)
	if err != nil || !exists {
		return "", state.Repository{}, exists, err
	}
	path, err := m.Path(id)
	if err != nil {
		return "", state.Repository{}, false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", state.Repository{}, false, fmt.Errorf("repository storage is unavailable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", state.Repository{}, false, errors.New("repository path must not be a symbolic link")
	}
	if !info.IsDir() {
		return "", state.Repository{}, false, errors.New("repository path is not a directory")
	}
	return path, repository, true, nil
}

func canonicalRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("repository storage is not configured")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect repository root: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("repository root is not a directory")
	}
	if _, err := os.Lstat(filepath.Join(absolute, state.IncompleteRestoreMarkerName)); err == nil {
		return "", errors.New("repository root belongs to an incomplete offline restore; follow the interrupted-restore procedure before use")
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect incomplete restore marker: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func repositoryConfig() [][2]string {
	config := [][2]string{
		{"http.receivepack", "true"},
		{"http.getanyfile", "false"},
		{"receive.hideRefs", "refs/owngit/"},
		{"uploadpack.hideRefs", "refs/owngit/"},
		{"transfer.hideRefs", "refs/owngit/"},
		{"receive.advertiseAtomic", "true"},
		{"receive.advertisePushOptions", "false"},
		{"receive.denyDeleteCurrent", "ignore"},
		{"receive.autogc", "false"},
		{"gc.auto", "0"},
		{"gc.autoDetach", "false"},
		{"maintenance.auto", "false"},
		{"maintenance.autoDetach", "false"},
		{"core.logAllRefUpdates", "false"},
	}
	if runtime.GOOS == "windows" {
		config = append(config, [2]string{"core.longpaths", "true"})
	}
	return config
}

// PrepareExisting reapplies safety-critical configuration to repositories that
// predate the current executable. It prevents automatic maintenance during
// retention and enables long repository paths on Windows.
func (m *Manager) PrepareExisting(ctx context.Context) error {
	return m.prepareExisting(ctx, m.Git)
}

// PrepareExistingForRuntime writes hooks for a runtime path that will become
// active after staged repository storage is published. Git commands still run
// through the manager's current, usable runner.
func (m *Manager) PrepareExistingForRuntime(ctx context.Context, hookRuntime *gitexec.Runner) error {
	if hookRuntime == nil {
		return errors.New("hook runtime is unavailable")
	}
	return m.prepareExisting(ctx, hookRuntime)
}

func (m *Manager) prepareExisting(ctx context.Context, hookRuntime *gitexec.Runner) error {
	repositories, err := m.Store.Repositories(ctx)
	if err != nil {
		return err
	}
	for _, stored := range repositories {
		path, _, exists, err := m.ExistingPath(ctx, stored.ID)
		if err != nil || !exists {
			if err == nil {
				err = errors.New("repository not found")
			}
			return fmt.Errorf("prepare repository %q: %w", stored.ID, err)
		}
		lock := m.Locks.For(stored.ID)
		lock.Lock()
		for _, setting := range repositoryConfig() {
			_, err = m.Git.Run(ctx, path, nil, "--git-dir", ".", "config", "--local", setting[0], setting[1])
			if err != nil {
				break
			}
		}
		if err == nil {
			err = writeRetentionHook(path, hookRuntime)
		}
		lock.Unlock()
		if err != nil {
			return fmt.Errorf("prepare repository %q: %w", stored.ID, err)
		}
	}
	return nil
}

func writeRetentionHook(repositoryPath string, runner *gitexec.Runner) error {
	hooks := filepath.Join(repositoryPath, "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}
	git := shellQuote(runner.GitPath)
	home := shellQuote(runner.HomeDir)
	config := shellQuote(runner.GlobalConfigPath)
	temp := shellQuote(runner.TempDir)
	updateScript := fmt.Sprintf(`#!/bin/sh
set -f
ref=$1
old=$2
new=$3
zero=0000000000000000000000000000000000000000
case "$ref" in
  refs/heads/*) kind=heads; short=${ref#refs/heads/} ;;
  refs/tags/*) kind=tags; short=${ref#refs/tags/} ;;
  refs/owngit/*) echo "OwnGit reserved refs cannot be changed" >&2; exit 1 ;;
  *) echo "OwnGit accepts only branch and tag refs" >&2; exit 1 ;;
esac
case "$old" in ''|*[!0-9a-f]*) echo "invalid old object ID" >&2; exit 1 ;; esac
case "$new" in ''|*[!0-9a-f]*) echo "invalid new object ID" >&2; exit 1 ;; esac
test "$old" = "$zero" && exit 0
run_git() {
  /usr/bin/env -i PATH=%s HOME=%s XDG_CONFIG_HOME=%s GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=%s GIT_CONFIG_GLOBAL=%s GIT_TERMINAL_PROMPT=0 LC_ALL=C LANG=C TZ=UTC TMPDIR=%s GIT_DIR="$GIT_DIR" %s "$@"
}
run_git_objects() {
  if test -z "${GIT_OBJECT_DIRECTORY:-}"; then run_git "$@"; return; fi
  /usr/bin/env -i PATH=%s HOME=%s XDG_CONFIG_HOME=%s GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=%s GIT_CONFIG_GLOBAL=%s GIT_TERMINAL_PROMPT=0 LC_ALL=C LANG=C TZ=UTC TMPDIR=%s GIT_DIR="$GIT_DIR" GIT_OBJECT_DIRECTORY="$GIT_OBJECT_DIRECTORY" GIT_ALTERNATE_OBJECT_DIRECTORIES="$GIT_ALTERNATE_OBJECT_DIRECTORIES" %s "$@"
}
actual=$(run_git show-ref --verify --hash "$ref" 2>/dev/null) || { echo "current ref is missing" >&2; exit 1; }
test "$actual" = "$old" || { echo "current ref changed concurrently" >&2; exit 1; }
run_git_objects cat-file -e "$old^{object}" || { echo "old object is unavailable" >&2; exit 1; }
if test "$kind" = heads && test "$new" != "$zero"; then
  if run_git_objects merge-base --is-ancestor "$old" "$new"; then
    exit 0
  else
    status=$?
    test "$status" = 1 || { echo "could not compare branch history" >&2; exit 1; }
  fi
fi
commands=$(mktemp %s) || exit 1
trap 'rm -f "$commands"' EXIT HUP INT TERM
retained="refs/owngit/retained/$kind/$old"
provenance="refs/owngit/provenance/$kind/$short/$old"
existing=$(run_git show-ref --verify --hash "$retained" 2>/dev/null || true)
if test -n "$existing"; then
  test "$existing" = "$old" || { echo "retained ref collision" >&2; exit 1; }
else
  printf 'create %%s %%s\n' "$retained" "$old" >>"$commands" || exit 1
fi
existing=$(run_git show-ref --verify --hash "$provenance" 2>/dev/null || true)
if test -n "$existing"; then
  test "$existing" = "$old" || { echo "retention provenance collision" >&2; exit 1; }
else
  printf 'create %%s %%s\n' "$provenance" "$old" >>"$commands" || exit 1
fi
if test -s "$commands"; then
  { printf 'start\n'; cat "$commands"; printf 'prepare\ncommit\n'; } |
    run_git update-ref --stdin >/dev/null 2>&1 || { echo "could not preserve previous history" >&2; exit 1; }
fi
`, shellQuote(filepath.Dir(runner.GitPath)), home, home, config, config, temp, git,
		shellQuote(filepath.Dir(runner.GitPath)), home, home, config, config, temp, git,
		shellQuote(filepath.Join(runner.TempDir, "owngit-retention-commands.XXXXXX")))
	if err := writeHookFile(filepath.Join(hooks, "update"), updateScript); err != nil {
		return err
	}
	for _, obsolete := range []string{"pre-receive", "reference-transaction"} {
		if err := os.Remove(filepath.Join(hooks, obsolete)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove obsolete Git hook %s: %w", obsolete, err)
		}
	}
	return nil
}

func writeHookFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		return fmt.Errorf("write Git hook %s: %w", filepath.Base(path), err)
	}
	return os.Chmod(path, 0o700)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
