package checkrunner

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"owngit/internal/checkapi"
	"owngit/internal/checkexec"
	"owngit/internal/state"
)

// The server measures uploaded text after JSON decoding, so the runner must
// bound what the server will see, not the raw bytes that it cut.
func TestRunnerEvidenceStaysWithinServerBoundsAfterJSON(t *testing.T) {
	results := []checkexec.Result{
		{Name: "small", Command: "small", Status: checkexec.StatusPassed, Output: "ok 가\n"},
		// CP949 bytes for "가" are not UTF-8; encoding/json would triple each.
		{Name: "legacy", Command: "legacy", Status: checkexec.StatusPassed, Output: strings.Repeat("\xb0\xa1 ", 20_000)},
		// A three-byte rune straddles every byte bound after the two-byte prefix.
		{Name: "korean", Command: "korean", Status: checkexec.StatusPassed, Output: "xy" + strings.Repeat("가", 100_000)},
	}
	converted := runnerResults(results, true)
	if converted[0].OutputExcerpt != "ok 가\n" || converted[0].Truncated {
		t.Fatalf("small excerpt changed: %+v", converted[0])
	}
	for _, result := range converted[1:] {
		excerpt := jsonRoundTrip(t, result.OutputExcerpt)
		if len(excerpt) > state.MaximumCheckExcerptBytes || !utf8.ValidString(result.OutputExcerpt) || !result.Truncated {
			t.Fatalf("%s excerpt decodes to %d bytes (bound %d), valid=%v truncated=%v", result.Name, len(excerpt), state.MaximumCheckExcerptBytes, utf8.ValidString(result.OutputExcerpt), result.Truncated)
		}
	}
	log, truncated := runnerLog(results)
	decoded := jsonRoundTrip(t, log)
	if len(decoded) > state.MaximumCheckLogBytes || !utf8.ValidString(log) || !truncated {
		t.Fatalf("log decodes to %d bytes (bound %d), valid=%v truncated=%v", len(decoded), state.MaximumCheckLogBytes, utf8.ValidString(log), truncated)
	}
	if log, truncated := runnerLog(results[:1]); truncated || log != "[passed] small\nok 가\n\n" {
		t.Fatalf("small log=%q truncated=%v", log, truncated)
	}
}

func TestRunnerUploadUsesStartCapability(t *testing.T) {
	result := checkexec.Result{Name: "step", Command: "print", Status: checkexec.StatusIncomplete, Output: "user output", Truncated: true, ExceededOutputLimit: 4096}
	for _, test := range []struct {
		start string
		limit int64
	}{
		{`{"attempt":{"result_facts":["output_limit_exceeded_bytes"]}}`, 4096},
		{`{"attempt":{}}`, 0},
		{`{bad`, 0},
	} {
		var answer checkapi.JobResponse
		_ = json.Unmarshal([]byte(test.start), &answer)
		facts := runnerResults([]checkexec.Result{result}, answer.Attempt.AcceptsResultFact(checkapi.OutputLimitExceededFact), "run")
		encoded, err := json.Marshal(facts[0])
		noErr(t, err)
		if facts[0].OutputLimitExceededBytes != test.limit || strings.Contains(string(encoded), checkapi.OutputLimitExceededFact) != (test.limit > 0) ||
			strings.Contains(facts[0].OutputExcerpt, checkexec.OutputLimitNote(4096)) != (test.limit == 0) || facts[0].Role != "run" {
			t.Fatalf("start=%q upload=%s", test.start, encoded)
		}
	}
	log, _ := runnerLog([]checkexec.Result{result})
	if !strings.Contains(log, checkexec.OutputLimitNote(4096)) {
		t.Fatalf("raw log lost the limit note: %q", log)
	}
}

func jsonRoundTrip(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	noErr(t, err)
	var decoded string
	noErr(t, json.Unmarshal(encoded, &decoded))
	return decoded
}

// The server stores unavailable summaries and cleanup errors as one line. A
// joined multi-line error used to be refused, which stopped the runner.
func TestRunnerSummaryIsOneBoundedLine(t *testing.T) {
	summary := bounded("prepare workspace: first\nsecond\r\nthird\x00" + strings.Repeat("가", 400))
	if strings.ContainsAny(summary, "\r\n\x00") || len(summary) > 500 || !utf8.ValidString(summary) {
		t.Fatalf("summary=%q len=%d", summary, len(summary))
	}
}

// A log part cut inside a three-byte character must still report the cut.
// Each shift moves the limit to a different byte of the character.
func TestRunnerLogReportsTruncationAtEveryCharacterAlignment(t *testing.T) {
	for shift := 0; shift < 3; shift++ {
		output := strings.Repeat("a", shift) + strings.Repeat("가", (maximumRunnerLog+300)/3)
		log, truncated := runnerLog([]checkexec.Result{{Status: checkexec.StatusPassed, Command: "c", Output: output}})
		if !truncated || len(log) > maximumRunnerLog || !utf8.ValidString(log) {
			t.Errorf("shift=%d stored=%d truncated=%v valid=%v", shift, len(log), truncated, utf8.ValidString(log))
		}
	}
}

// A check may print far more than the stored log keeps. Building the log must
// not copy that output whole before the cut.
func TestRunnerLogDoesNotCopyWholeOutput(t *testing.T) {
	results := []checkexec.Result{{Status: checkexec.StatusPassed, Command: "c", Output: strings.Repeat("x", 8<<20)}}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	log, truncated := runnerLog(results)
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4*maximumRunnerLog || len(log) > maximumRunnerLog || len(log) < maximumRunnerLog-64 || !truncated {
		t.Fatalf("allocated %d bytes for a %d-byte log, truncated=%v", allocated, len(log), truncated)
	}
}
