package checkrun

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"owngit/internal/actions"
	"owngit/internal/checkworkflow"
	"owngit/internal/repository"
	"owngit/internal/state"
)

type EventRequest struct {
	RepositoryID         string
	Event                string
	EventKey             string
	SourceOID            string
	PreviousOID          string
	BaseOID              string
	TriggerRef           string
	HeadRef              string
	PullRequestNumber    int64
	Action               string
	WorkflowPath         string
	Inputs               map[string]any
	ScheduledFor         *time.Time
	Actor                state.Actor
	BypassPaths          bool
	AcceptedPushSequence int64
	schedule             *state.ActionsSchedule
	admissionTime        time.Time
}

// AdmitEvent is shared by pushes, pull requests, dispatches and scheduled slots.
// The caller supplies an exact commit and, for a dispatch or slot, its event key.
func (coordinator *Coordinator) AdmitEvent(ctx context.Context, request EventRequest) (state.CheckEventAdmission, error) {
	policy, exists, err := coordinator.Store.CheckPolicy(ctx, request.RepositoryID)
	if err != nil {
		return state.CheckEventAdmission{}, err
	}
	if !exists {
		return state.CheckEventAdmission{}, state.ErrCheckPolicyMissing
	}
	if !policy.ConsentActive || policy.ConsentDigest != policy.Digest || policy.Execution.Legacy {
		return state.CheckEventAdmission{}, state.ErrCheckConsentRequired
	}
	if !contains(policy.AllowedEvents, request.Event) {
		return state.CheckEventAdmission{}, state.ErrCheckEventNotAllowed
	}
	if !policy.RunWorkflows && (request.Event == state.ActionsEventDispatch || request.Event == state.ActionsEventSchedule) {
		return state.CheckEventAdmission{}, state.ErrActionsWorkflowsOff
	}
	result, err := coordinator.admitEvent(ctx, policy, request)
	if err == nil {
		coordinator.Wake(request.RepositoryID)
	}
	return result, err
}

func (coordinator *Coordinator) admitAcceptedPushes(ctx context.Context, repositoryID string) (bool, error) {
	pushes, err := coordinator.Store.PendingAcceptedActionsPushes(ctx, repositoryID, state.MaximumAcceptedActionsPushes)
	if err != nil || len(pushes) == 0 {
		return err != nil, err
	}
	ceilings, err := coordinator.Store.CheckCeilings(ctx)
	if err != nil {
		return true, err
	}
	waiting := len(pushes) == state.MaximumAcceptedActionsPushes
	busy := map[string]bool{}
	for _, push := range pushes {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if busy[push.RepositoryID] {
			continue
		}
		policy, ready, err := coordinator.admissiblePushPolicy(ctx, push.RepositoryID, ceilings)
		if err != nil {
			return true, err
		}
		if !ready || !policy.RunWorkflows || !contains(policy.AllowedEvents, checkworkflow.EventPush) {
			if err := coordinator.Store.ConsumeAcceptedActionsPush(ctx, push.Sequence); err != nil {
				return true, err
			}
			continue
		}
		_, err = coordinator.admitEvent(ctx, policy, EventRequest{RepositoryID: push.RepositoryID, Event: checkworkflow.EventPush, SourceOID: push.NewOID, PreviousOID: push.OldOID, TriggerRef: branchName(push.Ref), AcceptedPushSequence: push.Sequence})
		switch {
		case err == nil:
		case errors.Is(err, repository.ErrPinnedRepositoryBusy):
			busy[push.RepositoryID], waiting = true, true
		case decidedPushRefusal(err):
			if err := coordinator.Store.ConsumeAcceptedActionsPush(ctx, push.Sequence); err != nil {
				return true, err
			}
		default:
			return true, err
		}
	}
	return waiting, nil
}

func pushActionsEventKey(ref, oid string) string {
	digest := sha256.Sum256([]byte(ref))
	return fmt.Sprintf("push/%x/%s", digest[:16], oid)
}

func pushEventKeys(ref, oid string) []string {
	return []string{ref + "@" + oid, pushActionsEventKey(ref, oid)}
}
func seenPushEvent(seen map[string]bool, ref, oid string) bool {
	return seen[ref+"@"+oid] || seen[pushActionsEventKey(ref, oid)]
}

