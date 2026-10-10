package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"owngit/internal/checkapi"
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
	facts := checkResultsJSON(results, true)
	for _, fact := range facts[:2] {
		excerpt := roundTripJSONString(t, fact.OutputExcerpt)
		if len(excerpt) > state.MaximumCheckExcerptBytes || !utf8.ValidString(fact.OutputExcerpt) || !fact.Truncated {
			t.Fatalf("%s excerpt decodes to %d bytes, valid=%v truncated=%v", fact.Name, len(excerpt), utf8.ValidString(fact.OutputExcerpt), fact.Truncated)
		}
	}
	if cleanup := roundTripJSONString(t, facts[2].CleanupError); cleanup == "" || len(cleanup) > state.MaximumCleanupErrorBytes {
		t.Fatalf("cleanup error decodes to %d bytes", len(cleanup))
	}
	log, truncated := buildCheckLog(results, "")
	if decoded := roundTripJSONString(t, log); len(decoded) > state.MaximumCheckLogBytes || !utf8.ValidString(log) || !truncated {
		t.Fatalf("log decodes to %d bytes, valid=%v truncated=%v", len(decoded), utf8.ValidString(log), truncated)
	}
	exit := 0
	small := []checkexec.Result{{Name: "small", Command: "small", Status: checkexec.StatusPassed, ExitCode: &exit, Output: "ok 가"}}
	if facts := checkResultsJSON(small, true); facts[0].OutputExcerpt != "ok 가" || facts[0].Truncated {
		t.Fatalf("small excerpt changed: %+v", facts[0])
	}
	if log, truncated := buildCheckLog(small, ""); log != "== small: passed (exit 0)\nok 가" || truncated {
		t.Fatalf("small log=%q truncated=%v", log, truncated)
	}
}

func TestCheckUploadUsesAdvertisedResultFacts(t *testing.T) {
	result := checkexec.Result{Name: "output", Command: "print", Status: checkexec.StatusIncomplete,
		Output: "user\n[... 15 bytes omitted ...]\ntail", Truncated: true, ExceededOutputLimit: 1024}
	result.OutputGap = checkapi.Gap{Start: len("user\n"), End: len(result.Output) - len("tail"), Omitted: 15}
	for _, test := range []struct {
		name    string
		attempt *checkapi.Attempt
		limit   int64
		legacy  bool
	}{
		{"advertised", &checkapi.Attempt{ResultFacts: []string{checkapi.OutputLimitExceededFact}}, 1024, false},
		{"legacy", &checkapi.Attempt{}, 0, true},
		{"unknown capability", &checkapi.Attempt{ResultFacts: []string{"other"}}, 0, true},
	} {
		facts := checkResultsJSON([]checkexec.Result{result}, test.attempt.AcceptsResultFact(checkapi.OutputLimitExceededFact))
		encoded, err := json.Marshal(facts[0])
		noErr(t, err)
		note := checkexec.OutputLimitNote(1024)
		if facts[0].OutputLimitExceededBytes != test.limit || strings.HasPrefix(facts[0].OutputExcerpt, note) != test.legacy ||
			strings.Contains(string(encoded), checkapi.OutputLimitExceededFact) == test.legacy || !facts[0].Truncated || !strings.Contains(facts[0].OutputExcerpt, "15 bytes omitted") {
			t.Fatalf("%s upload=%s", test.name, encoded)
		}
	}
	log, _ := buildCheckLog([]checkexec.Result{result}, "")
	if !strings.Contains(log, checkexec.OutputLimitNote(1024)) || !strings.Contains(log, "15 bytes omitted") {
		t.Fatalf("raw log lost the note or gap: %q", log)
	}
}

func roundTripJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	noErr(t, err)
	var decoded string
	noErr(t, json.Unmarshal(encoded, &decoded))
	return decoded
}

// A log part cut inside a three-byte character must still report the cut.
// Each shift moves the limit to a different byte of the character.
func TestCheckRunLogReportsTruncationAtEveryCharacterAlignment(t *testing.T) {
	for shift := 0; shift < 3; shift++ {
		output := strings.Repeat("a", shift) + strings.Repeat("가", (state.MaximumCheckLogBytes+300)/3)
		log, truncated := buildCheckLog([]checkexec.Result{{Name: "c", Status: checkexec.StatusPassed, Command: "c", Output: output}}, "")
		if !truncated || len(log) > state.MaximumCheckLogBytes || !utf8.ValidString(log) {
			t.Errorf("shift=%d stored=%d truncated=%v valid=%v", shift, len(log), truncated, utf8.ValidString(log))
		}
	}
}

// A credential whose occurrences overlap in check output leaves no part of it
// in the JSON output excerpt or the raw log. (The command is the check's own
// text and is recorded as written.)
func TestCheckRunEvidenceHidesOverlappingCredentials(t *testing.T) {
	const secret = "abababababab"
	results, _ := checkexec.Run(context.Background(), []checkexec.Definition{{Name: "overlap", Command: "echo ababababababababab"}},
		checkexec.Options{Timeout: 30 * time.Second, OutputLimit: 1 << 20, Redact: []string{secret}})
	encoded, err := json.Marshal(checkResultsJSON(results, true)[0].OutputExcerpt)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := buildCheckLog(results, "")
	for name, text := range map[string]string{"output_excerpt": string(encoded), "log": log} {
		if strings.Contains(strings.ReplaceAll(text, "[redacted]", ""), "ab") || !strings.Contains(text, "[redacted]") {
			t.Fatalf("%s shows part of the credential: %s", name, text)
		}
	}
}
