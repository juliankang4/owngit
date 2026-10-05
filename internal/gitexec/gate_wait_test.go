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
