package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestALocalRunReportsNoMeasuredBudget pins the fact the guide and the skill
// now state explicitly.
//
// Top-level correction_cycles_remaining is only assigned from a server
// response. A local run leaves it at the Go zero value, so the printed 0 is an
// unread field rather than an exhausted budget. The reader's rule is the task
// object: no task, no measurement. If that ever stops being true, the written
// guidance is wrong and this test has to say so.
func TestALocalRunReportsNoMeasuredBudget(t *testing.T) {
	work := t.TempDir()
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Budget Test")
	runPRGit(t, work, "config", "user.email", "budget-test@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("base\n"), 0o600))
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-m", "base")

	output, err := captureStdout(func() error {
		return checkCommand([]string{
			"run", "--task", "local-task", "--workdir", work,
			"--check", "pass=exit 0", "--no-upload",
		})
	})
	if err != nil {
		t.Fatalf("local run error=%v output=%s", err, output)
	}
	var result checkRunOutput
	noErr(t, json.Unmarshal([]byte(output), &result))

	if !result.OK || result.Registered || result.Uploaded {
		t.Fatalf("a local run reported server state: ok=%v registered=%v uploaded=%v",
			result.OK, result.Registered, result.Uploaded)
	}
	// The documented signal: an unmeasured budget has no task object.
	if result.Task != nil {
		t.Fatalf("a local run carries a task object: %+v", result.Task)
	}
	if result.CorrectionCyclesRemaining != 0 {
		t.Fatalf("the unread field is %d; the documented value is 0",
			result.CorrectionCyclesRemaining)
	}
	// The field is present in the JSON, which is why it can be misread as a
	// measurement and why the documents have to name it.
	var raw map[string]json.RawMessage
	noErr(t, json.Unmarshal([]byte(output), &raw))
	if _, ok := raw["correction_cycles_remaining"]; !ok {
		t.Fatal("correction_cycles_remaining is absent; the documented caution describes a printed 0")
	}
	if _, ok := raw["task"]; ok {
		t.Fatal("a local run emits a task key; the documented rule is that it is omitted")
	}
}

// TestTheDocumentsWarnAboutTheUnmeasuredBudget keeps the guide and the skill
// carrying the caution together.
//
// They are delivered as a pair, and an agent that reads only the skill must
// still be told not to infer exhaustion from the local 0.
func TestTheDocumentsWarnAboutTheUnmeasuredBudget(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	noErr(t, err)
	for _, document := range []string{
		"docs/CODING_TOOLS.md",
		"integrations/skills/owngit-checks/SKILL.md",
	} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(document)))
		noErr(t, err)
		// Markdown wraps these documents, so a phrase can be split across
		// lines. Compare on collapsed whitespace, not the raw text.
		body := strings.Join(strings.Fields(string(data)), " ")
		if !strings.Contains(body, "correction_cycles_remaining") {
			t.Errorf("%s does not name the field", document)
		}
		if !strings.Contains(body, "check status") {
			t.Errorf("%s does not send the reader to check status", document)
		}
		if !strings.Contains(body, "task") {
			t.Errorf("%s does not name the task object", document)
		}
		// An unconfirmed registration is not an absent one. Only --no-upload
		// establishes that the server task was not touched, so the documents
		// must not group the two into one "unchanged" claim.
		if !strings.Contains(body, "unconfirmed") && !strings.Contains(body, "may have been accepted") {
			t.Errorf("%s does not say a failed registration is unconfirmed rather than absent", document)
		}
		if strings.Contains(body, "In both cases the server task is unchanged") {
			t.Errorf("%s still claims a failed registration left the server task unchanged", document)
		}
		// The uncertainty must stay uncertain in both directions.
		if strings.Contains(body, "a failed registration did not") {
			t.Errorf("%s asserts a failed registration changed the task instead of reporting uncertainty", document)
		}
		// Reusing an attempt identifier is not an action check run offers.
		if strings.Contains(body, "reuse the same task and attempt identifiers") {
			t.Errorf("%s tells the reader to reuse an attempt identifier, which check run cannot do", document)
		}
		if !strings.Contains(body, "diagnostic evidence") {
			t.Errorf("%s does not keep the attempt identifier as diagnostic evidence", document)
		}
	}
}

// TestCheckRunCannotResubmitAnAttemptIdentifier is why the documents say to
// preserve the identifier as evidence rather than to reuse it.
//
// check run mints a fresh attempt identifier on every invocation and exposes
// no flag to resubmit an existing one, so a rerun starts a separate attempt.
// If that ever changes, the written guidance needs revisiting.
func TestCheckRunCannotResubmitAnAttemptIdentifier(t *testing.T) {
	work := t.TempDir()
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Budget Test")
	runPRGit(t, work, "config", "user.email", "budget-test@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("base\n"), 0o600))
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-m", "base")

	run := func() checkRunOutput {
		t.Helper()
		output, err := captureStdout(func() error {
			return checkCommand([]string{
				"run", "--task", "local-task", "--workdir", work,
				"--check", "pass=exit 0", "--no-upload",
			})
		})
		if err != nil {
			t.Fatalf("local run error=%v output=%s", err, output)
		}
		var result checkRunOutput
		noErr(t, json.Unmarshal([]byte(output), &result))
		return result
	}

	first, second := run(), run()
	if first.AttemptID == "" || second.AttemptID == "" {
		t.Fatal("a run printed no attempt identifier")
	}
	if first.AttemptID == second.AttemptID {
		t.Fatal("two runs shared an attempt identifier; a rerun could continue an attempt")
	}

	// No flag accepts an existing attempt identifier.
	output, err := captureStdout(func() error {
		return checkCommand([]string{
			"run", "--task", "local-task", "--workdir", work,
			"--check", "pass=exit 0", "--no-upload",
			"--attempt", first.AttemptID,
		})
	})
	if err == nil {
		t.Fatalf("check run accepted an --attempt flag: %s", output)
	}
	t.Logf("rejected: %v", err)
}
