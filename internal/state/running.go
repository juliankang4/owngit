package state

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// The running record (RunningNetwork) tells saved from running network
// values. It is trusted only while its publisher is alive: a serve process
// holds RunningNetworkLockFile in the state directory for its whole life
// (ClaimRunningNetwork), clears any record left by a crashed run as soon as
// it holds that lock, and publishes its own record once its listener is
// bound. The operating system releases the lock when the process ends in any
// way, including kill -9, so "lock held and record present" means the record
// belongs to the live holder. Other holders of the offline lock, such as an
// OwnGit 1.0.3 server or an offline backup, never take this lock, so a record
// they find is reported as stale, never as running. Unlike a process ID
// check this cannot be fooled by a reused process ID.
//
// Every reader goes through ObserveRunningNetwork (another process) or
// OwnRunningNetwork (the serve process itself), which apply this rule in one
// place.

// RunningNetworkLockFile is the lock a serve process holds while its running
// record is valid.
const RunningNetworkLockFile = ".network-running.lock"

// States of the server that uses a state directory, in RunningObservation.
const (
	// ServerRunning: a live serve process published what it uses.
	ServerRunning = "running"
	// ServerStarting: the live process has not published yet.
	ServerStarting = "starting"
	// ServerNotRunning: no process uses the state directory.
	ServerNotRunning = "not_running"
	// ServerUnknown: something else holds the state directory, such as an
	// older OwnGit or an offline backup, or the running record cannot be
	// vouched for.
	ServerUnknown = "unknown"
)

// RunningObservation is what a reader may believe about the server that
// uses a state directory.
type RunningObservation struct {
	// Server is one of ServerRunning, ServerStarting, ServerNotRunning and
	// ServerUnknown.
	Server string
	// Record is the running record, set only when Server is ServerRunning.
	Record *RunningNetwork
	// StaleRecord says that a record left by a server that ended without
	// cleanup was found and ignored.
	StaleRecord bool
}

// lockRetry bounds how long serve waits for a state lock that a momentary
// observation probe holds. A real second owner holds it longer and still
// stops the start.
const lockRetry = 500 * time.Millisecond

// AcquireLockBriefly takes a lock, retrying for a short while when another
// process holds it, such as the momentary probe of ObserveRunningNetwork.
func AcquireLockBriefly(acquire func() (func(), error)) (func(), error) {
	deadline := time.Now().Add(lockRetry)
	for {
		release, err := acquire()
		if !errors.Is(err, ErrInstanceRunning) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ClaimRunningNetwork takes the running-record lock for a serve process and
// removes a record left by a run that ended without cleanup. The release
// runs after the record is cleared at shutdown. When it fails, the caller
// must not publish a record: no reader would believe it, and the one left
// behind would only be reported as stale.
func (s *Store) ClaimRunningNetwork(ctx context.Context) (func(), error) {
	release, err := AcquireLockBriefly(func() (func(), error) {
		return AcquireExclusiveFileLock(filepath.Join(s.dir, RunningNetworkLockFile))
	})
	if err != nil {
		return nil, err
	}
	if err := s.ClearRunningNetwork(ctx); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// ObserveRunningNetwork tells from another process whether a server uses
// this state directory and what it runs with. It probes the running-record
// lock first, so a running server's offline lock is never touched, and each
// probe holds a lock only for an instant; serve waits for such a probe
// (AcquireLockBriefly) instead of failing.
func (s *Store) ObserveRunningNetwork(ctx context.Context) (RunningObservation, error) {
	live, err := lockHeld(func() (func(), error) {
		return AcquireExclusiveFileLock(filepath.Join(s.dir, RunningNetworkLockFile))
	})
	if err != nil {
		return RunningObservation{}, err
	}
	record, published, err := s.RunningNetwork(ctx)
	if err != nil {
		return RunningObservation{}, err
	}
	held := live
	if !live {
		if held, err = lockHeld(func() (func(), error) { return AcquireOfflineLock(s.dir) }); err != nil {
			return RunningObservation{}, err
		}
	}
	return observeRunning(live, held, record, published), nil
}

// OwnRunningNetwork is ObserveRunningNetwork for the serve process itself,
// which cannot probe a lock it holds. claimed says whether this process
// holds the running-record lock (ClaimRunningNetwork succeeded); the offline
// lock is held by this process in any case.
func (s *Store) OwnRunningNetwork(ctx context.Context, claimed bool) (RunningObservation, error) {
	record, published, err := s.RunningNetwork(ctx)
	if err != nil {
		return RunningObservation{}, err
	}
	return observeRunning(claimed, true, record, published), nil
}

// observeRunning is the one rule: the record is trusted only when its
// publisher holds the running-record lock (live). held says that something
// holds the offline lock; it matters only when live is false.
func observeRunning(live, held bool, record RunningNetwork, published bool) RunningObservation {
	switch {
	case live && published:
		return RunningObservation{Server: ServerRunning, Record: &record}
	case live:
		return RunningObservation{Server: ServerStarting}
	case held:
		return RunningObservation{Server: ServerUnknown, StaleRecord: published}
	default:
		return RunningObservation{Server: ServerNotRunning, StaleRecord: published}
	}
}

// lockHeld reports whether another process holds the lock that acquire
// takes.
func lockHeld(acquire func() (func(), error)) (bool, error) {
	release, err := acquire()
	if errors.Is(err, ErrInstanceRunning) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	release()
	return false, nil
}
