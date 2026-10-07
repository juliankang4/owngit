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

// ErrStorageChanged reports that a repository directory, the storage root or
// its lock file is no longer the one this server bound or claimed. Writes can
// resume when the original storage returns. Restart OwnGit to adopt changed
// storage.
var ErrStorageChanged = errors.New("the repository folder changed after this OwnGit server claimed it; restart OwnGit to use it again")

// storageHold is a taken claim: the lock, and the identities of the folder
// and lock file it was taken on.
type storageHold struct {
	root     string
	rootInfo os.FileInfo
	lockInfo os.FileInfo
	release  func()
}

// verify checks that the folder path and the lock name still lead to the
// objects the lock is held on. A removed or replaced lock file would let a
// second server claim the same folder, and a replaced folder (new directory,
// link, junction or mount) is not the one that was claimed.
func (h *storageHold) verify() error {
	root, err := os.Stat(h.root)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	if !os.SameFile(root, h.rootInfo) {
		return fmt.Errorf("%w: %s", ErrStorageChanged, h.root)
	}
	lock, err := os.Lstat(filepath.Join(h.root, storageLockName))
	if err != nil || !os.SameFile(lock, h.lockInfo) {
		return fmt.Errorf("%w: %s", ErrStorageChanged, h.root)
	}
	return nil
}

// storageClaimState records whether this manager claims the repository
// folder, and its hold once taken.
type storageClaimState struct {
	mu      sync.Mutex
	enabled bool
	hold    *storageHold
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
	m.installWriteGate()
	return m.claimStorageLocked(false)
}

// installWriteGate makes every repository write lock verify root and repository identity,
// so no writer needs its own call.
func (m *Manager) installWriteGate() {
	if m.Locks != nil {
		m.Locks.SetWriteGate(m.VerifyRepositoryStorage)
	}
}

// verifyStorageHold checks an existing claim and passes when there is none.
// It never claims: the claim of an empty folder, such as the mount point of a
// share that is not mounted yet, waits for Create, InitBareRepository or
// repository preparation, which claim it before they write.
func (m *Manager) verifyStorageHold() error {
	s := &m.storageClaim
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.hold == nil {
		return nil
	}
	return s.hold.verify()
}

// VerifyStorageHold checks an existing storage claim without claiming. It
// reports ErrStorageChanged when the claimed folder or its lock file is no
// longer the one this server holds, and passes when no claim was taken. Writes
// that do not take the repository write lock use it right before they touch
// the repository folder.
func (m *Manager) VerifyStorageHold() error { return m.verifyStorageHold() }

// ClaimStorageForWrite claims the folder if that is still pending, or verifies
// the claim, before a caller writes a new repository directory into it.
func (m *Manager) ClaimStorageForWrite() error { return m.claimStorageForWrite() }

// ReleaseStorage releases the lock that ClaimStorage took.
func (m *Manager) ReleaseStorage() {
	s := &m.storageClaim
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
	if s.hold != nil {
		s.hold.release()
		s.hold = nil
	}
}

// SetRootForSetup holds the selected folder's claim through save. A failed
// save releases only this tentative claim and leaves the manager unchanged.
// Empty folders keep the same lazy claim as startup.
func (m *Manager) SetRootForSetup(root string, save func() error) error {
	s := &m.storageClaim
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hold != nil {
		return errors.New("repository storage is already selected")
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	hold, err := claimStorageRoot(canonical, false)
	if err != nil {
		return err
	}
	if err := save(); err != nil {
		if hold != nil {
			hold.release()
		}
		return err
	}
	m.SetRoot(canonical)
	s.enabled = true
	m.installWriteGate()
	s.hold = hold
	return nil
}

// claimStorageForWrite claims the repository folder before OwnGit writes a
// repository or its hooks to it. Failed claims never authorize a write.
func (m *Manager) claimStorageForWrite() error {
	s := &m.storageClaim
	s.mu.Lock()
	defer s.mu.Unlock()
	return m.claimStorageLocked(true)
}

func (m *Manager) claimStorageLocked(writing bool) error {
	s := &m.storageClaim
	if !s.enabled {
		return nil
	}
	if s.hold != nil {
		if writing {
			return s.hold.verify()
		}
		return nil
	}
	root, err := canonicalRoot(m.RepositoryRoot())
	if err != nil {
		if !writing {
			// Not configured yet, or unavailable: claimed before the first write.
			return nil
		}
		return err
	}
	hold, err := claimStorageRoot(root, writing)
	if err == nil {
		s.hold = hold
	}
	return err
}

func claimStorageRoot(root string, writing bool) (*storageHold, error) {
	if !writing {
		if empty, err := emptyDirectory(root); err != nil || empty {
			return nil, err
		}
	}
	// The lock refuses a link or another account's file at its name (see
	// state.AcquireExclusiveFileLockHandle), but the folders on the way are
	// not held to the state directory's rule: a repository folder may be on
	// a network share or shared with a group, and whoever else can write
	// there is already trusted with the repositories in it, so refusing
	// such a folder would break a supported setup and protect nothing. A
	// link at the folder's own name is followed too: the owner may reach
	// the repository folder through a link they made, such as ~/git to a
	// folder on another disk.
	lockPath := filepath.Join(root, storageLockName)
	rootBefore, err := captureDirectoryIdentity(root)
	if err != nil {
		return nil, fmt.Errorf("inspect the repository folder: %w", err)
	}
	file, release, err := state.AcquireExclusiveFileLockHandle(lockPath)
	if errors.Is(err, state.ErrInstanceRunning) {
		return nil, fmt.Errorf("%w: %s", ErrStorageInUse, root)
	}
	if err != nil {
		return nil, fmt.Errorf("lock the repository folder: %w", err)
	}
	hold := &storageHold{root: root, rootInfo: rootBefore, release: release}
	// The folder and the lock name must lead to what was just locked; if a
	// replacement slipped in during the claim, this claim is not trusted.
	if hold.lockInfo, err = file.Stat(); err == nil {
		err = hold.verify()
	}
	if err != nil {
		release()
		if errors.Is(err, ErrStorageChanged) {
			return nil, err
		}
		return nil, fmt.Errorf("inspect the repository lock: %w", err)
	}
	return hold, nil
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
