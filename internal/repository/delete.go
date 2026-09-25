package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/publishdir"
	"owngit/internal/state"
)

// DeleteMode selects what happens to a deleted repository's files.
type DeleteMode string

const (
	// DeleteKeepFiles removes the repository from OwnGit and moves its
	// directory, unchanged, under the hidden .owngit-removed folder.
	DeleteKeepFiles DeleteMode = "keep_files"
	// DeleteFiles removes the repository from OwnGit and deletes its
	// directory, including retained history.
	DeleteFiles DeleteMode = "delete_files"
)

const (
	removedDirectoryName    = ".owngit-removed"
	deletingDirectoryPrefix = ".owngit-delete-"
	// deletionMarkerPrefix names the file that stays in the repository folder
	// while a deletion of that ID is unfinished. It proves that the folder a
	// later start inspects is the storage the deletion began on, so a missing
	// directory on an unmounted or replaced storage is never mistaken for a
	// finished move or removal.
	deletionMarkerPrefix = ".owngit-deletion-"
	// deleteLockWait bounds how long Delete waits for Git operations that
	// already hold the repository before it reports the repository busy.
	deleteLockWait = 2 * time.Second
)

var (
	ErrRepositoryNotFound = errors.New("repository not found")
	ErrRepositoryBusy     = errors.New("repository is busy")
	// The busy reasons wrap ErrRepositoryBusy, so callers can test either.
	ErrImportRunning = fmt.Errorf("%w: an import is running", ErrRepositoryBusy)
	ErrCheckRunning  = fmt.Errorf("%w: a check is running", ErrRepositoryBusy)
	// ErrCheckCleanupPending reports a check container that OwnGit has not
	// yet confirmed as removed; a later start retries that cleanup.
	ErrCheckCleanupPending = fmt.Errorf("%w: a check container still awaits cleanup", ErrRepositoryBusy)
	ErrRepositoryInUse     = fmt.Errorf("%w: another Git operation is using the repository", ErrRepositoryBusy)
	// ErrDeleteIncomplete reports that the repository is already removed from
	// OwnGit but its directory was not yet moved or fully deleted. Retrying
	// Delete, or the next start, finishes the work.
	ErrDeleteIncomplete = errors.New("the repository was removed from OwnGit, but its files were not fully moved or deleted yet")
	ErrBranchNotFound   = errors.New("branch not found")
)

// DeleteResult describes a completed deletion.
type DeleteResult struct {
	// KeptPath is the absolute path of the moved directory in keep mode and
	// empty otherwise.
	KeptPath string
}

