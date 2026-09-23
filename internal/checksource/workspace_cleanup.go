package checksource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"owngit/internal/state"
)

const (
	workspaceRootMarker = ".owngit-check-workspace-root.json"
	workspaceRootLock   = ".owngit-check-workspace.lock"
	workspaceJobMarker  = ".owngit-check-job.json"
	workspaceSourceName = "source"
	workspaceFormat     = 1
	maximumMarkerBytes  = 1024
	maximumCleanupScan  = 10000
)

type workspaceRootRecord struct {
	Format int    `json:"format"`
	ID     string `json:"id"`
}

type workspaceJobRecord struct {
	Format int    `json:"format"`
	RootID string `json:"root_id"`
	JobID  string `json:"job_id"`
}

// WorkspaceRoot is an exclusively held, private root for check-job envelopes.
// Each envelope stores ownership metadata beside, never inside, exact source.
type WorkspaceRoot struct {
	path    string
	id      string
	release func()
}

// AcquireWorkspaceRoot adopts an empty private directory or verifies a root
// previously created by OwnGit, then holds its process lock until Close.
func AcquireWorkspaceRoot(path string) (*WorkspaceRoot, error) {
	if path == "" || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return nil, errors.New("check workspace root must be an absolute cleaned path")
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("create check workspace root: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("inspect check workspace root: %w", err)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return nil, errors.New("check workspace root must be a real directory")
	}
	markerPath := filepath.Join(path, workspaceRootMarker)
	markerExists, err := workspaceMarkerExists(markerPath)
	if err != nil {
		return nil, err
	}
	if !markerExists {
		empty, err := workspaceRootIsEmpty(path)
		if err != nil {
			return nil, err
		}
		if !empty {
			return nil, errors.New("refusing an unowned nonempty check workspace root")
		}
	}
	if err := state.ProtectPrivatePath(path, true); err != nil {
		return nil, fmt.Errorf("protect check workspace root: %w", err)
	}
	lockPath := filepath.Join(path, workspaceRootLock)
	if err := validateWorkspaceLockPath(lockPath); err != nil {
		return nil, err
	}
	release, err := state.AcquireExclusiveFileLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("check workspace root is already active or cannot be locked: %w", err)
	}
	owned := &WorkspaceRoot{path: path, release: release}
	fail := func(err error) (*WorkspaceRoot, error) {
		owned.Close()
		return nil, err
	}
	markerExists, err = workspaceMarkerExists(markerPath)
	if err != nil {
		return fail(err)
	}
	if !markerExists {
		empty, err := workspaceRootContainsOnlyLock(path)
		if err != nil {
			return fail(err)
		}
		if !empty {
			return fail(errors.New("refusing an unowned nonempty check workspace root"))
		}
		id, err := state.RandomID()
		if err != nil {
			return fail(err)
		}
		record := workspaceRootRecord{Format: workspaceFormat, ID: id}
		if err := writeWorkspaceRecord(markerPath, record); err != nil {
			return fail(fmt.Errorf("create check workspace ownership marker: %w", err))
		}
	}
	record, err := readWorkspaceRootRecord(markerPath)
	if err != nil {
		return fail(err)
	}
	owned.id = record.ID
	return owned, nil
}

// Close releases exclusive ownership of the workspace root.
func (root *WorkspaceRoot) Close() {
	if root == nil || root.release == nil {
		return
	}
	root.release()
	root.release = nil
}

// PrepareJob creates an authenticated job envelope and returns its exact-source
// child. The source child does not exist yet and is safe for Materialize.
func (root *WorkspaceRoot) PrepareJob(jobID string) (envelope, source string, err error) {
	if root == nil || root.release == nil || !validWorkspaceID(jobID) {
		return "", "", errors.New("invalid or inactive check workspace root")
	}
	envelope = filepath.Join(root.path, jobID)
	if err := os.Mkdir(envelope, 0o700); err != nil {
		return "", "", fmt.Errorf("create check job envelope: %w", err)
	}
	record := workspaceJobRecord{Format: workspaceFormat, RootID: root.id, JobID: jobID}
	if err := writeWorkspaceRecord(filepath.Join(envelope, workspaceJobMarker), record); err != nil {
		_ = os.RemoveAll(envelope)
		return "", "", fmt.Errorf("create check job ownership marker: %w", err)
	}
	return envelope, filepath.Join(envelope, workspaceSourceName), nil
}

