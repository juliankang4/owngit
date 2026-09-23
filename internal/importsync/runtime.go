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
	"time"

	"owngit/internal/state"
)

// The staging runtime root is an exclusively held, private directory that
// outlives one import run. Ownership has two independent parts:
//
//   - a root marker written once while the process holds the root lock, so the
//     root identity does not depend on any entry inside it, and
//   - one authorization record per staging directory, written before the
//     directory can ever be cleaned.
//
// Nothing inside an entry can create cleanup authority by itself. An unknown
// directory, or a foreign or malformed marker, is preserved on every
// reconciliation and across restarts.
//
// A durable lease generation is bound into the marker, and the marker file
// itself is locked for the lifetime of the lease, so authority does not depend
// on the lock file still sitting at its original path. Replacing the lock file
// can therefore not hand the established root to a different process while an
// owner session is alive. Detected identity loss stays latched until all work
// stops and the caller explicitly closes the stale lease.
const (
	runtimeRootMarkerName     = ".owngit-import-root.json"
	runtimeRootLockName       = ".owngit-import.lock"
	runtimeMarkerVersion      = 2
	runtimeLockRecordVersion  = 1
	maxRuntimeMarkerBytes     = 1024
	maxRuntimeLockRecordBytes = 512
)

var (
	// ErrRuntimeHeld reports that another live process already owns the
	// staging runtime root for this state directory.
	ErrRuntimeHeld = errors.New("import runtime root is held by another process")
	// ErrRuntimeActive reports that Close was called while this process still
	// runs an import or an explicit runtime operation, so the lease was kept
	// instead of released.
	ErrRuntimeActive = errors.New("import runtime root still has active work")
	// ErrRuntimeUnsafe reports a staging root that cannot be validated safely.
	ErrRuntimeUnsafe = errors.New("import runtime root is not safely usable")
	// ErrRuntimeLost reports that the lease no longer matches its root. The
	// mismatch remains latched until idle Close releases the stale lease.
	ErrRuntimeLost = errors.New("import runtime ownership was lost")
)

// RuntimeInfo is the validated identity of a prepared runtime root.
type RuntimeInfo struct {
	StagingRoot string
	RootID      string
}

type runtimeMarker struct {
	Version        int    `json:"version"`
	RootID         string `json:"root_id"`
	LockGeneration string `json:"lock_generation"`
}

// runtimeLockRecord is written into the lock file and binds that file to the
// generation recorded in the root marker. An empty lock file has no record and
// is bonded before it may act.
type runtimeLockRecord struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
}

type runtimeRoot struct {
	directory     string
	staging       string
	rootID        string
	generation    string
	lockPath      string
	lockFile      *os.File
	markerPath    string
	markerFile    *os.File
	directoryInfo os.FileInfo
	stagingInfo   os.FileInfo
	release       func()
}

// stillOwned revalidates a held lease against the current root and its bound
// generation. Authority does not depend on the lock file still sitting at its
// original path, because the marker lock and the generation are the durable
// identity: a replaced lock file cannot demote a live owner, and a different
// process cannot inherit the root while that owner session is alive.
func (root runtimeRoot) stillOwned() (bool, error) {
	if _, err := root.lockFile.Stat(); err != nil {
		return false, fmt.Errorf("locked import runtime root file: %w", err)
	}
	markerLocked, err := root.markerFile.Stat()
	if err != nil {
		return false, fmt.Errorf("locked import runtime root marker: %w", err)
	}
	markerInfo, err := os.Lstat(root.markerPath)
	if err != nil {
		return false, fmt.Errorf("import runtime root marker: %w", err)
	}
	if !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(markerLocked, markerInfo) {
		return false, errors.New("import runtime root marker was replaced")
	}
	for _, check := range []struct {
		label string
		path  string
		info  os.FileInfo
	}{
		{"import runtime directory", root.directory, root.directoryInfo},
		{"import staging root", root.staging, root.stagingInfo},
	} {
		current, err := os.Lstat(check.path)
		if err != nil {
			return false, fmt.Errorf("%s: %w", check.label, err)
		}
		if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(check.info, current) {
			return false, fmt.Errorf("%s was replaced", check.label)
		}
	}
	marker, err := readRuntimeMarkerFile(root.markerFile)
	if err != nil {
		return false, fmt.Errorf("import runtime root marker: %w", err)
	}
	if marker.RootID != root.rootID {
		return false, errors.New("import runtime root marker identity changed")
	}
	if marker.LockGeneration != root.generation {
		return false, errors.New("import runtime root generation changed")
	}
	return true, nil
}

