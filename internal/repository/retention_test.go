package repository

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/state"
)

func TestActivityRevisionInputAvoidsOptionsInStdin(t *testing.T) {
	got := activityRevisionInput([]string{"refs/owngit/retained/heads/old"}, []string{"refs/heads/main", "refs/heads/release"})
	want := "refs/owngit/retained/heads/old\n^refs/heads/main\n^refs/heads/release\n"
	if got != want {
		t.Fatalf("activity revision input = %q, want %q", got, want)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "--") {
			t.Fatalf("activity input contains an option unsupported by older Git: %q", line)
		}
	}
}

type countingRetainedRunner struct {
	delegate *gitexec.Runner
	commands [][]string
	inputs   []string
}

func (runner *countingRetainedRunner) Run(ctx context.Context, directory string, stdin io.Reader, arguments ...string) (gitexec.Result, error) {
	runner.commands = append(runner.commands, append([]string(nil), arguments...))
	return runner.delegate.Run(ctx, directory, stdin, arguments...)
}

func (runner *countingRetainedRunner) RunWithOutputLimit(ctx context.Context, directory string, stdin io.Reader, limit int64, arguments ...string) (gitexec.Result, error) {
	runner.commands = append(runner.commands, append([]string(nil), arguments...))
	if stdin != nil {
		content, err := io.ReadAll(stdin)
		if err != nil {
			return gitexec.Result{}, err
		}
		runner.inputs = append(runner.inputs, string(content))
		stdin = bytes.NewReader(content)
	}
	return runner.delegate.RunWithOutputLimit(ctx, directory, stdin, limit, arguments...)
}

func TestRetainedRefsBatchPeelsAndLoadsMetadataWithoutDiffProcesses(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	var oids []string
	for index := 0; index < 12; index++ {
		commitFile(t, work, strings.Repeat("x", index+1), "retained metadata", "2024-01-01T00:00:00Z")
		oids = append(oids, gitOutput(t, work, "rev-parse", "HEAD"))
	}
	runGit(t, work, "push", "origin", "HEAD:refs/heads/transfer")
	runGit(t, work, "checkout", "--orphan", "replacement")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "replacement", "current main", "2024-02-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	for _, oid := range oids {
		runGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/retained/heads/"+oid, oid)
		runGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/provenance/heads/main/"+oid, oid)
	}

	runner := &countingRetainedRunner{delegate: manager.Git}
	retained, err := retainedRefs(context.Background(), runner, remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != len(oids) {
		t.Fatalf("retained refs=%d, want %d", len(retained), len(oids))
	}
	if len(runner.commands) != 6 {
		t.Fatalf("retained overview used %d Git processes for %d branch refs, want 6", len(runner.commands), len(oids))
	}
	metadataProcesses := 0
	ancestryProcesses := 0
	for _, arguments := range runner.commands {
		command := strings.Join(arguments, " ")
		if strings.Contains(command, " cat-file ") || strings.Contains(command, " rev-parse ") || strings.Contains(command, " merge-base ") || strings.Contains(command, " show ") || strings.Contains(command, " diff") {
			t.Fatalf("retained overview invoked object-by-object or diff command: %s", command)
		}
		if strings.Contains(command, "--no-merged=") {
			ancestryProcesses++
		}
		if strings.Contains(command, " log ") {
			metadataProcesses++
			if !strings.Contains(command, "--no-walk") || !strings.Contains(command, "--stdin") {
				t.Fatalf("metadata command is not bounded to supplied OIDs: %s", command)
			}
		}
	}
	if metadataProcesses != 1 || ancestryProcesses != 1 {
		t.Fatalf("metadata processes=%d ancestry processes=%d, want one batched process each", metadataProcesses, ancestryProcesses)
	}
	if len(runner.inputs) != 1 || len(strings.Fields(runner.inputs[0])) != len(oids) {
		t.Fatalf("metadata stdin does not contain one plain OID per retained commit: %q", runner.inputs)
	}
	for _, revision := range strings.Fields(runner.inputs[0]) {
		if !isOID(revision) {
			t.Fatalf("metadata stdin contains a non-OID revision: %q", revision)
		}
	}
	for _, ref := range retained {
		if ref.Commit.OID != ref.CommitOID || ref.Commit.Subject != "retained metadata" || ref.Commit.AuthoredAt.IsZero() {
			t.Fatalf("retained metadata is incomplete: %+v", ref)
		}
	}
}

