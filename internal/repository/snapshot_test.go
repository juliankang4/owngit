package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/gitexec"
)

func TestRefSnapshotMatchesSummaryAndHeadCommit(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()
	empty, err := manager.RefSnapshot(ctx, "sample")
	noErr(t, err)
	emptySummary, err := manager.Summary(ctx, "sample")
	noErr(t, err)
	if !reflect.DeepEqual(empty.Summary, emptySummary) || empty.HeadFound {
		t.Fatalf("empty snapshot=%+v summary=%+v", empty, emptySummary)
	}

	commitFile(t, work, "one", "first line\nstill the subject\n\nbody", "2024-03-04T05:06:07+09:00")
	runGit(t, work, "tag", "-a", "annotated", "-m", "annotated")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/side", "refs/tags/annotated")
	snapshot, err := manager.RefSnapshot(ctx, "sample")
	noErr(t, err)
	summary, err := manager.Summary(ctx, "sample")
	noErr(t, err)
	if !reflect.DeepEqual(snapshot.Summary, summary) {
		t.Fatalf("snapshot summary=%+v, want %+v", snapshot.Summary, summary)
	}
	_, commits, err := manager.Commits(ctx, "sample", "refs/heads/main", 1)
	noErr(t, err)
	head := commits[0]
	if !snapshot.HeadFound || snapshot.Head.OID != head.OID || snapshot.Head.AuthorName != head.AuthorName ||
		!snapshot.Head.AuthoredAt.Equal(head.AuthoredAt) || snapshot.Head.Subject != head.Subject {
		t.Fatalf("snapshot head=%+v, want %+v", snapshot.Head, head)
	}

	// Head fields must match git log for unusual messages. A commit with an
	// encoding header is converted to UTF-8 by git log but printed raw by
	// for-each-ref, so the snapshot must convert it too.
	tree := gitOutput(t, "", "--git-dir", remote, "rev-parse", "main^{tree}")
	parent := gitOutput(t, "", "--git-dir", remote, "rev-parse", "main")
	for name, message := range map[string]struct {
		header string
		body   string
	}{
		"latin1":                     {"author J\xfcrgen <j@example.invalid> 1700000000 +0100\ncommitter J\xfcrgen <j@example.invalid> 1700000000 +0100\nencoding ISO-8859-1\n", "Gr\xfc\xdfe aus K\xf6ln\n"},
		"crlf":                       {"author A <a@example.invalid> 1700000000 +0000\ncommitter A <a@example.invalid> 1700000000 +0000\n", "first\r\nsecond\r\n\r\nbody\r\n"},
		"leading-blank":              {"author A <a@example.invalid> 1700000000 +0000\ncommitter A <a@example.invalid> 1700000000 +0000\n", "\n\n  indented subject  \nnext\n\nbody\n"},
		"folded-trailing-whitespace": {"author A <a@example.invalid> 1700000000 +0000\ncommitter A <a@example.invalid> 1700000000 +0000\n", "a\tb   \n   c\t\nd\n"},
		"empty-message":              {"author A <a@example.invalid> 1700000000 +0000\ncommitter A <a@example.invalid> 1700000000 +0000\n", ""},
	} {
		oid := hashCommit(t, remote, "tree "+tree+"\nparent "+parent+"\n"+message.header+"\n"+message.body)
		runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", oid)
		snapshot, err := manager.RefSnapshot(ctx, "sample")
		noErr(t, err)
		_, commits, err := manager.Commits(ctx, "sample", "refs/heads/main", 1)
		noErr(t, err)
		head := commits[0]
		if !snapshot.HeadFound || snapshot.Head.OID != head.OID || snapshot.Head.AuthorName != head.AuthorName ||
			!snapshot.Head.AuthoredAt.Equal(head.AuthoredAt) || snapshot.Head.Subject != head.Subject {
			t.Errorf("%s: snapshot head author=%q subject=%q, want author=%q subject=%q", name, snapshot.Head.AuthorName, snapshot.Head.Subject, head.AuthorName, head.Subject)
		}
		if name == "latin1" && (head.AuthorName != "Jürgen" || head.Subject != "Grüße aus Köln") {
			t.Errorf("fixture did not produce a converted Latin-1 commit: %q %q", head.AuthorName, head.Subject)
		}
	}

	// A deleted default branch is still named, as Summary names it.
	runGit(t, work, "push", "origin", ":refs/heads/main")
	missing, err := manager.RefSnapshot(ctx, "sample")
	noErr(t, err)
	missingSummary, err := manager.Summary(ctx, "sample")
	noErr(t, err)
	if !reflect.DeepEqual(missing.Summary, missingSummary) || missing.Summary.DefaultBranch != "main" || missing.Summary.DefaultOID != "" || missing.HeadFound {
		_ = remote
		t.Fatalf("missing default snapshot=%+v summary=%+v", missing, missingSummary)
	}
}

