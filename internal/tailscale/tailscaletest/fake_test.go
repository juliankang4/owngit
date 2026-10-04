package tailscaletest

import (
	"context"
	"testing"
	"time"
)

// A read that a test abandons ends when the fixture is finished, before its
// server closes, so a delayed or held read never holds cleanup for the rest
// of its delay.
func TestAbandonedReadsEndWhenTheFixtureIsFinished(t *testing.T) {
	for _, test := range []struct {
		name  string
		state State
		held  bool
	}{
		{"delayed read", State{Status: Running(), ReadDelay: 60000}, false},
		{"held read", State{Status: Running(), HoldReads: true}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := New(t, test.state)
			reached := func() bool {
				state := fake.State()
				if test.held {
					return state.HeldReads == 1
				}
				return state.Running == 1
			}
			read := make(chan error, 1)
			go func() {
				_, err := fake.Command().Status(context.Background())
				read <- err
			}()
			for deadline := time.Now().Add(10 * time.Second); !reached(); {
				if time.Now().After(deadline) {
					t.Fatal("the read did not reach the fake")
				}
				time.Sleep(time.Millisecond)
			}
			// This is what the fixture's cleanup does before it closes the
			// LocalAPI server.
			fake.finish()
			select {
			case err := <-read:
				if err == nil {
					t.Fatal("the abandoned read answered although nothing released it")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the abandoned read did not end when the fixture was finished")
			}
			// An abandoned read stays counted and unrecorded, so a test can
			// tell that no new command started meanwhile.
			if state := fake.State(); state.Running != 1 || len(state.Calls) != 0 {
				t.Fatalf("after the abandoned read: running %d, calls %q", state.Running, fake.Calls())
			}
		})
	}
}
