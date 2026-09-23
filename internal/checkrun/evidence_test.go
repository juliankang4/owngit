package checkrun

import (
	"strings"
	"testing"
	"unicode/utf8"

	"owngit/internal/checkexec"
	"owngit/internal/state"
)

// Stored evidence is later served as JSON and copied into backups, so it must
// be valid UTF-8 within the durable bounds rather than a raw byte cut.
func TestAutomaticEvidenceIsValidUTF8WithinBounds(t *testing.T) {
	results := []checkexec.Result{
		{Name: "legacy", Command: "legacy", Status: checkexec.StatusPassed, Output: strings.Repeat("\xb0\xa1 ", 20_000)},
		{Name: "korean", Command: "korean", Status: checkexec.StatusPassed, Output: "xy" + strings.Repeat("가", 100_000)},
	}
	for _, result := range stateResults(results) {
		if len(result.OutputExcerpt) > state.MaximumCheckExcerptBytes || !utf8.ValidString(result.OutputExcerpt) || !result.Truncated {
			t.Fatalf("%s excerpt len=%d valid=%v truncated=%v", result.Name, len(result.OutputExcerpt), utf8.ValidString(result.OutputExcerpt), result.Truncated)
		}
	}
	log, truncated := buildLog(results)
	if len(log) > maximumAutomaticLogSize || !utf8.ValidString(log) || !truncated {
		t.Fatalf("log len=%d valid=%v truncated=%v", len(log), utf8.ValidString(log), truncated)
	}
	small := []checkexec.Result{{Name: "small", Command: "small", Status: checkexec.StatusPassed, Output: "ok 가"}}
	if converted := stateResults(small); converted[0].OutputExcerpt != "ok 가" || converted[0].Truncated {
		t.Fatalf("small excerpt changed: %+v", converted[0])
	}
	if log, truncated := buildLog(small); log != "[passed] small\nok 가\n" || truncated {
		t.Fatalf("small log=%q truncated=%v", log, truncated)
	}
}

// The store refuses a job summary with a line break, so a joined multi-line
// error used to leave the job without its recorded outcome.
func TestAutomaticSummaryIsOneBoundedLine(t *testing.T) {
	summary := boundedSummary("remove workspace: first\nsecond\r\nthird\x00" + strings.Repeat("가", 400))
	if strings.ContainsAny(summary, "\r\n\x00") || len(summary) > 500 || !utf8.ValidString(summary) {
		t.Fatalf("summary=%q len=%d", summary, len(summary))
	}
	if boundedSummary(" \n ") == "" {
		t.Fatal("an empty summary lost its fallback")
	}
}

// A log part cut inside a three-byte character must still report the cut.
// Each shift moves the limit to a different byte of the character.
func TestAutomaticLogReportsTruncationAtEveryCharacterAlignment(t *testing.T) {
	for shift := 0; shift < 3; shift++ {
		output := strings.Repeat("a", shift) + strings.Repeat("가", (maximumAutomaticLogSize+300)/3)
		log, truncated := buildLog([]checkexec.Result{{Status: checkexec.StatusPassed, Command: "c", Output: output}})
		if !truncated || len(log) > maximumAutomaticLogSize || !utf8.ValidString(log) {
			t.Errorf("shift=%d stored=%d truncated=%v valid=%v", shift, len(log), truncated, utf8.ValidString(log))
		}
	}
}
