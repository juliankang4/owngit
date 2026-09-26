package importsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"owngit/internal/publishdir"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// An initial import builds the destination at an unpublished path and records
// the repository row only after objects, refs, and HEAD are in place. A crash
// therefore leaves either nothing discoverable or a complete repository.
//
// Order:
//  1. Refuse an existing repository row or final path before ConfigureSource.
//  2. Record the ownership row before creating the directory.
//  3. Initialize, index, and publish refs, HEAD, retention, and the receipt
//     into that unpublished directory.
//  4. Under the repository lock, refuse a destination that appeared, then
//     rename once to <id>.git and read the result back.
//  5. Record the repository row and read it back. A failed rename or row write
//     is unresolved, never success.
//
// ExistingPath, the repository list, and Git HTTP all require the repository
// row, so they cannot see the unpublished directory. Refresh publication does
// not use this path.
const (
	unpublishedDirectoryPrefix = ".owngit-create-"
	initialMarkerName          = ".owngit-initial.json"
	initialMarkerVersion       = 1
	maxInitialMarkerSize       = 4096

	// initialCleanupTimeout bounds the removal of an unpublished destination
	// after the run stopped. The cleanup runs without the run's cancellation,
	// so a cancelled first import still leaves nothing behind.
	initialCleanupTimeout = time.Minute
)

// cleanupContext keeps the values of ctx but not its cancellation, bounded by
// initialCleanupTimeout.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), initialCleanupTimeout)
}

type initialMarker struct {
	Version      int    `json:"version"`
	Name         string `json:"name"`
	RootID       string `json:"root_id"`
	RunID        string `json:"run_id"`
	RepositoryID string `json:"repository_id"`
	Token        string `json:"token"`
	CreatedAt    int64  `json:"created_at"`
}

type initialDestination struct {
	storageRoot  string
	rootID       string
	generation   string
	name         string
	path         string
	finalPath    string
	token        string
	repositoryID string
	runID        string
	rowRecorded  bool
	// released is set once the unpublished directory was removed and its
	// ownership row released.
	released bool
}

func newUnpublishedDirectoryName() (string, error) {
	id, err := newImportID()
	if err != nil {
		return "", err
	}
	name := unpublishedDirectoryPrefix + id
	if err := repository.ValidateID(name); err == nil {
		return "", errors.New("unpublished destination name validated as a repository ID")
	}
	if !ownedUnpublishedName(name) {
		return "", errors.New("unpublished destination name is not task-owned")
	}
	return name, nil
}

