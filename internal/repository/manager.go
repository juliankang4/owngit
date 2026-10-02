package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	validName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	ErrInvalidName = errors.New("invalid repository name")
	// ErrReservedName wraps ErrInvalidName for names that address a form page.
	ErrReservedName        = fmt.Errorf("%w: reserved name", ErrInvalidName)
	ErrInvalidDescription  = errors.New("invalid repository description")
	ErrNameTaken           = errors.New("repository name is already in use")
	ErrFailedCreationLimit = errors.New("the failed-creation folder is full; check its contents and clear kept empty folders, then check and move the unaccepted repository folder aside before retrying")
	// ErrFolderExists identifies a folder with no repository record. OwnGit
	// never adopts or removes it when creating a repository.
	ErrFolderExists      = fmt.Errorf("%w: a folder already exists; choose another name, or move the existing folder aside after checking its contents", ErrNameTaken)
	ErrUnsupportedFormat = errors.New("unsupported repository object format")
	// ErrImportInProgress wraps ErrNameTaken when no repository has the name
	// yet but an import for it is running or still needs recovery.
	ErrImportInProgress = fmt.Errorf("%w: an import for this name is still running or needs recovery; try again after it finishes, or restart OwnGit if no import is running", ErrNameTaken)
)

// Manager owns the repository folder and its Git work. Store, Git and Locks
// are required: every production constructor sets them, so no method treats
// one as absent. Root is empty until setup chooses the repository folder.
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
	// snapshots caches RefSnapshot results between ref writes.
	snapshots snapshotCache
	// languages caches the newest language count of each repository.
	languages languageCache
	// objects caches Git reads fixed by object IDs; see object_cache.go.
	objects objectCache
	// preparation records the repositories that are still being prepared
	// after startup; see StartPreparation.
	preparation preparationState
	// maintenance schedules repository maintenance; see StartMaintenance.
	maintenance maintenanceState
	// backup records the repositories a running backup holds; see
	// HoldForBackup.
	backup backupState
	// maintenanceHook, when set by tests, runs before each maintenance
	// command with the repository write lock held.
	maintenanceHook func(ctx context.Context, id string, args []string) error
	// storageClaim holds this server's lock on the repository folder; see
	// ClaimStorage.
	storageClaim storageClaimState
	// failedCreationMu serializes the preservation count and move across names.
	failedCreationMu sync.Mutex
	// creationDirectoryHook, when set by tests, runs after atomic private
	// creation and before the separate protection verification.
	creationDirectoryHook func(string)
	// PreparationRetry replaces the 30-second first wait after a failed
	// preparation attempt. Tests shorten it; zero keeps the default.
	PreparationRetry time.Duration

	// deletionClock and deletionHook let tests fix the kept-folder time and
	// stop a deletion after a durable step, as a crash would.
	deletionClock func() time.Time
	deletionHook  func(step string) error
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

	// forgetImport removes import settings and credentials left for this
	// name by an import that never created its repository. Plain creation
	// sets it; an import's own creation does not.
	forgetImport bool
}