func (coordinator *Coordinator) admitEvent(ctx context.Context, policy state.CheckPolicy, request EventRequest) (state.CheckEventAdmission, error) {
	if !contains(policy.AllowedEvents, request.Event) {
		return state.CheckEventAdmission{}, nil
	}
	if request.Event == state.ActionsEventDispatch {
		ref := "refs/heads/" + request.TriggerRef
		currentRef, currentOID, err := coordinator.Repositories.ResolveRef(ctx, request.RepositoryID, ref)
		if errors.Is(err, repository.ErrNotFound) {
			return state.CheckEventAdmission{}, &actions.Refusal{Message: actions.Message{Code: "workflow.moved", Detail: "The branch moved. Check the new commit and run it again."}}
		}
		if err != nil {
			return state.CheckEventAdmission{}, err
		}
		if currentRef != ref || currentOID != request.SourceOID {
			return state.CheckEventAdmission{}, &actions.Refusal{Message: actions.Message{Code: "workflow.moved", Detail: "The branch moved. Check the new commit and run it again.", Args: map[string]string{"branch": request.TriggerRef, "oid": currentOID}}}
		}
		accepted, err := coordinator.Store.AcceptedActionsScheduleSource(ctx, request.RepositoryID, ref, currentOID)
		if err != nil {
			return state.CheckEventAdmission{}, err
		}
		if !accepted {
			return state.CheckEventAdmission{}, &actions.Refusal{Message: actions.Message{Code: "note.push_required", Detail: "This branch revision has no retained accepted OwnGit push. Push the branch to OwnGit to run its workflows."}}
		}
	}
	pinned, err := coordinator.Repositories.PinRepository(ctx, request.RepositoryID, request.SourceOID, request.SourceOID)
	if err != nil {
		return state.CheckEventAdmission{}, fmt.Errorf("pin check event source: %w", err)
	}
	jsonJob, jsonErr := readJSONEvent(ctx, pinned, policy, request)
	if jsonErr != nil && !errors.Is(jsonErr, errRevisionRejected) {
		return state.CheckEventAdmission{}, jsonErr
	}
	var runs []state.ActionsRunRequest
	actionsAllowed := policy.RunWorkflows
	if request.Event == checkworkflow.EventPush {
		var push state.AcceptedActionsPush
		var accepted bool
		if request.AcceptedPushSequence != 0 {
			push, accepted, err = coordinator.Store.AcceptedActionsPushBySequence(ctx, request.RepositoryID, request.AcceptedPushSequence)
		} else {
			push, accepted, err = coordinator.Store.AcceptedActionsPush(ctx, request.RepositoryID, "refs/heads/"+request.TriggerRef, request.SourceOID)
		}
		if err != nil {
			return state.CheckEventAdmission{}, err
		}
		actionsAllowed = actionsAllowed && accepted && push.NewOID == request.SourceOID && push.Ref == "refs/heads/"+request.TriggerRef
		if accepted {
			request.AcceptedPushSequence, request.PreviousOID = push.Sequence, push.OldOID
		}
	} else if request.Event == state.ActionsEventSchedule {
		accepted, err := coordinator.Store.AcceptedActionsScheduleSource(ctx, request.RepositoryID, "refs/heads/"+request.TriggerRef, request.SourceOID)
		if err != nil {
			return state.CheckEventAdmission{}, err
		}
		actionsAllowed = actionsAllowed && accepted
	} else if request.Event == checkworkflow.EventPullRequest {
		note, noteErr := coordinator.Store.ActionsPullRequestAdmissionNote(ctx, request.RepositoryID, request.SourceOID)
		if noteErr != nil {
			return state.CheckEventAdmission{}, noteErr
		}
		actionsAllowed = actionsAllowed && note == nil
	}
	if actionsAllowed {
		runs, err = coordinator.planActionsEvent(ctx, pinned, policy, request)
		if err != nil {
			return state.CheckEventAdmission{}, err
		}
	}
	if jsonJob == nil && len(runs) == 0 && request.AcceptedPushSequence == 0 && jsonErr == nil {
		return state.CheckEventAdmission{}, nil
	}
	var refusal *state.JSONAdmissionRefusal
	if jsonErr != nil {
		refusal = &state.JSONAdmissionRefusal{Request: state.CheckJobRequest{
			RepositoryID: request.RepositoryID, Trigger: request.Event, EventKey: jsonCheckEventKey(request),
			SourceOID: request.SourceOID, TriggerRef: request.TriggerRef,
			PullRequestNumber: request.PullRequestNumber,
		}, Reason: jsonErr.Error()}
	}
	var result state.CheckEventAdmission
	if request.Event == state.ActionsEventSchedule {
		return coordinator.admitPlannedSchedule(ctx, policy, request, runs)
	}
	record := func() error {
		var err error
		result, err = coordinator.Store.AdmitCheckEventWithJSONRefusal(ctx, request.RepositoryID, state.ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, jsonJob, refusal, runs, time.Now().UTC(), request.AcceptedPushSequence)
		return err
	}
	if request.Event == state.ActionsEventDispatch {
		err = pinned.WhileRefPresent(ctx, "refs/heads/"+request.TriggerRef, request.SourceOID, record)
	} else {
		err = pinned.WhilePresent(ctx, record)
	}
	if errors.Is(err, repository.ErrPinnedRefMoved) {
		return state.CheckEventAdmission{}, &actions.Refusal{Message: actions.Message{Code: "workflow.moved", Detail: "The branch moved. Check the new commit and run it again."}}
	}
	if err != nil {
		return result, err
	}
	for _, refusal := range result.Refusals {
		coordinator.log("workflow admission refused: %s path=%s detail=%s", refusal.Code, refusal.Path, refusal.Detail)
	}
	if len(result.Runs) == 0 {
		if jsonErr != nil {
			return result, jsonErr
		}
		if result.JSONQueueFull {
			return result, state.ErrCheckQueueFull
		}
	} else if jsonErr != nil {
		coordinator.log("configured check JSON file at %s was not admitted: %v", request.SourceOID, jsonErr)
	}
	return result, nil
}