func (s *Service) runtimeRootPath() string {
	return filepath.Join(s.Store.Dir(), "runtime")
}

func (s *Service) stagingRootPath() string {
	return filepath.Join(s.runtimeRootPath(), "import-staging")
}

// Prepare validates and takes the process-lifetime staging root lease. It is an
// explicit mutation; passive reads never create directories.
func (s *Service) Prepare(ctx context.Context) (RuntimeInfo, error) {
	s.beginRuntimeOperation()
	defer s.endRuntimeOperation()
	if err := s.prepareRuntime(ctx); err != nil {
		return RuntimeInfo{}, err
	}
	root, err := s.currentRuntime("")
	if err != nil {
		return RuntimeInfo{}, err
	}
	return RuntimeInfo{StagingRoot: root.staging, RootID: root.rootID}, nil
}

// Close releases the lifetime lease. It refuses while a run is still active or
// an explicit runtime operation (Prepare, Reconcile) is in flight, because
// releasing the lease would let another process reconcile, and remove the
// staging of, work this process is still executing or scanning. It is
// idempotent once idle, and the caller can retry after the work finishes (or
// ask the run to stop with Cancel).
func (s *Service) Close() error {
	s.lifecycle.Lock()
	if s.activeRuns() > 0 || s.operations > 0 {
		s.lifecycle.Unlock()
		return newProblem(CodeBusy, "import runtime root still has active work", ErrRuntimeActive)
	}
	root, prepared := s.takeRuntime()
	s.lifecycle.Unlock()
	if prepared && root.release != nil {
		root.release()
	}
	return nil
}

// Shutdown ends import work for a serving process that is exiting. It
// refuses new runs, cancels every run this process executes, and waits until
// each has recorded its outcome and settled its staging, or until ctx ends.
// It then releases the runtime lease. A run still active when ctx ends keeps
// the lease, and the next start reconciles whatever it left.
func (s *Service) Shutdown(ctx context.Context) error {
	s.lifecycle.Lock()
	s.closing = true
	s.active.Range(func(_, value any) bool {
		if execution, ok := value.(activeExecution); ok && execution.cancel != nil {
			execution.cancel(ErrShuttingDown)
		}
		return true
	})
	s.lifecycle.Unlock()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := s.Close()
		if err == nil || !errors.Is(err, ErrRuntimeActive) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("import runs did not finish before shutdown: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// beginRuntimeOperation and endRuntimeOperation count explicit runtime
// operations that hold or are about to hold the lease. They take the same
// barrier Close decides under, so an operation cannot start between Close's
// check and the release and then keep using a lease that was already dropped.
func (s *Service) beginRuntimeOperation() {
	s.lifecycle.Lock()
	s.operations++
	s.lifecycle.Unlock()
}

func (s *Service) endRuntimeOperation() {
	s.lifecycle.Lock()
	s.operations--
	s.lifecycle.Unlock()
}

// activeRuns counts the runs registered by this process. Registration happens
// under the lifecycle read side, and removal happens only after the run's row is
// terminal and its staging is settled, so a zero count under the write side
// means releasing the lease cannot hand live work to a new owner.
func (s *Service) activeRuns() int {
	count := 0
	s.active.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// takeRuntime detaches the prepared root so only one caller releases it. Close
// is the only operation that clears a detected ownership loss.
func (s *Service) takeRuntime() (runtimeRoot, bool) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runtimeLost = false
	if s.runtime == nil {
		return runtimeRoot{}, false
	}
	root := *s.runtime
	s.runtime = nil
	return root, true
}

// preparedRuntime reports the in-memory lease without touching the filesystem.
func (s *Service) preparedRuntime() (runtimeRoot, bool) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtime == nil {
		return runtimeRoot{}, false
	}
	return *s.runtime, true
}

func runtimeLostError(cause error) error {
	return newProblem(CodeRuntimeUnavailable, "import runtime ownership was lost", errors.Join(ErrRuntimeLost, cause))
}

// currentRuntime validates the held handle and, when supplied, requires the
// generation captured when an operation or run began. Once a mismatch is
// observed it remains latched, even if the original path is restored.
func (s *Service) currentRuntime(generation string) (runtimeRoot, error) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeLost {
		return runtimeRoot{}, runtimeLostError(nil)
	}
	if s.runtime == nil {
		return runtimeRoot{}, newProblem(CodeRuntimeUnavailable, "import runtime root was not prepared", nil)
	}
	root := *s.runtime
	owned, err := root.stillOwned()
	if !owned {
		s.runtimeLost = true
		return runtimeRoot{}, runtimeLostError(err)
	}
	if generation != "" && root.generation != generation {
		s.runtimeLost = true
		return runtimeRoot{}, runtimeLostError(errors.New("import runtime generation changed"))
	}
	return root, nil
}