func TestRetainedRefsBatchPeelsNestedAnnotatedTag(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "nested", "nested target", "2024-01-01T00:00:00Z")
	commitOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, work, "tag", "-a", "inner", "-m", "inner")
	runGit(t, work, "tag", "-a", "outer", "-m", "outer", "inner")
	oldOuter := gitOutput(t, work, "rev-parse", "refs/tags/outer")
	runGit(t, work, "push", "origin", "refs/tags/outer")
	peeled, err := batchPeelRetainedTags(context.Background(), manager.Git, remote, []string{oldOuter})
	if err != nil {
		t.Fatal(err)
	}
	if peeled[oldOuter].oid != commitOID || peeled[oldOuter].objectType != "commit" {
		t.Fatalf("batch peel=%+v, want terminal commit %s", peeled[oldOuter], commitOID)
	}
	runGit(t, work, "tag", "-f", "-a", "outer", "-m", "replacement", "HEAD")
	runGit(t, work, "push", "--force", "origin", "refs/tags/outer")

	retained, err := manager.RetainedRefs(context.Background(), "sample")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range retained {
		if ref.OID == oldOuter {
			if ref.CommitOID != commitOID || ref.Commit.OID != commitOID || ref.Commit.Subject != "nested target" {
				t.Fatalf("nested tag metadata=%+v", ref)
			}
			return
		}
	}
	t.Fatalf("nested retained tag %s was not returned: %+v", oldOuter, retained)
}