func jsonCheckEventKey(event EventRequest) string {
	if event.Event == checkworkflow.EventPush {
		return "refs/heads/" + event.TriggerRef + "@" + event.SourceOID
	}
	return event.EventKey
}

func readJSONEvent(ctx context.Context, pinned *repository.PinnedRepository, policy state.CheckPolicy, event EventRequest) (*state.CheckJobRequest, error) {
	if event.WorkflowPath != "" || event.Event != checkworkflow.EventPush && event.Event != checkworkflow.EventPullRequest {
		return nil, nil
	}
	blob, document, err := ReadPinnedWorkflow(ctx, pinned, policy.Execution.Source.MetadataLimit)
	if errors.Is(err, repository.ErrPinnedPathNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jsonEvents []string
	for _, event := range policy.AllowedEvents {
		if event == checkworkflow.EventPush || event == checkworkflow.EventPullRequest {
			jsonEvents = append(jsonEvents, event)
		}
	}
	effective, err := checkworkflow.Tighten(document, checkworkflow.OperatorPolicy{AllowedEvents: jsonEvents, MaxTimeoutMS: policy.MaxTimeoutMS, MaxOutputLimitBytes: policy.MaxOutputLimitBytes})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRevisionRejected, err)
	}
	if !effective.MatchesBranch(event.Event, event.TriggerRef) {
		return nil, nil
	}
	digest := sha256.Sum256(blob.Content)
	request := state.CheckJobRequest{RepositoryID: event.RepositoryID, Trigger: event.Event, EventKey: jsonCheckEventKey(event), SourceOID: event.SourceOID, BaseOID: event.BaseOID, TriggerRef: event.TriggerRef, PullRequestNumber: event.PullRequestNumber, WorkflowPath: checkworkflow.Path, WorkflowOID: blob.OID, WorkflowDigest: fmt.Sprintf("%x", digest[:]), TimeoutMS: document.Limits.TimeoutMS, OutputLimitBytes: document.Limits.OutputLimitBytes}
	if len(request.EventKey) > state.MaximumCheckEventKeyBytes || len(request.TriggerRef) > state.MaximumCheckTriggerRefBytes {
		return nil, fmt.Errorf("%w: JSON check event exceeds its record bound", errRevisionRejected)
	}
	for _, check := range document.Checks {
		request.Checks = append(request.Checks, state.CheckDefinition{Name: check.Name, Command: check.Command})
	}
	return &request, nil
}

