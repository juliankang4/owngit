package importsync

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

// clockIsInPublication reports whether the Clock callback was called from
// publication.
func clockIsInPublication() bool {
	var callers [32]uintptr
	count := runtime.Callers(2, callers[:])
	frames := runtime.CallersFrames(callers[:count])
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, "importsync.(*Service).publish") {
			return true
		}
		if !more {
			return false
		}
	}
}

// A publication callback that blocks, whether Clock or Logf, must not hold
// anything that stops another repository from being configured.
func TestBlockedPublicationCallbackAllowsOtherRepositoryConfiguration(t *testing.T) {
	for _, test := range []struct {
		name    string
		install func(t *testing.T, f *fixture, block func())
		check   func(t *testing.T, refreshErr error)
	}{
		{"Clock", func(t *testing.T, f *fixture, block func()) {
			f.service.Clock = func() time.Time {
				if clockIsInPublication() {
					block()
				}
				return f.now
			}
		}, func(t *testing.T, err error) {
			if err != nil {
				t.Errorf("publication after Clock release: %v", err)
			}
		}},
		{"Logf", func(t *testing.T, f *fixture, block func()) {
			// A failed observation write makes publication log.
			noErr(t, f.store.Exec(context.Background(), `CREATE TRIGGER fail_observation_write BEFORE INSERT ON import_ref_observations BEGIN SELECT RAISE(FAIL,'synthetic observation failure'); END`))
			f.service.Logf = func(format string, _ ...any) {
				if strings.Contains(format, "import publication") {
					block()
				}
			}
		}, func(t *testing.T, err error) {
			if err == nil || problemCode(err) != CodeStateUnavailable {
				t.Errorf("mandatory bookkeeping failure was not propagated after Logf release: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.commit("initial", "initial\n")
			f.mustImport(ImportInput{})
			f.commit("next", "next\n")
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			test.install(t, f, func() {
				once.Do(func() {
					close(entered)
					<-release
				})
			})
			refreshDone := make(chan error, 1)
			go func() {
				_, err := f.refresh()
				refreshDone <- err
			}()
			select {
			case <-entered:
			case err := <-refreshDone:
				t.Fatalf("publication %s was not reached: %v", test.name, err)
			}
			otherDone := make(chan error, 1)
			go func() {
				_, err := f.store.ConfigureImportSource(ctx, state.ImportSourceInput{
					RepositoryID: "other", URL: "https://example.invalid/other.git",
					Mode: state.ImportModeStandalone, Now: f.now,
				})
				otherDone <- err
			}()
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			completed := false
			select {
			case err := <-otherDone:
				completed = true
				if err != nil {
					t.Errorf("other repository configuration: %v", err)
				}
			case <-timer.C:
				t.Errorf("blocked publication %s stalled another repository configuration", test.name)
			}
			close(release)
			if !completed {
				if err := <-otherDone; err != nil {
					t.Errorf("other repository configuration after release: %v", err)
				}
			}
			test.check(t, <-refreshDone)
		})
	}
}

func TestPublicationClockCanReenterStateAuthorityHelper(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	second := f.commit("next", "next\n")
	var once sync.Once
	var callbackErr error
	f.service.Clock = func() time.Time {
		if clockIsInPublication() {
			once.Do(func() {
				done := make(chan error, 1)
				go func() {
					_, err := f.store.SetImportTransportConsent(ctx, "project", true, f.now.Add(time.Second))
					done <- err
				}()
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				select {
				case callbackErr = <-done:
				case <-timer.C:
					callbackErr = fmt.Errorf("publication Clock deadlocked while reentering state authority")
				}
			})
		}
		return f.now
	}
	run, err := f.refresh()
	noErr(t, callbackErr)
	if err == nil || problemCode(err) != CodeSuperseded || run.Status != state.ImportRunSuperseded {
		t.Fatalf("reentrant authority change did not supersede publication: run=%+v err=%v", run, err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != first || got == second {
		t.Fatalf("reentrant authority change allowed stale publication: got=%s first=%s second=%s", got, first, second)
	}
}