func TestRepositoryDisablesAutomaticMaintenanceOnCreateAndRestart(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	wantSettings := map[string]string{
		"receive.autogc": "false", "gc.auto": "0", "gc.autoDetach": "false",
		"maintenance.auto": "false", "maintenance.autoDetach": "false",
	}
	for key, want := range wantSettings {
		if got := gitOutput(t, "", "--git-dir", remote, "config", "--local", "--get", key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	for key, value := range map[string]string{
		"receive.autogc": "true", "gc.auto": "6700", "gc.autoDetach": "true",
		"maintenance.auto": "true", "maintenance.autoDetach": "true",
	} {
		runGit(t, "", "--git-dir", remote, "config", "--local", key, value)
	}
	if err := manager.PrepareExisting(context.Background()); err != nil {
		t.Fatal(err)
	}
	for key, want := range wantSettings {
		if got := gitOutput(t, "", "--git-dir", remote, "config", "--local", "--get", key); got != want {
			t.Fatalf("repaired %s = %q, want %q", key, got, want)
		}
	}
}

func TestRetentionSurvivesRewritesDeletionAndGC(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-02T23:30:00-08:00")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	first := gitOutput(t, work, "rev-parse", "HEAD")
	commitFile(t, work, "two", "two", "2024-02-03T01:00:00+09:00")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	second := gitOutput(t, work, "rev-parse", "HEAD")
	assertMissingRef(t, remote, "refs/owngit/retained/heads/"+first)

	runGit(t, work, "checkout", "--orphan", "rewrite")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "rewrite", "replacement", "2024-03-04T12:00:00+00:00")
	replacement := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "--force", "origin", "HEAD:refs/heads/main")
	assertRef(t, remote, "refs/owngit/retained/heads/"+second, second)

	runGit(t, work, "push", "origin", first+":refs/heads/to-delete")
	runGit(t, work, "push", "origin", ":refs/heads/to-delete")
	assertRef(t, remote, "refs/owngit/retained/heads/"+first, first)

	runGit(t, work, "tag", "light", replacement)
	runGit(t, work, "push", "origin", "refs/tags/light")
	runGit(t, work, "tag", "-f", "light", first)
	runGit(t, work, "push", "--force", "origin", "refs/tags/light")
	assertRef(t, remote, "refs/owngit/retained/tags/"+replacement, replacement)

	runGit(t, work, "tag", "-a", "annotated", "-m", "first annotation", replacement)
	oldAnnotated := gitOutput(t, work, "rev-parse", "refs/tags/annotated")
	runGit(t, work, "push", "origin", "refs/tags/annotated")
	runGit(t, work, "tag", "-f", "-a", "annotated", "-m", "second annotation", first)
	newAnnotated := gitOutput(t, work, "rev-parse", "refs/tags/annotated")
	runGit(t, work, "push", "--force", "origin", "refs/tags/annotated")
	assertRef(t, remote, "refs/owngit/retained/tags/"+oldAnnotated, oldAnnotated)
	if oldAnnotated == newAnnotated {
		t.Fatal("annotated tag object did not change")
	}
	runGit(t, work, "push", "origin", ":refs/tags/annotated")
	assertRef(t, remote, "refs/owngit/retained/tags/"+newAnnotated, newAnnotated)

	blobOnePath := filepath.Join(work, "blob-one.bin")
	blobTwoPath := filepath.Join(work, "blob-two.bin")
	if err := os.WriteFile(blobOnePath, []byte("first standalone blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobTwoPath, []byte("second standalone blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobOne := gitOutput(t, work, "hash-object", "-w", blobOnePath)
	blobTwo := gitOutput(t, work, "hash-object", "-w", blobTwoPath)
	runGit(t, work, "tag", "-a", "blob-target", "-m", "standalone blob", blobOne)
	oldBlobTag := gitOutput(t, work, "rev-parse", "refs/tags/blob-target")
	runGit(t, work, "push", "origin", "refs/tags/blob-target")
	runGit(t, work, "tag", "-f", "-a", "blob-target", "-m", "replacement blob", blobTwo)
	newBlobTag := gitOutput(t, work, "rev-parse", "refs/tags/blob-target")
	runGit(t, work, "push", "--force", "origin", "refs/tags/blob-target")
	assertRef(t, remote, "refs/owngit/retained/tags/"+oldBlobTag, oldBlobTag)
	runGit(t, work, "push", "origin", ":refs/tags/blob-target")
	assertRef(t, remote, "refs/owngit/retained/tags/"+newBlobTag, newBlobTag)

	if output, err := gitCombined(work, "push", "origin", "HEAD:refs/owngit/attack"); err == nil {
		t.Fatalf("reserved ref push succeeded: %s", output)
	}
	advertised := gitOutput(t, work, "ls-remote", "origin")
	if strings.Contains(advertised, "refs/owngit/") {
		t.Fatalf("reserved refs were advertised: %s", advertised)
	}

	if output, err := gitCombined(work, "push", "--atomic", "origin", "HEAD:refs/heads/atomic-ok", "HEAD:refs/meta/rejected"); err == nil {
		t.Fatalf("atomic push unexpectedly succeeded: %s", output)
	}
	assertMissingRef(t, remote, "refs/heads/atomic-ok")
	if output, err := gitCombined(work, "push", "origin", "HEAD:refs/heads/partial-ok", "HEAD:refs/meta/rejected"); err == nil {
		t.Fatalf("partially rejected push returned success: %s", output)
	}
	assertRef(t, remote, "refs/heads/partial-ok", replacement)

	runGit(t, "", "--git-dir", remote, "reflog", "expire", "--expire=now", "--all")
	runGit(t, "", "--git-dir", remote, "gc", "--prune=now")
	for _, oid := range []string{first, second, replacement, oldAnnotated, newAnnotated, oldBlobTag, newBlobTag, blobOne, blobTwo} {
		runGit(t, "", "--git-dir", remote, "cat-file", "-e", oid+"^{object}")
	}

	restartedRunner, err := gitexec.New("", filepath.Join(filepath.Dir(remote), "..", "runtime-after-restart"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Manager{Store: manager.Store, Git: restartedRunner, Locks: gitexec.NewLocks(), Root: filepath.Dir(remote)}
	retainedAfterRestart, err := restarted.RetainedRefs(context.Background(), "sample")
	if err != nil || len(retainedAfterRestart) == 0 {
		t.Fatalf("Git-authoritative retained refs were not reconstructed after restart: refs=%+v err=%v", retainedAfterRestart, err)
	}
	retainedCommits := make(map[string]string)
	retainedSeen := make(map[string]bool)
	for _, ref := range retainedAfterRestart {
		retainedSeen[ref.OID] = true
		retainedCommits[ref.OID] = ref.CommitOID
	}
	if retainedCommits[oldAnnotated] != replacement || retainedCommits[newAnnotated] != first {
		t.Fatalf("annotated retained tags were not peeled to commits: %+v", retainedAfterRestart)
	}
	if !retainedSeen[oldBlobTag] || !retainedSeen[newBlobTag] || retainedCommits[oldBlobTag] != "" || retainedCommits[newBlobTag] != "" {
		t.Fatalf("retained blob tags were exposed as recoverable commits: %+v", retainedAfterRestart)
	}
	activity, err := restarted.Activity(context.Background(), "sample", 100)
	if err != nil {
		t.Fatal(err)
	}
	if activity.Commits < 3 || activity.Incomplete {
		t.Fatalf("unexpected retained activity: %+v", activity)
	}
	counts := make(map[string]int)
	for _, day := range activity.Days {
		counts[day.Day] = day.Count
	}
	for _, day := range []string{"2024-01-02", "2024-02-03", "2024-03-04"} {
		if counts[day] == 0 {
			t.Errorf("author's recorded day %s was not counted: %+v", day, activity.Days)
		}
	}
}

func TestRetentionFailureRejectsPublicUpdate(t *testing.T) {
	_, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	old := gitOutput(t, work, "rev-parse", "HEAD")
	commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
	second := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "checkout", "--orphan", "blocked-rewrite")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "three", "three", "2024-01-03T00:00:00Z")

	blockedNamespace := filepath.Join(remote, "refs", "owngit")
	if err := os.WriteFile(blockedNamespace, []byte("synthetic retention failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := gitCombined(work, "push", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("push succeeded despite retention failure: %s", output)
	}
	assertRef(t, remote, "refs/heads/main", old)
	if second == old {
		t.Fatal("test did not create a distinct fast-forward commit")
	}
}

func TestActivityObservationStaysCurrentHistoryFirstAndHonest(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	commitFile(t, work, "two", "two", "2024-01-05T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, work, "reset", "--hard", "HEAD~1")
	runGit(t, work, "push", "--force", "origin", "HEAD:refs/heads/main")

	// The retained commit is newer, but a capped observation must stay
	// current-history-first and report itself incomplete.
	capped, err := manager.Activity(context.Background(), "sample", 1)
	if err != nil || !capped.Incomplete || len(capped.Days) != 1 || capped.Days[0].Day != "2024-01-01" {
		t.Fatalf("capped observation: days=%+v incomplete=%v err=%v", capped.Days, capped.Incomplete, err)
	}
	// The full budget observes current and retained history.
	complete, err := manager.Activity(context.Background(), "sample", 2)
	if err != nil || complete.Incomplete || len(complete.Days) != 2 {
		t.Fatalf("complete observation: days=%+v incomplete=%v err=%v", complete.Days, complete.Incomplete, err)
	}
	if _, err := manager.Activity(context.Background(), "missing", 10); err == nil {
		t.Fatal("missing repository did not return an error")
	}
}

func TestActivityObservationReportsExactBoundaryAndRetainedCompleteness(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	exact, err := manager.Activity(context.Background(), "sample", 2)
	if err != nil || exact.Incomplete || len(exact.Records) != 2 {
		t.Fatalf("exact boundary: records=%d incomplete=%v err=%v", len(exact.Records), exact.Incomplete, err)
	}
	capped, err := manager.Activity(context.Background(), "sample", 1)
	if err != nil || !capped.Incomplete || len(capped.Records) != 1 {
		t.Fatalf("capped boundary: records=%d incomplete=%v err=%v", len(capped.Records), capped.Incomplete, err)
	}

	runGit(t, work, "checkout", "--orphan", "replacement")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "replacement", "replacement", "2024-01-03T00:00:00Z")
	runGit(t, work, "push", "--force", "origin", "HEAD:refs/heads/main")

	// The current walk fills the budget exactly, so retained history was not
	// observed and the observation must not claim completeness.
	currentOnly, err := manager.Activity(context.Background(), "sample", 1)
	if err != nil || !currentOnly.Incomplete || len(currentOnly.Records) != 1 {
		t.Fatalf("current-only budget: records=%d incomplete=%v err=%v", len(currentOnly.Records), currentOnly.Incomplete, err)
	}
	// One more slot observes part of retained history, which is still short.
	partial, err := manager.Activity(context.Background(), "sample", 2)
	if err != nil || !partial.Incomplete || len(partial.Records) != 2 {
		t.Fatalf("partial retained budget: records=%d incomplete=%v err=%v", len(partial.Records), partial.Incomplete, err)
	}
	// The full budget observes all current and retained history.
	complete, err := manager.Activity(context.Background(), "sample", 3)
	if err != nil || complete.Incomplete || len(complete.Records) != 3 {
		t.Fatalf("complete budget: records=%d incomplete=%v err=%v", len(complete.Records), complete.Incomplete, err)
	}
	if _, err := manager.Activity(context.Background(), "missing", 10); err == nil {
		t.Fatal("missing repository did not return an error")
	}
}

func TestActivityRecordsSeparateCurrentAndRetainedProvenance(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
	discarded := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, work, "tag", "still-tagged", discarded)
	runGit(t, work, "push", "origin", "refs/tags/still-tagged")
	runGit(t, work, "checkout", "--orphan", "replacement")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "replacement", "replacement", "2024-01-03T00:00:00Z")
	current := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "--force", "origin", "HEAD:refs/heads/main")

	activity, err := manager.Activity(context.Background(), "sample", 100)
	if err != nil || activity.Incomplete {
		t.Fatalf("Activity incomplete=%v err=%v", activity.Incomplete, err)
	}
	records := activity.Records
	foundCurrent, foundRetained := false, false
	for _, record := range records {
		switch record.OID {
		case current:
			foundCurrent = !record.Retained && record.Source == "refs/heads/main"
		case discarded:
			foundRetained = record.Retained && record.Source == "refs/heads/main"
		}
	}
	if !foundCurrent || !foundRetained {
		t.Fatalf("activity provenance was mislabeled: %+v", records)
	}
}

