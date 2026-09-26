package githttp

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// countingWriter counts the flushes that reach the connection.
type countingWriter struct {
	*httptest.ResponseRecorder
	flushes atomic.Int32
}

func (writer *countingWriter) Flush() { writer.flushes.Add(1) }

func newTestResponseState(delay time.Duration, bodyEnded *atomic.Bool) (*responseState, *countingWriter) {
	recorder := &countingWriter{ResponseRecorder: httptest.NewRecorder()}
	state := &responseState{ResponseWriter: recorder, flushDelay: delay, bodyEnded: bodyEnded.Load,
		deadlines: &transferDeadlines{controller: http.NewResponseController(recorder)}}
	return state, recorder
}

// Nothing is flushed before the request body ends, and the first flush after
// it, which sends the headers, waits for the delay.
func TestTheFirstFlushWaitsForTheEndOfTheBodyAndTheDelay(t *testing.T) {
	var bodyEnded atomic.Bool
	state, recorder := newTestResponseState(time.Hour, &bodyEnded)
	defer state.stop()
	state.WriteHeader(http.StatusOK)
	_, err := state.Write([]byte("0008NAK\n"))
	noErr(t, err)
	state.scheduleFlush()
	bodyEnded.Store(true)
	state.requestBodyEnded()
	state.scheduleFlush()
	if got := recorder.flushes.Load(); got != 0 {
		t.Fatalf("%d flushes before the delay passed, want none", got)
	}
	noErr(t, state.finish())
	if got := recorder.flushes.Load(); got != 1 {
		t.Fatalf("%d flushes when Git finished, want 1", got)
	}
}

// After the first flush sent the headers, Git's later output, such as a
// keepalive, is flushed at once.
func TestFlushesAfterTheFirstAreNotDelayed(t *testing.T) {
	var bodyEnded atomic.Bool
	bodyEnded.Store(true)
	state, recorder := newTestResponseState(time.Millisecond, &bodyEnded)
	defer state.stop()
	state.WriteHeader(http.StatusOK)
	state.requestBodyEnded()
	waitFor(t, 10*time.Second, "the first flush", func() bool { return recorder.flushes.Load() == 1 })
	for want := int32(2); want <= 4; want++ {
		_, err := state.Write([]byte("0005\x02"))
		noErr(t, err)
		state.scheduleFlush()
		if got := recorder.flushes.Load(); got != want {
			t.Fatalf("%d flushes right after write %d, want %d", got, want-1, want)
		}
	}
}