// Delete removes a repository from OwnGit. The records go first, in one
// transaction that also stores a durable intent; the directory is then moved
// aside or deleted. If the directory step fails or the process stops, a later
// Delete call for the same name or the next start completes it. A call for a
// name with an unfinished deletion resumes that deletion in its original mode
// and then deletes a newer repository with the name, if one exists.
//
// Delete holds the repository write lock throughout. Git requests that were
// already waiting for the lock run afterwards against a path that no longer
// holds a repository and fail; later requests find no repository.
func (m *Manager) Delete(ctx context.Context, id string, mode DeleteMode) (DeleteResult, error) {
	if mode != DeleteKeepFiles && mode != DeleteFiles {
		return DeleteResult{}, fmt.Errorf("invalid delete mode %q", mode)
	}
	if ValidateID(id) != nil {
		return DeleteResult{}, ErrRepositoryNotFound
	}
	if m.Locks == nil {
		return DeleteResult{}, errors.New("repository locks are unavailable")
	}
	if err := deletionBusyError(m.Store.RepositoryDeletionBusy(ctx, id)); err != nil {
		return DeleteResult{}, err
	}
	// Maintenance never delays a deletion: a running one is stopped.
	m.stopMaintenanceOf(id)
	lock := m.Locks.For(id)
	if err := lockWithin(ctx, lock, deleteLockWait); err != nil {
		return DeleteResult{}, err
	}
	defer lock.Unlock()
	root, err := canonicalRoot(m.RepositoryRoot())
	if err != nil {
		return DeleteResult{}, err
	}
	if pending, exists, err := m.Store.RepositoryDeletion(ctx, id); err != nil {
		return DeleteResult{}, err
	} else if exists {
		result, err := m.finishDeletion(ctx, root, pending)
		if err != nil {
			return result, err
		}
		// AddRepository refuses the name while the intent exists, but an
		// older build ignores the intent and may have recorded a new
		// repository. Only then does the requested deletion continue with it.
		if _, taken, err := m.Store.Repository(ctx, id); err != nil || !taken {
			return result, err
		}
	}
	// A repository that is still being prepared can be deleted, so this
	// lookup skips the preparation check.
	if _, _, exists, err := m.existingPath(ctx, id); err != nil {
		return DeleteResult{}, err
	} else if !exists {
		return DeleteResult{}, ErrRepositoryNotFound
	}
	moved, err := m.deletionTarget(root, id, mode)
	if err != nil {
		return DeleteResult{}, err
	}
	token, err := randomHex()
	if err != nil {
		return DeleteResult{}, err
	}
	deletion := state.RepositoryDeletion{
		RepositoryID: id, Mode: string(mode), Phase: state.RepositoryDeletionPending,
		Root: root, Moved: moved, Marker: token, CreatedAt: m.deletionNow(),
	}
	// The marker is durable before the commit point, so every recorded
	// deletion has one on its storage.
	if err := writeDeletionMarker(root, id, token); err != nil {
		return DeleteResult{}, err
	}
	if err := m.Store.BeginRepositoryDeletion(ctx, deletion); err != nil {
		// No intent exists for the ID, so the marker is unused.
		_ = os.Remove(deletionMarkerPath(root, id))
		if errors.Is(err, state.ErrRepositoryNotFound) {
			return DeleteResult{}, ErrRepositoryNotFound
		}
		return DeleteResult{}, deletionBusyError(err)
	}
	// The repository is gone from OwnGit, so its preparation and maintenance,
	// if any, stop here under the repository lock and cannot touch a later
	// repository with the same ID.
	m.CancelPreparation(id)
	m.forgetMaintenance(id)
	if err := m.deletionStep("recorded"); err != nil {
		return DeleteResult{}, fmt.Errorf("%w: %v", ErrDeleteIncomplete, err)
	}
	return m.finishDeletion(ctx, root, deletion)
}

// ReconcileDeletions completes deletions that an earlier process started but
// did not finish. serve calls it before accepting requests. A deletion that
// cannot be completed safely stays recorded and is reported.
func (m *Manager) ReconcileDeletions(ctx context.Context) error {
	deletions, err := m.Store.RepositoryDeletions(ctx)
	if err != nil || len(deletions) == 0 {
		return err
	}
	root, err := canonicalRoot(m.RepositoryRoot())
	if err != nil {
		return fmt.Errorf("complete unfinished repository deletions: %w", err)
	}
	var problems []error
	for _, deletion := range deletions {
		lock := m.Locks.For(deletion.RepositoryID)
		lock.Lock()
		_, err := m.finishDeletion(ctx, root, deletion)
		lock.Unlock()
		if err != nil {
			problems = append(problems, fmt.Errorf("repository %q: %w", deletion.RepositoryID, err))
		}
	}
	return errors.Join(problems...)
}