func TestFailedAtomicDestructivePushPreservesObjectWithoutClaimingRewrite(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	old := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "checkout", "--orphan", "replacement")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "replacement", "replacement", "2024-01-02T00:00:00Z")

	if output, err := gitCombined(work, "push", "--atomic", "--force", "origin", "HEAD:refs/heads/main", "HEAD:refs/meta/rejected"); err == nil {
		t.Fatalf("invalid atomic push succeeded: %s", output)
	}
	assertRef(t, remote, "refs/heads/main", old)
	assertRef(t, remote, "refs/owngit/retained/heads/"+old, old)
	retained, err := manager.RetainedRefs(context.Background(), "sample")
	if err != nil || len(retained) != 0 {
		t.Fatalf("failed atomic update was classified as successful retention: refs=%+v err=%v", retained, err)
	}
	activity, err := manager.Activity(context.Background(), "sample", 100)
	if err != nil || len(activity.Records) != 1 || activity.Records[0].OID != old || activity.Records[0].Retained {
		t.Fatalf("failed atomic update mislabeled current activity: records=%+v err=%v", activity.Records, err)
	}

	runGit(t, work, "push", "--force", "origin", "HEAD:refs/heads/main")
	assertRef(t, remote, "refs/owngit/retained/heads/"+old, old)
	retained, err = manager.RetainedRefs(context.Background(), "sample")
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 1 || retained[0].OID != old || retained[0].Kind != "branch" {
		t.Fatalf("retained refs = %+v, want one destructive branch tip", retained)
	}
}

