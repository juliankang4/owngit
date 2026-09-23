package checkrunner

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

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
	converted := runnerResults(results)
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

func jsonRoundTrip(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
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
