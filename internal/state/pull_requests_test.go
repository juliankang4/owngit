package state

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenPullRequestPagesMakeFairProgressPastFirstPageAndBusyHistory(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
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

func TestPullRequestNumbersAreDurableAndPerRepository(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
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
