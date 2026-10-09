package webui

import (
	"html/template"
	"strings"
	"testing"
)

func workflowNav(active string) ChecksNav {
	return ChecksNav{TasksURL: "/repositories/r1/tasks", WorkflowsURL: "/repositories/r1/workflows", RunsURL: "/repositories/r1/workflow-runs", Active: active}
}

func workflowPages(c Chrome) map[string]Page {
	base := func(view, section string) WorkflowsPage {
		return WorkflowsPage{Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks), Nav: workflowNav(section), View: view,
			SelfURL: "/repositories/r1/workflows", SubmitURL: "/repositories/r1/workflows"}
	}
	files := base(WorkflowViewFiles, ChecksSectionWorkflows)
	files.Files = &WorkflowFilesView{Branch: "main", Branches: []string{"main", "feature"}, ShortOID: "a41c9e2", SecretsURL: "/repositories/r1/workflow-secrets",
		Status: WorkflowSwitch{Events: []WorkflowEventState{{Event: "push", Allowed: true}, {Event: "workflow_dispatch"}},
			Review: &WorkflowTurnOn{Executor: ExecutorHost, Events: []string{"pull_request", "push"}, QueueLimit: 32, Digest: "d1", Run: 1, Partial: 1, Refused: 1}},
		Files: []WorkflowFileView{
			{Path: ".github/workflows/ci.yml", Name: "CI", Verdict: VerdictNoted, ExpansionKnown: true, ExpandedJobs: 2, DispatchURL: "/repositories/r1/workflows/run?path=ci",
				Triggers:  []WorkflowTriggerView{{Event: "workflow_dispatch", Notes: []WorkflowMessage{{Code: "workflow.event_off", Args: map[string]string{"event": "workflow_dispatch"}}}}},
				Inputs:    []WorkflowInputView{{Name: "count", Type: "number", Default: "2"}},
				Schedules: []WorkflowScheduleView{{Cron: "0 3 * * *", NextDueAt: testNow, Paused: true}},
				Jobs: []WorkflowJobPreview{{Key: "build", Verdict: VerdictNoted, RunsOn: []string{"ubuntu-latest"}, Steps: []WorkflowStepLine{{Name: "Run go test", Command: "go test ./..."}}},
					{Key: "deploy", Verdict: VerdictRefused, Refusal: &WorkflowMessage{Code: "workflow.environment"}}}},
			{Path: ".github/workflows/gh.yml", Verdict: VerdictRefused, Refusal: &WorkflowMessage{Code: "workflow.yaml", Line: 3, Args: map[string]string{"detail": "bad indent"}}},
		}}
	runs := base(WorkflowViewRuns, ChecksSectionRuns)
	runs.Runs = &WorkflowRunsView{Limit: 50, Truncated: true, Runs: []WorkflowRunRow{{URL: "/repositories/r1/workflow-runs/run1", WorkflowPath: ".github/workflows/ci.yml", Event: "push", Conclusion: "partial", CreatedAt: testNow, ShortOID: "a41c9e2", Counts: WorkflowCounts{Total: 2, Passed: 1, Refused: 1}}}}
	job := WorkflowJobRow{ID: "job1", Key: "build", URL: "/repositories/r1/workflow-runs/run1/jobs/job1", Status: "failed", FinishedAt: testNow,
		Steps: []WorkflowStepRow{{Name: "Set up Go", Status: "not_run", Role: "builtin"}, {Name: "Run go test", Command: "go test ./...", Status: "failed", Role: "tolerated", ExitCode: "1", Excerpt: "FAIL ***"}}}
	run := base(WorkflowViewRun, ChecksSectionRuns)
	run.SubmitURL = "/repositories/r1/workflow-runs/run1"
	run.Run = &WorkflowRunView{ID: "run1", Number: 7, WorkflowPath: ".github/workflows/ci.yml", Event: "workflow_dispatch", Branch: "main", ShortOID: "a41c9e2", Conclusion: "running",
		CreatedAt: testNow, Cancellable: true, Inputs: []WorkflowValue{{Name: "count", Value: "2"}}, Jobs: []WorkflowJobRow{job},
		Refused: []WorkflowRefusedJob{{Key: "deploy", Reason: WorkflowMessage{Code: "workflow.environment"}}}, Secrets: []WorkflowSecretUse{{Name: "TOKEN"}}, SecretsKnown: true}
	jobPage := base(WorkflowViewJob, ChecksSectionRuns)
	jobPage.Job = &WorkflowJobView{RunURL: "/repositories/r1/workflow-runs/run1", RunNumber: 7, WorkflowPath: ".github/workflows/ci.yml", Job: job, LogStatus: LogAvailable, LogURL: "/repositories/r1/workflow-runs/run1/jobs/job1/log"}
	dispatch := base(WorkflowViewDispatch, ChecksSectionWorkflows)
	dispatch.Problem = &WorkflowMessage{Code: "workflow.moved", Args: map[string]string{"branch": "main", "oid": "b52d0f3"}}
	dispatch.Dispatch = &WorkflowDispatchView{Path: ".github/workflows/ci.yml", Branch: "main", Branches: []string{"main"}, SourceOID: strings.Repeat("a", 40), ShortOID: "aaaaaaa",
		Inputs: []WorkflowInputField{{WorkflowInputView: WorkflowInputView{Name: "enabled", Type: "boolean"}, Value: "true", Checked: true},
			{WorkflowInputView: WorkflowInputView{Name: "choice", Type: "choice", Required: true, Options: []string{"first", "second"}}, Value: "second"}}}
	busy := base(WorkflowViewFiles, ChecksSectionWorkflows)
	busy.Problem = &WorkflowMessage{Code: "wf.error.busy"}
	return map[string]Page{
		"workflow-busy":      busy,
		"workflows":          files,
		"workflow-runs":      runs,
		"workflow-run":       run,
		"workflow-job":       jobPage,
		"workflow-dispatch":  dispatch,
		"workflow-not-found": WorkflowsPage{Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks), Nav: workflowNav(ChecksSectionRuns), View: WorkflowViewRun, NotFound: true},
		"workflow-secrets": WorkflowSecretsPage{Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks), SubmitURL: "/repositories/r1/workflow-secrets",
			RunnerTokensMayExist: true, Secrets: []WorkflowSecretRow{{Name: "TOKEN", UpdatedAt: testNow}}},
		"workflow-secret-remove": WorkflowSecretsPage{Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks), SubmitURL: "/repositories/r1/workflow-secrets",
			Confirm: "TOKEN", Secrets: []WorkflowSecretRow{{Name: "TOKEN", UpdatedAt: testNow}, {Name: "OTHER", UpdatedAt: testNow}}},
	}
}

