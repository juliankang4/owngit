package state

import (
	"errors"
	"testing"
)

func TestOfflineLockRefusesConcurrentServerOrRecoveryOperation(t *testing.T) {
	directory := t.TempDir()
	unlock, err := AcquireOfflineLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := AcquireOfflineLock(directory); !errors.Is(err, ErrInstanceRunning) {
		if second != nil {
			second()
		}
		unlock()
		t.Fatalf("second lock error=%v, want instance running", err)
	}
	unlock()
	third, err := AcquireOfflineLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	third()
}