func TestInterruptedRetentionIsClassifiedFromPublicRefAfterRestart(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	old := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "checkout", "--orphan", "replacement")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "replacement", "replacement", "2024-01-02T00:00:00Z")
	replacement := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/staging")

	// Simulate interruption after durable old-object retention but before the
	// public ref transaction. The hidden object remains, but is not called a
	// successful rewrite while main still names it.
	if output, err := executeUpdateHook(manager.Git, remote, "refs/heads/main", old, replacement); err != nil {
		t.Fatalf("prepare retention interruption fixture: %v\n%s", err, output)
	}
	legacyPending := "refs/owngit/pending/retained/heads/main/" + old
	runGit(t, "", "--git-dir", remote, "update-ref", legacyPending, old)
	restartedRunner, err := gitexec.New("", filepath.Join(filepath.Dir(remote), "..", "runtime-interruption-restart"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Manager{Store: manager.Store, Git: restartedRunner, Locks: gitexec.NewLocks(), Root: filepath.Dir(remote)}
	if err := restarted.PrepareExisting(context.Background()); err != nil {
		t.Fatal(err)
	}
	retained, err := restarted.RetainedRefs(context.Background(), "sample")
	if err != nil || len(retained) != 0 {
		t.Fatalf("before-public interruption was called historical: refs=%+v err=%v", retained, err)
	}
	assertRef(t, remote, legacyPending, old)

	// Simulate the complementary crash point: public main changed, but no
	// post-update cleanup or journal reconciliation ran. Read-time
	// classification now truthfully reports the old main as historical.
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", replacement, old)
	runGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/heads/staging", replacement)
	if err := restarted.PrepareExisting(context.Background()); err != nil {
		t.Fatal(err)
	}
	retained, err = restarted.RetainedRefs(context.Background(), "sample")
	if err != nil || len(retained) != 1 || retained[0].OID != old || retained[0].Kind != "branch" {
		t.Fatalf("after-public interruption classification = %+v err=%v", retained, err)
	}
	activity, err := restarted.Activity(context.Background(), "sample", 100)
	if err != nil {
		t.Fatal(err)
	}
	foundHistorical := false
	for _, record := range activity.Records {
		if record.OID == old && record.Retained && record.Source == "refs/heads/main" {
			foundHistorical = true
		}
	}
	if !foundHistorical {
		t.Fatalf("historical activity provenance was lost after restart: %+v", activity.Records)
	}
	runGit(t, "", "--git-dir", remote, "reflog", "expire", "--expire=now", "--all")
	runGit(t, "", "--git-dir", remote, "gc", "--prune=now")
	runGit(t, "", "--git-dir", remote, "cat-file", "-e", old+"^{object}")
	assertRef(t, remote, legacyPending, old)
}

func TestPartialDestructivePushRetainsOnlySuccessfulUpdate(t *testing.T) {
	_, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	old := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "checkout", "--orphan", "replacement")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "replacement", "replacement", "2024-01-02T00:00:00Z")
	replacement := gitOutput(t, work, "rev-parse", "HEAD")
	if output, err := gitCombined(work, "push", "--force", "origin", "HEAD:refs/heads/main", "HEAD:refs/meta/rejected"); err == nil {
		t.Fatalf("partially rejected push returned success: %s", output)
	}
	assertRef(t, remote, "refs/heads/main", replacement)
	assertRef(t, remote, "refs/owngit/retained/heads/"+old, old)
}

