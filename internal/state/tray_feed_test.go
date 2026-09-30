package state

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Each kind reads the records written within the window, the oldest first,
// and counts all of them; a pull request still being created, a check or
// import that ended well and records outside the window are not read.
func TestFeedRecordsReadTheWindow(t *testing.T) {
	fixture := newCheckJobFixture(t)
	store, ctx, now := fixture.store, context.Background(), fixture.now
	window := func(kind string, after, until time.Time, limit int) ([]FeedRecord, int) {
		t.Helper()
		records, total, err := store.FeedRecords(ctx, kind, after, until, nil, limit)
		noErr(t, err)
		return records, total
	}

	for index, title := range []string{"First", "Second", "Third"} {
		_, err := store.CreatePullRequest(ctx, "project", title, "feature-"+title, "main", strings.Repeat("a", 40), strings.Repeat("b", 40), ReviewNotRequested, now.Add(time.Duration(index)*time.Second))
		noErr(t, err)
	}
	_, err := store.BeginPullRequestCreation(ctx, PullRequestCreation{
		RepositoryID: "project", Title: "Creating", SourceBranch: "creating", TargetBranch: "main",
		SourceOID: strings.Repeat("a", 40), TargetOID: strings.Repeat("b", 40), InitialReview: ReviewNotRequested,
	}, now.Add(time.Second))
	noErr(t, err)
	records, total := window(NotifyPullRequest, now.Add(-time.Second), now.Add(2*time.Second), 2)
	if total != 3 || len(records) != 2 || records[0].ID != "project/1" || records[0].Title != "First" || records[0].RepositoryName != "Project" ||
		records[0].Branch != "feature-First" || records[0].Number != 1 || !records[0].At.Equal(now) || records[1].Title != "Second" {
		t.Fatalf("pull requests %d %+v", total, records)
	}
	// The window starts after its first time and ends at its second.
	if records, total := window(NotifyPullRequest, now, now.Add(time.Second), 10); total != 1 || records[0].Title != "Second" {
		t.Fatalf("pull requests in (now, now+1s]: %d %+v", total, records)
	}

	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	runner, _ := fixture.issueRunner(t)
	failing := fixture.admit(t, pullRequestJobRequest())
	_, attempt := fixture.claimAndStart(t, failing, runner, "")
	completeJobAttempt(t, store, attempt, AttemptFailed, now.Add(3*time.Second))
	passing := fixture.admit(t, pushJobRequest())
	_, attempt = fixture.claimAndStart(t, passing, runner, "")
	completeJobAttempt(t, store, attempt, AttemptPassed, now.Add(3*time.Second))
	if records, total := window(NotifyCheckFailed, now, now.Add(3*time.Second), 10); total != 1 || records[0].ID != failing.ID ||
		records[0].Number != 1 || records[0].Branch != "main" || records[0].RepositoryName != "Project" || !records[0].At.Equal(now.Add(3*time.Second)) {
		t.Fatalf("failed checks %d %+v", total, records)
	}
	if _, total := window(NotifyCheckFailed, now.Add(3*time.Second), now.Add(time.Hour), 10); total != 0 {
		t.Fatalf("a check was read twice: %d", total)
	}

	// An import of a repository that is gone keeps its ID as its name.
	for _, run := range []struct{ id, repository, status string }{
		{"run-failed", "project", "failed"}, {"run-complete", "project", "complete"}, {"run-gone", "gone", "interrupted"},
	} {
		noErr(t, store.Exec(ctx, `INSERT INTO import_runs(id,repository_id,source_generation,authority_revision,kind,status,started_at,finished_at,message,created_at) VALUES(?,?,1,1,'scheduled',?,?,?,?,?)`,
			run.id, run.repository, run.status, now.Unix(), now.Add(4*time.Second).Unix(), "the source did not answer", now.Unix()))
	}
	records, total = window(NotifyImportFailed, now, now.Add(4*time.Second), 10)
	ids := []string{}
	for _, record := range records {
		ids = append(ids, record.ID+"="+record.RepositoryName)
	}
	if total != 2 || !slices.Equal(ids, []string{"run-failed=Project", "run-gone=gone"}) || records[0].Message != "the source did not answer" {
		t.Fatalf("failed imports %d %v", total, ids)
	}
	// Only the records include accepts are counted and returned.
	gone := func(record FeedRecord) bool { return record.RepositoryID == "gone" }
	if records, total, err := store.FeedRecords(ctx, NotifyImportFailed, now, now.Add(4*time.Second), gone, 10); err != nil || total != 1 || records[0].ID != "run-gone" {
		t.Fatalf("failed imports of a gone repository %d %+v %v", total, records, err)
	}

	noErr(t, store.Exec(ctx, `INSERT INTO backup_runs(id,kind,status,destination,message,started_at,finished_at) VALUES(?,'scheduled','failed','/backups','the disk is full',?,?)`,
		strings.Repeat("a", 32), now.Unix(), now.Add(5*time.Second).Unix()))
	noErr(t, store.Exec(ctx, `INSERT INTO backup_runs(id,kind,status,destination,started_at,finished_at) VALUES(?,'manual','succeeded','/backups',?,?)`,
		strings.Repeat("b", 32), now.Unix(), now.Add(5*time.Second).Unix()))
	if records, total := window(NotifyBackupFailed, now, now.Add(5*time.Second), 10); total != 1 || records[0].Message != "the disk is full" || records[0].RepositoryID != "" {
		t.Fatalf("failed backups %d %+v", total, records)
	}
	if _, _, err := store.FeedRecords(ctx, NotifyPush, now, now, nil, 1); err == nil {
		t.Fatal("pushes were read as timed records")
	}
}