// ValidateName applies the repository name and description rules used by
// Create, so an importer can reject a name before starting network work.
func ValidateName(name, description string) error {
	trimmed := strings.TrimSpace(name)
	if !validName.MatchString(trimmed) || ValidateID(strings.ToLower(trimmed)) != nil {
		return fmt.Errorf("%w: use 1-100 letters, numbers, dots, underscores, or hyphens, do not end in .git, and do not use a Windows device name such as CON", ErrInvalidName)
	}
	switch strings.ToLower(trimmed) {
	case "new", "new-import":
		return fmt.Errorf("%w: %q is used by a repository form", ErrReservedName, trimmed)
	}
	if len(description) > state.MaximumRepositoryDescriptionBytes {
		return ErrInvalidDescription
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, name, description string) (state.Repository, error) {
	return m.CreateWithOptions(ctx, name, description, CreateOptions{forgetImport: true})
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
	// Hold the repository lock from the existence check through the row write so
	// an initial import cannot configure this id between those steps.
	lock := m.Locks.For(id)
	lock.Lock()
	defer lock.Unlock()
	if taken, err := m.Store.RepositoryNameInUse(ctx, id, time.Now()); err != nil {
		return state.Repository{}, err
	} else if taken {
		return state.Repository{}, ErrNameTaken
	}
	// Results cached for an earlier repository with this name, which an
	// older build or a folder moved by hand may have left, never answer for
	// the new one.
	m.objects.drop(id)
	if _, pending, err := m.Store.RepositoryDeletion(ctx, id); err != nil {
		return state.Repository{}, err
	} else if pending {
		return state.Repository{}, fmt.Errorf("%w: an earlier repository with this name is still being deleted", ErrNameTaken)
	}
	root, err := canonicalRoot(m.RepositoryRoot())
	if err != nil {
		return state.Repository{}, err
	}
	finalPath := filepath.Join(root, id+".git")
	if info, err := os.Lstat(finalPath); err == nil {
		return state.Repository{}, fmt.Errorf("%w (%s)", ErrFolderExists, info.Name())
	} else if !os.IsNotExist(err) {
		return state.Repository{}, fmt.Errorf("inspect repository path: %w", err)
	}
	// A new repository must not inherit the source or credentials of an
	// earlier import that never created it.
	if options.forgetImport {
		if err := m.Store.ForgetUnpublishedImport(ctx, id); errors.Is(err, state.ErrImportNotForgettable) {
			return state.Repository{}, ErrImportInProgress
		} else if err != nil {
			return state.Repository{}, fmt.Errorf("clear earlier import settings: %w", err)
		}
	}

	// Claim before even the temporary folder is written. Another server's
	// claim must not leave creation debris in its storage.
	if err := m.claimStorageForWrite(); err != nil {
		return state.Repository{}, err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return state.Repository{}, err
	}
	temporaryPath := filepath.Join(root, ".owngit-create-"+hex.EncodeToString(suffix))
	if err := state.MkdirPrivate(temporaryPath); err != nil {
		if errors.Is(err, state.ErrPrivateDirectoryExists) {
			return state.Repository{}, fmt.Errorf("create temporary repository directory: generated name already exists: %w", err)
		}
		return state.Repository{}, fmt.Errorf("create temporary repository directory privately: %w", err)
	}
	created := false
	defer func() {
		if !created {
			_ = os.RemoveAll(temporaryPath)
		}
	}()
	if m.creationDirectoryHook != nil {
		m.creationDirectoryHook(temporaryPath)
	}
	if err := state.ProtectPrivatePath(temporaryPath, true); err != nil {
		return state.Repository{}, fmt.Errorf("protect temporary repository directory: %w", err)
	}

	if err := m.InitBareRepository(ctx, temporaryPath, options); err != nil {
		return state.Repository{}, err
	}
	creation, err := captureEmptyCreation(temporaryPath)
	if err != nil {
		return state.Repository{}, fmt.Errorf("inspect new repository: %w", err)
	}
	defer creation.parent.Close()
	if err := publishdir.Rename(ctx, temporaryPath, finalPath); err != nil {
		return state.Repository{}, fmt.Errorf("publish repository directory: %w", err)
	}
	created = true
	repository := state.Repository{ID: id, Name: name, Address: id, Description: strings.TrimSpace(description), CreatedAt: time.Now()}
	// Publication has landed. Finish its durable record even if the client
	// disconnects, but never keep recording alive without a bounded deadline.
	recordCtx, finishRecord := context.WithTimeout(context.WithoutCancel(ctx), creationRecordTimeout)
	recordErr := m.Store.AddRepository(recordCtx, repository)
	finishRecord()
	if err := recordErr; err != nil {
		// A cancelled request or storage error must not remove a directory
		// whose record might have committed. Read independently of the request.
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, accepted, checkErr := m.Store.Repository(checkCtx, id)
		cancel()
		if checkErr != nil {
			return state.Repository{}, fmt.Errorf("record repository: %w; preserve folder at %s because its record could not be checked: %v", err, finalPath, checkErr)
		}
		if accepted {
			return state.Repository{}, fmt.Errorf("record repository: %w; preserve the recorded repository at %s", err, finalPath)
		}
		// No request can use this repository before its row exists. Move
		// this attempt's unchanged empty tree out of the published name,
		// without deleting files that another writer could replace.
		m.failedCreationMu.Lock()
		rollbackErr := creation.rollback(finalPath)
		m.failedCreationMu.Unlock()
		if rollbackErr != nil {
			keptPath := finalPath
			if creation.preserved != "" {
				keptPath = creation.preserved
			}
			return state.Repository{}, fmt.Errorf("record repository: %w; preserve folder at %s: %w", err, keptPath, rollbackErr)
		}
		return state.Repository{}, fmt.Errorf("record repository: %w; the unaccepted empty folder is preserved at %s and may be removed", err, creation.preserved)
	}
	return repository, nil
}

type creationEntry struct {
	name   string
	info   os.FileInfo
	digest [sha256.Size]byte
}

const (
	failedCreateDirectory  = ".owngit-failed-create"
	maximumFailedCreations = 8
	creationRecordTimeout  = 5 * time.Second
)

// emptyCreation pins the storage parent and records this attempt's tree.
// No handle on the child directory is kept across publication or rollback:
// a top-level directory handle would prevent its rename on Windows.
type emptyCreation struct {
	parent    *os.Root
	entries   []creationEntry
	preserved string
}

func captureEmptyCreation(path string) (*emptyCreation, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	root, err := parent.OpenRoot(filepath.Base(path))
	if err != nil {
		parent.Close()
		return nil, err
	}
	entries, inspectErr := creationEntries(root)
	if err := errors.Join(inspectErr, root.Close()); err != nil {
		parent.Close()
		return nil, err
	}
	return &emptyCreation{parent: parent, entries: entries}, nil
}

func creationEntries(root *os.Root) ([]creationEntry, error) {
	var entries []creationEntry
	err := fs.WalkDir(root.FS(), ".", func(name string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !item.IsDir() && !item.Type().IsRegular() {
			return fmt.Errorf("unexpected entry in new repository: %s", name)
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		entry := creationEntry{name: name, info: info}
		if err == nil && info.Mode().IsRegular() {
			hash := sha256.New()
			_, err = io.Copy(hash, file)
			copy(entry.digest[:], hash.Sum(nil))
		}
		if err := errors.Join(err, file.Close()); err != nil {
			return err
		}
		entries = append(entries, entry)
		return nil
	})
	return entries, err
}

func (creation *emptyCreation) unchanged(name string) error {
	// Lstat binds the proof to the directory entry, not a link to the
	// original tree after the owner moved it somewhere else.
	entry, err := creation.parent.Lstat(name)
	if err != nil {
		return err
	}
	if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
		return errors.New("the new repository directory entry was replaced")
	}
	root, err := creation.parent.OpenRoot(name)
	if err != nil {
		return err
	}
	current, inspectErr := creationEntries(root)
	if err := errors.Join(inspectErr, root.Close()); err != nil {
		return err
	}
	if len(current) != len(creation.entries) {
		return errors.New("the new repository is no longer empty and unchanged")
	}
	for index, expected := range creation.entries {
		found := current[index]
		if expected.name != found.name || !os.SameFile(expected.info, found.info) ||
			expected.info.Mode() != found.info.Mode() || expected.digest != found.digest {
			return errors.New("the new repository is no longer empty and unchanged")
		}
	}
	// Recheck the no-follow entry after closing every child handle and before
	// changing its name. If it changed meanwhile, leave its data in place.
	entry, err = creation.parent.Lstat(name)
	if err != nil {
		return err
	}
	if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 || !os.SameFile(creation.entries[0].info, entry) {
		return errors.New("the new repository directory entry was replaced")
	}
	return nil
}

func (creation *emptyCreation) rollback(path string) error {
	name := filepath.Base(path)
	if err := creation.unchanged(name); err != nil {
		return err
	}
	if err := creation.parent.Mkdir(failedCreateDirectory, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create failed-creation folder: %w", err)
	}
	info, err := creation.parent.Lstat(failedCreateDirectory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("the failed-creation folder is not an unlinked directory")
	}
	directory, err := creation.parent.Open(failedCreateDirectory)
	if err != nil {
		return err
	}
	entries, readErr := directory.Readdirnames(maximumFailedCreations)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) >= maximumFailedCreations {
		return fmt.Errorf("%w (%d entries)", ErrFailedCreationLimit, maximumFailedCreations)
	}
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	kept := filepath.Join(failedCreateDirectory, hex.EncodeToString(suffix))
	if _, err := creation.parent.Lstat(kept); !os.IsNotExist(err) {
		return fmt.Errorf("failed-creation destination is not available: %v", err)
	}
	if err := creation.parent.Rename(name, kept); err != nil {
		return fmt.Errorf("preserve unaccepted creation: %w", err)
	}
	creation.preserved = filepath.Join(creation.parent.Name(), kept)
	// Nothing is deleted, including files changed after the snapshot. Verify
	// the landed tree before describing it as the removable empty creation.
	if err := creation.unchanged(kept); err != nil {
		return fmt.Errorf("the preserved creation changed; inspect its contents: %w", err)
	}
	return nil
}

