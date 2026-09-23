package importsync

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestBlockedPublicationLogAllowsOtherRepositoryConfiguration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	f.commit("next", "next\n")
	if err := f.store.Exec(ctx, `CREATE TRIGGER fail_observation_write BEFORE INSERT ON import_ref_observations BEGIN SELECT RAISE(FAIL,'synthetic observation failure'); END`); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.service.Logf = func(format string, _ ...any) {
		if strings.Contains(format, "import publication") {
			once.Do(func() {
				close(entered)
				<-release
			})
		}
	}
	refreshDone := make(chan error, 1)
	go func() {
		_, err := f.refresh()
		refreshDone <- err
	}()
	select {
	case <-entered:
	case err := <-refreshDone:
		t.Fatalf("publication Logf was not reached: %v", err)
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
		t.Error("blocked publication Logf stalled another repository configuration")
	}
	close(release)
	if !completed {
		if err := <-otherDone; err != nil {
			t.Errorf("other repository configuration after release: %v", err)
		}
	}
	if err := <-refreshDone; err == nil || problemCode(err) != CodeStateUnavailable {
		t.Errorf("mandatory bookkeeping failure was not propagated after Logf release: %v", err)
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
		if parentClockIsInPublication() {
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
	if callbackErr != nil {
		t.Fatal(callbackErr)
	}
	if err == nil || problemCode(err) != CodeSuperseded || run.Status != state.ImportRunSuperseded {
		t.Fatalf("reentrant authority change did not supersede publication: run=%+v err=%v", run, err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != first || got == second {
		t.Fatalf("reentrant authority change allowed stale publication: got=%s first=%s second=%s", got, first, second)
	}
}
