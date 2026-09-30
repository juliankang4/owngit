package backups

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/recovery"
	"owngit/internal/state"
)

// waitForTask waits for the verification or upload that runs.
func (f *fixture) waitForTask(t *testing.T) {
	t.Helper()
	f.service.work.Wait()
}

func TestCheckVerifiesAListedBackupAgainAndRecordsTheResult(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	run := f.backUpNow(t)
	if _, err := f.service.StartCheck(context.Background(), "unknown"); !errors.Is(err, ErrNoBackup) {
		t.Fatalf("unknown run: %v", err)
	}
	check, err := f.service.StartCheck(context.Background(), run.ID)
	noErr(t, err)
	if check.Status != CheckRunning || check.RunID != run.ID {
		t.Fatalf("check: %+v", check)
	}
	f.waitForTask(t)
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	if status.Check == nil || status.Check.Status != CheckPassed || status.Check.FinishedAt == nil {
		t.Fatalf("check after it ended: %+v", status.Check)
	}

	// A damaged bundle fails the verification, which is recorded, so the
	// backup no longer counts as verified.
	bundle := filepath.Join(runPath(run), "repositories", "project.bundle")
	content, err := os.ReadFile(bundle)
	noErr(t, err)
	content[len(content)/2] ^= 0xff
	noErr(t, os.WriteFile(bundle, content, 0o600))
	_, err = f.service.StartCheck(context.Background(), run.ID)
	noErr(t, err)
	f.waitForTask(t)
	status, err = f.service.Status(context.Background())
	noErr(t, err)
	if status.Check.Status != CheckFailed || status.Check.Message == "" || status.LastVerified != nil {
		t.Fatalf("status after a failed check: %+v %+v", status.Check, status.LastVerified)
	}
	if recorded := f.run(t, run.ID); recorded.Verification != state.BackupVerifyFailed {
		t.Fatalf("recorded verification %q", recorded.Verification)
	}

	noErr(t, os.RemoveAll(runPath(run)))
	if _, err := f.service.StartCheck(context.Background(), run.ID); !errors.Is(err, ErrBackupGone) {
		t.Fatalf("missing backup: %v", err)
	}
}

// One backup, verification or upload runs at a time.
func TestBackupsVerificationsAndUploadsTakeTurns(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	run := f.backUpNow(t)
	ctx := context.Background()

	f.service.mu.Lock()
	noErr(t, f.service.begin(ctx, "check"))
	f.service.mu.Unlock()
	if _, err := f.service.StartNow(); !errors.Is(err, ErrBusy) {
		t.Fatalf("backup during a verification: %v", err)
	}
	if _, err := f.service.StartCheck(ctx, run.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("second verification: %v", err)
	}
	if _, err := f.service.ReceiveUpload(ctx, strings.NewReader("x"), 1); !errors.Is(err, ErrBusy) {
		t.Fatalf("upload during a verification: %v", err)
	}
	f.service.end()

	running := state.BackupRun{ID: strings.Repeat("a", 32), Kind: state.BackupRunManual, Status: state.BackupRunning, Destination: f.destination, StartedAt: f.clock.Now()}
	noErr(t, f.store.StartBackupRun(ctx, running))
	if _, err := f.service.StartCheck(ctx, run.ID); !errors.Is(err, state.ErrBackupRunning) {
		t.Fatalf("verification during a backup: %v", err)
	}
	if _, err := f.service.ReceiveUpload(ctx, strings.NewReader("x"), 1); !errors.Is(err, state.ErrBackupRunning) {
		t.Fatalf("upload during a backup: %v", err)
	}
}

// download writes the backup of run as an archive.
func (f *fixture) download(t *testing.T, run state.BackupRun) []byte {
	t.Helper()
	download, err := f.service.OpenDownload(context.Background(), run.ID)
	noErr(t, err)
	defer download.Close()
	var archive bytes.Buffer
	noErr(t, download.WriteArchive(context.Background(), &archive))
	return archive.Bytes()
}

func TestDownloadedBackupIsKeptUntilItEnds(t *testing.T) {
	f := newFixture(t)
	keep := 1
	f.configure(t, ScheduleChange{Keep: &keep})
	first := f.backUpNow(t)
	download, err := f.service.OpenDownload(context.Background(), first.ID)
	noErr(t, err)
	second := f.backUpNow(t)
	if !present(first) || !strings.Contains(second.Message, "was being downloaded") {
		t.Fatalf("a backup being downloaded was removed: message %q", second.Message)
	}
	var archive bytes.Buffer
	noErr(t, download.WriteArchive(context.Background(), &archive))
	download.Close()
	f.backUpNow(t)
	if present(first) || present(second) {
		t.Fatal("older backups stayed after the download ended")
	}
	if _, err := f.service.OpenDownload(context.Background(), first.ID); !errors.Is(err, ErrNoBackup) {
		t.Fatalf("download of a removed backup: %v", err)
	}
}