func ownedUnpublishedName(name string) bool {
	if !strings.HasPrefix(name, unpublishedDirectoryPrefix) || strings.ContainsAny(name, `/\`) {
		return false
	}
	return len(name) == len(unpublishedDirectoryPrefix)+32 && isLowerHexString(name[len(unpublishedDirectoryPrefix):])
}

func unpublishedInitialName(name string) bool {
	return strings.HasPrefix(name, unpublishedDirectoryPrefix) && name != unpublishedDirectoryPrefix && !strings.ContainsAny(name, `/\`)
}

func ownerRecoveryMessage(finalPath string) string {
	return fmt.Sprintf("record repository (the new bare repository remains at %s for owner recovery)", finalPath)
}

func ownerRecoveryProblem(finalPath string, cause error) *Problem {
	return newProblem(CodeUnresolved, ownerRecoveryMessage(finalPath), cause)
}

// destinationTaken reports whether a repository row, an unfinished deletion
// of the same name, or a final directory already exists. It does not create,
// rename, or remove anything.
func (s *Service) destinationTaken(ctx context.Context, repositoryID string) (bool, error) {
	if s.Store == nil || s.Repositories == nil {
		return false, newProblem(CodeRuntimeUnavailable, "import configuration runtime is unavailable", nil)
	}
	if _, exists, err := s.Store.Repository(ctx, repositoryID); err != nil {
		return false, runStateReadProblem("repository could not be read", err)
	} else if exists {
		return true, nil
	}
	if _, pending, err := s.Store.RepositoryDeletion(ctx, repositoryID); err != nil {
		return false, runStateReadProblem("repository deletion state could not be read", err)
	} else if pending {
		return true, nil
	}
	finalPath, err := s.Repositories.Path(repositoryID)
	if err != nil {
		return false, newProblem(CodeInvalidSource, err.Error(), err)
	}
	if _, err := os.Lstat(finalPath); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, newProblem(CodeRepositoryMissing, "repository path could not be inspected", err)
	}
	return false, nil
}

func (s *Service) prepareInitialDestination(ctx context.Context, run *runState) (string, error) {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return "", err
	}
	root, err := s.currentRuntime(run.runtimeGeneration)
	if err != nil {
		return "", err
	}
	storageRoot, err := s.Repositories.CanonicalStorageRoot()
	if err != nil {
		return "", newProblem(CodeRepositoryMissing, "repository storage is unavailable", err)
	}
	name, err := newUnpublishedDirectoryName()
	if err != nil {
		return "", newProblem(CodeStateUnavailable, "unpublished destination name could not be generated", err)
	}
	token, err := newStagingToken()
	if err != nil {
		return "", newProblem(CodeStateUnavailable, "unpublished destination token could not be generated", err)
	}
	now := s.clock()
	record := state.ImportInitialDestination{
		Name: name, RepositoryID: run.run.RepositoryID, RunID: run.run.ID, RootID: root.rootID, Token: token,
		DisplayName: run.name, Description: run.description, State: state.ImportInitialPreparing, CreatedAt: now,
	}
	// The record and its read-back do not follow the run's cancellation (see
	// recordContext). A run stopped meanwhile still creates the directory it
	// now owns, and the next step that uses the run's context reports the
	// stop and removes it, so no record is left without its directory.
	if s.beforeRecord != nil {
		s.beforeRecord(ctx, "initial destination")
	}
	recordCtx, cancelRecord := recordContext(ctx)
	defer cancelRecord()
	if err := s.Store.RegisterImportInitialDestination(recordCtx, record); err != nil {
		return "", newProblem(CodeStateUnavailable, "unpublished destination ownership could not be recorded", err)
	}
	stored, exists, err := s.Store.ImportInitialDestination(recordCtx, name)
	if err != nil || !exists || stored.Token != token || stored.RunID != run.run.ID || stored.RootID != root.rootID || stored.State != state.ImportInitialPreparing {
		return "", newProblem(CodeUnresolved, "unpublished destination ownership could not be read back", err)
	}
	path := filepath.Join(storageRoot, name)
	if filepath.Dir(path) != storageRoot {
		return "", newProblem(CodeRepositoryMissing, "unpublished destination path escapes the repository root", nil)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		cleanupCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = s.setInitialDestinationState(cleanupCtx, name, state.ImportInitialUnknown, "directory name was already present", now)
		return "", newProblem(CodeRepositoryTaken, "unpublished destination path already exists and was not changed", err)
	}
	dest := &initialDestination{
		storageRoot: storageRoot, rootID: root.rootID, generation: run.runtimeGeneration,
		name: name, path: path, token: token, repositoryID: run.run.RepositoryID, runID: run.run.ID,
	}
	if s.afterInitialDirectoryCreated != nil {
		s.afterInitialDirectoryCreated()
	}
	marker := initialMarker{
		Version: initialMarkerVersion, Name: name, RootID: root.rootID, RunID: run.run.ID,
		RepositoryID: run.run.RepositoryID, Token: token, CreatedAt: now.Unix(),
	}
	if err := writeInitialMarker(path, marker); err != nil {
		return "", s.discardInitialDestination(ctx, dest, now, err)
	}
	if err := s.Repositories.InitBareRepository(ctx, path, repository.CreateOptions{ObjectFormat: run.objectFormat}); err != nil {
		return "", s.discardInitialDestination(ctx, dest, now, err)
	}
	run.initialDestination = dest
	return path, nil
}

// discardInitialDestination removes a destination whose preparation failed.
// ctx is the run's context; the removal does not use its cancellation, and a
// failure the run's own stop caused is reported as that stop.
func (s *Service) discardInitialDestination(ctx context.Context, dest *initialDestination, now time.Time, cause error) error {
	cleanupCtx, cancel := cleanupContext(ctx)
	defer cancel()
	if removeErr := s.removeOwnedInitialDirectory(cleanupCtx, dest, now); removeErr != nil {
		return newProblem(CodeUnresolved, "initial destination creation failed and cleanup also failed", errors.Join(cause, removeErr))
	}
	if cause == nil {
		return nil
	}
	var problem *Problem
	if errors.As(cause, &problem) {
		return problem
	}
	if ctx.Err() != nil {
		return stoppedProblem(ctx, "while creating the destination", cause)
	}
	return newProblem(CodePublishFailed, "initial destination could not be prepared", cause)
}

// discardStoppedInitial removes the unpublished destination of a first import
// that was stopped (cancelled, superseded, or out of time) after its
// destination was prepared and before it was renamed into place, and settles
// its publication intent. The directory was never visible, so nothing was
// published. It runs without the run's cancellation. An unresolved outcome,
// and a destination whose ref transaction process could not be reaped, are
// left for restart reconciliation as before, because their directory may
// still change. When the cleanup itself fails, the directory also stays for
// restart reconciliation and the run keeps its own outcome.
func (s *Service) discardStoppedInitial(ctx context.Context, run *runState, cause error) error {
	dest := run.initialDestination
	if dest == nil || dest.released || dest.finalPath != "" || run.refProcessUnreaped || problemCode(cause) == CodeUnresolved {
		return cause
	}
	cleanupCtx, cancel := cleanupContext(ctx)
	defer cancel()
	now := s.clock()
	intent, exists, err := s.intentForInitialRun(cleanupCtx, run.run.RepositoryID, run.run.ID)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("the unpublished initial destination was preserved: %w", err))
	}
	if exists {
		return s.abandonUnpublishedInitial(ctx, cleanupCtx, dest, intent, cause, "", now)
	}
	if err := s.removeOwnedInitialDirectory(cleanupCtx, dest, now); err != nil {
		return errors.Join(cause, fmt.Errorf("the unpublished initial destination was preserved: %w", err))
	}
	return cause
}

func (s *Service) setInitialDestinationState(ctx context.Context, name, destinationState, issue string, now time.Time) error {
	if err := s.Store.SetImportInitialDestinationState(ctx, name, destinationState, boundedImportMessage(issue), now); err != nil {
		return err
	}
	stored, exists, err := s.Store.ImportInitialDestination(ctx, name)
	if err != nil || !exists || stored.State != destinationState {
		return errors.New("initial destination state write could not be read back")
	}
	return nil
}

func writeInitialMarker(directory string, marker initialMarker) error {
	content, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	content = append(content, '\n')
	file, err := state.CreatePrivateFile(filepath.Join(directory, initialMarkerName))
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readInitialMarker(directory string) (initialMarker, error) {
	var marker initialMarker
	path := filepath.Join(directory, initialMarkerName)
	info, err := os.Lstat(path)
	if err != nil {
		return marker, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return marker, errors.New("initial destination marker is not a regular file")
	}
	if info.Size() < 2 || info.Size() > maxInitialMarkerSize {
		return marker, errors.New("initial destination marker has an unexpected size")
	}
	if err := state.ValidatePrivateFile(path); err != nil {
		return marker, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return marker, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return marker, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return marker, errors.New("initial destination marker has trailing content")
	}
	if marker.Version != initialMarkerVersion || marker.Name == "" || marker.RunID == "" || marker.RepositoryID == "" || marker.Token == "" || marker.RootID == "" {
		return marker, errors.New("initial destination marker is incomplete")
	}
	return marker, nil
}

func (s *Service) proveInitialOwnership(ctx context.Context, dest *initialDestination) (state.ImportInitialDestination, error) {
	if dest == nil || dest.generation == "" {
		return state.ImportInitialDestination{}, errors.New("initial destination has no runtime generation")
	}
	root, err := s.currentRuntime(dest.generation)
	if err != nil {
		return state.ImportInitialDestination{}, err
	}
	if !ownedUnpublishedName(dest.name) || filepath.Dir(dest.path) != dest.storageRoot {
		return state.ImportInitialDestination{}, errors.New("initial destination path is not task-owned")
	}
	info, err := os.Lstat(dest.path)
	if err != nil {
		return state.ImportInitialDestination{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return state.ImportInitialDestination{}, errors.New("initial destination path is not a directory")
	}
	marker, err := readInitialMarker(dest.path)
	if err != nil {
		return state.ImportInitialDestination{}, fmt.Errorf("initial destination marker: %w", err)
	}
	if marker.RootID != root.rootID || marker.RootID != dest.rootID || marker.Name != dest.name || marker.Token != dest.token || marker.RunID != dest.runID || marker.RepositoryID != dest.repositoryID {
		return state.ImportInitialDestination{}, errors.New("initial destination marker does not match this run")
	}
	row, exists, err := s.Store.ImportInitialDestination(ctx, dest.name)
	if err != nil {
		return state.ImportInitialDestination{}, err
	}
	if !exists {
		return state.ImportInitialDestination{}, errors.New("initial destination has no ownership record")
	}
	if row.State != state.ImportInitialPreparing && row.State != state.ImportInitialReady && row.State != state.ImportInitialCleanupFailed {
		return row, errors.New("initial destination ownership record does not authorize this run")
	}
	if row.RootID != marker.RootID || row.Token != marker.Token || row.RunID != marker.RunID || row.RepositoryID != marker.RepositoryID {
		return row, errors.New("initial destination ownership record does not match the marker")
	}
	return row, nil
}

func (s *Service) removeOwnedInitialDirectory(ctx context.Context, dest *initialDestination, now time.Time) error {
	if _, err := s.proveInitialOwnership(ctx, dest); err != nil {
		_ = s.Store.SetImportInitialDestinationState(ctx, dest.name, state.ImportInitialCleanupFailed, boundedImportMessage(err.Error()), now)
		return err
	}
	if err := os.RemoveAll(dest.path); err != nil {
		_ = s.setInitialDestinationState(ctx, dest.name, state.ImportInitialCleanupFailed, err.Error(), now)
		return err
	}
	if _, err := os.Lstat(dest.path); !os.IsNotExist(err) {
		_ = s.setInitialDestinationState(ctx, dest.name, state.ImportInitialCleanupFailed, "initial destination directory remained after removal", now)
		return errors.New("initial destination directory remained after removal")
	}
	if err := s.setInitialDestinationState(ctx, dest.name, state.ImportInitialReleased, "", now); err != nil {
		return err
	}
	dest.released = true
	return nil
}

// finishInitialDestination records the receipt, publishes the directory, and
// records the repository row. It runs under the repository and credential locks
// and must not call Clock or Logf.
func (s *Service) finishInitialDestination(ctx context.Context, run *runState, intent *state.ImportIntent, observation intentObservation, now time.Time) error {
	dest := run.initialDestination
	if dest == nil {
		return newProblem(CodeUnresolved, "initial destination ownership is missing", nil)
	}
	// State writes do not use the run's cancellation, so a cancel cannot leave
	// a half-written record. The run's context is checked once more just
	// before the rename, which is the point of no return: before it a cancel
	// stops the import and its destination is removed; after it the import
	// completes, and a later cancel comes too late.
	stateCtx, cancelState := cleanupContext(ctx)
	defer cancelState()
	receipt, digest := buildReceipt(*intent, observation)
	if err := s.Store.UpdateImportIntent(stateCtx, intent.ID, state.ImportIntentApplied, receipt, digest, intent.Reason, now); err != nil {
		return newProblem(CodeStateUnavailable, "publication receipt could not be recorded before destination publication", err)
	}
	storedIntent, exists, err := s.Store.ImportIntent(stateCtx, intent.ID)
	if err != nil || !exists || storedIntent.ReceiptJSON != receipt || storedIntent.ReceiptDigest != digest || storedIntent.Status != state.ImportIntentApplied {
		return newProblem(CodeUnresolved, "publication receipt could not be read back before destination publication", err)
	}
	intent.ReceiptJSON = receipt
	intent.ReceiptDigest = digest
	intent.Status = state.ImportIntentApplied
	if err := s.setInitialDestinationState(stateCtx, dest.name, state.ImportInitialReady, "", now); err != nil {
		return newProblem(CodeUnresolved, "initial destination readiness could not be recorded", err)
	}
	if s.beforeInitialRename != nil {
		if err := s.beforeInitialRename(); err != nil {
			return newProblem(CodeUnresolved, "initial destination publication stopped before rename", err)
		}
	}
	if err := s.authorityCurrent(ctx, run); err != nil {
		return err
	}
	taken, err := s.destinationTaken(stateCtx, run.run.RepositoryID)
	if err != nil {
		return err
	}
	if taken {
		return s.refuseTakenNewDestination(stateCtx, run)
	}
	finalPath, err := s.Repositories.Path(run.run.RepositoryID)
	if err != nil {
		return newProblem(CodeRepositoryMissing, "final repository path could not be resolved", err)
	}
	renameErr := publishdir.Rename(stateCtx, dest.path, finalPath)
	landed, landErr := initialRenameLanded(dest, finalPath)
	if renameErr != nil && !landed {
		cause := errors.Join(renameErr, landErr)
		return newProblem(CodeUnresolved, "initial destination rename failed and the directory was preserved", cause)
	}
	if !landed {
		return newProblem(CodeUnresolved, "initial destination rename could not be read back", landErr)
	}
	dest.finalPath = finalPath
	if s.beforeInitialRepositoryRecord != nil {
		if err := s.beforeInitialRepositoryRecord(); err != nil {
			return ownerRecoveryProblem(finalPath, err)
		}
	}
	// Past the rename only a changed authority, not a cancel, stops the run.
	if err := s.authorityUnchanged(stateCtx, run); err != nil {
		return ownerRecoveryProblem(finalPath, err)
	}
	repositoryRecord := state.Repository{
		ID: run.run.RepositoryID, Name: run.name, Description: strings.TrimSpace(run.description), CreatedAt: now,
	}
	if err := s.Store.AddRepository(stateCtx, repositoryRecord); err != nil {
		return ownerRecoveryProblem(finalPath, err)
	}
	recorded, exists, err := s.Store.Repository(stateCtx, run.run.RepositoryID)
	if err != nil || !exists || recorded.ID != repositoryRecord.ID || recorded.Name != repositoryRecord.Name {
		return ownerRecoveryProblem(finalPath, err)
	}
	dest.rowRecorded = true
	if err := s.setInitialDestinationState(stateCtx, dest.name, state.ImportInitialPublished, "", now); err != nil {
		return ownerRecoveryProblem(finalPath, err)
	}
	if err := os.Remove(filepath.Join(finalPath, initialMarkerName)); err != nil && !os.IsNotExist(err) {
		_ = s.Store.SetImportInitialDestinationState(stateCtx, dest.name, state.ImportInitialPublished, boundedImportMessage(err.Error()), now)
	}
	return nil
}

// initialRenameLanded reports whether the unpublished directory is gone and the
// final path is the same owned directory. It does not use inode identity, so a
// Unix rename assumption is not treated as Windows proof. A failed rename,
// including access denied that outlasts publishdir's retry while another
// process holds a file in the directory, stays unresolved when this readback
// does not prove the rename.
func initialRenameLanded(dest *initialDestination, finalPath string) (bool, error) {
	_, unpublishedErr := os.Lstat(dest.path)
	finalInfo, finalErr := os.Lstat(finalPath)
	switch {
	case unpublishedErr == nil && os.IsNotExist(finalErr):
		return false, nil
	case unpublishedErr != nil && !os.IsNotExist(unpublishedErr):
		return false, unpublishedErr
	case finalErr != nil:
		return false, finalErr
	case unpublishedErr == nil:
		return false, errors.New("initial destination rename left both the unpublished and final paths")
	}
	if finalInfo.Mode()&os.ModeSymlink != 0 || !finalInfo.IsDir() {
		return false, errors.New("published repository path is not a directory")
	}
	marker, err := readInitialMarker(finalPath)
	if err != nil {
		return false, err
	}
	if marker.Token != dest.token || marker.Name != dest.name || marker.RunID != dest.runID || marker.RootID != dest.rootID || marker.RepositoryID != dest.repositoryID {
		return false, errors.New("published directory marker does not match this run")
	}
	return true, nil
}

func (s *Service) reconcileInitialDestinations(ctx context.Context, generation string) (int, error) {
	if s.Repositories == nil || s.Repositories.Locks == nil {
		return 0, errors.New("repository storage is unavailable")
	}
	root, err := s.currentRuntime(generation)
	if err != nil {
		return 0, err
	}
	storageRoot, err := s.Repositories.CanonicalStorageRoot()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(storageRoot)
	if err != nil {
		return 0, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if unpublishedInitialName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	seen := map[string]bool{}
	issues := 0
	for start := 0; start < len(names); start += reconcilePageLimit {
		end := start + reconcilePageLimit
		if end > len(names) {
			end = len(names)
		}
		for _, name := range names[start:end] {
			if err := ctx.Err(); err != nil {
				return issues, err
			}
			seen[name] = true
			row, exists, err := s.Store.ImportInitialDestination(ctx, name)
			if err != nil {
				return issues, err
			}
			if !exists {
				row = state.ImportInitialDestination{}
			}
			now := s.clock()
			count, err := s.reconcileOneInitialDestination(ctx, generation, root.rootID, storageRoot, name, row, now)
			if err != nil {
				return issues, err
			}
			issues += count
		}
	}
	afterName := ""
	for {
		page, err := s.Store.ImportInitialDestinationsPage(ctx, afterName, reconcilePageLimit)
		if err != nil {
			return issues, err
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			if seen[row.Name] || row.State == state.ImportInitialUnknown || row.State == state.ImportInitialPublished || row.State == state.ImportInitialReleased {
				continue
			}
			now := s.clock()
			count, err := s.reconcileLandedInitialDestination(ctx, generation, root.rootID, storageRoot, row, now)
			if err != nil {
				return issues, err
			}
			issues += count
		}
		afterName = page[len(page)-1].Name
		if len(page) < reconcilePageLimit {
			break
		}
	}
	return issues, nil
}

func (s *Service) reconcileOneInitialDestination(ctx context.Context, generation, rootID, storageRoot, name string, row state.ImportInitialDestination, now time.Time) (int, error) {
	path := filepath.Join(storageRoot, name)
	info, err := os.Lstat(path)
	if err != nil {
		return 1, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !ownedUnpublishedName(name) {
		return s.preserveUnknownInitial(ctx, name, "unexpected unpublished destination entry", now)
	}
	if row.Name == "" {
		return s.preserveUnknownInitial(ctx, name, "no ownership record was written by a run", now)
	}
	if row.State == state.ImportInitialUnknown || row.State == state.ImportInitialPublished || row.State == state.ImportInitialReleased {
		return 1, nil
	}
	if s.runIsLive(row.RunID) {
		return 0, nil
	}
	dest := &initialDestination{
		storageRoot: storageRoot, rootID: rootID, generation: generation, name: name, path: path,
		token: row.Token, repositoryID: row.RepositoryID, runID: row.RunID,
	}
	if _, err := s.proveInitialOwnership(ctx, dest); err != nil {
		if errors.Is(err, ErrRuntimeLost) {
			return 0, err
		}
		return 1, nil
	}
	// As in reconcileLandedInitialDestination, a repository being prepared
	// is left for the next start instead of waiting for its lock.
	if s.Repositories.Preparing(row.RepositoryID) {
		return 1, nil
	}
	lock := s.Repositories.Locks.For(row.RepositoryID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.currentRuntime(generation); err != nil {
		return 0, err
	}
	run, runExists, err := s.Store.ImportRun(ctx, row.RunID)
	if err != nil {
		return 0, err
	}
	if !runExists || !terminalImportRun(run.Status) || s.runIsLive(row.RunID) {
		return 1, nil
	}
	intent, intentExists, err := s.intentForInitialRun(ctx, row.RepositoryID, row.RunID)
	if err != nil {
		return 0, err
	}
	complete := false
	if intentExists {
		observation, observeErr := s.observeIntent(ctx, path, intent)
		complete = observeErr == nil && observation.matchesDesired && observation.retentionComplete && observation.headMatches
	}
	if complete {
		return s.publishReconciledInitial(ctx, dest, row, intent, now)
	}
	if row.State == state.ImportInitialPreparing {
		// The intent is settled before the directory goes, so a stop between
		// the two steps leaves a settled intent and a directory that the next
		// reconciliation removes, never an unresolved intent with nothing left.
		if intentExists {
			if err := s.settleNeverPublishedInitial(ctx, intent, "", now); err != nil {
				return 0, err
			}
		}
		if err := s.removeOwnedInitialDirectory(ctx, dest, now); err != nil {
			return 1, nil
		}
		// A run that stopped before it recorded an intent has nothing else
		// that would settle it.
		if !intentExists {
			if err := s.settleNeverPublishedRun(ctx, row.RunID); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
	return 1, nil
}

// initialNeverPublishedReason is recorded on the intent of an initial import
// whose unpublished destination can no longer become its repository.
const initialNeverPublishedReason = "the initial import stopped before its repository was created; its unpublished destination was never visible, so nothing was published"

// settleNeverPublishedInitial invalidates the open or unresolved intent of an
// initial import that can no longer publish, and records an unresolved run as
// failed with the same explanation. It never records anything as complete and
// writes nothing to Git.
func (s *Service) settleNeverPublishedInitial(ctx context.Context, intent state.ImportIntent, detail string, now time.Time) error {
	switch intent.Status {
	case state.ImportIntentPlanning, state.ImportIntentApplied, state.ImportIntentUnresolved:
	default:
		return nil
	}
	reason := initialNeverPublishedReason
	for _, earlier := range []string{detail, intent.Reason} {
		if earlier != "" {
			reason += "; " + earlier
		}
	}
	if err := s.Store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentInvalidated, "", "", boundedImportMessage(reason), now); err != nil {
		return err
	}
	return s.settleNeverPublishedRun(ctx, intent.RunID)
}

// settleNeverPublishedRun records an unresolved initial run whose destination
// can no longer become its repository as failed. An unresolved run keeps its
// name blocked, so without this a first import that stopped while preparing
// its destination would block the name for good.
func (s *Service) settleNeverPublishedRun(ctx context.Context, runID string) error {
	run, exists, err := s.Store.ImportRun(ctx, runID)
	if err != nil || !exists || run.Status != state.ImportRunUnresolved {
		return err
	}
	run.Status = state.ImportRunFailed
	run.ErrorClass = CodePublishFailed
	run.Message = boundedImportMessage(initialNeverPublishedReason + "; earlier outcome: " + run.Message)
	return s.Store.FinishImportRun(ctx, run)
}

// initialPublicationGone reports whether the publication of an initial import
// can no longer become a repository: its run is over, and none of its
// unpublished directories remains in a state that reconciliation could still
// publish or that the owner still has to inspect. A directory that was
// published, or may have been renamed into place, keeps the intent open.
func (s *Service) initialPublicationGone(ctx context.Context, intent state.ImportIntent) (bool, error) {
	if s.runIsLive(intent.RunID) {
		return false, nil
	}
	run, exists, err := s.Store.ImportRun(ctx, intent.RunID)
	if err != nil || !exists || !terminalImportRun(run.Status) {
		return false, err
	}
	rows, err := s.Store.ImportInitialDestinationsForRun(ctx, intent.RunID)
	if err != nil || len(rows) == 0 {
		return false, err
	}
	return s.initialDestinationsGone(rows)
}

// initialDestinationsGone reports whether none of rows can still become a
// repository or still needs the owner's inspection.
func (s *Service) initialDestinationsGone(rows []state.ImportInitialDestination) (bool, error) {
	storageRoot, err := s.Repositories.CanonicalStorageRoot()
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		switch row.State {
		case state.ImportInitialReleased, state.ImportInitialUnknown:
		case state.ImportInitialPreparing, state.ImportInitialCleanupFailed:
			if !ownedUnpublishedName(row.Name) {
				return false, nil
			}
			if _, err := os.Lstat(filepath.Join(storageRoot, row.Name)); !os.IsNotExist(err) {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}

// settleStrandedInitialRuns records as failed the unresolved first-import
// runs of a name without a repository that stopped before they recorded a
// publication intent, once none of their unpublished directories remains.
// Such a run can no longer publish, but as unresolved it would keep the name
// blocked. The caller holds the repository lock.
func (s *Service) settleStrandedInitialRuns(ctx context.Context, repositoryID string, now time.Time) error {
	runs, _, err := s.Store.ImportRuns(ctx, repositoryID, 20)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status != state.ImportRunUnresolved || run.Kind != state.ImportKindInitial || s.runIsLive(run.ID) {
			continue
		}
		if _, exists, err := s.intentForInitialRun(ctx, repositoryID, run.ID); err != nil || exists {
			if err != nil {
				return err
			}
			continue
		}
		rows, err := s.Store.ImportInitialDestinationsForRun(ctx, run.ID)
		if err != nil {
			return err
		}
		if gone, err := s.initialDestinationsGone(rows); err != nil || !gone {
			if err != nil {
				return err
			}
			continue
		}
		// A directory that is already gone no longer needs cleanup.
		for _, row := range rows {
			if row.State == state.ImportInitialPreparing || row.State == state.ImportInitialCleanupFailed {
				if err := s.setInitialDestinationState(ctx, row.Name, state.ImportInitialReleased, "the unpublished directory was already gone", now); err != nil {
					return err
				}
			}
		}
		if err := s.settleNeverPublishedRun(ctx, run.ID); err != nil {
			return err
		}
	}
	return nil
}

// settleGoneInitialIntent settles an intent whose initial publication can no
// longer become a repository. It reports whether the intent was settled.
func (s *Service) settleGoneInitialIntent(ctx context.Context, intent state.ImportIntent, now time.Time) (bool, error) {
	if intent.Status != state.ImportIntentPlanning && intent.Status != state.ImportIntentApplied && intent.Status != state.ImportIntentUnresolved {
		return false, nil
	}
	gone, err := s.initialPublicationGone(ctx, intent)
	if err != nil || !gone {
		return false, err
	}
	return true, s.settleNeverPublishedInitial(ctx, intent, "", now)
}

// abandonUnpublishedInitial settles a partial publication inside this run's
// own unpublished directory. Nothing outside the run can reach that directory,
// so the partial outcome was never visible: the intent is invalidated, then the
// owned directory is removed. The run reports why it stopped. runCtx is the
// run's own context; ctx is the uncancelled bookkeeping context.
func (s *Service) abandonUnpublishedInitial(runCtx, ctx context.Context, dest *initialDestination, intent state.ImportIntent, cause error, detail string, now time.Time) error {
	var problem *Problem
	switch {
	case cause == nil:
		cause = newProblem(CodePublishFailed, detail, nil)
	case errors.As(cause, &problem):
	case runCtx.Err() != nil:
		cause = stoppedProblem(runCtx, "during publication", cause)
	default:
		cause = newProblem(CodePublishFailed, "initial publication did not finish", cause)
	}
	if err := s.settleNeverPublishedInitial(ctx, intent, detail, now); err != nil {
		// The intent stays open. Restart reconciliation invalidates it and
		// removes the directory.
		return newProblem(CodeStateUnavailable, "the stopped initial publication could not be recorded", errors.Join(cause, err))
	}
	if err := s.removeOwnedInitialDirectory(ctx, dest, now); err != nil {
		return errors.Join(cause, fmt.Errorf("the unpublished initial destination was preserved: %w", err))
	}
	return cause
}

func (s *Service) publishReconciledInitial(ctx context.Context, dest *initialDestination, row state.ImportInitialDestination, intent state.ImportIntent, now time.Time) (int, error) {
	source, sourceExists, err := s.Store.ImportSource(ctx, row.RepositoryID)
	if err != nil {
		return 0, err
	}
	if !sourceExists || source.SourceGeneration != intent.SourceGeneration || source.AuthorityRevision != intent.AuthorityRevision {
		return 1, nil
	}
	taken, err := s.destinationTaken(ctx, row.RepositoryID)
	if err != nil || taken {
		return 1, err
	}
	finalPath, err := s.Repositories.Path(row.RepositoryID)
	if err != nil {
		return 1, nil
	}
	if err := publishdir.Rename(ctx, dest.path, finalPath); err != nil {
		landed, landErr := initialRenameLanded(dest, finalPath)
		if !landed {
			return 1, landErr
		}
	} else if landed, landErr := initialRenameLanded(dest, finalPath); !landed {
		return 1, landErr
	}
	dest.finalPath = finalPath
	if err := s.recordInitialRepository(ctx, row, finalPath, now); err != nil {
		return 1, nil
	}
	return 0, nil
}

func (s *Service) reconcileLandedInitialDestination(ctx context.Context, generation, rootID, storageRoot string, row state.ImportInitialDestination, now time.Time) (int, error) {
	if s.runIsLive(row.RunID) || !ownedUnpublishedName(row.Name) {
		return 0, nil
	}
	finalPath, err := s.Repositories.Path(row.RepositoryID)
	if err != nil {
		return 1, nil
	}
	info, err := os.Lstat(finalPath)
	if err != nil {
		return 1, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 1, nil
	}
	marker, err := readInitialMarker(finalPath)
	if err != nil || marker.Token != row.Token || marker.Name != row.Name || marker.RunID != row.RunID || marker.RepositoryID != row.RepositoryID || marker.RootID != row.RootID || marker.RootID != rootID {
		return 1, nil
	}
	if _, err := s.currentRuntime(generation); err != nil {
		return 0, err
	}
	intent, intentExists, err := s.intentForInitialRun(ctx, row.RepositoryID, row.RunID)
	if err != nil {
		return 0, err
	}
	if !intentExists {
		return 1, nil
	}
	// A registered repository still being prepared after startup may have a
	// hung preparation attempt holding its lock. Startup recovery does not
	// wait for it; the destination stays unresolved until the next start.
	if s.Repositories.Preparing(row.RepositoryID) {
		return 1, nil
	}
	lock := s.Repositories.Locks.For(row.RepositoryID)
	lock.Lock()
	defer lock.Unlock()
	marker, err = readInitialMarker(finalPath)
	if err != nil || marker.Token != row.Token || marker.Name != row.Name || marker.RunID != row.RunID || marker.RepositoryID != row.RepositoryID || marker.RootID != row.RootID {
		return 1, nil
	}
	observation, observeErr := s.observeIntent(ctx, finalPath, intent)
	if observeErr != nil || !observation.matchesDesired || !observation.retentionComplete || !observation.headMatches {
		return 1, nil
	}
	if err := s.recordInitialRepository(ctx, row, finalPath, now); err != nil {
		return 1, nil
	}
	return 0, nil
}

func (s *Service) recordInitialRepository(ctx context.Context, row state.ImportInitialDestination, finalPath string, now time.Time) error {
	if _, exists, err := s.Store.Repository(ctx, row.RepositoryID); err != nil {
		return err
	} else if !exists {
		// The directory was configured by an earlier process, possibly for
		// another runtime. The repository is served only after preparation
		// for the current runtime succeeds, so it is marked first.
		s.Repositories.PrepareRegistered(row.RepositoryID)
		record := state.Repository{ID: row.RepositoryID, Name: row.DisplayName, Description: row.Description, CreatedAt: row.CreatedAt}
		if err := s.Store.AddRepository(ctx, record); err != nil {
			s.Repositories.CancelPreparation(row.RepositoryID)
			_ = s.Store.SetImportInitialDestinationState(ctx, row.Name, state.ImportInitialReady, boundedImportMessage(ownerRecoveryMessage(finalPath)), now)
			return err
		}
	}
	stored, exists, err := s.Store.Repository(ctx, row.RepositoryID)
	if err != nil || !exists || stored.ID != row.RepositoryID {
		_ = s.Store.SetImportInitialDestinationState(ctx, row.Name, state.ImportInitialReady, boundedImportMessage(ownerRecoveryMessage(finalPath)), now)
		return errors.New(ownerRecoveryMessage(finalPath))
	}
	if err := s.setInitialDestinationState(ctx, row.Name, state.ImportInitialPublished, "", now); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(finalPath, initialMarkerName)); err != nil && !os.IsNotExist(err) {
		_ = s.Store.SetImportInitialDestinationState(ctx, row.Name, state.ImportInitialPublished, boundedImportMessage(err.Error()), now)
	}
	return nil
}

func (s *Service) preserveUnknownInitial(ctx context.Context, name, issue string, now time.Time) (int, error) {
	if len(name) > 80 {
		return 1, nil
	}
	row, exists, err := s.Store.ImportInitialDestination(ctx, name)
	if err != nil {
		return 0, err
	}
	if exists {
		if row.State == state.ImportInitialUnknown {
			return 1, nil
		}
		if err := s.setInitialDestinationState(ctx, name, state.ImportInitialUnknown, issue, now); err != nil {
			return 1, nil
		}
		return 1, nil
	}
	token, err := newStagingToken()
	if err != nil {
		return 0, err
	}
	rootID, err := newImportID()
	if err != nil {
		return 0, err
	}
	item := state.ImportInitialDestination{
		Name: name, RootID: rootID, Token: token, State: state.ImportInitialUnknown,
		Issue: boundedImportMessage(issue), CreatedAt: now,
	}
	if err := s.Store.RegisterImportInitialDestination(ctx, item); err != nil {
		if _, exists, readErr := s.Store.ImportInitialDestination(ctx, name); readErr == nil && exists {
			return 1, nil
		}
		return 0, err
	}
	return 1, nil
}

// refuseTakenNewDestination restores the pre-import source and credential
// binding after a destination appears, but only while that binding is still the
// one this import wrote. A later ConfigureSource or SetCredentials is left
// untouched, including its credential blocking state. It does not change the
// destination.
func (s *Service) refuseTakenNewDestination(ctx context.Context, run *runState) error {
	if run == nil || run.bindingSnapshot == nil {
		return newProblem(CodeRepositoryTaken, "repository destination already exists", nil)
	}
	if s.Repositories == nil || s.Repositories.Locks == nil {
		return newProblem(CodeRuntimeUnavailable, "repository locks are unavailable", nil)
	}
	lock := s.Repositories.Locks.For(run.run.RepositoryID)
	if !run.credentialAuthorityLocked {
		lock.Lock()
		defer lock.Unlock()
		release := s.Store.LockImportCredentialAuthority(run.run.RepositoryID)
		defer release()
	}
	current, exists, err := s.Store.ImportSource(ctx, run.run.RepositoryID)
	if err != nil {
		return runStateReadProblem("import source could not be read after a destination collision", err)
	}
	if !importBindingStillWritten(exists, current, run.writtenBinding) {
		return newProblem(CodeRepositoryTaken, "repository destination already exists", nil)
	}
	if err := s.Store.RestoreImportBinding(ctx, run.run.RepositoryID, *run.bindingSnapshot); err != nil {
		return newProblem(CodeUnresolved, "import binding could not be restored after a destination collision", err)
	}
	return newProblem(CodeRepositoryTaken, "repository destination already exists", nil)
}

func importBindingStillWritten(exists bool, current, written state.ImportSource) bool {
	return exists && current.URL == written.URL && current.SourceGeneration == written.SourceGeneration &&
		current.AuthorityRevision == written.AuthorityRevision && current.CredentialGeneration == written.CredentialGeneration
}

// initialIntentObservationAllowed reports whether repositoryPath is the
// directory proven to belong to an initial publication. A refresh intent has no
// ownership row and is observed normally. An initial intent is never judged
// against a different directory.
func (s *Service) initialIntentObservationAllowed(ctx context.Context, intent state.ImportIntent, repositoryPath, generation string) (bool, error) {
	rows, err := s.Store.ImportInitialDestinationsForRun(ctx, intent.RunID)
	if err != nil {
		return false, runStateReadProblem("initial destination ownership could not be read", err)
	}
	if len(rows) == 0 {
		return true, nil
	}
	root, err := s.currentRuntime(generation)
	if err != nil {
		return false, err
	}
	storageRoot, err := s.Repositories.CanonicalStorageRoot()
	if err != nil {
		return false, newProblem(CodeRepositoryMissing, "repository storage is unavailable", err)
	}
	for _, row := range rows {
		if row.State == state.ImportInitialUnknown || row.State == state.ImportInitialReleased {
			continue
		}
		if initialBindingMatchesPath(row, repositoryPath, storageRoot, root.rootID) {
			return true, nil
		}
		if row.State == state.ImportInitialPublished {
			finalPath, pathErr := s.Repositories.Path(row.RepositoryID)
			if pathErr == nil && repositoryPath == finalPath {
				if _, exists, readErr := s.Store.Repository(ctx, row.RepositoryID); readErr != nil {
					return false, readErr
				} else if exists {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func initialBindingMatchesPath(row state.ImportInitialDestination, repositoryPath, storageRoot, rootID string) bool {
	if row.RootID != rootID || !ownedUnpublishedName(row.Name) {
		return false
	}
	unpublished := filepath.Join(storageRoot, row.Name)
	if repositoryPath == unpublished && initialMarkerMatches(unpublished, row) {
		return true
	}
	finalPath := filepath.Join(storageRoot, row.RepositoryID+".git")
	return repositoryPath == finalPath && initialMarkerMatches(finalPath, row)
}

func initialMarkerMatches(directory string, row state.ImportInitialDestination) bool {
	marker, err := readInitialMarker(directory)
	if err != nil {
		return false
	}
	return marker.Token == row.Token && marker.Name == row.Name && marker.RunID == row.RunID && marker.RepositoryID == row.RepositoryID && marker.RootID == row.RootID
}

func (s *Service) intentForInitialRun(ctx context.Context, repositoryID, runID string) (state.ImportIntent, bool, error) {
	if intent, exists, err := s.Store.CompletedImportIntentForRun(ctx, runID); err != nil || exists {
		return intent, exists, err
	}
	intents, err := s.Store.PendingImportIntents(ctx, repositoryID)
	if err != nil {
		return state.ImportIntent{}, false, err
	}
	for _, intent := range intents {
		if intent.RunID == runID {
			return intent, true, nil
		}
	}
	return state.ImportIntent{}, false, nil
}
