package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenPullRequestPagesMakeFairProgressPastFirstPageAndBusyHistory(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}))
	for index := 0; index < 130; index++ {
		if _, err := store.CreatePullRequest(ctx, "project", fmt.Sprintf("Request %03d", index), fmt.Sprintf("feature-%03d", index), "main", strings.Repeat("a", 40), strings.Repeat("b", 40), ReviewSkipped, now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for index := 1; index <= 100; index++ {
		if err := store.RecordPullRequestRevision(ctx, PullRequestRevision{
			RepositoryID: "project", PullRequestNumber: 1,
			SourceOID: fmt.Sprintf("%040x", index), TargetOID: strings.Repeat("c", 40),
			RecordedAt: now.Add(time.Duration(200+index) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[int64]bool)
	var after int64
	for pass := 0; pass < 3; pass++ {
		records, more, err := store.OpenPullRequestsAfter(ctx, "project", after, 64)
		if err != nil || !more || len(records) != 64 {
			t.Fatalf("pass %d records=%d more=%v err=%v", pass, len(records), more, err)
		}
		for _, record := range records {
			seen[record.Number] = true
		}
		after = records[len(records)-1].Number
	}
	if len(seen) != 130 {
		t.Fatalf("fair pages reached %d/130 open pull requests", len(seen))
	}
	if revisions, err := store.PullRequestRevisions(ctx, "project"); err != nil || len(revisions) != 230 {
		t.Fatalf("historical revisions=%d err=%v", len(revisions), err)
	}
}

func TestMergeReceiptBelongsOnlyToItsPublishedRevision(t *testing.T) {
	record := PullRequest{RepositoryID: "project", Number: 1, Status: PullRequestOpen}
	ref := "refs/owngit/pull-requests/1/merge-receipt"
	oldSource, source, target := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	published := PullRequestMergeIntent{RepositoryID: record.RepositoryID, PullRequestNumber: record.Number,
		SourceOID: source, TargetOID: target, ResultOID: source, ReceiptRef: ref, Status: MergeIntentReady}
	for _, status := range []string{MergeIntentPreparing, MergeIntentPlanned, MergeIntentReady} {
		t.Run(status, func(t *testing.T) {
			abandoned := published
			abandoned.SourceOID, abandoned.ResultOID, abandoned.Status = oldSource, oldSource, status
			if status == MergeIntentPreparing {
				abandoned.ResultOID = ""
			}
			intents := []PullRequestMergeIntent{abandoned, published}
			owner, err := PullRequestMergeReceiptOwner(record, intents, source)
			noErr(t, err)
			if owner.SourceOID != source || owner.TargetOID != target {
				t.Fatalf("receipt owner: %+v", owner)
			}
			if _, err := PullRequestMergeReceiptOwner(record, intents, strings.Repeat("d", 40)); err == nil {
				t.Fatal("unrelated receipt was accepted")
			}
			merged := record
			merged.Status, merged.MergeSourceOID, merged.MergeTargetOID = PullRequestMerged, source, target
			merged.MergeOID, merged.MergeReceipt = source, ref
			intents[1].Status = MergeIntentComplete
			_, err = PullRequestMergeReceiptOwner(merged, intents, source)
			noErr(t, err)
			if _, err := PullRequestMergeReceiptOwner(merged, intents, oldSource); err == nil {
				t.Fatal("abandoned receipt replaced the merged revision")
			}
		})
	}
	if _, err := PullRequestMergeReceiptOwner(record, []PullRequestMergeIntent{published, published}, source); err == nil {
		t.Fatal("ambiguous receipt was accepted")
	}
	for _, status := range []string{MergeIntentPreparing, MergeIntentPlanned, MergeIntentComplete} {
		intent := published
		intent.Status = status
		if _, err := PullRequestMergeReceiptOwner(record, []PullRequestMergeIntent{intent}, source); err == nil {
			t.Fatalf("open PR accepted receipt for %s plan", status)
		}
	}
	foreign := published
	foreign.RepositoryID = "other"
	if _, err := PullRequestMergeReceiptOwner(record, []PullRequestMergeIntent{foreign}, source); err == nil {
		t.Fatal("another repository's receipt was accepted")
	}
}

func TestCompletedMergeCannotBeReassignedToAnotherRevision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}))
	source, target := strings.Repeat("a", 40), strings.Repeat("b", 40)
	request, err := store.CreatePullRequest(ctx, "project", "Already contained", "feature", "main", source, target, ReviewSkipped, now)
	noErr(t, err)
	complete := func(source string) (PullRequestMergeIntent, error) {
		intent, err := store.BeginPullRequestMerge(ctx, PullRequestMergeIntent{RepositoryID: "project", PullRequestNumber: request.Number,
			SourceOID: source, TargetOID: target, ReceiptRef: "refs/owngit/pull-requests/1/merge-receipt", CreatedAt: now})
		noErr(t, err)
		intent.Mode, intent.ResultOID, intent.Status = "up_to_date", target, MergeIntentReady
		intent, err = store.UpdatePullRequestMergeIntent(ctx, intent)
		noErr(t, err)
		return intent, store.CompletePullRequestMerge(ctx, intent, now, Actor{})
	}
	first, err := complete(source)
	noErr(t, err)
	_, err = complete(strings.Repeat("c", 40))
	if err == nil {
		t.Fatal("another revision with the same result claimed the completed merge")
	}
	noErr(t, store.DeleteUnpublishedPullRequestMerge(ctx, first))
	stored, ok, err := store.PullRequestMergeIntent(ctx, "project", request.Number, source, target)
	noErr(t, err)
	if !ok || stored.Status != MergeIntentComplete {
		t.Fatal("discarding a plan removed published evidence")
	}
}