func TestWorkflowMessageText(t *testing.T) {
	for _, row := range []struct {
		name   string
		m      WorkflowMessage
		en, ko string
	}{
		{"text without placeholders", WorkflowMessage{Code: "note.cache", Detail: "backend"}, "Not run: OwnGit keeps no cache, so later steps start without restored files.", "실행하지 않음: OwnGit은 캐시를 보관하지 않으므로 다음 단계는 복원된 파일 없이 시작합니다."},
		{"args fill both languages", WorkflowMessage{Code: "workflow.action", Args: map[string]string{"action": "foo/bar@v1"}}, "OwnGit does not download actions, so foo/bar@v1 does not run.", "OwnGit은 액션을 내려받지 않으므로 foo/bar@v1은 실행되지 않습니다."},
		{"path and line come from their fields", WorkflowMessage{Code: "workflow.wrong_type", Path: "jobs.build", Line: 4, Args: map[string]string{"expected": "a string"}}, "jobs.build (line 4) must be a string.", "jobs.build(4번째 줄)에는 문자열 값을 써야 합니다."},
		{"missing args show the detail", WorkflowMessage{Code: "workflow.action", Detail: "Recorded detail."}, "Recorded detail.", "Recorded detail."},
		{"unknown code shows the detail", WorkflowMessage{Code: "workflow.plan", Detail: "Plan detail."}, "Plan detail.", "Plan detail."},
		{"never blank", WorkflowMessage{Code: "workflow.plan"}, "workflow.plan", "workflow.plan"},
		{"workflow runner minimum version", WorkflowMessage{Code: "note.runner_old"}, "OwnGit 1.1.8 or later", "OwnGit 1.1.8 이상"},
		{"hint names a shared sentence", WorkflowMessage{Code: "workflow.function", Args: map[string]string{"function": "hashFiles", "hint": "hashFiles"}}, "hashFiles() is not supported. Compute the hash in a run step", "hashFiles()은 지원하지 않습니다. run 단계에서"},
		{"{0} is literal", WorkflowMessage{Code: "workflow.shell", Args: map[string]string{"shell": "fish"}}, "a command with {0} where", "{0}을 넣은 명령"},
		{"populated lines come from args", WorkflowMessage{Code: "workflow.unknown_key", Args: map[string]string{"path": "jobs.build.extra", "line": "7"}}, "jobs.build.extra (line 7)", "jobs.build.extra(7번째 줄)"},
		{"a missing value retains detail", WorkflowMessage{Code: "workflow.moved", Args: map[string]string{"branch": "main"}, Detail: "The branch was deleted."}, "The branch was deleted.", "The branch was deleted."},
		{"absent args retain detail despite location", WorkflowMessage{Code: "workflow.unknown_key", Path: "extra", Line: 2, Detail: "Recorded detail."}, "Recorded detail.", "Recorded detail."},
		{"a zero line retains detail", WorkflowMessage{Code: "workflow.unknown_key", Args: map[string]string{"path": "extra", "line": "0"}, Detail: "Unknown location."}, "Unknown location.", "Unknown location."},
		{"an unknown line retains detail", WorkflowMessage{Code: "workflow.yaml", Args: map[string]string{"line": "unknown", "detail": "bad YAML"}, Detail: "Unknown location."}, "Unknown location.", "Unknown location."},
		{"an empty required value retains detail", WorkflowMessage{Code: "workflow.moved", Args: map[string]string{"branch": "main", "oid": ""}, Detail: "No current commit."}, "No current commit.", "No current commit."},
		{"missing values without detail retain code", WorkflowMessage{Code: "workflow.moved", Args: map[string]string{"branch": "main"}}, "workflow.moved", "workflow.moved"},
		{"push authority has no translation contract", WorkflowMessage{Code: "note.push_required", Detail: "Push this branch before running it."}, "Push this branch before running it.", "Push this branch before running it."},
		{"English hints use exact mappings", WorkflowMessage{Code: "workflow.context", Args: map[string]string{"context": "vars", "key": "step.run", "hint": "Use env in the workflow, or a secret."}}, "Use env in the workflow, or a secret.", "워크플로의 env나 시크릿을 쓰세요."},
		{"unknown hint text stays truthful", WorkflowMessage{Code: "workflow.function", Args: map[string]string{"function": "future", "hint": "New guidance {function}"}}, "New guidance {function}", "New guidance {function}"},
		{"finite limits are translated", WorkflowMessage{Code: "workflow.limit", Args: map[string]string{"what": "YAML expansion", "limit": "100000 nodes or depth 64"}}, "YAML expansion is over OwnGit's limit of 100000 nodes or depth 64.", "YAML 확장: OwnGit의 한도(노드 100000개 또는 깊이 64)를 넘었습니다."},
		{"user data is not a translation key", WorkflowMessage{Code: "workflow.action", Args: map[string]string{"action": "a string"}}, "so a string does not run", "a string은 실행되지 않습니다"},
		{"inserted braces are literal", WorkflowMessage{Code: "workflow.shell", Args: map[string]string{"shell": `<img src=x onerror="bad">&{shell}{0}`}}, `<img src=x onerror="bad">&{shell}{0}`, `<img src=x onerror="bad">&{shell}{0}`},
		{"a duplicate with an unknown line retains detail", WorkflowMessage{Code: "workflow.duplicate_key", Args: map[string]string{"key": "run", "path": "step", "a": "0", "b": "7"}, Detail: "Duplicate key."}, "Duplicate key.", "Duplicate key."},
		{"an unknown code ignores populated args", WorkflowMessage{Code: "workflow.future", Args: map[string]string{"path": "x", "line": "2"}, Detail: "Future detail."}, "Future detail.", "Future detail."},
		{"known YAML features are translated", WorkflowMessage{Code: "workflow.yaml_feature", Line: 3, Args: map[string]string{"feature": "a merge key (<<)"}}, "Line 3 uses a merge key (<<)", "3번째 줄에 OwnGit이 워크플로 파일에서 읽지 않는 병합 키(<<)"},
		{"input reasons are translated", WorkflowMessage{Code: "workflow.dispatch_input", Args: map[string]string{"input": "count", "reason": "use a finite number"}}, "use a finite number", "유한한 숫자를 쓰세요"},
		{"path reasons are translated", WorkflowMessage{Code: "note.paths_unknown", Args: map[string]string{"reason": "the base commit is unavailable"}}, "the base commit is unavailable", "기준 커밋을 사용할 수 없음"},
		{"raw YAML diagnostics are preserved", WorkflowMessage{Code: "workflow.yaml", Line: 4, Args: map[string]string{"detail": "yaml: line 4: found unexpected key {path}"}}, "yaml: line 4: found unexpected key {path}", "yaml: line 4: found unexpected key {path}"},
		{"raw versions are preserved", WorkflowMessage{Code: "note.setup", Args: map[string]string{"tool": "Go", "version": "stable {version}"}}, "Go stable {version}", "Go stable {version}"},
		{"engine name of the unset secret note", WorkflowMessage{Code: "note.secret_unset", Args: map[string]string{"name": "TOKEN"}}, "Secret TOKEN is not set for this repository", "TOKEN 시크릿이 설정되어 있지 않아"},
	} {
		t.Run(row.name, func(t *testing.T) {
			en, ko := workflowMessageText(row.m)
			if !strings.Contains(en, row.en) || !strings.Contains(ko, row.ko) {
				t.Fatalf("en=%q ko=%q", en, ko)
			}
			for _, lang := range []Lang{LangEN, LangKO} {
				html := string(biWorkflowMessage(lang, row.m))
				if !strings.Contains(html, template.HTMLEscapeString(row.en)) || !strings.Contains(html, template.HTMLEscapeString(row.ko)) {
					t.Fatalf("missing escaped text: %s", html)
				}
				if strings.Contains(html, "<img") || strings.Contains(html, `onerror="bad"`) {
					t.Fatalf("unescaped message: %s", html)
				}
			}
		})
	}
}