// runtimeCurrent is the passive form used by status and legacy ownership
// checks. A failed validation still latches the loss.
func (s *Service) runtimeCurrent() (runtimeRoot, bool) {
	root, err := s.currentRuntime("")
	return root, err == nil
}

func (s *Service) runtimeCurrentForRun(run *runState) error {
	if run == nil || run.runtimeGeneration == "" {
		return runtimeLostError(errors.New("import run has no runtime generation"))
	}
	_, err := s.currentRuntime(run.runtimeGeneration)
	return err
}

// prepareRuntime is idempotent while the lease identity is intact. Two layers
// protect the root: the lock file excludes another process at the same path,
// and an exclusive lock on the root marker names the live authority for the
// root itself. A lock file whose record does not match the marker generation is
// refused while that marker lock is held, and is bonded to the marker
// generation only when the free marker lock proves no owner session survives.
// Detected identity loss never rebinds in place. Close must release and clear
// the stale lease after active runs and operations finish.
func (s *Service) prepareRuntime(ctx context.Context) error {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeLost {
		return runtimeLostError(nil)
	}
	if s.runtime != nil {
		if owned, err := s.runtime.stillOwned(); owned {
			return nil
		} else {
			s.runtimeLost = true
			return runtimeLostError(err)
		}
	}
	if s.Store == nil {
		return newProblem(CodeRuntimeUnavailable, "state store is unavailable", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := s.runtimeRootPath()
	stagingRoot := s.stagingRootPath()
	if err := ensureRuntimeDirectory(directory); err != nil {
		return err
	}
	if err := ensureStagingDirectory(stagingRoot); err != nil {
		return err
	}
	lockPath := filepath.Join(stagingRoot, runtimeRootLockName)
	if err := validateRuntimeLockPath(lockPath); err != nil {
		return err
	}
	lockFile, lockRelease, err := state.AcquireExclusiveFileLockHandle(lockPath)
	if err != nil {
		if errors.Is(err, state.ErrInstanceRunning) {
			return newProblem(CodeRuntimeUnavailable, "another process owns the import runtime root", ErrRuntimeHeld)
		}
		return newProblem(CodeRuntimeUnavailable, "import runtime root could not be locked", err)
	}
	fail := func(cause error) error {
		lockRelease()
		return cause
	}
	markerPath := filepath.Join(stagingRoot, runtimeRootMarkerName)
	markerInfo, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		// An unknown root is initialized only when the newly opened lock file is
		// empty. A generation without its marker is a partial initialization and
		// stays fail-closed.
		record, readErr := readRuntimeLockRecord(lockFile)
		if readErr != nil || record.Generation != "" {
			return fail(newProblem(CodeRuntimeUnsafe, "import runtime marker is missing for an initialized lock generation", errors.Join(ErrRuntimeUnsafe, readErr)))
		}
		generation, idErr := state.RandomID()
		if idErr != nil {
			return fail(newProblem(CodeRuntimeUnavailable, "import runtime generation could not be generated", idErr))
		}
		if err := writeRuntimeLockRecord(lockFile, generation); err != nil {
			return fail(newProblem(CodeRuntimeUnavailable, "import runtime lock record could not be created", err))
		}
		rootID, idErr := state.RandomID()
		if idErr != nil {
			return fail(newProblem(CodeRuntimeUnavailable, "import runtime root identity could not be generated", idErr))
		}
		marker := runtimeMarker{Version: runtimeMarkerVersion, RootID: rootID, LockGeneration: generation}
		if err := writeRuntimeMarker(markerPath, marker); err != nil {
			return fail(newProblem(CodeRuntimeUnavailable, "import runtime root marker could not be created", err))
		}
	} else if err != nil {
		return fail(newProblem(CodeRuntimeUnavailable, "import runtime root marker could not be inspected", err))
	} else if !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 {
		return fail(newProblem(CodeRuntimeUnsafe, "import runtime root marker is not a regular file", ErrRuntimeUnsafe))
	} else if err := state.ValidatePrivateFile(markerPath); err != nil {
		return fail(newProblem(CodeRuntimeUnsafe, "import runtime root marker is not private", errors.Join(ErrRuntimeUnsafe, err)))
	}

	// Lock before reading marker content. On Windows LockFileEx protects byte
	// zero, so reopening and reading the path while this handle is held fails.
	markerFile, markerRelease, err := state.AcquireExclusivePrivateFileLockHandle(markerPath)
	if err != nil {
		lockRelease()
		if errors.Is(err, state.ErrInstanceRunning) {
			return newProblem(CodeRuntimeUnavailable, "another process holds the import runtime root authority", ErrRuntimeHeld)
		}
		return newProblem(CodeRuntimeUnavailable, "import runtime root authority could not be locked", err)
	}
	failAll := func(cause error) error {
		markerRelease()
		lockRelease()
		return cause
	}
	if err := validateRuntimeMarkerHandle(markerPath, markerFile); err != nil {
		return failAll(newProblem(CodeRuntimeUnsafe, "import runtime root marker changed while it was locked", errors.Join(ErrRuntimeUnsafe, err)))
	}
	marker, err := readRuntimeMarkerFile(markerFile)
	if err != nil {
		return failAll(newProblem(CodeRuntimeUnsafe, "import runtime root marker is invalid", errors.Join(ErrRuntimeUnsafe, err)))
	}
	// A matching record is the ordinary restart. A missing or different record
	// means the previous lock file is gone; the free marker lock proves no owner
	// session survives, so this file may carry the established generation.
	if record, readErr := readRuntimeLockRecord(lockFile); readErr != nil || record.Generation != marker.LockGeneration {
		if err := writeRuntimeLockRecord(lockFile, marker.LockGeneration); err != nil {
			return failAll(newProblem(CodeRuntimeUnavailable, "import runtime lock record could not be written", err))
		}
	}
	// These identities are compared by every later stillOwned call, so they
	// are fixed here rather than resolved from the paths at the first check.
	directoryInfo, err := state.LstatIdentity(directory)
	if err != nil {
		return failAll(newProblem(CodeRuntimeUnavailable, "import runtime directory could not be inspected", err))
	}
	stagingInfo, err := state.LstatIdentity(stagingRoot)
	if err != nil {
		return failAll(newProblem(CodeRuntimeUnavailable, "import staging root could not be inspected", err))
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 || !stagingInfo.IsDir() || stagingInfo.Mode()&os.ModeSymlink != 0 {
		return failAll(newProblem(CodeRuntimeUnsafe, "import runtime path is not a real directory", ErrRuntimeUnsafe))
	}
	s.runtime = &runtimeRoot{
		directory: directory, staging: stagingRoot, rootID: marker.RootID, generation: marker.LockGeneration,
		lockPath: lockPath, lockFile: lockFile, markerPath: markerPath, markerFile: markerFile,
		directoryInfo: directoryInfo, stagingInfo: stagingInfo,
		release: func() {
			markerRelease()
			lockRelease()
		},
	}
	return nil
}

// ensureRuntimeDirectory validates <state>/runtime. A directory this call
// creates, or an empty one, is made private; an unknown nonempty one keeps its
// existing mode.
func ensureRuntimeDirectory(directory string) error {
	info, err := os.Lstat(directory)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return newProblem(CodeRuntimeUnavailable, "import runtime directory could not be created", err)
		}
		if err := state.ProtectPrivatePath(directory, true); err != nil {
			return newProblem(CodeRuntimeUnavailable, "import runtime directory could not be protected", err)
		}
		return nil
	case err != nil:
		return newProblem(CodeRuntimeUnavailable, "import runtime directory could not be inspected", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return newProblem(CodeRuntimeUnsafe, "import runtime directory is not a real directory", ErrRuntimeUnsafe)
	}
	empty, err := directoryIsEmpty(directory)
	if err != nil {
		return newProblem(CodeRuntimeUnavailable, "import runtime directory could not be read", err)
	}
	if empty {
		if err := state.ProtectPrivatePath(directory, true); err != nil {
			return newProblem(CodeRuntimeUnavailable, "import runtime directory could not be protected", err)
		}
	}
	return nil
}

// ensureStagingDirectory validates <state>/runtime/import-staging. An existing
// nonempty root without a marker is adopted without chmod once the lease is
// held, because the root identity is recorded by the marker that follows.
func ensureStagingDirectory(stagingRoot string) error {
	info, err := os.Lstat(stagingRoot)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
			return newProblem(CodeRuntimeUnavailable, "import staging root could not be created", err)
		}
		if err := state.ProtectPrivatePath(stagingRoot, true); err != nil {
			return newProblem(CodeRuntimeUnavailable, "import staging root could not be protected", err)
		}
		return nil
	case err != nil:
		return newProblem(CodeRuntimeUnavailable, "import staging root could not be inspected", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return newProblem(CodeRuntimeUnsafe, "import staging root is not a real directory", ErrRuntimeUnsafe)
	}
	empty, err := directoryIsEmpty(stagingRoot)
	if err != nil {
		return newProblem(CodeRuntimeUnavailable, "import staging root could not be read", err)
	}
	if empty {
		if err := state.ProtectPrivatePath(stagingRoot, true); err != nil {
			return newProblem(CodeRuntimeUnavailable, "import staging root could not be protected", err)
		}
	}
	return nil
}