func TestRetentionHookRejectsMismatchedOldOID(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	current := gitOutput(t, work, "rev-parse", "HEAD")
	wrong := strings.Repeat("1", len(current))
	if output, err := executeUpdateHook(manager.Git, remote, "refs/heads/main", wrong, current); err == nil {
		t.Fatalf("hook accepted mismatched old OID: %s", output)
	}
}

func executeUpdateHook(runner *gitexec.Runner, repositoryPath string, arguments ...string) ([]byte, error) {
	// A ! alias runs through Git's trusted shell, including the bundled shell on
	// Windows, while still executing the generated hook as the command itself.
	hookPath := filepath.ToSlash(filepath.Join(repositoryPath, "hooks", "update"))
	gitArguments := []string{
		"-c", `alias.owngit-test-update=!f() { "$@"; }; f`,
		"owngit-test-update", hookPath,
	}
	gitArguments = append(gitArguments, arguments...)
	command := exec.Command(runner.GitPath, gitArguments...)
	command.Dir = repositoryPath
	for _, variable := range runner.Environment("GIT_DIR=" + repositoryPath) {
		name, _, _ := strings.Cut(variable, "=")
		if !strings.EqualFold(name, "TMPDIR") {
			command.Env = append(command.Env, variable)
		}
	}
	return command.CombinedOutput()
}

func newTestRepository(t *testing.T) (*Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := state.Open(context.Background(), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	repositoriesRoot := filepath.Join(root, "repositories")
	if err := os.Mkdir(repositoriesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	if _, err := manager.Create(context.Background(), "sample", "test repository"); err != nil {
		t.Fatal(err)
	}
	remote, err := manager.Path("sample")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	runGit(t, "", "init", "--initial-branch=main", work)
	runGit(t, work, "config", "user.name", "Test Author")
	runGit(t, work, "config", "user.email", "test@example.invalid")
	runGit(t, work, "remote", "add", "origin", remote)
	return manager, remote, work
}

func commitFile(t *testing.T, directory, content, message, authored string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "file.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "add", "file.txt")
	command := exec.Command("git", "commit", "-m", message)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+authored, "GIT_COMMITTER_DATE="+authored)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, output)
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	if output, err := gitCombined(directory, arguments...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	output, err := gitCombined(directory, arguments...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(output)
}

func gitCombined(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	return string(output), err
}

func assertRef(t *testing.T, repositoryPath, ref, want string) {
	t.Helper()
	got := gitOutput(t, "", "--git-dir", repositoryPath, "rev-parse", "--verify", ref)
	if got != want {
		t.Fatalf("%s = %s, want %s", ref, got, want)
	}
}

func assertMissingRef(t *testing.T, repositoryPath, ref string) {
	t.Helper()
	if output, err := gitCombined("", "--git-dir", repositoryPath, "show-ref", "--verify", ref); err == nil {
		t.Fatalf("ref %s unexpectedly exists: %s", ref, output)
	}
}
