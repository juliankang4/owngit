package checkrun

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
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

// A check may print far more than the stored log keeps. Building the log must
// not copy that output whole before the cut.
func TestAutomaticLogDoesNotCopyWholeOutput(t *testing.T) {
	results := []checkexec.Result{{Status: checkexec.StatusPassed, Command: "c", Output: strings.Repeat("x", 8<<20)}}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	log, truncated := buildLog(results)
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4*maximumAutomaticLogSize || len(log) > maximumAutomaticLogSize || len(log) < maximumAutomaticLogSize-64 || !truncated {
		t.Fatalf("allocated %d bytes for a %d-byte log, truncated=%v", allocated, len(log), truncated)
	}
}

const evidenceChildMode = "OWNGIT_CHECKRUN_EVIDENCE_MODE"

var evidenceSecret = "synthetic-token-" + strings.Repeat("s", 49)

func evidenceChildOutput(mode string) string {
	switch mode {
	case "invalid":
		return strings.Repeat("\xff", 1<<20) + "END_MARKER_MUST_BE_KEPT\n"
	case "secret":
		return strings.Repeat(evidenceSecret+"\n", 16000) + "END_MARKER_MUST_BE_KEPT\n"
	}
	return strings.Repeat("head α🙂\n", 100000) + "TAIL_NOT_IN_THE_HEAD"
}

// TestEvidenceChild prints one of the evidence outputs when a test runs the
// test binary as a check.
func TestEvidenceChild(t *testing.T) {
	mode := os.Getenv(evidenceChildMode)
	if mode == "" {
		t.Skip("evidence fixture only")
	}
	_, _ = os.Stdout.WriteString(evidenceChildOutput(mode))
	os.Exit(0)
}

// requireOmittedCount checks that text holds one marker and that the marker's
// count plus the real bytes shown equals the size of the original output, even
// though the output was cut twice on its way here.
func requireOmittedCount(t *testing.T, name, text string, source int) {
	t.Helper()
	start := strings.Index(text, "\n[... ")
	end := strings.Index(text, "bytes omitted ...]\n") + len("bytes omitted ...]\n")
	if start < 0 || strings.Count(text, "bytes omitted") != 1 {
		t.Fatalf("%s: want one marker, got %q", name, text[:min(len(text), 200)])
	}
	omitted, _ := strconv.Atoi(strings.Fields(text[start:end])[1])
	if shown := len(text) - (end - start); omitted+shown != source {
		t.Fatalf("%s: marker says %d omitted and %d bytes are shown, but the output had %d", name, omitted, shown, source)
	}
}

// Output under the ceiling that shrinks when invalid UTF-8 or secrets are
// replaced stores the same log and excerpt as the whole output does. Output
// far over the ceiling, such as a check that prints 1.2 MB and then fails,
// keeps its final line in both the stored log and the excerpt.
func TestEvidenceOfARealCheckRun(t *testing.T) {
	for _, mode := range []string{"invalid", "secret", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			results, _ := checkexec.Run(context.Background(), []checkexec.Definition{{Name: "head", Command: "evidence",
				Executable: os.Args[0], Arguments: []string{"-test.run=^TestEvidenceChild$"}}}, checkexec.Options{
				Timeout: 30 * time.Second, OutputLimit: 8 << 20, Redact: []string{evidenceSecret},
				Env: append(os.Environ(), evidenceChildMode+"="+mode),
			})
			if results[0].Status != checkexec.StatusPassed || results[0].Truncated {
				t.Fatalf("status=%s truncated=%v", results[0].Status, results[0].Truncated)
			}
			gotLog, gotCut := buildLog(results)
			got := stateResults(results)[0]
			if mode == "ordinary" {
				if !gotCut || !got.Truncated || !strings.HasSuffix(gotLog, "TAIL_NOT_IN_THE_HEAD\n") || !strings.HasSuffix(got.OutputExcerpt, "TAIL_NOT_IN_THE_HEAD") {
					t.Fatalf("the final line is missing: log cut=%v excerpt truncated=%v log end %q", gotCut, got.Truncated, gotLog[len(gotLog)-40:])
				}
				original := len(evidenceChildOutput(mode))
				requireOmittedCount(t, "log", gotLog, len("[passed] evidence\n")+original+1)
				requireOmittedCount(t, "excerpt", got.OutputExcerpt, original)
				return
			}
			whole := append([]checkexec.Result(nil), results...)
			whole[0].Output = strings.ReplaceAll(evidenceChildOutput(mode), evidenceSecret, "[redacted]")
			wantLog, wantCut := buildLog(whole)
			want := stateResults(whole)[0]
			if gotLog != wantLog || gotCut != wantCut || got.OutputExcerpt != want.OutputExcerpt || got.Truncated != want.Truncated {
				t.Fatalf("log %d cut=%v excerpt truncated=%v; whole output gives log %d cut=%v excerpt truncated=%v",
					len(gotLog), gotCut, got.Truncated, len(wantLog), wantCut, want.Truncated)
			}
			if !strings.Contains(gotLog, "END_MARKER_MUST_BE_KEPT") {
				t.Fatal("the end marker is missing from the log")
			}
		})
	}
}
