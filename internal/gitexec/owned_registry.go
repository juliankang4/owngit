package gitexec

import (
	"errors"
	"sync"
	"time"
)

// errOwnedProcessesClosed is returned to the caller that starts an owned
// process after TerminateAllOwnedProcesses began.
var errOwnedProcessesClosed = errors.New("attach owned process: OwnGit is giving up and starts no more processes")

// liveOwners are the owners of started processes that were not closed yet,
// so a shutdown that must end the process can end them first. Once
// TerminateAllOwnedProcesses begins the registry is closed: a process
// attached after that is ended at once, so nothing started during the
// shutdown outlives it.
var liveOwners = struct {
	sync.Mutex
	set    map[*ProcessOwner]struct{}
	closed bool
}{set: map[*ProcessOwner]struct{}{}}

// registerOwner records owner, or reports false when the registry is closed.
func registerOwner(owner *ProcessOwner) bool {
	liveOwners.Lock()
	defer liveOwners.Unlock()
	if liveOwners.closed {
		return false
	}
	liveOwners.set[owner] = struct{}{}
	return true
}

func unregisterOwner(owner *ProcessOwner) {
	liveOwners.Lock()
	defer liveOwners.Unlock()
	delete(liveOwners.set, owner)
}

// TerminateAllOwnedProcesses closes the registry, then ends, concurrently and
// within about grace, every process group or job that this process started
// through the ownership functions and has not closed. It signals nothing
// else. A group is signalled until CloseOwnedProcess, even after its leader
// was reaped, since members it started may still run, and a group ID cannot
// be reused while any member is alive. Accepted residual: the whole group
// exited and its ID was reused before the owner was closed.
func TerminateAllOwnedProcesses(grace time.Duration) error {
	liveOwners.Lock()
	defer liveOwners.Unlock()
	liveOwners.closed = true
	var wait sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for owner := range liveOwners.set {
		wait.Go(func() {
			if err := TerminateOwnedProcess(owner, grace); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wait.Wait()
	return errors.Join(errs...)
}
