package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreationRecordSurvivesClientCancellation(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	// WAL readers and Git preparation can continue while this real second
	// connection holds off the first write of AddRepository.
	database, err := sql.Open("sqlite", filepath.Join(manager.Store.Dir(), "owngit.sqlite"))
	noErr(t, err)
	defer database.Close()
	connection, err := database.Conn(context.Background())
	noErr(t, err)
	defer connection.Close()
	_, err = connection.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	noErr(t, err)
	defer connection.ExecContext(context.Background(), "ROLLBACK")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := manager.CreateWithOptions(ctx, "disconnected", "", CreateOptions{})
		result <- err
	}()
	published := filepath.Join(manager.RepositoryRoot(), "disconnected.git")
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(published); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("creation ended before publication: %v", err)
		case <-deadline:
			t.Fatal("creation did not reach publication")
		case <-ticker.C:
		}
	}
	cancel()
	_, err = connection.ExecContext(context.Background(), "ROLLBACK")
	noErr(t, err)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("published creation lost its record when the client cancelled: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("detached recording did not finish within its bound")
	}
	if _, exists, err := manager.Store.Repository(context.Background(), "disconnected"); err != nil || !exists {
		t.Fatalf("accepted creation record exists=%v err=%v", exists, err)
	}
	if entries, err := os.ReadDir(filepath.Join(manager.RepositoryRoot(), ".owngit-failed-create")); !os.IsNotExist(err) && (err != nil || len(entries) != 0) {
		t.Fatalf("cancellation created preservation entries: %v %v", entries, err)
	}
}

func TestCreationRecordTimeoutLeavesNoLateRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 5 second creation record timeout")
	}
	manager, _, _ := newTestRepository(t)
	database, err := sql.Open("sqlite", filepath.Join(manager.Store.Dir(), "owngit.sqlite"))
	noErr(t, err)
	defer database.Close()
	connection, err := database.Conn(context.Background())
	noErr(t, err)
	defer connection.Close()
	// CreateWithOptions does no import-settings write. WAL reads and Git
	// preparation proceed; this writer lock blocks only the record step.
	_, err = connection.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	noErr(t, err)
	defer connection.ExecContext(context.Background(), "ROLLBACK")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := manager.CreateWithOptions(ctx, "record-deadline", "", CreateOptions{})
		result <- err
	}()
	published := filepath.Join(manager.RepositoryRoot(), "record-deadline.git")
	// Preparation has a separate generous bound: loaded hosts can take
	// longer to launch Git, but that time is not the recording timeout.
	preparationDeadline := time.NewTimer(6 * creationRecordTimeout)
	defer preparationDeadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(published); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("creation ended before publication: %v", err)
		case <-preparationDeadline.C:
			t.Fatal("creation did not publish while the record writer was locked")
		case <-ticker.C:
		}
	}
	observedPublication := time.Now()
	cancel()
	// Keep the lock until the synchronous attempt returns. A stopped
	// request must not turn this into an early cancellation or a late save.
	select {
	case err := <-result:
		if err == nil || !strings.HasPrefix(err.Error(), "record repository:") {
			t.Fatalf("record deadline result=%v", err)
		}
		// Start time is a conservative lower bound that avoids depending on
		// when a loaded observer was scheduled after the actual rename.
		if elapsed := time.Since(started); elapsed < creationRecordTimeout-time.Second {
			t.Fatalf("recording returned before its timeout: elapsed=%v timeout=%v", elapsed, creationRecordTimeout)
		}
		t.Logf("record failure returned %v after observed publication (configured timeout %v)", time.Since(observedPublication), creationRecordTimeout)
	case <-time.After(4 * creationRecordTimeout):
		t.Fatal("recording did not stop within the timeout and scheduling slack")
	}
	if _, exists, err := manager.Store.Repository(context.Background(), "record-deadline"); err != nil || exists {
		t.Fatalf("timed-out record before releasing writer: exists=%v err=%v", exists, err)
	}
	if _, err := os.Lstat(published); !os.IsNotExist(err) {
		t.Fatalf("timed-out unaccepted folder remains at its final name: %v", err)
	}
	kept, err := os.ReadDir(filepath.Join(manager.RepositoryRoot(), failedCreateDirectory))
	noErr(t, err)
	if len(kept) != 1 || !kept[0].IsDir() {
		t.Fatalf("timed-out creation preservation entries=%v", kept)
	}
	_, err = connection.ExecContext(context.Background(), "ROLLBACK")
	noErr(t, err)
	// CreateWithOptions has returned and has no background record worker.
	// Check after release as well, allowing the driver scheduler to settle.
	time.Sleep(250 * time.Millisecond)
	if _, exists, err := manager.Store.Repository(context.Background(), "record-deadline"); err != nil || exists {
		t.Fatalf("timed-out record appeared after releasing writer: exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(filepath.Join(manager.RepositoryRoot(), failedCreateDirectory, kept[0].Name(), "HEAD")); err != nil {
		t.Fatalf("timed-out creation's initialized tree was not preserved: %v", err)
	}
}