func TestUploadedBackupIsVerifiedAndReplacedOrRemoved(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	run := f.backUpNow(t)
	archive := f.download(t, run)
	ctx := context.Background()

	upload, err := f.service.ReceiveUpload(ctx, bytes.NewReader(archive), int64(len(archive)))
	noErr(t, err)
	if upload.Status != UploadVerifying || upload.Name != run.BackupName || upload.Path != filepath.Join(f.store.Dir(), UploadsFolder, run.BackupName) {
		t.Fatalf("upload: %+v", upload)
	}
	f.waitForTask(t)
	status, err := f.service.Status(ctx)
	noErr(t, err)
	if status.Upload == nil || status.Upload.Status != UploadPassed || status.Upload.RemovesAt == nil ||
		!status.Upload.RemovesAt.Equal(upload.ReceivedAt.Add(UploadKept)) {
		t.Fatalf("upload after verification: %+v", status.Upload)
	}
	result, err := recovery.Verify(ctx, upload.Path, "", "")
	noErr(t, err)
	if !result.Verified {
		t.Fatalf("the uploaded backup does not verify offline: %+v", result)
	}

	// An archive that is no backup replaces nothing it could use and
	// leaves nothing behind.
	if _, err := f.service.ReceiveUpload(ctx, bytes.NewReader(archive[:len(archive)/2]), int64(len(archive)/2)); !errors.Is(err, ErrUploadRefused) {
		t.Fatalf("truncated upload: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.store.Dir(), UploadsFolder)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused upload left its folder: %v", err)
	}
	if _, err := f.service.ReceiveUpload(ctx, bytes.NewReader(archive), 1<<62); !errors.Is(err, ErrUploadRefused) || !strings.Contains(err.Error(), "free space") {
		t.Fatalf("upload larger than the disk: %v", err)
	}

	// A backup whose bundle does not match its manifest is removed once
	// its verification fails.
	damaged := bytes.Clone(archive)
	marker := bytes.LastIndex(damaged, []byte("PACK"))
	damaged[marker+20] ^= 0xff
	_, err = f.service.ReceiveUpload(ctx, bytes.NewReader(damaged), int64(len(damaged)))
	noErr(t, err)
	f.waitForTask(t)
	status, err = f.service.Status(ctx)
	noErr(t, err)
	if status.Upload.Status != UploadFailed || status.Upload.Path != "" || !strings.Contains(status.Upload.Message, "did not pass verification") {
		t.Fatalf("damaged upload: %+v", status.Upload)
	}
	if _, err := os.Lstat(filepath.Join(f.store.Dir(), UploadsFolder)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a backup that failed verification stayed: %v", err)
	}

	// A starting OwnGit removes an upload left from before.
	noErr(t, os.MkdirAll(filepath.Join(f.store.Dir(), UploadsFolder, "left"), 0o700))
	restarted := &Service{Store: f.store, Repositories: f.manager, Logf: t.Logf}
	noErr(t, restarted.Start(ctx))
	noErr(t, restarted.Stop(ctx))
	if _, err := os.Lstat(filepath.Join(f.store.Dir(), UploadsFolder)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the upload from before stayed: %v", err)
	}
}

// exchange swaps the folders of two backups by name. It says false when
// Windows keeps the first from being renamed because OwnGit holds it.
func exchange(t *testing.T, first, second string) bool {
	t.Helper()
	aside := first + ".aside"
	if err := os.Rename(first, aside); err != nil && runtime.GOOS == "windows" {
		return false
	} else {
		noErr(t, err)
	}
	noErr(t, os.Rename(second, first))
	noErr(t, os.Rename(aside, second))
	return true
}

// A backup whose folder another backup takes the name of while it is
// verified again is not recorded as verified by the other backup.
func TestCheckRecordsNothingForAnExchangedBackup(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	first := f.backUpNow(t)
	// The second backup holds another commit, so no bundle of it passes
	// for one of the first.
	work := filepath.Join(filepath.Dir(f.store.Dir()), "work")
	noErr(t, os.WriteFile(filepath.Join(work, "file"), []byte("changed"), 0o600))
	git(t, work, "commit", "-am", "changed")
	remote, err := f.manager.Path("project")
	noErr(t, err)
	git(t, work, "push", remote, "HEAD:refs/heads/main")
	second := f.backUpNow(t)
	noErr(t, f.store.RecordBackupVerification(context.Background(), first.ID, state.BackupVerifyFailed))

	_, err = f.service.StartCheck(context.Background(), first.ID)
	noErr(t, err)
	if !exchange(t, runPath(first), runPath(second)) {
		f.waitForTask(t)
		if recorded := f.run(t, first.ID); recorded.Verification != state.BackupVerifyPassed {
			t.Fatalf("the held backup, which could not be exchanged, was recorded %q", recorded.Verification)
		}
		return
	}
	f.waitForTask(t)
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	if status.Check.Status != CheckFailed || !strings.Contains(status.Check.Message, "another backup") {
		t.Fatalf("check of an exchanged backup: %+v", status.Check)
	}
	if recorded := f.run(t, first.ID); recorded.Verification != state.BackupVerifyFailed {
		t.Fatalf("the exchanged backup was recorded %q", recorded.Verification)
	}
}
