//go:build !windows

package gitexec

import (
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// reopenOwnedProcesses undoes the closing by TerminateAllOwnedProcesses. Only
// tests call it; the process exits after a real termination.
func reopenOwnedProcesses() {
	liveOwners.Lock()
	defer liveOwners.Unlock()
	liveOwners.closed = false
}

// Giving up must end a process whose attachment is still in progress, and
// any process that is attached after giving up began.
func TestTerminateAllEndsStartingAndLaterProcesses(t *testing.T) {
	t.Cleanup(reopenOwnedProcesses)
	start := func() *exec.Cmd {
		command := exec.Command("sleep", "60")
		ConfigureOwnedProcess(command)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		return command
	}
	attaching := start()
	entered, release := make(chan struct{}), make(chan struct{})
	attached := make(chan error, 1)
	go func() {
		owner, err := AttachOwnedProcessObserved(attaching, func() error { close(entered); <-release; return nil })
		if err == nil {
			defer CloseOwnedProcess(owner)
		}
		attached <- err
	}()
	<-entered
	attachingDone := make(chan error, 1)
	go func() { attachingDone <- attaching.Wait() }()
	noErr(t, TerminateAllOwnedProcesses(20*time.Millisecond))
	select {
	case <-attachingDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the process survived while its attachment was in progress")
	}
	close(release)
	noErr(t, <-attached)

	// A group is still ended after its leader was reaped, while a child of
	// the group runs.
	reopenOwnedProcesses()
	leader := exec.Command("sh", "-c", "sleep 60 & echo $!; exit 0")
	ConfigureOwnedProcess(leader)
	out, err := leader.StdoutPipe()
	noErr(t, err)
	noErr(t, leader.Start())
	owner, err := AttachOwnedProcess(leader)
	noErr(t, err)
	defer CloseOwnedProcess(owner)
	var childPID int
	_, err = fmt.Fscan(out, &childPID)
	noErr(t, err)
	noErr(t, leader.Wait())
	if syscall.Kill(childPID, 0) != nil {
		t.Fatal("the child of the reaped leader is not running")
	}
	noErr(t, TerminateAllOwnedProcesses(20*time.Millisecond))
	for deadline := time.Now().Add(10 * time.Second); syscall.Kill(childPID, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a child survived in the group of a reaped leader")
		}
	}

	later := start()
	if _, err := AttachOwnedProcess(later); err == nil {
		t.Fatal("attached a process after giving up began")
	}
	laterDone := make(chan error, 1)
	go func() { laterDone <- later.Wait() }()
	select {
	case <-laterDone:
	case <-time.After(10 * time.Second):
		t.Fatal("a process attached after giving up began survived")
	}
}