func TestConcurrentFailedCreationsShareThePreservationBound(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	ctx := context.Background()
	noErr(t, manager.Store.Exec(ctx, `CREATE TRIGGER refuse_creation BEFORE INSERT ON repositories BEGIN SELECT RAISE(ABORT,'recording refused'); END`))
	defer manager.Store.Exec(ctx, `DROP TRIGGER refuse_creation`)
	results := make(chan error, 16)
	for index := 0; index < 16; index++ {
		go func(index int) {
			_, err := manager.Create(ctx, fmt.Sprintf("failed-%d", index), "")
			results <- err
		}(index)
	}
	for index := 0; index < 16; index++ {
		if err := <-results; err == nil {
			t.Fatal("injected concurrent recording failure succeeded")
		}
	}
	entries, err := os.ReadDir(filepath.Join(manager.RepositoryRoot(), failedCreateDirectory))
	noErr(t, err)
	if len(entries) != maximumFailedCreations {
		t.Fatalf("concurrent failures kept %d entries, want %d", len(entries), maximumFailedCreations)
	}
}

func TestFailedCreationPreservationHasAFixedBound(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	ctx := context.Background()
	noErr(t, manager.Store.Exec(ctx, `CREATE TRIGGER refuse_creation BEFORE INSERT ON repositories BEGIN SELECT RAISE(ABORT,'recording refused'); END`))
	parent := filepath.Join(manager.RepositoryRoot(), ".owngit-failed-create")
	for attempt := 0; attempt < 32; attempt++ {
		_, err := manager.Create(ctx, "retry", "")
		if err == nil {
			t.Fatal("injected recording failure succeeded")
		}
		entries, err := os.ReadDir(parent)
		noErr(t, err)
		if len(entries) > 8 {
			t.Fatalf("unbounded kept entries after attempt %d: %d", attempt+1, len(entries))
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := manager.Create(cancelled, "retry", ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled retry error=%v", err)
		}
	}
	entries, err := os.ReadDir(parent)
	noErr(t, err)
	if len(entries) != 8 {
		t.Fatalf("kept count=%d, want 8", len(entries))
	}
	final := filepath.Join(manager.RepositoryRoot(), "retry.git")
	if _, err := os.Stat(filepath.Join(final, "HEAD")); err != nil {
		t.Fatalf("full preservation area did not leave the unaccepted final folder: %v", err)
	}
	noErr(t, manager.Store.Exec(ctx, `DROP TRIGGER refuse_creation`))
	// Model owner recovery without deleting any fixture: move both the kept
	// area and checked unaccepted final folder outside the storage root.
	cleared := t.TempDir()
	noErr(t, os.Rename(parent, filepath.Join(cleared, "kept")))
	noErr(t, os.Rename(final, filepath.Join(cleared, "unaccepted")))
	_, err = manager.Create(ctx, "retry", "legitimate recovery")
	noErr(t, err)
	row, exists, err := manager.Store.Repository(ctx, "retry")
	if err != nil || !exists || !strings.Contains(row.Description, "legitimate") {
		t.Fatalf("recovery row=%+v exists=%v err=%v", row, exists, err)
	}
}
