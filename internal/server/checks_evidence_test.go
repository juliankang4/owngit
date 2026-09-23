package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"owngit/internal/checkapi"
	"owngit/internal/state"
)

// Refusing finished evidence because its text is slightly over a bound loses
// the result. The server clips to the stored bound on a character boundary and
// records the cut instead.
func TestCompletionClipsOverlongTextInsteadOfRefusing(t *testing.T) {
	upload := checkapi.AttemptCompletion{
		FinishedAt: time.Now(), WorktreeState: state.WorktreeClean,
		Log: strings.Repeat("x", state.MaximumCheckLogBytes-1) + "가",
		Results: []checkapi.Result{
			{Name: "over", Command: "over", Status: state.AttemptPassed, OutputExcerpt: strings.Repeat("x", state.MaximumCheckExcerptBytes-2) + "가"},
			{Name: "fits", Command: "fits", Status: state.AttemptPassed, OutputExcerpt: "ok 가"},
		},
	}
	completion, problem := completionFromUpload(upload, "repository", "task", "attempt")
	if problem != nil {
		t.Fatalf("completion refused: %+v", problem)
	}
	over := completion.Results[0]
	if over.OutputExcerpt != strings.Repeat("x", state.MaximumCheckExcerptBytes-2) || !over.Truncated {
		t.Fatalf("over-long excerpt len=%d truncated=%v", len(over.OutputExcerpt), over.Truncated)
	}
	if fits := completion.Results[1]; fits.OutputExcerpt != "ok 가" || fits.Truncated {
		t.Fatalf("fitting excerpt changed: %+v", fits)
	}
	if len(completion.Log) != state.MaximumCheckLogBytes-1 || !utf8.ValidString(completion.Log) || !completion.LogTruncated {
		t.Fatalf("over-long log len=%d truncated=%v", len(completion.Log), completion.LogTruncated)
	}

	upload.Results[0].DurationMS = -1
	if _, problem := completionFromUpload(upload, "repository", "task", "attempt"); problem == nil || problem.Code != "invalid_attempt" {
		t.Fatalf("negative duration problem=%+v", problem)
	}
}

// An unclassified runner failure can name internal state or host paths. The
// runner receives a fixed message; the cause stays in the server log.
func TestRunnerErrorDoesNotExposeInternalCause(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeRunnerError(recorder, errors.New("open /private/state/owngit.db: disk I/O error"))
	body := recorder.Body.String()
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(body, `"state_unavailable"`) || strings.Contains(body, "/private/state") {
		t.Fatalf("runner error status=%d body=%s", recorder.Code, body)
	}
}
