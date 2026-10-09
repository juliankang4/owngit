// Package workflows exposes read-only discovery and commit-bound manual admission.
package workflows

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"owngit/internal/actions"
	"owngit/internal/checkrun"
	"owngit/internal/repository"
	"owngit/internal/state"
)

type Service struct {
	Store        *state.Store
	Repositories *repository.Manager
}

type Trigger struct {
	Branches       []string          `json:"branches,omitempty"`
	BranchesIgnore []string          `json:"branches_ignore,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	TagsIgnore     []string          `json:"tags_ignore,omitempty"`
	Paths          []string          `json:"paths,omitempty"`
	PathsIgnore    []string          `json:"paths_ignore,omitempty"`
	Types          []string          `json:"types,omitempty"`
	Inputs         map[string]Input  `json:"inputs,omitempty"`
	Schedules      []string          `json:"schedules,omitempty"`
	Notes          []actions.Message `json:"notes,omitempty"`
}
type Input struct {
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}
type Step struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Uses    string `json:"uses,omitempty"`
}
type Job struct {
	Key     string           `json:"key"`
	Name    string           `json:"name,omitempty"`
	RunsOn  []string         `json:"runs_on,omitempty"`
	Needs   []string         `json:"needs,omitempty"`
	Steps   []Step           `json:"steps"`
	Refusal *actions.Message `json:"refusal,omitempty"`
}
type File struct {
	PreviewRequiresInputs bool                    `json:"preview_requires_inputs"`
	ExpansionKnown        bool                    `json:"expansion_known"`
	Schedules             []state.ActionsSchedule `json:"schedules"`
	Path                  string                  `json:"path"`
	OID                   string                  `json:"oid"`
	Name                  string                  `json:"name,omitempty"`
	Triggers              map[string]Trigger      `json:"triggers,omitempty"`
	Jobs                  []Job                   `json:"jobs,omitempty"`
	Notes                 []actions.Message       `json:"notes,omitempty"`
	Refusal               *actions.Message        `json:"refusal,omitempty"`
	ExpandedJobs          int                     `json:"expanded_jobs,omitempty"`
	NeverFits             bool                    `json:"never_fits"`
}
type Discovery struct {
	OK            bool     `json:"ok"`
	Ref           string   `json:"ref"`
	SourceOID     string   `json:"source_oid"`
	RunWorkflows  bool     `json:"run_workflows"`
	ConsentActive bool     `json:"consent_active"`
	AllowedEvents []string `json:"allowed_events"`
	Workflows     []File   `json:"workflows"`
}
type DispatchInput struct {
	Path        string         `json:"path"`
	Ref         string         `json:"ref,omitempty"`
	ExpectedOID string         `json:"expected_oid,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
}

type Problem struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (p *Problem) Error() string { return p.Message }

func problem(status int, code, message string) error {
	return &Problem{Status: status, Code: code, Message: message}
}

func (s Service) branch(ctx context.Context, id, ref string) (string, string, string, error) {
	snapshot, err := s.Repositories.RefSnapshotAfterWrites(ctx, id)
	if err != nil {
		return "", "", "", err
	}
	if ref == "" {
		ref = snapshot.Summary.DefaultBranch
	}
	ref = strings.TrimPrefix(ref, "refs/heads/")
	for _, branch := range snapshot.Summary.Branches {
		if branch.Name == ref && branch.Type == "commit" {
			return ref, branch.OID, "refs/heads/" + snapshot.Summary.DefaultBranch, nil
		}
	}
	return "", "", "", problem(404, "workflow.branch_not_found", "The workflow branch does not point to a commit.")
}

