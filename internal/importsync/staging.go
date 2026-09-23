package importsync

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
	"strings"
	"time"

	"owngit/internal/state"
)

// Task-owned staging lives outside the repository root and is never published
// until verification and publication succeed. Removal needs two independent
// records to agree: the on-disk marker and the authorization row written when
// the run was created. A directory name or marker alone proves nothing, so
// unknown content survives reconciliation and restarts instead of being
// deleted.
const (
	stagingMarkerName    = ".owngit-import-stage.json"
	stagingNamePrefix    = "run-"
	stagingMarkerVersion = 2
	maxStagingMarkerSize = 4096
)

type stagingMarker struct {
	Version      int    `json:"version"`
	Name         string `json:"name"`
	RootID       string `json:"root_id"`
	RunID        string `json:"run_id"`
	RepositoryID string `json:"repository_id"`
	Token        string `json:"token"`
	CreatedAt    int64  `json:"created_at"`
}

type stagingDir struct {
	root         string
	rootID       string
	generation   string
	name         string
	path         string
	runID        string
	token        string
	repositoryID string
}

func validStagingName(name string) bool {
	if !strings.HasPrefix(name, stagingNamePrefix) {
		return false
	}
	return len(name) == len(stagingNamePrefix)+32 && isLowerHexString(name[len(stagingNamePrefix):])
}