func (coordinator *Coordinator) planActionsEvent(ctx context.Context, pinned *repository.PinnedRepository, policy state.CheckPolicy, event EventRequest) ([]state.ActionsRunRequest, error) {
	files, oids, err := ReadActionsWorkflows(ctx, pinned, policy.Execution.Source.MetadataLimit)
	if err != nil {
		return nil, err
	}
	filter := actions.Event{Name: event.Event, RefName: event.TriggerRef, RefType: "branch", BaseRef: event.TriggerRef, Action: event.Action, BypassPaths: event.BypassPaths}
	changedRead := false
	var runs []state.ActionsRunRequest
	for _, file := range actions.ParseFiles(files) {
		path := file.Path
		if !state.ValidActionsWorkflowPath(path) {
			note := WorkflowNameRefusal(path)
			file.Refusal = &note
			path = refusedIdentity(state.ActionsRefusedWorkflowPrefix, file.Path)
		}
		if event.WorkflowPath != "" && path != event.WorkflowPath {
			continue
		}
		run := state.ActionsRun{RepositoryID: event.RepositoryID, WorkflowPath: path, Event: event.Event, EventKey: event.EventKey, SourceOID: event.SourceOID, BaseOID: event.BaseOID, PullRequestNumber: event.PullRequestNumber, TriggerRef: event.TriggerRef, ScheduledFor: event.ScheduledFor, Actor: event.Actor, Facts: actions.RunFacts{WorkflowOID: oids[file.Path]}}
		if event.Event == checkworkflow.EventPullRequest {
			run.Facts.PullRequestAction, run.Facts.PullRequestHeadRef = event.Action, event.HeadRef
		}
		if event.Event == checkworkflow.EventPush {
			run.EventKey = pushActionsEventKey("refs/heads/"+event.TriggerRef, event.SourceOID)
		}
		if file.Refusal != nil {
			run.Outcome, run.Reason = actions.StatusRefused, file.Refusal.Detail
			run.Facts.Notes = []actions.Message{*file.Refusal}
		} else {
			workflow := file.Workflow
			trigger := workflow.Events[event.Event]
			if !changedRead && !event.BypassPaths && (len(trigger.Paths) > 0 || len(trigger.PathsIgnore) > 0) {
				filter.Changed, err = coordinator.eventChangedPaths(ctx, pinned, event)
				if err != nil {
					return nil, err
				}
				changedRead = true
			}
			decision, err := actions.MatchEvent(workflow, filter)
			if err != nil {
				return nil, err
			}
			if !decision.Matched && !decision.Unknown {
				continue
			}
			if decision.Unknown {
				run.Outcome, run.Reason = actions.StatusNotRun, decision.Note.Detail
				run.Facts.Notes = []actions.Message{*decision.Note}
			} else {
				context, err := eventPlanContext(event, workflow)
				if err != nil {
					return nil, err
				}
				planned, err := actions.Plan(workflow, context)
				if err != nil {
					run.Outcome, run.Reason = actions.StatusRefused, err.Error()
					run.Facts.Notes = []actions.Message{refusalMessage(err)}
				} else {
					planned.Facts.PullRequestAction, planned.Facts.PullRequestHeadRef = run.Facts.PullRequestAction, run.Facts.PullRequestHeadRef
					run.Facts = planned.Facts
					run.Facts.WorkflowOID = oids[file.Path]
					run.Facts.Needs = map[string][]string{}
					for _, definition := range workflow.Jobs {
						run.Facts.Needs[definition.JobKey] = append([]string(nil), definition.Needs...)
					}
					inputs, err := json.Marshal(context.Inputs)
					if err != nil {
						return nil, err
					}
					if context.Inputs == nil {
						inputs = []byte(`{}`)
					}
					run.InputsJSON = string(inputs)
					request := state.ActionsRunRequest{Run: run, Context: context, Concurrency: &workflow.Concurrency}
					for _, job := range planned.Jobs {
						checks := actionsCheckDefinitions(job.Plan)
						request.Jobs = append(request.Jobs, state.ActionsJobRequest{CheckJobRequest: state.CheckJobRequest{WorkflowOID: oids[file.Path], WorkflowDigest: workflow.Digest, Checks: checks, JobKey: job.Plan.JobKey, MatrixIndex: job.Plan.MatrixIndex, PlanDigest: job.Digest, Waiting: job.Status != actions.StatusPending, Tolerated: job.Tolerated, ConcurrencyGroup: job.Concurrency.Group, MaxParallel: min(job.Plan.Context.Strategy.MaxParallel, actions.MaxJobs)}, Plan: job.Encoded})
					}
					if len(request.Jobs) == 0 {
						request.Run.Outcome, request.Run.Reason = actions.StatusRefused, "This workflow has no supported jobs."
					}
					if len(event.TriggerRef) <= state.MaximumCheckTriggerRefBytes {
						runs = append(runs, request)
						continue
					}
					run = request.Run
				}
			}
		}
		if len(event.TriggerRef) > state.MaximumCheckTriggerRefBytes {
			run.TriggerRef = refusedIdentity(state.ActionsRefusedRefPrefix, event.TriggerRef)
			run.Outcome, run.Reason = actions.StatusRefused, "Branch name exceeds 200 bytes."
			run.Facts.Needs = nil
			run.Facts.Notes = append(run.Facts.Notes, workflowLimit(run.Reason, event.TriggerRef, "200 bytes"))
		}
		runs = append(runs, state.ActionsRunRequest{Run: run})
	}
	return runs, nil
}