func directoryIsEmpty(path string) (bool, error) {
	directory, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	names, err := directory.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return len(names) == 0, err
}

func validateRuntimeLockPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return newProblem(CodeRuntimeUnavailable, "import runtime root lock could not be inspected", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return newProblem(CodeRuntimeUnsafe, "import runtime root lock is not a regular file", ErrRuntimeUnsafe)
	}
	return nil
}

func writeRuntimeMarker(path string, record runtimeMarker) error {
	content, err := json.Marshal(record)
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
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func validateRuntimeMarkerHandle(path string, file *os.File) error {
	locked, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(locked, current) {
		return errors.New("import runtime root marker was replaced")
	}
	return state.ValidatePrivateFileHandle(file)
}

// readRuntimeMarkerFile reads through the owning handle. Reopening markerPath
// is invalid on Windows because the lease's LockFileEx covers byte zero.
func readRuntimeMarkerFile(file *os.File) (runtimeMarker, error) {
	var record runtimeMarker
	info, err := file.Stat()
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() {
		return record, errors.New("import runtime root marker is not a regular file")
	}
	if info.Size() < 2 || info.Size() > maxRuntimeMarkerBytes {
		return record, errors.New("import runtime root marker has an unexpected size")
	}
	if err := state.ValidatePrivateFileHandle(file); err != nil {
		return record, err
	}
	content := make([]byte, info.Size())
	if _, err := file.ReadAt(content, 0); err != nil {
		return record, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return record, errors.New("import runtime root marker has trailing content")
	}
	if record.Version != runtimeMarkerVersion || len(record.RootID) != 32 || !isLowerHexString(record.RootID) ||
		len(record.LockGeneration) != 32 || !isLowerHexString(record.LockGeneration) {
		return record, errors.New("import runtime root marker is incomplete")
	}
	return record, nil
}

// readRuntimeLockRecord reads the record through the held handle, so a path
// replacement cannot mix one file's record with another file's lock. An empty
// file is a freshly created lock file with no record yet.
func readRuntimeLockRecord(file *os.File) (runtimeLockRecord, error) {
	var record runtimeLockRecord
	info, err := file.Stat()
	if err != nil {
		return record, err
	}
	if info.Size() == 0 {
		return record, nil
	}
	if info.Size() < 2 || info.Size() > maxRuntimeLockRecordBytes {
		return record, errors.New("import runtime lock record has an unexpected size")
	}
	content := make([]byte, info.Size())
	if _, err := file.ReadAt(content, 0); err != nil {
		return record, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return record, errors.New("import runtime lock record has trailing content")
	}
	if record.Version != runtimeLockRecordVersion || len(record.Generation) != 32 || !isLowerHexString(record.Generation) {
		return record, errors.New("import runtime lock record is incomplete")
	}
	return record, nil
}

func writeRuntimeLockRecord(file *os.File, generation string) error {
	content, err := json.Marshal(runtimeLockRecord{Version: runtimeLockRecordVersion, Generation: generation})
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.WriteAt(content, 0); err != nil {
		return err
	}
	return file.Sync()
}