// InitBareRepository initializes a bare repository at directory. It does not
// rename the directory or record a repository row.
func (m *Manager) InitBareRepository(ctx context.Context, directory string, options CreateOptions) error {
	if options.ObjectFormat != "" && options.ObjectFormat != ObjectFormatSHA1 && options.ObjectFormat != ObjectFormatSHA256 {
		return fmt.Errorf("%w: %q", ErrUnsupportedFormat, options.ObjectFormat)
	}
	if err := m.claimStorageForWrite(); err != nil {
		return err
	}
	// A new repository starts on the branch the owner chose in Settings.
	branch, err := m.Store.InitialBranch(ctx)
	if err != nil {
		return err
	}
	initArguments := []string{"init", "--bare", "--initial-branch=" + branch}
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

// ExistingPath returns the storage path of a recorded repository. It refuses a
// repository that is still being prepared with ErrRepositoryPreparing, before
// touching the state or the filesystem.
func (m *Manager) ExistingPath(ctx context.Context, id string) (string, state.Repository, bool, error) {
	if m.Preparing(id) {
		return "", state.Repository{}, false, ErrRepositoryPreparing
	}
	return m.existingPath(ctx, id)
}

// existingPath is ExistingPath without the preparation check, for preparation
// itself and for deletion.
func (m *Manager) existingPath(ctx context.Context, id string) (string, state.Repository, bool, error) {
	repository, exists, err := m.Store.Repository(ctx, id)
	if err != nil || !exists {
		return "", state.Repository{}, exists, err
	}
	path, err := m.StoragePath(id)
	if err != nil {
		return "", state.Repository{}, false, err
	}
	return path, repository, true, nil
}

// StoragePath returns the folder of repository id, which must be a
// directory and not a link. It does not read the state, so a caller that
// already knows the repository is recorded, such as a backup, uses it.
func (m *Manager) StoragePath(id string) (string, error) {
	path, err := m.Path(id)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: repository path must not be a symbolic link", ErrStorageUnavailable)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: repository path is not a directory", ErrStorageUnavailable)
	}
	return path, nil
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