func TestWorkflowScreens(t *testing.T) {
	pages := workflowPages(fullChrome(LangEN))
	ko := workflowPages(fullChrome(LangKO))
	lanes := &EvidenceLanes{Conclusion: "incomplete", RevisionShortOID: "a41c9e2", Stale: true, WorkflowsTotal: 30, WorkflowsTruncated: true,
		JSON:      &EvidenceLane{Conclusion: "passed", ShortOID: "a41c9e2"},
		Workflows: []WorkflowRunRow{{URL: "/repositories/r1/workflow-runs/run0", WorkflowPath: ".github/workflows/old.yml", Conclusion: "failed", Stale: true, ShortOID: "0c1d2e3"}}}
	task := tasksPage(fullChrome(LangEN), true)
	task.Detail.Task.Evidence = lanes
	pr := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	pr.Checks.Evidence = lanes
	checkScreens(t,
		screen{name: "workflow files say what would run and what turning on saves", page: pages["workflows"],
			want: []MessageCode{"wf.verdict.noted", "wf.state.refused", "wf.turnon.first_policy", "wf.turnon.button", "wf.schedule.paused", "wf.dispatch.open", "wf.event.not_allowed"},
			markup: []string{`name="review_digest" value="d1"`, `name="admin_password"`, `aria-current="page"`, "Line 3 is not valid YAML: bad indent", "The check policy does not allow workflow_dispatch.",
				"1 would run, 0 would run with notes, 1 partly supported (their supported jobs still run), 1 not supported by OwnGit"}},
		screen{name: "a busy repository offers to try again", page: pages["workflow-busy"], want: []MessageCode{"wf.error.busy", "wf.retry"},
			markup: []string{`href="/repositories/r1/workflows"`}},
		screen{name: "a cut run list says so", page: pages["workflow-runs"], want: []MessageCode{"wf.state.partial"},
			markup: []string{"Showing the newest 50 runs.", ">2 jobs<", ">1 not supported<"}},
		screen{name: "a run offers cancel and lists jobs, refusals and secrets", page: pages["workflow-run"],
			want:     []MessageCode{"wf.run.cancel", "wf.state.running", "wf.secret.not_set", "wf.step.builtin"},
			absent:   []MessageCode{"wf.run.rerun_button"},
			markup:   []string{`action="/repositories/r1/workflow-runs/run1/cancel"`, `name="csrf"`, "deploy", "no deployment environments"},
			noMarkup: []string{"FAIL ***"}},
		screen{name: "a job shows commands, excerpts and the log link", page: pages["workflow-job"], want: []MessageCode{"wf.job.open_log"},
			markup: []string{"go test ./...", "FAIL ***", `href="/repositories/r1/workflow-runs/run1/jobs/job1/log"`, "continue-on-error"}},
		screen{name: "the run form binds the commit and warns in open access", page: pages["workflow-dispatch"],
			markup: []string{`name="expected_oid" value="` + strings.Repeat("a", 40) + `"`, "anyone who can reach it can start this workflow", "Branch main moved to b52d0f3",
				`name="input.enabled" value="true" checked`, `<option value="second" selected>`}},
		screen{name: "the run form in Korean", lang: LangKO, page: ko["workflow-dispatch"],
			markup: []string{"main 브랜치가 b52d0f3로 바뀌었습니다", "공개 접근 모드입니다"}},
		screen{name: "secrets list names only and warn about readers", page: pages["workflow-secrets"],
			want:     []MessageCode{"wf.secrets.warning", "wf.secrets.warning_open", "wf.secrets.warning_runner", "wf.secrets.remove"},
			markup:   []string{`<textarea id="wf-secret-value" name="value"`, `href="/repositories/r1/workflow-secrets?remove=TOKEN#wf-secret-remove"`},
			noMarkup: []string{`name="action" value="remove"`}},
		screen{name: "removing a secret asks first and names it", page: pages["workflow-secret-remove"],
			markup:   []string{"Remove TOKEN?", "secrets.TOKEN", `name="action" value="remove"`, `name="name" value="TOKEN"`, "Remove TOKEN<", `href="/repositories/r1/workflow-secrets?remove=OTHER#wf-secret-remove"`},
			noMarkup: []string{`name="name" value="OTHER"`}},
		screen{name: "the removal step in Korean", lang: LangKO, page: ko["workflow-secret-remove"],
			markup: []string{"TOKEN 시크릿을 삭제할까요?", "secrets.TOKEN 값이 비어 있습니다"}},
		screen{name: "task evidence keeps lanes and names stale commits", page: task,
			want:   []MessageCode{"wf.lanes.json", "wf.lanes.stale", "wf.state.incomplete"},
			markup: []string{"0c1d2e3", "Showing 1 of 30 workflows."}},
		screen{name: "pull request summary uses every lane", page: pr,
			want: []MessageCode{"wf.pr.lanes", "wf.state.incomplete", MsgRelevancePrior}},
	)
}
