package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
