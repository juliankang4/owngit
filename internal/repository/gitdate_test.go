package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseGitDate(t *testing.T) {
	const seconds = 1312735823
	for _, test := range []struct {
		raw    string
		offset int // seconds east of UTC
	}{
		{"1312735823 +0000", 0},
		{"1312735823 -0000", 0},
		{"1312735823 +0530", 5*3600 + 30*60},
		{"1312735823 -0930", -(9*3600 + 30*60)},
		{"1312735823 +2359", 23*3600 + 59*60},
		{"1312735823 -2359", -(23*3600 + 59*60)},
		// Git reads HHMM, so 75 minutes is an hour and a quarter.
		{"1312735823 +0075", 75 * 60},
		// Offsets of 24 hours or more, which Git accepts from old history
		// (rails/rails has +051800), are shown in UTC at the exact instant.
		{"1312735823 +51800", 0},
		{"1312735823 +2400", 0},
		{"1312735823 +2360", 0},
		{"1312735823 -99999999999999999999", 0},
	} {
		got, err := parseGitDate([]byte(test.raw))
		if err != nil {
			t.Fatalf("%q: %v", test.raw, err)
		}
		if got.Unix() != seconds {
			t.Errorf("%q: instant %d, want %d", test.raw, got.Unix(), seconds)
		}
		if _, offset := got.Zone(); offset != test.offset {
			t.Errorf("%q: offset %d, want %d", test.raw, offset, test.offset)
		}
		if _, err := json.Marshal(got); err != nil {
			t.Errorf("%q: JSON: %v", test.raw, err)
		}
	}
	for _, raw := range []string{
		"", "1312735823", "1312735823 ", "1312735823 0530", "1312735823 +053", "1312735823 +05a0",
		"-5 +0000", "abc +0000", "1312735823  +0000", "1312735823 +0000 x", "2011-08-07T12:00:00+05:30",
		"99999999999999999999 +0000",
	} {
		if got, err := parseGitDate([]byte(raw)); !errors.Is(err, errMalformedGitDate) {
			t.Errorf("%q: %v, %v, want a malformed date", raw, got, err)
		}
	}
}

// A commit whose offset Git accepted but no zone can hold, as in rails/rails
// commit 4cf94979, shows in the history, on its commit page, in activity and
// as the default branch tip, at its exact instant in UTC. A normal offset
// beside it keeps its zone.
func TestCommitWithAnOversizedOffsetIsShown(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	ctx := context.Background()
	tree := gitOutput(t, "", "--git-dir", remote, "hash-object", "-t", "tree", "-w", "--stdin")
	literal := func(object string) string {
		command := exec.Command("git", "--git-dir", remote, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
		command.Stdin = strings.NewReader(object)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("hash-object: %v\n%s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	normal := literal("tree " + tree + "\nauthor A <a@example.invalid> 1312700000 +0900\ncommitter A <a@example.invalid> 1312700000 +0900\n\nnormal\n")
	oversized := literal("tree " + tree + "\nparent " + normal +
		"\nauthor V <v@example.invalid> 1312735823 +051800\ncommitter V <v@example.invalid> 1312735823 +051800\n\noversized\n")
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", oversized)
	wroteRefs(manager, "sample")

	utc := time.Unix(1312735823, 0).UTC()
	seoul := time.Unix(1312700000, 0).In(time.FixedZone("", 9*3600))
	sameMoment := func(got, want time.Time) bool {
		_, gotOffset := got.Zone()
		_, wantOffset := want.Zone()
		return got.Equal(want) && gotOffset == wantOffset
	}

	_, history, err := manager.Commits(ctx, "sample", "refs/heads/main", 10)
	if err != nil || len(history) != 2 || history[0].OID != oversized || !sameMoment(history[0].AuthoredAt, utc) ||
		!sameMoment(history[0].CommittedAt, utc) || !sameMoment(history[1].AuthoredAt, seoul) {
		t.Fatalf("history = %+v, %v", history, err)
	}
	commit, _, err := manager.CommitFiles(ctx, "sample", oversized)
	if err != nil || !sameMoment(commit.AuthoredAt, utc) || !sameMoment(commit.CommittedAt, utc) {
		t.Fatalf("commit page = %+v, %v", commit, err)
	}
	activity, err := manager.Activity(ctx, "sample", 10)
	if err != nil || activity.Commits != 2 || len(activity.Records) != 2 || !sameMoment(activity.Records[0].AuthoredAt, utc) {
		t.Fatalf("activity = %+v, %v", activity, err)
	}
	snapshot, err := manager.RefSnapshot(ctx, "sample")
	if err != nil || !snapshot.HeadFound || snapshot.Head.OID != oversized || !sameMoment(snapshot.Head.AuthoredAt, utc) {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
}
