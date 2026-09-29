package pullrequest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

var access = state.Actor{Kind: state.ActorAccess}

// newTextFixture is a repository with main and a feature branch one commit
// ahead, and returns the source and target commit IDs.
func newTextFixture(t *testing.T) (*serviceFixture, string, string) {
	t.Helper()
	fixture := newServiceFixture(t)
	fixture.commitFile("file.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("file.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	return fixture, sourceOID, fixture.ref("refs/heads/main")
}

func int64Pointer(value int64) *int64 { return &value }

func stringPointer(value string) *string { return &value }

// A description is kept as LF text, shown by show and every change but not
// by a list, and the access that created it is recorded.
func TestPullRequestDescriptionIsKeptWithWhoCreatedIt(t *testing.T) {
	fixture, _, _ := newTextFixture(t)
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Described", Body: "## What\r\n\r\nWhy <b>it</b> matters\r\n",
		SourceBranch: "feature", TargetBranch: "main", Actor: access,
	})
	noErr(t, err)
	if created.Body == nil || *created.Body != "## What\n\nWhy <b>it</b> matters\n" || created.EditRevision != 0 || created.EditedAt != nil {
		t.Fatalf("created body=%v revision=%d edited=%v", created.Body, created.EditRevision, created.EditedAt)
	}
	if created.CreatedBy == nil || *created.CreatedBy != access || created.EditedBy != nil || created.MergedBy != nil {
		t.Fatalf("created actors=%v %v %v", created.CreatedBy, created.EditedBy, created.MergedBy)
	}
	listed, err := fixture.service.List(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	if len(listed) != 1 || listed[0].Body != nil || listed[0].ReviewNotes != nil {
		t.Fatalf("a list carried the description: %+v", listed[0])
	}
	record, _, err := fixture.store.PullRequest(fixture.ctx, fixture.repositoryID, created.Number)
	noErr(t, err)
	if record.Body != *created.Body || record.CreatedBy != access {
		t.Fatalf("stored record=%+v", record)
	}
	// The creation's review choice is a review row made by the same access.
	review, _, err := fixture.store.PullRequestReviewForRevision(fixture.ctx, fixture.repositoryID, created.Number, created.Source.OID, created.Target.OID)
	noErr(t, err)
	if review.Actor != access {
		t.Fatalf("initial review actor=%+v", review.Actor)
	}
}

// Text is at most 64 KiB of UTF-8 without NUL, counted after CRLF becomes LF.
// Titles keep their own rule.
func TestPullRequestTextLimits(t *testing.T) {
	limit := state.MaximumPullRequestTextBytes
	for _, test := range []struct {
		name, text string
		accepted   bool
	}{
		{"empty", "", true},
		{"at the limit", strings.Repeat("<", limit), true},
		{"at the limit once CRLF is LF", strings.Repeat("\r\n", limit), true},
		{"one byte over", strings.Repeat("a", limit+1), false},
		{"NUL", "a\x00b", false},
		{"not UTF-8", "\xff", false},
	} {
		text, err := pullRequestText(test.text, "invalid_body", "description")
		if (err == nil) != test.accepted {
			t.Errorf("%s: err=%v", test.name, err)
		}
		if err != nil && problemCode(err) != "invalid_body" {
			t.Errorf("%s: code=%q", test.name, problemCode(err))
		}
		if err == nil && strings.Contains(text, "\r\n") {
			t.Errorf("%s: CRLF was kept", test.name)
		}
	}
	fixture, _, _ := newTextFixture(t)
	_, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Too long", Body: strings.Repeat("a", limit+1), SourceBranch: "feature", TargetBranch: "main",
	})
	if problemCode(err) != "invalid_body" {
		t.Fatalf("oversized description err=%v", err)
	}
	if records, err := fixture.store.PullRequests(fixture.ctx, fixture.repositoryID); err != nil || len(records) != 0 {
		t.Fatalf("a refused description created records=%v err=%v", records, err)
	}
}