func (coordinator *Coordinator) eventChangedPaths(ctx context.Context, pinned *repository.PinnedRepository, event EventRequest) (actions.ChangedPaths, error) {
	request := repository.PathChangeRequest{PreviousOID: event.PreviousOID, PullRequest: event.Event == checkworkflow.EventPullRequest}
	if request.PullRequest {
		var err error
		pinned, err = coordinator.Repositories.PinRepository(ctx, event.RepositoryID, event.BaseOID, event.SourceOID)
		if err != nil {
			return actions.ChangedPaths{Reason: "the pull request base is unavailable"}, nil
		}
	} else if request.PreviousOID == "" {
		_, oid, err := coordinator.Repositories.ResolveRef(ctx, event.RepositoryID, "")
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return actions.ChangedPaths{}, err
		}
		request.DefaultOID = oid
	}
	changed, err := pinned.ChangedPaths(ctx, request)
	return actions.ChangedPaths{Files: changed.Files, Complete: changed.Complete, Reason: changed.Reason}, err
}

func eventPlanContext(event EventRequest, workflow *actions.Workflow) (actions.PlanContext, error) {
	github := actions.GitHubContext{SHA: event.SourceOID, Ref: "refs/heads/" + event.TriggerRef, RefName: event.TriggerRef, RefType: "branch", EventName: event.Event, Repository: event.RepositoryID, Actor: event.Actor.Kind, TriggeringActor: event.Actor.Kind}
	context := actions.PlanContext{GitHub: github}
	if event.Event == checkworkflow.EventPullRequest {
		context.GitHub.Ref = fmt.Sprintf("refs/pull/%d/head", event.PullRequestNumber)
		context.GitHub.RefName = fmt.Sprintf("%d/head", event.PullRequestNumber)
		context.GitHub.HeadRef, context.GitHub.BaseRef = event.HeadRef, event.TriggerRef
		context.GitHub.Event = actions.GitHubEvent{Action: event.Action, Number: event.PullRequestNumber, PullRequest: &actions.PullRequestContext{Number: event.PullRequestNumber, Head: actions.RefContext{Ref: event.HeadRef, SHA: event.SourceOID}, Base: actions.RefContext{Ref: event.TriggerRef, SHA: event.BaseOID}}}
	}
	if event.Event == state.ActionsEventDispatch {
		typed, inputs, err := actions.DispatchInputs(workflow.Events[event.Event].Inputs, event.Inputs)
		if err != nil {
			return context, err
		}
		context.Inputs, context.GitHub.Event.Inputs = typed, inputs
	}
	return context, nil
}