// prepareConcurrency bounds how many repositories startup prepares at once.
// Over a network share each Git process mostly waits on storage, so a few in
// parallel shorten startup without a burst of processes.
const prepareConcurrency = 8

func (m *Manager) prepareExisting(ctx context.Context, hookRuntime *gitexec.Runner) error {
	repositories, err := m.Store.Repositories(ctx)
	if err != nil {
		return err
	}
	errs := make([]error, len(repositories))
	slots := make(chan struct{}, prepareConcurrency)
	var group sync.WaitGroup
	for index, stored := range repositories {
		group.Add(1)
		slots <- struct{}{}
		go func() {
			defer group.Done()
			defer func() { <-slots }()
			errs[index] = m.prepareRepository(ctx, stored.ID, hookRuntime)
		}()
	}
	group.Wait()
	for index, err := range errs {
		if err != nil {
			return fmt.Errorf("prepare repository %q: %w", repositories[index].ID, err)
		}
	}
	return nil
}

func (m *Manager) prepareRepository(ctx context.Context, id string, hookRuntime *gitexec.Runner) error {
	path, _, exists, err := m.existingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return err
	}
	lock := m.Locks.For(id)
	lock.Lock()
	defer lock.Unlock()
	return m.configureLocked(ctx, path, hookRuntime)
}