// RemoveJob removes only an envelope whose authenticated metadata matches this
// root and the requested job. Unknown or replaced paths are preserved.
func (root *WorkspaceRoot) RemoveJob(jobID string) error {
	if root == nil || root.release == nil || !validWorkspaceID(jobID) {
		return errors.New("invalid or inactive check workspace root")
	}
	envelope := filepath.Join(root.path, jobID)
	owned, err := root.ownsJobEnvelope(envelope, jobID)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("refusing to remove an unowned check workspace")
	}
	return os.RemoveAll(envelope)
}

// Cleanup removes a bounded batch of authenticated interrupted job envelopes.
// Unknown entries are preserved. more reports an incomplete bounded scan or
// additional owned envelopes beyond the removal limit.
func (root *WorkspaceRoot) Cleanup(limit int) (removed int, more bool, err error) {
	if root == nil || root.release == nil || limit < 1 || limit > maximumCleanupScan {
		return 0, false, errors.New("invalid workspace cleanup bound")
	}
	directory, err := os.Open(root.path)
	if err != nil {
		return 0, false, err
	}
	defer directory.Close()
	var failures error
	for inspected := 0; inspected < maximumCleanupScan; inspected++ {
		entries, readErr := directory.Readdir(1)
		if errors.Is(readErr, io.EOF) {
			return removed, more, failures
		}
		if readErr != nil {
			return removed, more, errors.Join(failures, readErr)
		}
		entry := entries[0]
		if entry.Name() == workspaceRootMarker || entry.Name() == workspaceRootLock {
			continue
		}
		if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 || !validWorkspaceID(entry.Name()) {
			failures = errors.Join(failures, fmt.Errorf("preserved unrecognized workspace entry %s", entry.Name()))
			continue
		}
		envelope := filepath.Join(root.path, entry.Name())
		owned, ownershipErr := root.ownsJobEnvelope(envelope, entry.Name())
		if ownershipErr != nil {
			failures = errors.Join(failures, ownershipErr)
			continue
		}
		if !owned {
			failures = errors.Join(failures, fmt.Errorf("preserved unrecognized workspace %s", entry.Name()))
			continue
		}
		if removed >= limit {
			more = true
			continue
		}
		if err := os.RemoveAll(envelope); err != nil {
			failures = errors.Join(failures, fmt.Errorf("remove workspace %s: %w", entry.Name(), err))
			continue
		}
		removed++
	}
	return removed, true, errors.Join(failures, errors.New("workspace cleanup scan reached its bound"))
}

func (root *WorkspaceRoot) ownsJobEnvelope(envelope, jobID string) (bool, error) {
	info, err := os.Lstat(envelope)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	var record workspaceJobRecord
	if err := readWorkspaceRecord(filepath.Join(envelope, workspaceJobMarker), &record); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read workspace %s ownership: %w", jobID, err)
	}
	return record.Format == workspaceFormat && record.RootID == root.id && record.JobID == jobID, nil
}

// CleanupWorkspaceRoot preserves the original one-shot cleanup API. It refuses
// an unowned nonempty root and cannot run while another lifecycle holds it.
func CleanupWorkspaceRoot(path string, limit int) (removed int, more bool, err error) {
	root, err := AcquireWorkspaceRoot(path)
	if err != nil {
		return 0, false, err
	}
	defer root.Close()
	return root.Cleanup(limit)
}

func validateWorkspaceLockPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("check workspace root lock is not a regular file")
	}
	return nil
}

func workspaceMarkerExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("check workspace root ownership marker is not a regular file")
	}
	return true, nil
}

func workspaceRootIsEmpty(path string) (bool, error) {
	directory, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	entries, err := directory.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return len(entries) == 0, err
}

func workspaceRootContainsOnlyLock(path string) (bool, error) {
	directory, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	entries, err := directory.Readdirnames(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return len(entries) == 1 && entries[0] == workspaceRootLock, nil
}

func writeWorkspaceRecord(path string, value any) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	content = append(content, '\n')
	file, err := state.CreatePrivateFile(path)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readWorkspaceRootRecord(path string) (workspaceRootRecord, error) {
	var record workspaceRootRecord
	if err := readWorkspaceRecord(path, &record); err != nil {
		return workspaceRootRecord{}, fmt.Errorf("read check workspace root ownership: %w", err)
	}
	if record.Format != workspaceFormat || !validWorkspaceID(record.ID) {
		return workspaceRootRecord{}, errors.New("check workspace root ownership marker is invalid")
	}
	return record, nil
}

func readWorkspaceRecord(path string, destination any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > maximumMarkerBytes {
		return errors.New("workspace ownership marker is invalid")
	}
	if err := state.ValidatePrivateFile(path); err != nil {
		return fmt.Errorf("workspace ownership marker is not private: %w", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("workspace ownership marker has trailing content")
	}
	return nil
}

func validWorkspaceID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
