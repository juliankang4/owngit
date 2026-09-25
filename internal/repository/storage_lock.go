package repository

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"owngit/internal/state"
)

// storageLockName names the file in the repository folder that a serving
// OwnGit keeps locked. The state directory has its own instance lock, but a
// copy of the state directory has a separate one, so only a lock in the
// repository folder shows that another server already uses the repositories.
const storageLockName = ".owngit-serve.lock"

// ErrStorageInUse reports that another OwnGit server holds the repository
// folder.
var ErrStorageInUse = errors.New("the repository folder is in use by another OwnGit server")

// storageClaimState records whether this manager claims the repository
// folder, and its lock once taken.
type storageClaimState struct {
	mu      sync.Mutex
	enabled bool
	release func()
}

// ClaimStorage makes this manager keep the repository folder locked until
// ReleaseStorage, and locks it now when the folder already holds something.
// An empty or missing folder, such as the mount point of a share that is not
// mounted yet, is not written to; it is claimed before the first repository
// hook or repository is written to it. It returns an error wrapping
// ErrStorageInUse when another OwnGit server holds the folder. The operating
// system drops the lock when the process ends, so a state directory moved
// after its server stopped claims the folder again.
func (m *Manager) ClaimStorage() error {
	s := &m.storageClaim
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = true
	return m.claimStorageLocked(false)
}

// ReleaseStorage releases the lock that ClaimStorage took.
func (m *Manager) ReleaseStorage() {
	s := &m.storageClaim
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
	if s.release != nil {
		s.release()
		s.release = nil
	}
}

// claimStorageForWrite claims the repository folder before OwnGit writes a
// repository or its hooks to it. Only another server holding the folder is
// an error; a folder that cannot be locked for another reason is written as
// before.
func (m *Manager) claimStorageForWrite() error {
	s := &m.storageClaim
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := m.claimStorageLocked(true); errors.Is(err, ErrStorageInUse) {
		return err
	}
	return nil
}

func (m *Manager) claimStorageLocked(writing bool) error {
	s := &m.storageClaim
	if !s.enabled || s.release != nil {
		return nil
	}
	root, err := canonicalRoot(m.RepositoryRoot())
	if err != nil {
		// Not configured yet, or unavailable: claimed before the first write.
		return nil
	}
	if !writing {
		if empty, err := emptyDirectory(root); err != nil || empty {
			return err
		}
	}
	release, err := state.AcquireExclusiveFileLock(filepath.Join(root, storageLockName))
	if errors.Is(err, state.ErrInstanceRunning) {
		return fmt.Errorf("%w: %s", ErrStorageInUse, root)
	}
	if err != nil {
		return fmt.Errorf("lock the repository folder: %w", err)
	}
	s.release = release
	return nil
}

func emptyDirectory(path string) (bool, error) {
	directory, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	names, err := directory.Readdirnames(1)
	if len(names) != 0 {
		return false, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return true, nil
}