// configureLocked reads the local configuration once and writes only the
// settings that differ, then refreshes the retention hook. The caller holds
// the repository write lock.
func (m *Manager) configureLocked(ctx context.Context, path string, hookRuntime *gitexec.Runner) error {
	// The hook names this server's runtime, so another server that serves
	// the same folder must keep its own.
	if err := m.claimStorageForWrite(); err != nil {
		return err
	}
	current, err := m.localConfig(ctx, path)
	if err != nil {
		return err
	}
	for _, setting := range repositoryConfig() {
		// A setting is already correct only with exactly one matching value.
		// Anything else is written as before, so a multi-valued key still
		// fails instead of being accepted.
		if values := current[strings.ToLower(setting[0])]; len(values) == 1 && values[0] == setting[1] {
			continue
		}
		if _, err := m.Git.Run(ctx, path, nil, "--git-dir", ".", "config", "--local", setting[0], setting[1]); err != nil {
			return err
		}
	}
	return writeRetentionHook(path, hookRuntime)
}

// localConfig returns the repository's local configuration values by key.
// Git prints section and variable names in lower case.
func (m *Manager) localConfig(ctx context.Context, repositoryPath string) (map[string][]string, error) {
	result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "config", "--local", "--list", "-z")
	if err != nil {
		return nil, err
	}
	values := make(map[string][]string)
	for _, entry := range strings.Split(string(result.Stdout), "\x00") {
		if entry == "" {
			continue
		}
		key, value, _ := strings.Cut(entry, "\n")
		values[key] = append(values[key], value)
	}
	return values, nil
}

// RefWriteEnvironment returns the variables that give a push to repository
// id the kept history, default branch protection and extra ref namespaces
// it follows now (see writeRetentionHook). A choice that cannot be read is
// a state.PolicyError.
func (m *Manager) RefWriteEnvironment(ctx context.Context, id string) ([]string, error) {
	writes, err := m.Store.RefWrites(ctx, id)
	if err != nil {
		return nil, err
	}
	keep, protect := "off", "off"
	if writes.KeepHistory {
		keep = "on"
	}
	if writes.ProtectDefaultBranch {
		protect = "on"
	}
	// The namespaces hold no space (state.ValidateExtraRefPrefixes), so the
	// hook splits them at spaces.
	return []string{"OWNGIT_KEEP_HISTORY=" + keep, "OWNGIT_PROTECT_DEFAULT_BRANCH=" + protect,
		"OWNGIT_EXTRA_REF_PREFIXES=" + strings.Join(writes.ExtraRefPrefixes, " ")}, nil
}

