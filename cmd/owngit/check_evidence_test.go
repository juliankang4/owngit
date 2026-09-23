package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"owngit/internal/checkexec"
	"owngit/internal/state"
)

// The server measures uploaded text after JSON decoding. A raw byte cut through
// a multibyte character or non-UTF-8 output grew past the bound in transit, and
// the server refused the completion.
func TestCheckRunEvidenceStaysWithinServerBoundsAfterJSON(t *testing.T) {
	results := []checkexec.Result{
		{Name: "legacy", Command: "legacy", Status: checkexec.StatusPassed, Output: strings.Repeat("\xb0\xa1 ", 20_000)},
		{Name: "korean", Command: "korean", Status: checkexec.StatusPassed, Output: "xy" + strings.Repeat("가", 100_000), Truncated: true},
		{Name: "cleanup", Command: "cleanup", Status: checkexec.StatusError, CleanupError: strings.Repeat("\xff", 400)},
	}
	facts := checkResultsJSON(results)
	for _, fact := range facts[:2] {
		excerpt := roundTripJSONString(t, fact.OutputExcerpt)
		if len(excerpt) > state.MaximumCheckExcerptBytes || !utf8.ValidString(fact.OutputExcerpt) || !fact.Truncated {
			t.Fatalf("%s excerpt decodes to %d bytes, valid=%v truncated=%v", fact.Name, len(excerpt), utf8.ValidString(fact.OutputExcerpt), fact.Truncated)
		}
	}
	if cleanup := roundTripJSONString(t, facts[2].CleanupError); cleanup == "" || len(cleanup) > state.MaximumCleanupErrorBytes {
		t.Fatalf("cleanup error decodes to %d bytes", len(cleanup))
	}
	log, truncated := buildCheckLog(results)
	if decoded := roundTripJSONString(t, log); len(decoded) > state.MaximumCheckLogBytes || !utf8.ValidString(log) || !truncated {
		t.Fatalf("log decodes to %d bytes, valid=%v truncated=%v", len(decoded), utf8.ValidString(log), truncated)
	}
	exit := 0
	small := []checkexec.Result{{Name: "small", Command: "small", Status: checkexec.StatusPassed, ExitCode: &exit, Output: "ok 가"}}
	if facts := checkResultsJSON(small); facts[0].OutputExcerpt != "ok 가" || facts[0].Truncated {
		t.Fatalf("small excerpt changed: %+v", facts[0])
	}
	if log, truncated := buildCheckLog(small); log != "== small: passed (exit 0)\nok 가" || truncated {
		t.Fatalf("small log=%q truncated=%v", log, truncated)
	}
}

func roundTripJSONString(t *testing.T, value string) string {
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

// A log part cut inside a three-byte character must still report the cut.
// Each shift moves the limit to a different byte of the character.
func TestCheckRunLogReportsTruncationAtEveryCharacterAlignment(t *testing.T) {
	for shift := 0; shift < 3; shift++ {
		output := strings.Repeat("a", shift) + strings.Repeat("가", (state.MaximumCheckLogBytes+300)/3)
		log, truncated := buildCheckLog([]checkexec.Result{{Name: "c", Status: checkexec.StatusPassed, Command: "c", Output: output}})
		if !truncated || len(log) > state.MaximumCheckLogBytes || !utf8.ValidString(log) {
			t.Errorf("shift=%d stored=%d truncated=%v valid=%v", shift, len(log), truncated, utf8.ValidString(log))
		}
	}
}