func actionsCheckDefinitions(plan actions.JobPlan) []state.CheckDefinition {
	checks := make([]state.CheckDefinition, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		name, command := actions.StepDisplay(step)
		checks = append(checks, state.CheckDefinition{Name: name, Command: command})
	}
	return checks
}

func refusalMessage(err error) actions.Message {
	var refusal *actions.Refusal
	if errors.As(err, &refusal) {
		return refusal.Message
	}
	return actions.Message{Code: "workflow.plan", Detail: err.Error()}
}

func refusedIdentity(prefix, original string) string {
	return fmt.Sprintf("%s%x", prefix, sha256.Sum256([]byte(original)))
}

// WorkflowNameRefusal gives discovery and admission the same refusal for an unsupported workflow filename.
func WorkflowNameRefusal(path string) actions.Message {
	name := strings.TrimPrefix(path, ".github/workflows/")
	if utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl) && len(name) > 100 {
		return workflowLimit("Workflow filename exceeds 100 bytes.", path, "100 bytes")
	}
	return workflowLimit("Workflow filename cannot be stored as a run identity.", path, "")
}

func workflowLimit(detail, original, limit string) actions.Message {
	what := original
	if len(what) > 4096 {
		what = strings.ToValidUTF8(what[:4093], "�") + "..."
	}
	original = strconv.QuoteToASCII(original)
	if len(original) > 4096 {
		original = original[:4080] + " [cut]"
	}
	message := actions.Message{Code: "workflow.limit", Path: original, Detail: detail}
	if limit != "" {
		message.Args = map[string]string{"what": what, "limit": limit}
	}
	return message
}

func (coordinator *Coordinator) RerunActionsRun(ctx context.Context, repositoryID, originalID string) (state.ActionsRun, bool, error) {
	if run, exists, err := coordinator.Store.UnfinishedActionsRerun(ctx, repositoryID, originalID); err != nil || exists {
		return run, exists, err
	}
	original, exists, err := coordinator.Store.ActionsRun(ctx, repositoryID, originalID)
	if err != nil {
		return state.ActionsRun{}, false, err
	}
	if !exists {
		return state.ActionsRun{}, false, state.ErrInvalidActionsRun
	}
	policy, _, err := coordinator.Store.CheckPolicy(ctx, repositoryID)
	if err != nil {
		return state.ActionsRun{}, false, err
	}
	var inputs map[string]any
	if err := json.Unmarshal([]byte(original.InputsJSON), &inputs); err != nil {
		return state.ActionsRun{}, false, err
	}
	pinned, err := coordinator.Repositories.PinRepository(ctx, repositoryID, original.SourceOID, original.SourceOID)
	if err != nil {
		return state.ActionsRun{}, false, err
	}
	event := EventRequest{RepositoryID: repositoryID, Event: original.Event, EventKey: original.EventKey, SourceOID: original.SourceOID, BaseOID: original.BaseOID, TriggerRef: original.TriggerRef, PullRequestNumber: original.PullRequestNumber, WorkflowPath: original.WorkflowPath, Inputs: inputs, ScheduledFor: original.ScheduledFor, Actor: original.Actor, BypassPaths: true}
	if original.Event == checkworkflow.EventPullRequest {
		event.HeadRef, event.Action = original.Facts.PullRequestHeadRef, original.Facts.PullRequestAction
	}
	requests, err := coordinator.planActionsEvent(ctx, pinned, policy, event)
	if err != nil {
		return state.ActionsRun{}, false, err
	}
	if len(requests) != 1 {
		return state.ActionsRun{}, false, state.ErrInvalidActionsRun
	}
	requests[0].Run.EventKey, requests[0].Run.InputsJSON = original.EventKey, original.InputsJSON
	var run state.ActionsRun
	var deduped bool
	err = pinned.WhilePresent(ctx, func() error {
		var err error
		run, deduped, err = coordinator.Store.RerunActionsRun(ctx, originalID, requests[0], time.Now().UTC())
		return err
	})
	if err == nil {
		coordinator.Wake(repositoryID)
	}
	return run, deduped, err
}