// An edit names the revision it read. A second edit from the same revision is
// refused with the current one and changes nothing; an edit that changes
// nothing records nothing; and text edits leave revisions, reviews, checks
// and the merge record as they were, in every state.
func TestPullRequestEditRefusesStaleRevisionsAndLeavesEvidence(t *testing.T) {
	fixture, sourceOID, targetOID := newTextFixture(t)
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Original", Body: "first", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	noErr(t, err)
	number := created.Number
	if _, err := fixture.service.Edit(fixture.ctx, fixture.repositoryID, number, EditInput{Title: stringPointer("x")}); problemCode(err) != "invalid_edit_revision" {
		t.Fatalf("edit without a revision err=%v", err)
	}
	if _, err := fixture.service.Edit(fixture.ctx, fixture.repositoryID, number, EditInput{EditRevision: int64Pointer(0)}); problemCode(err) != "invalid_edit" {
		t.Fatalf("empty edit err=%v", err)
	}

	unchanged, err := fixture.service.Edit(fixture.ctx, fixture.repositoryID, number, EditInput{EditRevision: int64Pointer(0), Title: stringPointer(" Original "), Actor: access})
	noErr(t, err)
	if unchanged.EditRevision != 0 || unchanged.EditedAt != nil || unchanged.EditedBy != nil {
		t.Fatalf("an edit that changed nothing was recorded: %+v", unchanged)
	}

	titled, err := fixture.service.Edit(fixture.ctx, fixture.repositoryID, number, EditInput{EditRevision: int64Pointer(0), Title: stringPointer("Renamed"), Actor: access})
	noErr(t, err)
	if titled.Title != "Renamed" || *titled.Body != "first" || titled.EditRevision != 1 || titled.EditedAt == nil || titled.EditedBy == nil || *titled.EditedBy != access {
		t.Fatalf("title edit=%+v", titled)
	}
	_, err = fixture.service.Edit(fixture.ctx, fixture.repositoryID, number, EditInput{EditRevision: int64Pointer(0), Body: stringPointer("overwrite"), Actor: access})
	var problem *Problem
	if !errors.As(err, &problem) || problem.Code != "stale_edit" || problem.Details != (StaleEdit{CurrentEditRevision: 1}) {
		t.Fatalf("stale edit err=%v", err)
	}
	shown, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, number)
	noErr(t, err)
	if shown.Title != "Renamed" || *shown.Body != "first" || shown.EditRevision != 1 {
		t.Fatalf("a stale edit changed the pull request: %+v", shown)
	}

	revisionsBefore, err := fixture.store.PullRequestRevisionsFor(fixture.ctx, fixture.repositoryID, number)
	noErr(t, err)
	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID, Actor: access})
	noErr(t, err)
	if merged.MergedBy == nil || *merged.MergedBy != access {
		t.Fatalf("merge actor=%v", merged.MergedBy)
	}
	described, err := fixture.service.Edit(fixture.ctx, fixture.repositoryID, number, EditInput{EditRevision: int64Pointer(1), Body: stringPointer("after the merge"), Actor: access})
	noErr(t, err)
	if *described.Body != "after the merge" || described.EditRevision != 2 || described.State != state.PullRequestMerged || described.Merge == nil ||
		described.Merge.OID != merged.Merge.OID || !described.Merge.MergedAt.Equal(merged.Merge.MergedAt) || described.MergedBy == nil ||
		described.Review.Status != merged.Review.Status || described.Checks.Status != merged.Checks.Status {
		t.Fatalf("an edit of a merged pull request changed its record: %+v", described)
	}
	revisionsAfter, err := fixture.store.PullRequestRevisionsFor(fixture.ctx, fixture.repositoryID, number)
	noErr(t, err)
	if len(revisionsAfter) != len(revisionsBefore) {
		t.Fatalf("text edits changed revision bindings: %d, then %d", len(revisionsBefore), len(revisionsAfter))
	}
}

