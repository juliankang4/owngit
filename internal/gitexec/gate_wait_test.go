package gitexec

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/hostmem"
)

// A background command run while a repository lock is held must not wait for
// the memory gate: transfers holding every slot may be waiting for that very
// lock. It returns ErrMemoryBusy at once so the lock is released.
func TestCommandUnderALockDoesNotWaitForTheMemoryGate(t *testing.T) {
	gate := hostmem.NewGate(1)
	hostmem.Shared.Store(gate)
	defer hostmem.Shared.Store(nil)
	transfer, _ := gate.TryAcquire()
	defer transfer()
	runner, err := New("", filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := runner.RunWithLimits(WithoutGateWait(context.Background()), "", nil, CommandLimits{}, "repack", "-h")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrMemoryBusy) {
			t.Fatalf("error = %v, want ErrMemoryBusy", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the command waited for the memory gate")
	}
}

// A command whose caller holds a slot of the memory gate takes no second one,
// so work that paid for a slot, such as a maintenance repack that waited for
// one, runs while every other slot is in use and keeps the slot it waited for.
func TestCommandWithAHeldGateSlotTakesNoSecondOne(t *testing.T) {
	gate := hostmem.NewGate(1)
	hostmem.Shared.Store(gate)
	defer hostmem.Shared.Store(nil)
	caller, _ := gate.TryAcquire()
	if caller == nil {
		t.Fatal("the gate is full before the test")
	}
	defer caller()
	runner, err := New("", filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	repository := t.TempDir()
	if _, err := runner.Run(context.Background(), "", nil, "init", "--bare", repository); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Without the mark the command leaves, because the only slot is the
	// caller's.
	if _, err := runner.RunWithLimits(WithoutGateWait(context.Background()), repository, nil, CommandLimits{}, "repack", "-d"); !errors.Is(err, ErrMemoryBusy) {
		t.Fatalf("error = %v, want ErrMemoryBusy", err)
	}
	if _, err := runner.RunWithLimits(WithHeldGateSlot(context.Background()), repository, nil, CommandLimits{}, "repack", "-d"); err != nil {
		t.Fatalf("the command with a held slot failed: %v", err)
	}
}
