package repository

import (
	"errors"
	"fmt"
	"sync"
)

// ErrBackupReading reports a repository that a backup holds until its
// bundle is written. Deleting it would remove objects the backup still
// reads.
var ErrBackupReading = fmt.Errorf("%w: a backup is reading the repository", ErrRepositoryBusy)

// ErrBackupRunning reports that this process already makes a backup.
var ErrBackupRunning = errors.New("another backup is running")

// backupState records the repositories a running backup holds, and the
// repositories whose Git ref writer may still run although OwnGit gave up
// on it. It lives in memory: a backup cannot continue after its process
// ends, so nothing is left to release after a restart.
type backupState struct {
	mu        sync.Mutex
	running   bool
	held      map[string]bool
	unsettled map[string]bool
}

// BackupHold is one backup's hold on repositories. While a repository is
// held, deletion refuses it (ErrBackupReading) and maintenance leaves it
// alone, so no object file the backup reads is removed. Pushes, imports and
// pull requests are not affected.
type BackupHold struct {
	m *Manager
}

// HoldForBackup starts a backup's hold. Only one backup runs at a time.
func (m *Manager) HoldForBackup() (*BackupHold, error) {
	s := &m.backup
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil, ErrBackupRunning
	}
	s.running = true
	s.held = map[string]bool{}
	return &BackupHold{m: m}, nil
}

// Add holds ids and stops a maintenance of any of them that is running; it
// starts again later from its first step.
func (h *BackupHold) Add(ids ...string) {
	s := &h.m.backup
	s.mu.Lock()
	for _, id := range ids {
		s.held[id] = true
	}
	s.mu.Unlock()
	for _, id := range ids {
		h.m.stopMaintenanceOf(id)
	}
}

// Release ends the hold on id, once the backup no longer reads it.
func (h *BackupHold) Release(id string) {
	s := &h.m.backup
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.held, id)
}

// Close releases every repository and ends the backup. It may be called
// more than once.
func (h *BackupHold) Close() {
	if h.m == nil {
		return
	}
	s := &h.m.backup
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.held = nil
	h.m = nil
}

func (m *Manager) heldForBackup(id string) bool {
	s := &m.backup
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held[id]
}

// NoteUnsettledRefWriter records that a Git process that writes refs of id
// could not be stopped and may still change them. A backup refuses the
// repository until OwnGit restarts, when reconciliation settles it.
func (m *Manager) NoteUnsettledRefWriter(id string) {
	s := &m.backup
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unsettled == nil {
		s.unsettled = map[string]bool{}
	}
	s.unsettled[id] = true
}

// UnsettledRefWriter reports whether NoteUnsettledRefWriter recorded id.
func (m *Manager) UnsettledRefWriter(id string) bool {
	s := &m.backup
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unsettled[id]
}
