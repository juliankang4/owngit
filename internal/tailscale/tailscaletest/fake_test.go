package tailscaletest

import (
	"testing"
	"time"
)

// The state is written in place, so a read or change made without the lock
// could see a half-written file. The test's reads and changes therefore
// wait while another holder has the lock.
func TestStateAndUpdateWaitForTheLock(t *testing.T) {
	for name, use := range map[string]func(*Fake){
		"State":  func(fake *Fake) { fake.State() },
		"Update": func(fake *Fake) { fake.Update(func(*State) {}) },
	} {
		t.Run(name, func(t *testing.T) {
			fake := New(t, State{Status: Running()})
			unlock, err := lockFile(fake.file + ".lock")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { use(fake); close(done) }()
			// The wait only shows an early return; a slow machine can make
			// the check pass wrongly, never fail wrongly.
			select {
			case <-done:
				unlock()
				t.Fatalf("%s used the state file while another holder had the lock", name)
			case <-time.After(300 * time.Millisecond):
			}
			unlock()
			<-done
		})
	}
}