// writeRetentionHook writes the update hook that checks every ref update a
// push makes. Variables that OwnGit's Git service sets for each push
// (RefWriteEnvironment) carry the repository's choices, read when the push
// starts: OWNGIT_PROTECT_DEFAULT_BRANCH=on refuses rewriting or deleting the
// branch HEAD names, OWNGIT_KEEP_HISTORY=off leaves the previous tip of an
// overwritten or deleted branch or tag unkept, and OWNGIT_EXTRA_REF_PREFIXES
// lists the namespaces beyond branches and tags whose refs a push may
// change, without kept history or protection. Without them, as for a push
// that does not go through OwnGit, history is kept, nothing is protected
// and only branches and tags are accepted. OWNGIT_NAME_CONFLICTS_FILE names a file that lists, one per
// line, the refs of the push that the hook refuses because a file system
// can treat their name or folder as another ref's (RefNameConflicts; the
// Git service allows deleting such a ref that exists), and the hook
// refuses every ref if the file cannot be read.
func writeRetentionHook(repositoryPath string, runner *gitexec.Runner) error {
	repositoryDir, err := os.Open(repositoryPath)
	if err != nil {
		return fmt.Errorf("open repository for hook refresh: %w", err)
	}
	defer repositoryDir.Close()
	hooksPath := filepath.Join(repositoryPath, "hooks")
	if err := os.Mkdir(hooksPath, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create hooks directory: %w", err)
	}
	// Open the child relative to the held repository without following links,
	// then make that exact directory private before opening any hook in it.
	hooks, err := state.OpenOwnFolderIn(repositoryDir, "hooks")
	if err != nil {
		return hookEntryError(hooksPath, "hooks", true, err)
	}
	defer hooks.Close()
	if err := state.ProtectPrivateHandle(hooks, true); err != nil {
		return fmt.Errorf("protect hooks directory: %w", err)
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
case "$ref" in
  refs/heads/*) kind=heads; short=${ref#refs/heads/} ;;
  refs/tags/*) kind=tags; short=${ref#refs/tags/} ;;
  refs/owngit/*) echo "OwnGit reserved refs cannot be changed" >&2; exit 1 ;;
  *)
    kind=
    for prefix in ${OWNGIT_EXTRA_REF_PREFIXES:-}; do
      case "$ref" in "$prefix"*) kind=extra; short=$ref ;; esac
    done
    test -n "$kind" || { echo "OwnGit accepts branches, tags and the ref namespaces listed in the repository's settings. An administrator can add a namespace such as refs/notes/ in the repository's Settings tab, under Other ref namespaces, or with owngit repo settings set --extra-ref-prefixes." >&2; exit 1; } ;;
esac
if test -n "${OWNGIT_NAME_CONFLICTS_FILE:-}"; then
  test -r "$OWNGIT_NAME_CONFLICTS_FILE" || { echo "OwnGit could not check the pushed ref names against the existing ones" >&2; exit 1; }
  while IFS= read -r conflict; do
    if test "$conflict" = "$ref"; then
      printf 'OwnGit refused changing %%s because another branch or tag, or one of its folders, has a name that some file systems treat as the same, for example one that differs only in letter case or accent encoding. Use a clearly different name. If both names already exist, delete one with: git push origin --delete %%s\n' "$ref" "$short" >&2
      exit 1
    fi
  done <"$OWNGIT_NAME_CONFLICTS_FILE"
fi
case "$old" in ''|*[!0-9a-f]*) echo "invalid old object ID" >&2; exit 1 ;; esac
case "$new" in ''|*[!0-9a-f]*) echo "invalid new object ID" >&2; exit 1 ;; esac
case "${#old}:${#new}" in
  40:40|64:64) ;;
  *) echo "invalid object ID width" >&2; exit 1 ;;
esac
is_null_oid() {
  case "$1" in
    *[!0]*) return 1 ;;
    *) return 0 ;;
  esac
}
run_git() {
  /usr/bin/env -i PATH=%s HOME=%s XDG_CONFIG_HOME=%s GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=%s GIT_CONFIG_GLOBAL=%s GIT_TERMINAL_PROMPT=0 LC_ALL=C LANG=C TZ=UTC TMPDIR=%s GIT_DIR="$GIT_DIR" %s -c core.precomposeUnicode=false "$@"
}
run_git_objects() {
  if test -z "${GIT_OBJECT_DIRECTORY:-}"; then run_git "$@"; return; fi
  /usr/bin/env -i PATH=%s HOME=%s XDG_CONFIG_HOME=%s GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=%s GIT_CONFIG_GLOBAL=%s GIT_TERMINAL_PROMPT=0 LC_ALL=C LANG=C TZ=UTC TMPDIR=%s GIT_DIR="$GIT_DIR" GIT_OBJECT_DIRECTORY="$GIT_OBJECT_DIRECTORY" GIT_ALTERNATE_OBJECT_DIRECTORIES="$GIT_ALTERNATE_OBJECT_DIRECTORIES" %s -c core.precomposeUnicode=false "$@"
}
# A symbolic ref would pass the update on to its target, which may be a
# ref this hook protects or reserves. Only HEAD is symbolic by design.
if run_git symbolic-ref --quiet "$ref" >/dev/null 2>&1; then
  printf 'OwnGit refused updating %%s because it is a symbolic ref that points to another ref. Push to the ref it points to directly.\n' "$ref" >&2
  exit 1
else
  status=$?
  test "$status" = 1 || { echo "OwnGit could not check whether the ref is symbolic" >&2; exit 1; }
fi
is_null_oid "$old" && exit 0
actual=$(run_git show-ref --verify --hash "$ref" 2>/dev/null) || { echo "current ref is missing" >&2; exit 1; }
test "$actual" = "$old" || { echo "current ref changed concurrently" >&2; exit 1; }
run_git_objects cat-file -e "$old^{object}" || { echo "old object is unavailable" >&2; exit 1; }
test "$kind" = extra && exit 0
if test "$kind" = heads && ! is_null_oid "$new"; then
  if run_git_objects merge-base --is-ancestor "$old" "$new"; then
    exit 0
  else
    status=$?
    test "$status" = 1 || { echo "could not compare branch history" >&2; exit 1; }
  fi
fi
if test "$kind" = heads && test "${OWNGIT_PROTECT_DEFAULT_BRANCH:-}" = on; then
  default=$(run_git symbolic-ref --quiet HEAD) || {
    status=$?
    test "$status" = 1 || { echo "could not read the default branch" >&2; exit 1; }
    default=
  }
  if test "$ref" = "$default"; then
    change="a push that is not a fast-forward"
    is_null_oid "$new" && change="deleting it"
    printf 'OwnGit protects the default branch %%s and refused %%s. The repository settings can turn this protection off.\n' "$short" "$change" >&2
    exit 1
  fi
fi
test "${OWNGIT_KEEP_HISTORY:-}" = off && exit 0
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
	if err := writeHookFile(hooks, "update", updateScript); err != nil {
		return err
	}
	for _, obsolete := range []string{"pre-receive", "reference-transaction"} {
		if err := os.Remove(filepath.Join(hooks.Name(), obsolete)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove obsolete Git hook %s: %w", obsolete, err)
		}
	}
	return nil
}

func writeHookFile(hooks *os.File, name, content string) error {
	file, err := state.OpenOwnFile(hooks, name, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return hookEntryError(filepath.Join(hooks.Name(), name), "hooks/"+name, false, err)
	}
	defer file.Close()
	if err := state.ProtectPrivateHandle(file, false); err != nil {
		return fmt.Errorf("protect Git hook %s: %w", name, err)
	}
	// Startup refreshes every hook. Read only enough to decide whether this
	// script is already exact, without trusting an existing file's size.
	existing, err := io.ReadAll(io.LimitReader(file, int64(len(content)+1)))
	if err != nil {
		return fmt.Errorf("read Git hook %s: %w", name, err)
	}
	if string(existing) != content {
		if err := file.Truncate(0); err != nil {
			return fmt.Errorf("truncate Git hook %s: %w", name, err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind Git hook %s: %w", name, err)
		}
		if _, err := io.WriteString(file, content); err != nil {
			return fmt.Errorf("write Git hook %s: %w", name, err)
		}
	}
	if err := file.Chmod(0o700); err != nil {
		return fmt.Errorf("make Git hook %s executable: %w", name, err)
	}
	return nil
}

func hookEntryError(path, label string, directory bool, cause error) error {
	reason := ""
	if info, err := os.Lstat(path); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			reason = "is a link"
		case directory && !info.IsDir():
			reason = "is not a plain folder"
		case !directory && !info.Mode().IsRegular():
			reason = "is not a plain file"
		}
	}
	if strings.Contains(cause.Error(), "belongs to another account") {
		reason = "is owned by another account"
	}
	if strings.Contains(cause.Error(), "has another name") {
		reason = "has another filesystem name"
	}
	if reason == "" {
		return fmt.Errorf("open managed Git %s without following links: %w", label, cause)
	}
	return fmt.Errorf("managed Git %s %s; move %s out of the repository folder so OwnGit can recreate it on the next retry: %w", label, reason, path, cause)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