func (s Service) Discover(ctx context.Context, id, ref string) (Discovery, error) {
	branch, oid, defaultRef, err := s.branch(ctx, id, ref)
	if err != nil {
		return Discovery{}, err
	}
	policy, _, err := s.Store.CheckPolicy(ctx, id)
	if err != nil {
		return Discovery{}, err
	}
	pinned, err := s.Repositories.PinRepository(ctx, id, oid, oid)
	if err != nil {
		return Discovery{}, err
	}
	limit := policy.Execution.Source.MetadataLimit
	if limit <= 0 {
		limit = state.DefaultCheckSourceLimits().MetadataLimit
	}
	files, oids, err := checkrun.ReadActionsWorkflows(ctx, pinned, limit)
	if err != nil {
		return Discovery{}, err
	}
	schedules, err := s.Store.ActionsSchedules(ctx, id, defaultRef)
	if err != nil {
		return Discovery{}, err
	}
	result := Discovery{OK: true, Ref: branch, SourceOID: oid, RunWorkflows: policy.RunWorkflows, ConsentActive: policy.ConsentActive && policy.ConsentDigest == policy.Digest, AllowedEvents: policy.AllowedEvents, Workflows: []File{}}
	for _, read := range actions.ParseFiles(files) {
		file := File{Path: read.Path, OID: oids[read.Path], Refusal: read.Refusal, Schedules: []state.ActionsSchedule{}}
		for _, schedule := range schedules {
			if schedule.WorkflowPath == read.Path {
				file.Schedules = append(file.Schedules, schedule)
			}
		}
		if !state.ValidActionsWorkflowPath(read.Path) {
			refusal := checkrun.WorkflowNameRefusal(read.Path)
			file.Refusal = &refusal
		}
		if w := read.Workflow; w != nil && file.Refusal == nil {
			file.Name, file.Notes, file.Triggers = w.Name, w.Notes, map[string]Trigger{}
			for event, trigger := range w.Events {
				view := Trigger{Branches: trigger.Branches, BranchesIgnore: trigger.BranchesIgnore, Tags: trigger.Tags, TagsIgnore: trigger.TagsIgnore, Paths: trigger.Paths, PathsIgnore: trigger.PathsIgnore, Types: trigger.Types, Schedules: trigger.Schedules, Notes: trigger.RefusedSchedules}
				if len(trigger.Inputs) > 0 {
					view.Inputs = map[string]Input{}
					for name, input := range trigger.Inputs {
						view.Inputs[name] = Input{input.Description, input.Type, input.Required, input.Default, input.Options}
					}
				}
				file.Triggers[event] = view
			}
			for _, definition := range w.Jobs {
				job := Job{Key: definition.JobKey, Name: definition.Name, RunsOn: definition.RunsOn, Needs: definition.Needs, Refusal: definition.Refusal, Steps: []Step{}}
				for _, step := range definition.Steps {
					name, command := actions.StepDisplay(step)
					job.Steps = append(job.Steps, Step{name, command, step.Uses})
				}
				file.Jobs = append(file.Jobs, job)
			}
			context, requiredInputs := previewContext(w, id, branch, oid)
			rootDependent := inputDependent([]any{w.RunName, w.Concurrency})
			dependentJobs := map[string]bool{}
			file.PreviewRequiresInputs = requiredInputs || rootDependent
			file.ExpansionKnown = !rootDependent
			for _, definition := range w.Jobs {
				dependentJobs[definition.JobKey] = jobAdmissionDependsOnInputs(definition)
				if dependentJobs[definition.JobKey] {
					file.PreviewRequiresInputs = true
					file.ExpansionKnown = false
				}
			}
			planned, err := actions.Plan(w, context)
			if err != nil {
				var refusal *actions.Refusal
				if errors.As(err, &refusal) {
					if !unresolvedPreview(refusal.Code, !file.ExpansionKnown) {
						file.Refusal = &refusal.Message
					}
				} else {
					return Discovery{}, err
				}
			} else {
				if file.ExpansionKnown {
					file.ExpandedJobs = len(planned.Jobs)
				}
				file.Notes = planned.Facts.Notes
				for _, refused := range planned.Facts.RefusedJobs {
					for i := range file.Jobs {
						if file.Jobs[i].Key == refused.JobKey && !unresolvedPreview(refused.Reason.Code, dependentJobs[refused.JobKey]) {
							note := refused.Reason
							file.Jobs[i].Refusal = &note
						}
					}
				}
				if file.ExpansionKnown && policy.QueueLimit > 0 && file.ExpandedJobs > policy.QueueLimit {
					file.NeverFits = true
					file.Notes = append(file.Notes, actions.Message{Code: "workflow.never_fits", Detail: fmt.Sprintf("This workflow needs %d jobs at once, but the check policy's queue holds at most %d. Raise queue_limit, or make the matrix smaller.", file.ExpandedJobs, policy.QueueLimit), Args: map[string]string{"count": fmt.Sprint(file.ExpandedJobs), "limit": fmt.Sprint(policy.QueueLimit)}})
				}
			}
		}
		result.Workflows = append(result.Workflows, file)
	}
	return result, nil
}