// finishDeletion advances a recorded deletion to completion. The caller holds
// the repository write lock. Every step is idempotent, so it serves both the
// first attempt and later resumption.
func (m *Manager) finishDeletion(ctx context.Context, root string, deletion state.RepositoryDeletion) (DeleteResult, error) {
	incomplete := func(err error) (DeleteResult, error) {
		return DeleteResult{}, fmt.Errorf("%w: %v", ErrDeleteIncomplete, err)
	}
	m.snapshots.drop(deletion.RepositoryID)
	m.objects.drop(deletion.RepositoryID)
	if deletion.Root != root {
		return incomplete(fmt.Errorf("the repository folder changed from %s since the deletion began; its directory is left in place", deletion.Root))
	}
	movedPath, err := deletionMovedPath(root, deletion)
	if err != nil {
		return incomplete(err)
	}
	if err := checkDeletionMarker(root, deletion); err != nil {
		return incomplete(err)
	}
	if deletion.Phase == state.RepositoryDeletionPending {
		if err := m.moveDeletedRepository(ctx, root, deletion, movedPath); err != nil {
			return incomplete(err)
		}
		if err := m.deletionStep("moved"); err != nil {
			return incomplete(err)
		}
		if deletion.Mode == state.RepositoryDeletionKeepFiles {
			if err := m.completeDeletion(ctx, root, deletion.RepositoryID); err != nil {
				return incomplete(err)
			}
			if _, err := state.LstatIdentity(movedPath); err != nil {
				// The directory was already gone when the deletion resumed.
				return DeleteResult{}, nil
			}
			return DeleteResult{KeptPath: movedPath}, nil
		}
		if err := m.Store.MarkRepositoryDeletionMoved(ctx, deletion.RepositoryID); err != nil {
			return incomplete(err)
		}
		if err := m.deletionStep("marked"); err != nil {
			return incomplete(err)
		}
	}
	// Only the renamed directory is removed from here on. The repository path
	// may already belong to a new repository with the same name.
	if info, err := state.LstatIdentity(movedPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return incomplete(errors.New("the directory being deleted was replaced by another kind of file; it is left in place"))
		}
		if err := os.RemoveAll(movedPath); err != nil {
			return incomplete(fmt.Errorf("delete repository files: %w", err))
		}
	} else if !os.IsNotExist(err) {
		return incomplete(err)
	}
	syncDirectory(root)
	if err := m.deletionStep("removed"); err != nil {
		return incomplete(err)
	}
	if err := m.completeDeletion(ctx, root, deletion.RepositoryID); err != nil {
		return incomplete(err)
	}
	return DeleteResult{}, nil
}

// completeDeletion removes the intent and then the marker. The marker goes
// last: if it went first and the process stopped, the recorded deletion
// would find no marker and never finish. A marker left behind here is
// harmless, because no intent refers to it and its token matches no later
// deletion.
func (m *Manager) completeDeletion(ctx context.Context, root, id string) error {
	if err := m.Store.FinishRepositoryDeletion(ctx, id); err != nil {
		return err
	}
	if err := m.deletionStep("finished"); err != nil {
		return err
	}
	_ = os.Remove(deletionMarkerPath(root, id))
	return nil
}

func deletionMarkerPath(root, id string) string {
	return filepath.Join(root, deletionMarkerPrefix+id)
}

// deletionMarkerTokenLine is the line of a marker file that names its
// deletion. Other lines are ignored, so an owner can recreate a marker from the
// token alone.
func deletionMarkerTokenLine(token string) string {
	return "token " + token
}

// writeDeletionMarker writes a new marker holding token and makes it durable.
// A regular file left by an earlier deletion of the ID is replaced; anything
// else in its place is refused.
func writeDeletionMarker(root, id, token string) error {
	markerPath := deletionMarkerPath(root, id)
	if info, err := state.LstatIdentity(markerPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file, not a link or directory", markerPath)
		}
		// No intent exists for the ID, so the old marker belongs to no deletion.
		if err := os.Remove(markerPath); err != nil {
			return fmt.Errorf("replace deletion marker: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create deletion marker: %w", err)
	}
	_, writeErr := file.WriteString("OwnGit keeps this file while a deletion of repository " + id + " is unfinished.\n" + deletionMarkerTokenLine(token) + "\n")
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		_ = os.Remove(markerPath)
		return fmt.Errorf("write deletion marker: %w", err)
	}
	syncDirectory(root)
	return nil
}

