package githttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"owngit/internal/hostmem"
)

// With a pack limit, a further clone waits for the one running, while a push
// or a ref advertisement finds a slot at once; a clone that waits past the
// queue wait is refused as busy and holds nothing.
func TestPackSlotsHoldBackOnlyRequestsThatBuildAPack(t *testing.T) {
	slots := newAdmission()
	ctx := context.Background()
	limits := func(wait time.Duration) Limits {
		return Limits{PerRepository: 4, ExtraSlots: 1, PackSlots: 1, QueueWait: wait}
	}
	clone, err := slots.acquirePacking(ctx, "a", limits(0))
	noErr(t, err, "first clone")

	for range 2 {
		release, err := slots.acquire(ctx, "a", limits(0))
		noErr(t, err, "a push or ref advertisement behind a running clone")
		defer release()
	}
	if _, err := slots.acquirePacking(ctx, "a", limits(50*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("second clone: %v, want busy", err)
	}
	if _, err := slots.acquirePacking(ctx, "other", limits(50*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("clone of another repository: %v, want busy", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := slots.acquirePacking(cancelled, "a", limits(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled clone: %v, want cancelled", err)
	}
	slots.mu.Lock()
	packing, active := slots.packing, slots.active
	slots.mu.Unlock()
	if packing != 1 || active != 3 {
		t.Fatalf("after refused waiters: %d packing, %d active; want 1 and 3 (only the clone and two requests hold slots)", packing, active)
	}

	got := make(chan error, 1)
	go func() {
		release, err := slots.acquirePacking(ctx, "a", limits(5*time.Second))
		if err == nil {
			release()
		}
		got <- err
	}()
	clone()
	noErr(t, <-got, "a waiting clone did not get the released pack slot")
}

// Without a pack limit, requests that build a pack are limited only by the
// transfer slots, as before.
func TestNoPackSlotsLeavesTransferSlotsAlone(t *testing.T) {
	slots := newAdmission()
	ctx := context.Background()
	limits := Limits{PerRepository: 4, ExtraSlots: 0}
	for range 4 {
		release, err := slots.acquirePacking(ctx, "a", limits)
		noErr(t, err, "clone within the transfer slots")
		defer release()
	}
	if _, err := slots.acquirePacking(ctx, "a", limits); !errors.Is(err, errBusy) {
		t.Fatalf("fifth clone of one repository: %v, want busy", err)
	}
}

// A protocol version 2 ref listing is a POST to the upload-pack service but
// builds no pack, so it does not use a pack slot; a fetch does, and the body
// still reaches the backend whole.
func TestBuildsPackTellsAFetchFromARefListing(t *testing.T) {
	tests := []struct {
		name, method, body string
		want               bool
	}{
		{"version 2 ref listing", http.MethodPost, "0014command=ls-refs\n0001peel\n0000", false},
		{"version 2 fetch", http.MethodPost, "0012command=fetch\n0001done\n0000", true},
		{"version 0 request", http.MethodPost, "0032want 0123456789012345678901234567890123456789\n0000", true},
		{"ref advertisement", http.MethodGet, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/git/a.git/git-upload-pack", strings.NewReader(test.body))
			if got := buildsPack(request, route{service: "git-upload-pack"}); got != test.want {
				t.Errorf("buildsPack = %v, want %v", got, test.want)
			}
			rest, err := io.ReadAll(request.Body)
			noErr(t, err, "read the body")
			if string(rest) != test.body {
				t.Errorf("body after the check = %q, want %q", rest, test.body)
			}
		})
	}
}

// With a memory gate every request, a push included, needs a gate slot as well
// as its transfer slot; it waits for one and starts when it is released.
func TestMemoryGateHoldsBackEveryTransferUntilAReleasedSlot(t *testing.T) {
	slots := newAdmission()
	ctx := context.Background()
	limits := func(wait time.Duration) Limits {
		return Limits{PerRepository: 4, ExtraSlots: 1, QueueWait: wait, Memory: hostmem.NewGate(1)}
	}
	shared := limits(0)
	first, err := slots.acquire(ctx, "a", shared)
	noErr(t, err, "first transfer")
	shared.QueueWait = 50 * time.Millisecond
	if _, err := slots.acquire(ctx, "b", shared); !errors.Is(err, errBusy) {
		t.Fatalf("second transfer with a full gate: %v, want busy", err)
	}
	shared.QueueWait = 5 * time.Second
	got := make(chan error, 1)
	go func() {
		release, err := slots.acquire(ctx, "b", shared)
		if err == nil {
			release()
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	first()
	noErr(t, <-got, "a waiting transfer did not get the released gate slot")
}