// A review note stays with the pair it reviewed. After a branch moves the
// current review no longer applies, but the note is still shown, marked as
// about earlier commits. Only the newest notes are shown, and the view says
// that older ones exist.
func TestReviewNotesStayWithTheirRevisions(t *testing.T) {
	fixture, sourceOID, targetOID := newTextFixture(t)
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Reviewed", SourceBranch: "feature", TargetBranch: "main",
	})
	noErr(t, err)
	number := created.Number
	noted, err := fixture.service.SubmitReview(fixture.ctx, fixture.repositoryID, number, ReviewSubmitInput{
		SourceOID: sourceOID, TargetOID: targetOID, Decision: state.ReviewChangesRequested, ReviewerLabel: "tool: reviewer",
		Note: "Rename `x`.\r\n", Actor: access,
	})
	noErr(t, err)
	if len(noted.ReviewNotes) != 1 || noted.ReviewNotesTruncated {
		t.Fatalf("notes=%+v", noted.ReviewNotes)
	}
	note := noted.ReviewNotes[0]
	if note.Note != "Rename `x`.\n" || !note.Current || note.SourceOID != sourceOID || note.TargetOID != targetOID ||
		note.Decision != state.ReviewChangesRequested || note.ReviewerLabel != "tool: reviewer" || note.Actor == nil || *note.Actor != access {
		t.Fatalf("note=%+v", note)
	}
	if _, err := fixture.service.SubmitReview(fixture.ctx, fixture.repositoryID, number, ReviewSubmitInput{
		SourceOID: sourceOID, TargetOID: targetOID, Decision: state.ReviewApproved, ReviewerLabel: "tool", Note: "\x00",
	}); problemCode(err) != "invalid_note" {
		t.Fatalf("NUL note err=%v", err)
	}

	newSource := fixture.commitFile("file.txt", "moved\n", "moved")
	fixture.push("HEAD:refs/heads/feature")
	moved, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, number)
	noErr(t, err)
	if moved.Review.Status != "decision_required" || len(moved.ReviewNotes) != 1 || moved.ReviewNotes[0].Current || moved.ReviewNotes[0].SourceOID != sourceOID {
		t.Fatalf("after the branch moved review=%+v notes=%+v", moved.Review, moved.ReviewNotes)
	}

	for index := 0; index < MaximumReviewNotes; index++ {
		_, err := fixture.service.SubmitReview(fixture.ctx, fixture.repositoryID, number, ReviewSubmitInput{
			SourceOID: newSource, TargetOID: targetOID, Decision: state.ReviewApproved, ReviewerLabel: "tool", Note: "later",
		})
		noErr(t, err)
	}
	// A review without a note is not a note.
	_, err = fixture.service.SubmitReview(fixture.ctx, fixture.repositoryID, number, ReviewSubmitInput{
		SourceOID: newSource, TargetOID: targetOID, Decision: state.ReviewApproved, ReviewerLabel: "tool",
	})
	noErr(t, err)
	shown, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, number)
	noErr(t, err)
	if len(shown.ReviewNotes) != MaximumReviewNotes || !shown.ReviewNotesTruncated || !shown.ReviewNotes[0].Current || shown.ReviewNotes[0].Actor != nil {
		t.Fatalf("newest notes=%+v truncated=%v", shown.ReviewNotes, shown.ReviewNotesTruncated)
	}
}

// A merge that OwnGit completes on its own after an interruption does not
// know who asked for it, so it records nobody rather than a guess.
func TestMergeCompletedByRecoveryRecordsNobody(t *testing.T) {
	fixture, sourceOID, targetOID := newTextFixture(t)
	created, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Recovered", SourceBranch: "feature", TargetBranch: "main", Actor: access})
	noErr(t, err)
	fixture.service.CompleteMerge = func(ctx context.Context, intent state.PullRequestMergeIntent, at time.Time) error {
		return errors.New("injected state failure")
	}
	if _, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID, Actor: access}); problemCode(err) != "merge_reconciliation_pending" {
		t.Fatalf("merge err=%v", err)
	}
	fixture.service.CompleteMerge = nil
	noErr(t, fixture.service.ReconcileAll(fixture.ctx))
	shown, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, created.Number)
	noErr(t, err)
	if shown.State != state.PullRequestMerged || shown.MergedBy != nil || shown.CreatedBy == nil {
		t.Fatalf("recovered merge=%+v merged by %v", shown.State, shown.MergedBy)
	}
}