func isLowerHexString(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func newImportID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func newStagingToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// authorizedStagingState reports whether a row written by acquireStaging may
// later authorize removal. Rows created by reconciliation from unproven disk
// content are informational and never authorize.
func authorizedStagingState(rowState string) bool {
	return rowState == state.ImportStagingActive || rowState == state.ImportStagingCleanupFailed
}

// acquireStaging creates one fresh unpublished staging directory, records its
// unpredictable authorization, and only then returns. The root lease is held
// for the whole call, so a fresh directory cannot be observed or deleted by
// another process.
func (s *Service) acquireStaging(ctx context.Context, runID, repositoryID string, now time.Time) (stagingDir, error) {
	if err := ctx.Err(); err != nil {
		return stagingDir{}, err
	}
	if err := s.prepareRuntime(ctx); err != nil {
		return stagingDir{}, err
	}
	root, err := s.currentRuntime("")
	if err != nil {
		return stagingDir{}, err
	}
	token, err := newStagingToken()
	if err != nil {
		return stagingDir{}, err
	}
	dir := stagingDir{
		root: root.staging, rootID: root.rootID, generation: root.generation, name: stagingNamePrefix + runID,
		token: token, repositoryID: repositoryID, runID: runID,
	}
	dir.path = filepath.Join(root.staging, dir.name)
	// A name collision refuses instead of adopting, so pre-existing content is
	// never touched by the creation path.
	if err := os.Mkdir(dir.path, 0o700); err != nil {
		return dir, newProblem(CodeStagingUnsafe, "staging directory already exists and was not cleaned", err)
	}
	// Every step below only ever removes the directory created above.
	discard := func(cause error) (stagingDir, error) {
		if removeErr := os.RemoveAll(dir.path); removeErr != nil {
			return dir, newProblem(CodeStagingUnsafe, "staging creation failed and cleanup also failed", errors.Join(cause, removeErr))
		}
		return dir, cause
	}
	marker := stagingMarker{
		Version: stagingMarkerVersion, Name: dir.name, RootID: root.rootID, RunID: runID,
		RepositoryID: repositoryID, Token: token, CreatedAt: now.Unix(),
	}
	if err := writeStagingMarker(dir.path, marker); err != nil {
		return discard(err)
	}
	claimed, err := s.Store.ClaimImportStaging(ctx, state.ImportStaging{
		Name: dir.name, RepositoryID: repositoryID, RunID: runID, Token: token,
		State: state.ImportStagingActive, CreatedAt: now,
	})
	if err != nil {
		return discard(err)
	}
	if !claimed {
		// A foreign authorized row keeps its identity; the fresh directory was
		// only created by this call, so it is discarded instead of being
		// silently adopted under another run's authorization.
		return discard(newProblem(CodeStagingUnsafe, "staging name is already authorized for another run", nil))
	}
	return dir, nil
}

func writeStagingMarker(directory string, marker stagingMarker) error {
	content, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	content = append(content, '\n')
	file, err := state.CreatePrivateFile(filepath.Join(directory, stagingMarkerName))
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

// readStagingMarker requires a bounded, private, regular file with exactly the
// known fields. A marker that does not parse is not adopted; the directory is
// preserved.
func readStagingMarker(directory string) (stagingMarker, error) {
	var marker stagingMarker
	path := filepath.Join(directory, stagingMarkerName)
	info, err := os.Lstat(path)
	if err != nil {
		return marker, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return marker, errors.New("staging marker is not a regular file")
	}
	if info.Size() < 2 || info.Size() > maxStagingMarkerSize {
		return marker, errors.New("staging marker has an unexpected size")
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
		return marker, errors.New("staging marker has trailing content")
	}
	if marker.Version != stagingMarkerVersion || marker.Name == "" || marker.RunID == "" || marker.RepositoryID == "" || marker.Token == "" {
		return marker, errors.New("staging marker is incomplete")
	}
	if len(marker.RootID) != 32 || !isLowerHexString(marker.RootID) {
		return marker, errors.New("staging marker has no runtime root identity")
	}
	return marker, nil
}

// proveStagingOwnership requires the current root identity, the marker, the
// directory name, and an authorizing row to agree before any cleanup. The row
// is returned even on failure so callers can report without mutating it.
func (s *Service) proveStagingOwnership(ctx context.Context, dir stagingDir) (state.ImportStaging, error) {
	if dir.generation == "" {
		return state.ImportStaging{}, errors.New("staging directory has no runtime generation")
	}
	root, err := s.currentRuntime(dir.generation)
	if err != nil {
		return state.ImportStaging{}, err
	}
	if !validStagingName(dir.name) || filepath.Dir(dir.path) != root.staging {
		return state.ImportStaging{}, errors.New("staging directory name is not task-owned")
	}
	info, err := os.Lstat(dir.path)
	if err != nil {
		return state.ImportStaging{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return state.ImportStaging{}, errors.New("staging path is not a directory")
	}
	marker, err := readStagingMarker(dir.path)
	if err != nil {
		return state.ImportStaging{}, fmt.Errorf("staging marker: %w", err)
	}
	if marker.RootID != root.rootID {
		return state.ImportStaging{}, errors.New("staging marker belongs to another runtime root")
	}
	if marker.Name != dir.name || marker.RepositoryID != dir.repositoryID {
		return state.ImportStaging{}, errors.New("staging marker does not match its directory")
	}
	if dir.runID != "" && marker.RunID != dir.runID {
		return state.ImportStaging{}, errors.New("staging marker run does not match its directory")
	}
	if dir.token != "" && marker.Token != dir.token {
		return state.ImportStaging{}, errors.New("staging marker token does not match the run")
	}
	row, exists, err := s.Store.ImportStaging(ctx, dir.name)
	if err != nil {
		return state.ImportStaging{}, err
	}
	if !exists {
		return state.ImportStaging{}, errors.New("staging directory has no authorization record")
	}
	if !authorizedStagingState(row.State) {
		return row, errors.New("staging authorization record was not written by a run")
	}
	if row.RunID != marker.RunID || row.Token != marker.Token || row.RepositoryID != marker.RepositoryID {
		return row, errors.New("staging authorization record does not match the marker")
	}
	return row, nil
}

// settleStaging deletes a proven staging directory and records the outcome on
// the run. Failure keeps the content and the authorization row, so a later
// reconciliation can retry instead of losing the directory forever.
func (s *Service) settleStaging(ctx context.Context, dir stagingDir, run state.ImportRun) state.ImportRun {
	return s.settleStagingAt(ctx, dir, run, s.clock())
}

// settleStagingAt avoids invoking Clock while a publication lock is held.
func (s *Service) settleStagingAt(ctx context.Context, dir stagingDir, run state.ImportRun, now time.Time) state.ImportRun {
	_, err := s.proveStagingOwnership(ctx, dir)
	if err != nil {
		appendImportCleanup(&run, fmt.Sprintf("%s: %v", dir.name, err))
		return run
	}
	if err := os.RemoveAll(dir.path); err != nil {
		appendImportCleanup(&run, fmt.Sprintf("%s: cleanup failed: %v", dir.name, err))
		_ = s.Store.ReleaseImportStaging(ctx, dir.name, state.ImportStagingCleanupFailed, boundedImportMessage(err.Error()), now)
		return run
	}
	_ = s.Store.ReleaseImportStaging(ctx, dir.name, state.ImportStagingReleased, "", now)
	return run
}

func appendImportCleanup(run *state.ImportRun, value string) {
	if run.CleanupError == "" {
		run.CleanupError = value
		return
	}
	combined := run.CleanupError + "; " + value
	if len(combined) > 500 {
		combined = combined[:500]
	}
	run.CleanupError = combined
}

func boundedImportMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

// reconcileStaging removes only entries whose authorization row and terminal
// run prove ownership, and only when this process is not running them. Every
// other entry is preserved and counted.
func (s *Service) reconcileStaging(ctx context.Context, generation string) (int, error) {
	root, err := s.currentRuntime(generation)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(root.staging)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	issues := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return issues, err
		}
		if _, err := s.currentRuntime(generation); err != nil {
			return issues, err
		}
		name := entry.Name()
		if name == runtimeRootMarkerName || name == runtimeRootLockName {
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validStagingName(name) {
			if err := s.recordUnknownStagingEntry(ctx, name); err != nil {
				return issues, err
			}
			issues++
			continue
		}
		dir := stagingDir{root: root.staging, rootID: root.rootID, generation: generation, name: name, path: filepath.Join(root.staging, name)}
		row, exists, err := s.Store.ImportStaging(ctx, name)
		if err != nil {
			return issues, err
		}
		if !exists {
			if err := s.recordUnknownStagingDirectory(ctx, dir); err != nil {
				return issues, err
			}
			issues++
			continue
		}
		if !authorizedStagingState(row.State) {
			// A row registered by an earlier reconciliation is informational.
			issues++
			continue
		}
		if s.runIsLive(row.RunID) {
			continue
		}
		run, runExists, err := s.Store.ImportRun(ctx, row.RunID)
		if err != nil {
			return issues, err
		}
		if !runExists || !terminalImportRun(run.Status) {
			issues++
			continue
		}
		dir.runID, dir.token, dir.repositoryID = row.RunID, row.Token, row.RepositoryID
		if _, err := s.proveStagingOwnership(ctx, dir); err != nil {
			if errors.Is(err, ErrRuntimeLost) {
				return issues, err
			}
			issues++
			continue
		}
		if _, err := s.currentRuntime(generation); err != nil {
			return issues, err
		}
		if err := os.RemoveAll(dir.path); err != nil {
			_ = s.Store.ReleaseImportStaging(ctx, name, state.ImportStagingCleanupFailed, boundedImportMessage(err.Error()), s.clock())
			issues++
			continue
		}
		_ = s.Store.ReleaseImportStaging(ctx, name, state.ImportStagingReleased, "", s.clock())
	}
	return issues, nil
}

// recordUnknownStagingEntry registers a visible, non-authorizing row for an
// entry that is not a staging directory, so it is reported and never removed.
func (s *Service) recordUnknownStagingEntry(ctx context.Context, name string) error {
	row, exists, err := s.Store.ImportStaging(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		if row.State == state.ImportStagingUnknown {
			return nil
		}
		return s.Store.ReleaseImportStaging(ctx, name, state.ImportStagingUnknown, "unexpected staging entry", s.clock())
	}
	token, err := newStagingToken()
	if err != nil {
		return err
	}
	return s.registerUnknownStaging(ctx, state.ImportStaging{
		Name: name, Token: token, State: state.ImportStagingUnknown,
		Issue: "unexpected staging entry", CreatedAt: s.clock(),
	})
}

// recordUnknownStagingDirectory preserves a staging-shaped directory whose
// authorization row is missing. The marker, if readable, only describes the
// directory; it does not authorize removal, and its field values are treated as
// hints rather than as trusted identities.
func (s *Service) recordUnknownStagingDirectory(ctx context.Context, dir stagingDir) error {
	token, err := newStagingToken()
	if err != nil {
		return err
	}
	item := state.ImportStaging{
		Name: dir.name, Token: token, State: state.ImportStagingUnknown,
		Issue: "marker is missing or invalid", CreatedAt: s.clock(),
	}
	if marker, err := readStagingMarker(dir.path); err == nil {
		if len(marker.RepositoryID) <= 100 {
			item.RepositoryID = marker.RepositoryID
		}
		if len(marker.RunID) <= 64 {
			item.RunID = marker.RunID
		}
		item.Issue = "no authorization record was written by a run"
	}
	return s.registerUnknownStaging(ctx, item)
}

// registerUnknownStaging writes an informational row. The scan runs outside the
// lifecycle barrier, so a run may claim the same name between the existence
// check and this insert; an existing row then simply wins, because a claimed row
// is the better authority.
func (s *Service) registerUnknownStaging(ctx context.Context, item state.ImportStaging) error {
	err := s.Store.RegisterImportStaging(ctx, item)
	if err == nil {
		return nil
	}
	if _, exists, readErr := s.Store.ImportStaging(ctx, item.Name); readErr == nil && exists {
		return nil
	}
	return err
}

func terminalImportRun(status string) bool {
	switch status {
	case state.ImportRunComplete, state.ImportRunFailed, state.ImportRunCancelled,
		state.ImportRunSuperseded, state.ImportRunInterrupted, state.ImportRunUnresolved:
		return true
	}
	return false
}
