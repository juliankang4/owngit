package webui

import (
	"html/template"
	"strings"
	"testing"
)

func TestCheckOutcomePresentation(t *testing.T) {
	for _, row := range []struct {
		name    string
		outcome CheckOutcome
		en, ko  string
	}{
		{"singular", CheckOutcome{Total: 1, Failed: 1}, "1 check: 1 failed", "체크 1개: 실패 1개"},
		{"plural ordered", CheckOutcome{Total: 6, Failed: 1, Error: 1, Unavailable: 1, Incomplete: 1, CancelledCount: 1, Passed: 1}, "6 checks: 1 failed, 1 could not finish, 1 environment unavailable, 1 incomplete, 1 cancelled, 1 passed", "체크 6개: 실패 1개, 끝내지 못함 1개, 실행 환경 없음 1개, 완료되지 않음 1개, 취소됨 1개, 통과 1개"},
		{"worktree cancellation", CheckOutcome{Total: 1, Passed: 1, Worktree: "dirty", Cancelled: true}, "1 check: 1 passed; worktree dirty; The run was cancelled", "체크 1개: 통과 1개; 작업 트리에 변경 사항 있음; 실행이 취소되었습니다"},
		{"unknown", CheckOutcome{Worktree: "unknown"}, "0 checks; worktree state unknown", "체크 0개; 작업 트리 상태 알 수 없음"},
		{"tolerated", CheckOutcome{Workflow: true, Total: 1, Failed: 1, Tolerated: 1}, "1 step: 1 failed; 1 tolerated failure", "단계 1개: 실패 1개; 허용된 실패 1개"},
		{"grouped", CheckOutcome{Workflow: true, Total: 1000, Failed: 1000, Tolerated: 1000}, "1,000 steps: 1,000 failed; 1,000 tolerated failures", "단계 1,000개: 실패 1,000개; 허용된 실패 1,000개"},
		{"nothing ran", CheckOutcome{Workflow: true, NothingRan: true}, "Nothing ran: every step was skipped or built in.", "실행한 단계가 없습니다. 모든 단계가 건너뛰기 또는 내장 단계입니다."},
		{"refusal", CheckOutcome{Refusal: `<img src=x> 100% %s "user"`}, `Check unavailable: <img src=x> 100% %s "user"`, `체크 실행 불가: <img src=x> 100% %s "user"`},
	} {
		t.Run(row.name, func(t *testing.T) {
			for lang, want := range map[Lang]string{LangEN: row.en, LangKO: row.ko} {
				if got := CheckOutcomeText(lang, row.outcome); got != want {
					t.Fatalf("%s: %q != %q", lang, got, want)
				}
				markup := string(biCheckOutcome(lang, row.outcome))
				if !strings.Contains(markup, template.HTMLEscapeString(want)) || strings.Contains(markup, "<img") {
					t.Fatalf("unsafe or missing text: %s", markup)
				}
			}
		})
	}
}

func TestCheckOutcomeScreens(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range []Lang{LangEN, LangKO} {
		for _, row := range []struct {
			name, excerpt string
			limit         int64
			truncated     bool
		}{
			{"overflow", "raw <user> %s", 1024, true},
			{"empty overflow", "", 1024, true},
			{"clip only", "clipped user text", 0, true},
			{"timeout", "timeout user text", 0, true},
			{"legacy", "[OwnGit stopped this check: its output passed the limit of 1024 bytes.]", 0, true},
		} {
			t.Run(string(lang)+"/"+row.name, func(t *testing.T) {
				outcome := &CheckOutcome{Total: 1, Incomplete: 1}
				task := tasksPage(fullChrome(lang), true)
				a := &task.Detail.Attempts[0]
				a.Outcome, a.Summary = outcome, "stored summary"
				a.Results = []CheckResultLine{{Name: "result", Status: CheckIncomplete, OutputExcerpt: row.excerpt, Truncated: row.truncated, OutputLimitExceededBytes: row.limit}}
				workflow := workflowPages(fullChrome(lang))["workflow-job"].(WorkflowsPage)
				workflow.Job.Job.Outcome = outcome
				workflow.Job.Job.Steps = []WorkflowStepRow{{Name: "result", Status: CheckIncomplete, Excerpt: row.excerpt, Truncated: row.truncated, OutputLimitExceededBytes: row.limit}}
				for _, page := range []Page{task, workflow} {
					body := render(t, r, page)
					if strings.Contains(body, "Execution stopped because output exceeded") != (row.limit > 0) {
						t.Fatal("limit notice does not follow the typed fact")
					}
					if !strings.Contains(body, template.HTMLEscapeString(row.excerpt)) || !strings.Contains(body, CheckOutcomeText(lang, *outcome)) {
						t.Fatal("missing recorded text or localized outcome")
					}
				}
				a.Outcome = nil
				if !strings.Contains(render(t, r, task), "stored summary") {
					t.Fatal("stored fallback lost")
				}
			})
		}
	}
}