// TestActivityKeyFollowsEveryActivityInput proves that the snapshot key and
// the key of a real observation agree, change with every ref that Activity
// reads, and ignore refs it does not read.
func TestActivityKeyFollowsEveryActivityInput(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()
	check := func(step string) string {
		t.Helper()
		snapshot, err := manager.RefSnapshot(ctx, "sample")
		noErr(t, err)
		activity, err := manager.Activity(ctx, "sample", 100)
		noErr(t, err)
		if snapshot.ActivityKey == "" || snapshot.ActivityKey != activity.Key {
			t.Fatalf("%s: snapshot key %q differs from activity key %q", step, snapshot.ActivityKey, activity.Key)
		}
		return activity.Key
	}
	emptyKey := check("empty")
	commitFile(t, work, "one", "one", "2024-01-01T10:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/other")
	pushed := check("first push")
	if pushed == emptyKey {
		t.Fatal("pushing branches did not change the activity key")
	}
	runGit(t, work, "tag", "lightweight")
	runGit(t, work, "push", "origin", "refs/tags/lightweight")
	if check("tag push") != pushed {
		t.Fatal("a tag, which activity does not count, changed the activity key")
	}
	commitFile(t, work, "two", "two", "2024-01-02T10:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	advanced := check("fast-forward")
	if advanced == pushed {
		t.Fatal("a fast-forward push did not change the activity key")
	}
	// Rewriting main retains its old tip. Rewriting other, which points at an
	// already retained commit, adds only a provenance ref, which still changes
	// how retained activity is labelled.
	runGit(t, work, "reset", "--hard", "HEAD~1")
	commitFile(t, work, "three", "three", "2024-01-03T10:00:00Z")
	runGit(t, work, "push", "--force", "origin", "HEAD:refs/heads/main")
	rewritten := check("rewrite main")
	if rewritten == advanced {
		t.Fatal("rewriting main did not change the activity key")
	}
	// A provenance ref alone, without any branch or retained ref moving,
	// changes how retained activity is labelled, so it must change the key.
	retainedOID := gitOutput(t, "", "--git-dir", remote, "for-each-ref", "--format=%(objectname)", "refs/owngit/retained/heads")
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/provenance/heads/other/"+retainedOID, retainedOID)
	if check("provenance only") == rewritten {
		t.Fatal("a new retained provenance ref did not change the activity key")
	}
}

func TestPrepareExistingWritesOnlySettingsThatDiffer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-recording wrapper is a Unix test fixture")
	}
	manager, remote, _ := newTestRepository(t)
	ctx := context.Background()
	tracePath := recordGitCommands(t, manager)
	noErr(t, manager.PrepareExisting(ctx))
	if configs := recordedConfigCommands(t, tracePath); !reflect.DeepEqual(configs, []string{"config --local --list -z"}) {
		t.Fatalf("prepare of a correct repository ran %q, want one configuration read", configs)
	}
	hook := filepath.Join(remote, "hooks", "update")
	before, err := os.Stat(hook)
	noErr(t, err)

	runGit(t, "", "--git-dir", remote, "config", "--local", "gc.auto", "6700")
	runGit(t, "", "--git-dir", remote, "config", "--local", "--unset", "receive.hideRefs")
	noErr(t, os.WriteFile(tracePath, nil, 0o600))
	noErr(t, manager.PrepareExisting(ctx))
	want := []string{"config --local --list -z", "config --local receive.hideRefs refs/owngit/", "config --local gc.auto 0"}
	if configs := recordedConfigCommands(t, tracePath); !reflect.DeepEqual(configs, want) {
		t.Fatalf("prepare ran %q, want %q", configs, want)
	}
	for key, value := range map[string]string{"gc.auto": "0", "receive.hideRefs": "refs/owngit/"} {
		if got := gitOutput(t, "", "--git-dir", remote, "config", "--local", "--get", key); got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
	after, err := os.Stat(hook)
	noErr(t, err)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an unchanged retention hook was rewritten")
	}

	// A key with several values is not treated as correct. Git refuses to
	// overwrite it, so startup still reports the repository.
	runGit(t, "", "--git-dir", remote, "config", "--local", "--add", "transfer.hideRefs", "refs/other/")
	if err := manager.PrepareExisting(ctx); err == nil || !strings.Contains(err.Error(), `prepare repository "sample"`) {
		t.Fatalf("multi-valued setting error=%v", err)
	}
}

// recordGitCommands replaces the manager's runner with one that appends each
// Git invocation's arguments to a trace file, one line per invocation.
func recordGitCommands(t *testing.T, manager *Manager) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace")
	wrapper := filepath.Join(dir, "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(tracePath) + "\nexec " + shellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	runner, err := gitexec.New(wrapper, filepath.Join(dir, "runtime"))
	noErr(t, err)
	manager.Git = runner
	noErr(t, os.WriteFile(tracePath, nil, 0o600))
	return tracePath
}

