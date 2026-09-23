package importsync

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

func parentClockIsInPublication() bool {
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

func TestParentBlockedPublicationClockAllowsOtherRepositoryConfiguration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	f.commit("next", "next\n")
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.service.Clock = func() time.Time {
		if parentClockIsInPublication() {
			once.Do(func() {
				close(entered)
				<-release
			})
		}
		return f.now
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.refresh()
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("publication Clock was not reached: %v", err)
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
		t.Error("blocked publication Clock stalled another repository configuration")
	}
	close(release)
	if !completed {
		if err := <-otherDone; err != nil {
			t.Errorf("other repository configuration after release: %v", err)
		}
	}
	if err := <-done; err != nil {
		t.Errorf("publication after Clock release: %v", err)
	}
}