func TestPullRequestNumbersAreDurableAndPerRepository(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	store, err := Open(ctx, directory)
	noErr(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, repository := range []Repository{
		{ID: "alpha", Name: "Alpha", CreatedAt: now},
		{ID: "beta", Name: "Beta", CreatedAt: now},
	} {
		if err := store.AddRepository(ctx, repository); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	sourceOID := strings.Repeat("a", 40)
	targetOID := strings.Repeat("b", 40)
	first, err := store.CreatePullRequest(ctx, "alpha", "First", "feature-one", "main", sourceOID, targetOID, ReviewPending, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	second, err := store.CreatePullRequest(ctx, "alpha", "Second", "feature-two", "main", sourceOID, targetOID, ReviewSkipped, now.Add(time.Second))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	other, err := store.CreatePullRequest(ctx, "beta", "Other", "feature", "main", sourceOID, targetOID, ReviewPending, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if first.Number != 1 || second.Number != 2 || other.Number != 1 {
		store.Close()
		t.Fatalf("pull request numbers: alpha=%d,%d beta=%d", first.Number, second.Number, other.Number)
	}
	noErr(t, store.Close())

	reopened, err := Open(ctx, directory)
	noErr(t, err)
	defer reopened.Close()
	stored, exists, err := reopened.PullRequest(ctx, "alpha", second.Number)
	if err != nil || !exists || stored.Title != second.Title || stored.SourceBranch != second.SourceBranch {
		t.Fatalf("durable pull request=%+v exists=%v err=%v", stored, exists, err)
	}
	review, exists, err := reopened.PullRequestReviewForRevision(ctx, "alpha", second.Number, sourceOID, targetOID)
	if err != nil || !exists || review.Status != ReviewSkipped || review.Provenance != ReviewProvenanceSkip {
		t.Fatalf("durable review=%+v exists=%v err=%v", review, exists, err)
	}
}

// An edit names the edit revision it read. A second edit from the same
// revision is refused and changes nothing, so a later edit is never
// overwritten. An edit is never recorded as earlier than the creation.
func TestPullRequestEditIsRefusedOnceAnotherEditMovedIt(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}))
	created, err := store.CreatePullRequest(ctx, "project", "Title", "feature", "main", strings.Repeat("a", 40), strings.Repeat("b", 40), ReviewNotRequested, now)
	noErr(t, err)
	access := Actor{Kind: ActorAccess}

	edited, err := store.EditPullRequest(ctx, PullRequestEdit{RepositoryID: "project", Number: created.Number, BasedOn: 0, Title: "First", Body: "one", EditedBy: access}, now.Add(-time.Hour))
	noErr(t, err)
	if edited.EditRevision != 1 || edited.Title != "First" || edited.Body != "one" || edited.EditedBy != access || edited.EditedAt == nil || edited.EditedAt.Before(edited.CreatedAt) {
		t.Fatalf("first edit=%+v", edited)
	}
	if _, err := store.EditPullRequest(ctx, PullRequestEdit{RepositoryID: "project", Number: created.Number, BasedOn: 0, Title: "Stale", Body: "two", EditedBy: access}, now.Add(time.Minute)); !errors.Is(err, ErrPullRequestEdited) {
		t.Fatalf("stale edit err=%v", err)
	}
	current, _, err := store.PullRequest(ctx, "project", created.Number)
	noErr(t, err)
	if current.EditRevision != 1 || current.Title != "First" || current.Body != "one" {
		t.Fatalf("a stale edit changed the record: %+v", current)
	}
	// A restore accepts the record as it was written.
	noErr(t, validatePullRequestRecord(current))
}