func recordedConfigCommands(t *testing.T, tracePath string) []string {
	t.Helper()
	trace, err := os.ReadFile(tracePath)
	noErr(t, err)
	var configs []string
	for _, line := range strings.Split(strings.TrimSpace(string(trace)), "\n") {
		if strings.HasPrefix(line, "--git-dir . config ") {
			configs = append(configs, strings.TrimPrefix(line, "--git-dir . "))
		}
	}
	return configs
}

func TestRefSnapshotMatchesSummaryForHeadStates(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()
	compare := func(step string) RefSnapshot {
		t.Helper()
		snapshot, err := manager.RefSnapshot(ctx, "sample")
		noErr(t, err)
		summary, err := manager.Summary(ctx, "sample")
		noErr(t, err)
		if !reflect.DeepEqual(snapshot.Summary, summary) {
			t.Errorf("%s: snapshot summary %+v, want %+v", step, snapshot.Summary, summary)
		}
		return snapshot
	}
	commitFile(t, work, "one", "one", "2024-01-01T10:00:00Z")
	runGit(t, work, "tag", "v1")
	runGit(t, work, "push", "origin", "refs/tags/v1", "HEAD:refs/heads/other")
	if snapshot := compare("unborn HEAD with a tag and another branch"); snapshot.HeadFound || snapshot.Summary.DefaultBranch != "main" {
		t.Errorf("unborn HEAD snapshot=%+v", snapshot)
	}
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	compare("born")
	oid := gitOutput(t, "", "--git-dir", remote, "rev-parse", "main")
	runGit(t, "", "--git-dir", remote, "update-ref", "--no-deref", "HEAD", oid)
	if snapshot := compare("detached HEAD"); snapshot.HeadFound {
		t.Errorf("detached HEAD reported a default branch head: %+v", snapshot)
	}
	runGit(t, "", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	commitFile(t, work, "two", "two", "2024-01-02T10:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, work, "reset", "--hard", "HEAD~1")
	runGit(t, work, "push", "origin", ":refs/heads/main", ":refs/heads/other", ":refs/tags/v1")
	if snapshot := compare("retained history only"); snapshot.Summary.Empty {
		t.Error("a repository with only retained history was reported empty")
	}
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, "", "--git-dir", remote, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	compare("symbolic branch")
	runGit(t, "", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/alias")
	compare("HEAD pointing to a symbolic branch")
}

// TestPrepareExistingRepairsEverySettingAndHook proves that skipping correct
// settings and hooks never skips a wrong one.
func TestPrepareExistingRepairsEverySettingAndHook(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	ctx := context.Background()
	hook := filepath.Join(remote, "hooks", "update")
	want := readFile(t, hook)
	for index, setting := range repositoryConfig() {
		if index%2 == 0 {
			runGit(t, "", "--git-dir", remote, "config", "--local", "--unset-all", setting[0])
		} else {
			runGit(t, "", "--git-dir", remote, "config", "--local", setting[0], "wrong")
		}
	}
	// An upper-case spelling of a correct key is recognized, not duplicated.
	_, _ = gitCombined("", "--git-dir", remote, "config", "--local", "--unset-all", "gc.autoDetach")
	config := filepath.Join(remote, "config")
	noErr(t, os.WriteFile(config, append(readFile(t, config), "[GC]\n\tAUTODETACH = false\n"...), 0o600))
	noErr(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte("#!/bin/sh\n"), 0o700))
	noErr(t, manager.PrepareExisting(ctx))
	for _, setting := range repositoryConfig() {
		if got := gitOutput(t, "", "--git-dir", remote, "config", "--local", "--get-all", setting[0]); got != setting[1] {
			t.Errorf("%s = %q, want %q", setting[0], got, setting[1])
		}
	}
	if string(readFile(t, hook)) != string(want) {
		t.Error("a hook with stale content was not rewritten")
	}
	if _, err := os.Stat(filepath.Join(remote, "hooks", "pre-receive")); !os.IsNotExist(err) {
		t.Error("an obsolete hook was kept")
	}
	// Windows keeps only a read-only attribute, so a file mode such as 0700
	// cannot be set or observed there.
	if runtime.GOOS != "windows" {
		noErr(t, os.Chmod(hook, 0o755))
		noErr(t, manager.PrepareExisting(ctx))
		if info, err := os.Stat(hook); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("hook mode was not repaired: %v %v", info, err)
		}
	}
	noErr(t, os.Remove(hook))
	noErr(t, manager.PrepareExisting(ctx))
	if string(readFile(t, hook)) != string(want) {
		t.Error("a missing hook was not written")
	}
}

// hashCommit writes a raw commit object and returns its ID.
func hashCommit(t *testing.T, gitDir, object string) string {
	t.Helper()
	command := exec.Command("git", "--git-dir", gitDir, "hash-object", "-t", "commit", "-w", "--stdin")
	command.Stdin = strings.NewReader(object)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("hash-object: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	noErr(t, err)
	return content
}