// checkDeletionMarker refuses to continue a deletion unless its marker, with
// the deletion's token, is in the repository folder. Without it the folder
// may be an unmounted mount point, a replaced disk or an older copy of the
// storage, and a missing directory there says nothing about the deletion.
func checkDeletionMarker(root string, deletion state.RepositoryDeletion) error {
	markerPath := deletionMarkerPath(root, deletion.RepositoryID)
	refuse := func(reason string) error {
		return fmt.Errorf("deletion marker %s %s, so the repository storage may be unavailable or not the one the deletion began on; the deletion stays recorded and continues at a later start once the marker holds the line %q",
			markerPath, reason, deletionMarkerTokenLine(deletion.Marker))
	}
	info, err := state.LstatIdentity(markerPath)
	if os.IsNotExist(err) {
		return refuse("is missing")
	}
	if err != nil {
		return fmt.Errorf("inspect deletion marker: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("deletion marker %s is not a regular file; the deletion stays recorded", markerPath)
	}
	file, err := os.Open(markerPath)
	if err != nil {
		return fmt.Errorf("open deletion marker: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect deletion marker: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return fmt.Errorf("deletion marker %s changed while it was read; the deletion stays recorded", markerPath)
	}
	content, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return fmt.Errorf("read deletion marker: %w", err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == deletionMarkerTokenLine(deletion.Marker) {
			return nil
		}
	}
	return refuse("belongs to another deletion")
}

// moveDeletedRepository renames the repository directory to its recorded
// name. It does nothing when that name already exists, because the rename
// happened before an interruption, and it never touches a repository path
// that a new repository row owns.
func (m *Manager) moveDeletedRepository(ctx context.Context, root string, deletion state.RepositoryDeletion, movedPath string) error {
	if _, err := state.LstatIdentity(movedPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, exists, err := m.Store.Repository(ctx, deletion.RepositoryID); err != nil {
		return err
	} else if exists {
		return errors.New("a new repository with this name exists and the earlier directory was not moved; move it manually")
	}
	repositoryPath, err := m.Path(deletion.RepositoryID)
	if err != nil {
		return err
	}
	info, err := state.LstatIdentity(repositoryPath)
	if os.IsNotExist(err) {
		// The marker shows this is the deletion's storage, so the directory
		// was already moved or removed by hand. Nothing is left to move.
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("the repository path is not a directory; it is left in place")
	}
	parent := filepath.Dir(movedPath)
	if parent != root {
		if err := ensureRemovedDirectory(parent); err != nil {
			return err
		}
	}
	if err := publishdir.Rename(ctx, repositoryPath, movedPath); err != nil {
		return fmt.Errorf("move repository directory: %w", err)
	}
	syncDirectory(root)
	if parent != root {
		syncDirectory(parent)
	}
	return nil
}

// deletionTarget chooses the recorded name for the repository directory.
func (m *Manager) deletionTarget(root, id string, mode DeleteMode) (string, error) {
	if mode == DeleteFiles {
		suffix, err := randomHex()
		if err != nil {
			return "", err
		}
		return deletingDirectoryPrefix + suffix, nil
	}
	removed := filepath.Join(root, removedDirectoryName)
	if err := ensureRemovedDirectory(removed); err != nil {
		return "", err
	}
	stamp := m.deletionNow().UTC().Format("20060102T150405Z")
	for attempt := 1; attempt <= 1000; attempt++ {
		name := id + "-" + stamp
		if attempt > 1 {
			name += "-" + strconv.Itoa(attempt)
		}
		name += ".git"
		if _, err := state.LstatIdentity(filepath.Join(removed, name)); os.IsNotExist(err) {
			return removedDirectoryName + "/" + name, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not choose a free name under " + removedDirectoryName)
}

// deletionMovedPath validates a recorded target before any filesystem use and
// returns its absolute path inside root.
func deletionMovedPath(root string, deletion state.RepositoryDeletion) (string, error) {
	moved := deletion.Moved
	valid := false
	switch deletion.Mode {
	case state.RepositoryDeletionKeepFiles:
		directory, name := path.Split(moved)
		valid = directory == removedDirectoryName+"/" && path.Clean(moved) == moved &&
			strings.HasPrefix(name, deletion.RepositoryID+"-") && strings.HasSuffix(name, ".git") && !strings.ContainsAny(name, `/\`)
	case state.RepositoryDeletionDeleteFiles:
		suffix := strings.TrimPrefix(moved, deletingDirectoryPrefix)
		valid = strings.HasPrefix(moved, deletingDirectoryPrefix) && len(suffix) == 32 && strings.Trim(suffix, "0123456789abcdef") == ""
	}
	if !valid {
		return "", fmt.Errorf("recorded deletion target %q is not an OwnGit deletion name", moved)
	}
	return filepath.Join(root, filepath.FromSlash(moved)), nil
}

// ensureRemovedDirectory creates the hidden folder for kept repositories and
// refuses a symbolic link or file in its place.
func ensureRemovedDirectory(directory string) error {
	if err := os.Mkdir(directory, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create %s: %w", removedDirectoryName, err)
	}
	info, err := state.LstatIdentity(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a directory, not a link or file", removedDirectoryName)
	}
	return nil
}

// randomHex returns 32 lowercase hexadecimal characters.
func randomHex() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func deletionBusyError(err error) error {
	switch {
	case errors.Is(err, state.ErrRepositoryDeletionImportActive):
		return ErrImportRunning
	case errors.Is(err, state.ErrRepositoryDeletionCheckActive):
		return ErrCheckRunning
	case errors.Is(err, state.ErrRepositoryDeletionCheckCleanup):
		return ErrCheckCleanupPending
	}
	return err
}

// readLock takes the repository read lock for a request unless ctx ends
// first. The error then wraps ErrRepositoryInUse and the context error, so a
// page can say that another Git operation holds the repository.
func readLock(ctx context.Context, lock *gitexec.RepositoryLock) error {
	if err := lock.RLockContext(ctx); err != nil {
		return fmt.Errorf("%w (%w)", ErrRepositoryInUse, err)
	}
	return nil
}

// InUse reports whether a Git operation holds or waits for the write lock of
// repository id, so a reader would have to wait.
func (m *Manager) InUse(id string) bool {
	lock := m.Locks.For(id)
	if !lock.TryRLock() {
		return true
	}
	lock.RUnlock()
	return false
}

// writeLock is readLock for the write lock.
func writeLock(ctx context.Context, lock *gitexec.RepositoryLock) error {
	if err := lock.LockContext(ctx); err != nil {
		return fmt.Errorf("%w (%w)", ErrRepositoryInUse, err)
	}
	return nil
}

// lockWithin takes the write lock unless wait or ctx ends first.
func lockWithin(ctx context.Context, lock *gitexec.RepositoryLock, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for !lock.TryLock() {
		if time.Now().After(deadline) {
			return ErrRepositoryInUse
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (m *Manager) deletionNow() time.Time {
	if m.deletionClock != nil {
		return m.deletionClock()
	}
	return time.Now()
}

func (m *Manager) deletionStep(step string) error {
	if m.deletionHook != nil {
		return m.deletionHook(step)
	}
	return nil
}

// syncDirectory makes a rename durable where the platform allows it. Some
// network file systems refuse directory sync, and Windows has none, so a
// failure is ignored. If power loss then undoes a rename, the directory stays
// at its repository path outside OwnGit, and Create refuses that name until
// the owner moves it.
func syncDirectory(directory string) {
	if runtime.GOOS == "windows" {
		return
	}
	if file, err := os.Open(directory); err == nil {
		_ = file.Sync()
		_ = file.Close()
	}
}