// Pushes after a sequence come in order, and the last sequence stays once
// the pushes are trimmed away.
func TestPushesAfterAndTheLastSequence(t *testing.T) {
	store, ctx, now := newProjectStore(t)
	if last, err := store.LastPushSequence(ctx); err != nil || last != 0 {
		t.Fatalf("before any push: %d %v", last, err)
	}
	for index := range 3 {
		sequence, err := store.RecordPush(ctx, pushAt(now.Add(time.Duration(index)*time.Second)))
		noErr(t, err)
		if sequence != int64(index+1) {
			t.Fatalf("push %d has sequence %d", index, sequence)
		}
	}
	pushes, err := store.PushesAfter(ctx, 1)
	noErr(t, err)
	if len(pushes) != 2 || pushes[0].Sequence != 2 || pushes[1].Sequence != 3 || pushes[0].RepositoryName != "Project" {
		t.Fatalf("pushes after 1: %+v", pushes)
	}
	noErr(t, store.Exec(ctx, `DELETE FROM push_events`))
	if last, err := store.LastPushSequence(ctx); err != nil || last != 3 {
		t.Fatalf("after the pushes were removed: %d %v", last, err)
	}
}

// The notification choice and the cursor are files of this computer: a
// missing choice shows everything, a damaged one is an error, and hiding
// the icon removes the cursor.
func TestTrayNotificationFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	held, err := CreateDirectory(directory)
	noErr(t, err)
	defer held.Close()
	choice, err := ReadTrayNotifications(held)
	noErr(t, err)
	if !slices.Equal(choice.Kinds(), NotifyKinds) || choice.OnlyOthers {
		t.Fatalf("without a file: %+v", choice)
	}
	noErr(t, WriteTrayNotifications(held, TrayNotifications{OnlyOthers: true, KindsOff: []string{NotifyUpdate, NotifyPush}}))
	choice, err = ReadTrayNotifications(held)
	noErr(t, err)
	if !choice.OnlyOthers || !slices.Equal(choice.KindsOff, []string{NotifyPush, NotifyUpdate}) || choice.Shows(NotifyPush) || !choice.Shows(NotifyCheckFailed) {
		t.Fatalf("saved choice read as %+v", choice)
	}
	if kinds := (TrayNotifications{Off: true}).Kinds(); len(kinds) != 0 {
		t.Fatalf("all off shows %v", kinds)
	}
	if err := WriteTrayNotifications(held, TrayNotifications{KindsOff: []string{"webhook"}}); err == nil {
		t.Fatal("an unknown kind was saved")
	}
	for name, content := range map[string]string{
		"not JSON":      "on",
		"unknown field": `{"sound":true}`,
		"unknown kind":  `{"kinds_off":["webhook"]}`,
	} {
		noErr(t, os.WriteFile(filepath.Join(directory, TrayNotificationsFile), []byte(content), 0o600))
		if _, err := ReadTrayNotifications(held); err == nil {
			t.Errorf("%s: read as a choice", name)
		}
	}

	if cursor, err := ReadTrayCursor(held); err != nil || cursor != "" {
		t.Fatalf("without a cursor: %q %v", cursor, err)
	}
	noErr(t, WriteTrayCursor(held, "eyJwIjoxfQ"))
	if cursor, err := ReadTrayCursor(held); err != nil || cursor != "eyJwIjoxfQ" {
		t.Fatalf("saved cursor read as %q %v", cursor, err)
	}
	if err := WriteTrayCursor(held, "not a cursor"); err == nil {
		t.Fatal("a cursor that is not base64url was saved")
	}
	noErr(t, os.WriteFile(filepath.Join(directory, TrayCursorFile), []byte("not a cursor"), 0o600))
	if _, err := ReadTrayCursor(held); err == nil {
		t.Fatal("a damaged cursor was read")
	}
	noErr(t, WriteTrayCursor(held, "eyJwIjoxfQ"))
	noErr(t, SetTrayHidden(held, true))
	if cursor, err := ReadTrayCursor(held); err != nil || cursor != "" {
		t.Fatalf("hiding kept the cursor %q %v", cursor, err)
	}
	entries, err := os.ReadDir(directory)
	noErr(t, err)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("temporary file %s left behind", entry.Name())
		}
	}
}
