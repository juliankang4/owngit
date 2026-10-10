package server

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
	"owngit/internal/checkapi"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

func TestWorkflowResponseBounds(t *testing.T) {
	name, command := actions.StepDisplay(actions.Step{Name: strings.Repeat("\x01", state.MaximumCheckNameBytes), Run: strings.Repeat("\x01", 512)})
	now := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	exit := math.MaxInt
	record := workflowJobView{Job: &workflowJobMetadata{ID: strings.Repeat("a", 32), RepositoryID: strings.Repeat("r", 100), TaskID: strings.Repeat("b", 32), RunID: strings.Repeat("c", 32), JobKey: strings.Repeat("j", 100), MatrixIndex: 15, SourceOID: strings.Repeat("d", 64), Executor: state.CheckExecutorExternalRunner, PolicyVersion: math.MaxInt64, ConsentVersion: math.MaxInt64, Tolerated: true, Status: state.CheckJobError, Summary: strings.Repeat("\x01", 500), Protection: state.ProtectionRunnerReported, CancelRequested: true, AdmittedAt: now, StartedAt: &now, FinishedAt: &now}, Attempt: &checkapi.Attempt{ID: strings.Repeat("e", 32), TaskID: strings.Repeat("b", 32), RepositoryID: strings.Repeat("r", 100), RevisionOID: strings.Repeat("d", 64), JobID: strings.Repeat("f", 32), CredentialID: strings.Repeat("a", 32), CycleID: strings.Repeat("b", 32), WorktreeState: state.WorktreeUnknown, ConfigurationVersion: math.MaxInt64, Status: state.AttemptError, ExitCode: &exit, StartedAt: now, FinishedAt: now, DurationMS: math.MaxInt64, Summary: strings.Repeat("\x01", 500), Sequence: math.MaxInt64, Protection: state.ProtectionRunnerReported, ExecutionScope: state.ExecutionScopeExternalRunner, TimeoutMS: math.MaxInt64, OutputLimitBytes: math.MaxInt64, LogID: strings.Repeat("a", 32), LogExpiresAt: &now, LogTruncated: true, LogError: strings.Repeat("\x01", 200), CleanupFailed: true}, ExcerptsIncluded: true}
	for range state.MaximumCheckDefinitions {
		record.Job.Checks = append(record.Job.Checks, checkapi.CheckDefinition{Name: name, Command: command})
		record.Attempt.Results = append(record.Attempt.Results, checkapi.Result{Name: name, Command: command, Status: state.AttemptError, Role: actions.RoleRun, ExitCode: &exit, DurationMS: math.MaxInt64, OutputExcerpt: strings.Repeat("\x01", state.MaximumCheckExcerptBytes), CleanupError: strings.Repeat("\x01", state.MaximumCleanupErrorBytes), Truncated: true, OutputLimitExceededBytes: math.MaxInt64})
	}
	facts := actions.RunFacts{WorkflowName: strings.Repeat("n", 100), WorkflowOID: strings.Repeat("a", 64), WorkflowDigest: strings.Repeat("b", 64)}
	secrets := []workflowSecretStatus{}
	for i := range state.MaxWorkflowSecrets {
		secret := fmt.Sprintf("%s%03d", strings.Repeat("S", 597), i)
		facts.SecretNames = append(facts.SecretNames, secret)
		secrets = append(secrets, workflowSecretStatus{Name: secret, Set: true})
	}
	facts.Notes = []actions.Message{{Code: "workflow.invalid", Path: strings.Repeat("p", 4096), Detail: "x", Args: map[string]string{"what": "synthetic workflow", "limit": "64 KiB"}}}
	encodedFacts, err := json.Marshal(facts)
	noErr(t, err)
	facts.Notes[0].Detail = strings.Repeat("\u061c", (state.MaximumActionsJSONBytes-len(encodedFacts))/2)
	encodedFacts, err = json.Marshal(facts)
	noErr(t, err)
	if len(encodedFacts) > state.MaximumActionsJSONBytes || len(encodedFacts) < state.MaximumActionsJSONBytes-2 || len(facts.Notes[0].Args) == 0 {
		t.Fatalf("facts with args did not fill their durable note budget: %d", len(encodedFacts))
	}
	inputs := map[string]string{}
	for i := range 25 {
		inputs[fmt.Sprintf("%s%02d", strings.Repeat("I", 98), i)] = strings.Repeat("\x01", 410)
	}
	encodedInputs, err := json.Marshal(inputs)
	noErr(t, err)
	last := fmt.Sprintf("%s%02d", strings.Repeat("I", 98), 24)
	inputs[last] += strings.Repeat("\x01", (state.MaximumActionsJSONBytes-len(encodedInputs))/6)
	encodedInputs, err = json.Marshal(inputs)
	noErr(t, err)
	if len(encodedInputs) > state.MaximumActionsJSONBytes || len(inputs[last]) > 1024 {
		t.Fatal("inputs exceeded their durable or value budget")
	}
	run := workflowRunResponse{OK: true, Run: state.ActionsRunEvidence{ActionsRun: state.ActionsRun{ID: strings.Repeat("c", 32), RepositoryID: strings.Repeat("r", 100), WorkflowPath: ".github/workflows/" + strings.Repeat("w", 96) + ".yml", Number: math.MaxInt64, Event: state.ActionsEventDispatch, EventKey: strings.Repeat("k", 200), SourceOID: strings.Repeat("d", 64), TriggerRef: strings.Repeat("b", 200), InputsJSON: string(encodedInputs), Reason: strings.Repeat("\x01", state.MaximumActionsJSONBytes), ConcurrencyGroup: strings.Repeat("g", 200), ConcurrencyQueue: "max", CancelRequestedAt: &now, Facts: facts, PolicyVersion: math.MaxInt64, ConsentVersion: math.MaxInt64, Actor: state.Actor{Kind: state.ActorAccess}, CreatedAt: now}, Conclusion: actions.StatusIncomplete}, Secrets: secrets, SecretsKnown: true, Deduplicated: true}
	unbounded := run
	for range state.MaximumActionsRunJobs {
		run.Jobs = append(run.Jobs, workflowJobSummary(record))
		unbounded.Jobs = append(unbounded.Jobs, record)
	}
	noteKeys := make([]string, state.MaximumActionsRunSummaryNoteKeys)
	for i := range noteKeys {
		noteKeys[i] = fmt.Sprintf("note.%s%03d", strings.Repeat("n", state.MaximumActionsRunSummaryNoteKeyBytes-len("note.")-3), i)
	}
	summary := state.ActionsRunSummary{
		ID: strings.Repeat("a", 32), WorkflowPath: ".github/workflows/" + strings.Repeat("<", 96) + ".yml", Event: state.ActionsEventDispatch,
		Status: "completed", Conclusion: actions.StatusIncomplete, CreatedAt: now, StartedAt: &now, FinishedAt: &now,
		Counts:   state.ActionsRunSummaryCounts{Total: 16, Waiting: 16, Queued: 16, Running: 16, Passed: 16, Failed: 16, Cancelled: 16, Skipped: 16, Incomplete: 16, Refused: 16, Notes: math.MaxInt},
		NoteKeys: noteKeys, NotesTruncated: true, Stale: true,
	}
	workflowSummaries := make([]state.ActionsRunSummary, state.MaximumRevisionWorkflowSummaries)
	for i := range workflowSummaries {
		workflowSummaries[i] = summary
	}
	revisionSummary := state.RevisionCheckEvidence{RevisionOID: strings.Repeat("d", 64), Conclusion: actions.StatusIncomplete, JSONRevisionOID: strings.Repeat("e", 64), JSONConclusion: actions.StatusIncomplete, JSONStale: true, Workflows: workflowSummaries, WorkflowsTotal: math.MaxInt, WorkflowsTruncated: true}
	tasks := make([]*checkapi.Task, maximumTaskPageSize)
	for i := range tasks {
		tasks[i] = &checkapi.Task{Evidence: &revisionSummary, ID: strings.Repeat("a", 32), RepositoryID: strings.Repeat("r", 100), Title: strings.Repeat("<", 200), Status: state.TaskExhausted, CorrectionCyclesUsed: math.MaxInt, CorrectionCyclesRemaining: math.MaxInt, CorrectionCycleLimit: math.MaxInt, InitialCheckDone: true, CreatedAt: now, UpdatedAt: now, LastRegisteredSequence: math.MaxInt64, LastAppliedSequence: math.MaxInt64, PendingAttemptID: strings.Repeat("b", 32), LastAppliedAttemptID: strings.Repeat("c", 32)}
	}
	runList := struct {
		OK        bool                      `json:"ok"`
		Runs      []state.ActionsRunSummary `json:"runs"`
		Truncated bool                      `json:"truncated"`
	}{OK: true, Runs: make([]state.ActionsRunSummary, 999), Truncated: true}
	for i := range runList.Runs {
		runList.Runs[i] = summary
	}
	taskList := checkapi.TaskListResponse{OK: true, Tasks: tasks, Next: strings.Repeat("9", 100)}
	pullRequestSummary := pullrequest.Checks{Evidence: &revisionSummary, Status: actions.StatusIncomplete, Configured: true, Advisory: true, RevisionOID: strings.Repeat("d", 64), WorktreeState: state.WorktreeUnknown, Stale: true, ConfigurationVersion: math.MaxInt64, AttemptID: strings.Repeat("a", 32), FinishedAt: &now, Summary: strings.Repeat("\u061c", 166), LogStatus: state.CheckLogUnavailable, LogExpiresAt: &now, LogTruncated: true, Protection: state.ProtectionRunnerReported, ExecutionScope: state.ExecutionScopeExternalRunner, CredentialID: strings.Repeat("b", 32), TaskID: strings.Repeat("c", 32), RegisteredAt: &now, LogError: strings.Repeat("\u061c", 66), CleanupFailed: true}
	runBound := 6*state.MaximumCheckNameBytes*state.MaximumActionsRunJobs*state.MaximumCheckDefinitions + 256*state.MaximumActionsRunJobs*state.MaximumCheckDefinitions + 16384*state.MaximumActionsRunJobs + 12*state.MaximumActionsJSONBytes + state.MaximumActionsJSONBytes + 64*state.MaxWorkflowSecrets + 8192
	jobBound := 6*(state.MaximumCheckNameBytes+512+state.MaximumCheckExcerptBytes+state.MaximumCleanupErrorBytes)*state.MaximumCheckDefinitions + 6*(state.MaximumCheckNameBytes+512)*state.MaximumCheckDefinitions + (256+64+len(checkapi.OutputLimitExceededFact)+24)*state.MaximumCheckDefinitions + 16384 + 8192
	workflowPathBound := len(".github/workflows/") + 6*(100-len(".yml")) + len(".yml")
	summaryBound := workflowPathBound + 32 + 20 + 2*len(actions.StatusIncomplete) + state.MaximumActionsRunSummaryNoteKeys*state.MaximumActionsRunSummaryNoteKeyBytes + 640
	runListBound := 999*summaryBound + 8192
	revisionSummaryBound := state.MaximumRevisionWorkflowSummaries*summaryBound + 128
	taskListBound := maximumTaskPageSize*(revisionSummaryBound+1024+6*200) + 8192
	pullRequestBound := revisionSummaryBound + 16384
	if taskListBound > maximumAPIResponse*9/10 {
		t.Fatalf("maximal task list bound=%d exceeds the response cap with 10%% margin", taskListBound)
	}
	for _, row := range []struct {
		name          string
		body          any
		bound, status int
	}{
		{"maximal run summaries", run, runBound, 200},
		{"maximal job excerpts", workflowJobResponse{OK: true, RunID: record.Job.RunID, workflowJobView: record}, jobBound, 200},
		{"maximal run list", runList, runListBound, 200},
		{"maximal task list summaries", taskList, taskListBound, 200},
		{"maximal pull request summary", pullRequestSummary, pullRequestBound, 200},
		{"aggregate excerpts do not fit the unchanged limit", unbounded, 0, 507},
	} {
		t.Run(row.name, func(t *testing.T) {
			status, encoded := encodeAPIJSON(200, row.body)
			if status != row.status || row.bound > 0 && (len(encoded) > row.bound || row.bound >= maximumAPIResponse) {
				t.Fatalf("status=%d bytes=%d bound=%d cap=%d", status, len(encoded), row.bound, maximumAPIResponse)
			}
			t.Logf("encoded bytes=%d conservative bound=%d cap=%d", len(encoded), row.bound, maximumAPIResponse)
			if row.status == 200 && !json.Valid(encoded) {
				t.Fatal("maximal response is not valid JSON")
			}
		})
	}
	jobSummary := workflowJobSummary(record)
	if len(jobSummary.Steps) != state.MaximumCheckDefinitions || !jobSummary.Steps[0].CleanupFailed || jobSummary.Attempt.Results != nil || jobSummary.Job.Checks != nil || jobSummary.ExcerptsIncluded {
		t.Fatal("run projection duplicated details or lost cleanup evidence")
	}
	if len(record.Attempt.Results[0].OutputExcerpt) != state.MaximumCheckExcerptBytes || len(record.Job.Checks) != state.MaximumCheckDefinitions {
		t.Fatal("summary projection mutated the full job record")
	}
}
