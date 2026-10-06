package pullrequest

import (
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"

	"owngit/internal/checkworkflow"
)

// gitInput runs Git in directory with input on stdin and returns its output.
func gitInput(t *testing.T, directory string, input []byte, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// storeTwoWorkflowRecords writes a commit whose tree names the workflow path
// twice, as objects stored before the receive-side object check can hold, and
// returns the commit ID. A directory named .owngit and an entry whose name
// already holds the separator both flatten to that one path.
func storeTwoWorkflowRecords(t *testing.T, fixture *serviceFixture, first, second string) string {
	t.Helper()
	blob := func(content string) string {
		return gitInput(t, fixture.remote, []byte(content), "hash-object", "-w", "--stdin")
	}
	inner := gitInput(t, fixture.remote, []byte("100644 blob "+blob(first)+"\tchecks.json\n"), "mktree")
	var raw []byte
	write := func(mode, name, oid string) {
		raw = append(raw, mode+" "+name...)
		raw = append(raw, 0)
		decoded, err := hex.DecodeString(oid)
		noErr(t, err)
		raw = append(raw, decoded...)
	}
	write("40000", ".owngit", inner)
	write("100644", checkworkflow.Path, blob(second))
	root := gitInput(t, fixture.remote, raw, "hash-object", "--literally", "-t", "tree", "-w", "--stdin")
	return gitInput(t, fixture.remote, []byte("two workflows\n"), "-c", "user.name=Committed Checks",
		"-c", "user.email=checks@example.invalid", "commit-tree", root)
}

// A revision whose tree names the workflow path twice must not be judged by one
// of the two workflows there: the reader reports the collision, which the
// failure-check configuration read reports as a failure. One committed workflow
// reads, and a revision without the file keeps no committed configuration.
func TestCommittedChecksReadsOneWorkflowRecordOrReportsTheCollision(t *testing.T) {
	fixture := newServiceFixture(t)
	one := `{"version":1,"events":{"push":{}},"checks":[{"name":"one","command":"true"}]}`
	two := `{"version":1,"events":{"push":{}},"checks":[{"name":"two","command":"true"}]}`
	fixture.commitFile("file.txt", "no workflow here\n", "no workflow")
	fixture.push("HEAD:refs/heads/main")
	withoutWorkflow := fixture.gitOutput("rev-parse", "HEAD")
	fixture.commitFile(checkworkflow.Path, one, "one workflow")
	fixture.push("HEAD:refs/heads/workflow")
	withWorkflow := fixture.gitOutput("rev-parse", "HEAD")
	colliding := storeTwoWorkflowRecords(t, fixture, one, two)
	gitInput(t, fixture.remote, nil, "update-ref", "refs/heads/collision", colliding)
	listing := gitInput(t, fixture.remote, nil, "ls-tree", "-z", "-l", colliding, "--", checkworkflow.Path)
	if count := strings.Count(listing, checkworkflow.Path); count != 2 {
		t.Fatalf("the fixture's tree lists %s %d times, want 2:\n%s", checkworkflow.Path, count, listing)
	}

	checks, present, err := fixture.service.committedChecks(fixture.ctx, fixture.remote, withWorkflow)
	if err != nil || !present || len(checks) != 1 || checks[0].Name != "one" || checks[0].Command != "true" {
		t.Fatalf("one committed workflow: checks=%+v present=%v err=%v", checks, present, err)
	}
	if checks, present, err := fixture.service.committedChecks(fixture.ctx, fixture.remote, withoutWorkflow); err != nil || present || checks != nil {
		t.Fatalf("no committed workflow: checks=%+v present=%v err=%v, want an absent configuration", checks, present, err)
	}
	checks, present, err = fixture.service.committedChecks(fixture.ctx, fixture.remote, colliding)
	if err == nil || present || checks != nil {
		t.Fatalf("two committed workflows: checks=%+v present=%v err=%v, want a reported failure", checks, present, err)
	}
	if !strings.Contains(err.Error(), "2 entries at "+checkworkflow.Path) {
		t.Fatalf("two committed workflows: err=%v, want the two records named", err)
	}
}