func (s Service) Dispatch(ctx context.Context, id string, input DispatchInput, actor state.Actor) (state.ActionsRun, error) {
	if !state.ValidActionsWorkflowPath(input.Path) {
		return state.ActionsRun{}, problem(422, "workflow.invalid", "Choose a top-level workflow file.")
	}
	policy, found, err := s.Store.CheckPolicy(ctx, id)
	if err != nil {
		return state.ActionsRun{}, err
	}
	if !found || !policy.RunWorkflows || !policy.ConsentActive || policy.ConsentDigest != policy.Digest || policy.Execution.Legacy {
		return state.ActionsRun{}, problem(409, "workflow.off", "Workflows are off or need current administrator consent.")
	}
	allowed := false
	for _, event := range policy.AllowedEvents {
		allowed = allowed || event == state.ActionsEventDispatch
	}
	if !allowed {
		return state.ActionsRun{}, problem(409, "workflow.event_off", "The check policy does not allow workflow_dispatch.")
	}
	branch, oid, _, err := s.branch(ctx, id, input.Ref)
	if err != nil {
		return state.ActionsRun{}, err
	}
	if input.ExpectedOID != "" && input.ExpectedOID != oid {
		return state.ActionsRun{}, &Problem{Status: 409, Code: "workflow.moved", Message: "The branch moved. Check the new commit and run it again.", Details: map[string]string{"source_oid": oid, "ref": branch}}
	}
	pinned, err := s.Repositories.PinRepository(ctx, id, oid, oid)
	if err != nil {
		return state.ActionsRun{}, err
	}
	files, _, err := checkrun.ReadActionsWorkflows(ctx, pinned, policy.Execution.Source.MetadataLimit)
	if err != nil {
		return state.ActionsRun{}, err
	}
	var workflow *actions.Workflow
	for _, file := range actions.ParseFiles(files) {
		if file.Path != input.Path {
			continue
		}
		if file.Refusal != nil {
			return state.ActionsRun{}, problem(409, file.Refusal.Code, file.Refusal.Detail)
		}
		workflow = file.Workflow
	}
	if workflow == nil {
		return state.ActionsRun{}, problem(404, "workflow.not_found", "The workflow file does not exist at this branch.")
	}
	trigger, dispatch := workflow.Events[state.ActionsEventDispatch]
	if !dispatch {
		return state.ActionsRun{}, problem(409, "workflow.event_off", "This workflow has no workflow_dispatch trigger.")
	}
	typed, eventInputs, err := actions.DispatchInputs(trigger.Inputs, input.Inputs)
	if err != nil {
		return state.ActionsRun{}, err
	}
	planned, err := actions.Plan(workflow, actions.PlanContext{Inputs: typed, GitHub: actions.GitHubContext{SHA: oid, Ref: "refs/heads/" + branch, RefName: branch, RefType: "branch", EventName: state.ActionsEventDispatch, Repository: id, Actor: actor.Kind, TriggeringActor: actor.Kind, Event: actions.GitHubEvent{Inputs: eventInputs}}})
	if err != nil {
		return state.ActionsRun{}, err
	}
	if len(planned.Jobs) > policy.QueueLimit {
		return state.ActionsRun{}, problem(409, "workflow.never_fits", "This workflow needs more jobs than queue_limit. Raise queue_limit, or make the matrix smaller.")
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return state.ActionsRun{}, err
	}
	coordinator := checkrun.Coordinator{Store: s.Store, Repositories: s.Repositories}
	result, err := coordinator.AdmitEvent(ctx, checkrun.EventRequest{RepositoryID: id, Event: state.ActionsEventDispatch, EventKey: "dispatch/" + hex.EncodeToString(key[:]), SourceOID: oid, TriggerRef: branch, WorkflowPath: input.Path, Inputs: input.Inputs, Actor: actor})
	if err != nil {
		return state.ActionsRun{}, err
	}
	if len(result.Runs) != 1 {
		return state.ActionsRun{}, problem(409, "workflow.invalid", "The workflow could not be admitted.")
	}
	return result.Runs[0], nil
}
