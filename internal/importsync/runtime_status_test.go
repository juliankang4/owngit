package importsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A scheduler that cannot start is visible on the status surface, not only in
// the log, and a running scheduler is reported as running.
func TestStatusReportsSchedulerAvailability(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	status, err := f.service.Status(ctx, "project")
	if err != nil || status.Runtime.SchedulerRunning || status.Runtime.Code != "" {
		t.Fatalf("status before any scheduler runtime=%+v err=%v", status.Runtime, err)
	}
	scheduler := &Scheduler{Service: f.service}
	noErr(t, scheduler.Start(ctx))
	status, err = f.service.Status(ctx, "project")
	if err != nil || !status.Runtime.SchedulerRunning || status.Runtime.Code != "" {
		t.Fatalf("status with a running scheduler runtime=%+v err=%v", status.Runtime, err)
	}
	noErr(t, scheduler.Stop(ctx))
	noErr(t, f.service.Close())

	// A second process-like service on a blocked staging root cannot prepare.
	blocked := newFixture(t)
	noErr(t, os.MkdirAll(filepath.Join(blocked.store.Dir(), "runtime"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(blocked.store.Dir(), "runtime", "import-staging"), []byte("not a directory"), 0o600))
	failed := &Scheduler{Service: blocked.service}
	if err := failed.Start(ctx); err == nil {
		t.Fatal("scheduler started on a blocked staging root")
	}
	status, err = blocked.service.Status(ctx, "project")
	if err != nil || status.Runtime.SchedulerRunning || !status.Runtime.SchedulerFailed || status.Runtime.Code == "" || status.Runtime.Reason == "" {
		t.Fatalf("status after a failed scheduler start runtime=%+v err=%v", status.Runtime, err)
	}
	if availability := blocked.service.Availability(ctx); availability.Code == "" {
		t.Fatalf("availability hides the scheduler failure: %+v", availability)
	}
}

// Status does not wait for a writer that holds the repository lock, such as
// a long publication. Local refs read as unknown until the writer finishes.
func TestStatusDoesNotWaitForRepositoryWriter(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	lock := f.manager.Locks.For("project")
	lock.Lock()
	done := make(chan Status, 1)
	go func() {
		status, err := f.service.Status(context.Background(), "project")
		if err != nil {
			t.Error(err)
		}
		done <- status
	}()
	var busy Status
	select {
	case busy = <-done:
	case <-time.After(10 * time.Second):
		lock.Unlock()
		t.Fatal("status waited for the repository writer")
	}
	lock.Unlock()
	if len(busy.Refs) == 0 {
		t.Fatal("status during a write lost the observed refs")
	}
	for _, ref := range busy.Refs {
		if ref.State != "unknown_local" || ref.LocalOID != "" {
			t.Fatalf("ref during a write=%+v", ref)
		}
	}
	idle, err := f.service.Status(context.Background(), "project")
	if err != nil || len(idle.Refs) == 0 || idle.Refs[0].State != "tracked" {
		t.Fatalf("status after the write refs=%+v err=%v", idle.Refs, err)
	}
}
