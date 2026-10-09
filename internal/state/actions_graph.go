package state

import (
	"errors"
	"slices"
	"strings"

	"owngit/internal/actions"
)

func validRefusedActionsIdentity(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validDigest(strings.TrimPrefix(value, prefix))
}

func validActionsRunWorkflowPath(run ActionsRun) bool {
	return validActionsWorkflowPath(run.WorkflowPath) || run.Outcome == actions.StatusRefused && validRefusedActionsIdentity(run.WorkflowPath, ActionsRefusedWorkflowPrefix)
}

func prepareActionsGraph(request *ActionsRunRequest) error {
	if request.Run.Facts.Needs != nil {
		graph := make(map[string][]string, len(request.Run.Facts.Needs))
		for key, needs := range request.Run.Facts.Needs {
			graph[key] = slices.Clone(needs)
		}
		request.Run.Facts.Needs = graph
	}
	if request.Run.Facts.Needs == nil {
		request.Run.Facts.Needs = map[string][]string{}
		for _, refused := range request.Run.Facts.RefusedJobs {
			request.Run.Facts.Needs[refused.JobKey] = nil
		}
	}
	flags := make(map[string]bool, len(request.Run.Facts.FailFast))
	for key, value := range request.Run.Facts.FailFast {
		flags[key] = value
	}
	request.Run.Facts.FailFast = flags
	for _, job := range request.Jobs {
		plan, err := actions.DecodePlan(job.Plan, job.PlanDigest)
		if err != nil {
			return err
		}
		if needs, exists := request.Run.Facts.Needs[job.JobKey]; exists && !slices.Equal(needs, plan.Needs) {
			return errors.New("workflow graph differs from its job plan")
		}
		request.Run.Facts.Needs[job.JobKey] = slices.Clone(plan.Needs)
		if flag, exists := flags[job.JobKey]; exists && flag != plan.Context.Strategy.FailFast {
			return errors.New("workflow fail-fast differs from its matrix plans")
		}
		flags[job.JobKey] = plan.Context.Strategy.FailFast
	}
	jobs := make([]CheckJob, 0, len(request.Jobs))
	for _, job := range request.Jobs {
		jobs = append(jobs, CheckJob{JobKey: job.JobKey, MatrixIndex: job.MatrixIndex})
	}
	return validateActionsGraph(request.Run, jobs)
}

func validateActionsGraph(run ActionsRun, jobs []CheckJob) error {
	keys, identities := map[string]bool{}, map[struct {
		key   string
		index int
	}]bool{}
	for _, job := range jobs {
		keys[job.JobKey] = true
		identity := struct {
			key   string
			index int
		}{job.JobKey, job.MatrixIndex}
		if identities[identity] {
			return errors.New("duplicate workflow graph job")
		}
		identities[identity] = true
	}
	supportedKeys := len(keys)
	if len(run.Facts.FailFast) != supportedKeys {
		return errors.New("workflow fail-fast does not describe every supported job")
	}
	for key := range run.Facts.FailFast {
		if !keys[key] {
			return errors.New("workflow fail-fast names an unknown job")
		}
	}
	for _, refused := range run.Facts.RefusedJobs {
		if !validText(refused.JobKey, 100) || refused.MatrixIndex < 0 || refused.MatrixIndex >= MaximumActionsRunJobs {
			return errors.New("invalid refused workflow graph job")
		}
		keys[refused.JobKey] = true
		identity := struct {
			key   string
			index int
		}{refused.JobKey, refused.MatrixIndex}
		if identities[identity] {
			return errors.New("duplicate workflow graph job")
		}
		identities[identity] = true
	}
	if len(identities) > MaximumActionsRunJobs || len(run.Facts.Needs) > MaximumActionsRunJobs {
		return errors.New("workflow graph exceeds the job bound")
	}
	if run.Outcome == "" && len(run.Facts.Needs) != len(keys) {
		return errors.New("workflow graph does not describe every job")
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(key string) bool {
		if visiting[key] || !keys[key] {
			return false
		}
		if visited[key] {
			return true
		}
		visiting[key] = true
		seen := map[string]bool{}
		if len(run.Facts.Needs[key]) > MaximumActionsRunJobs {
			return false
		}
		for _, target := range run.Facts.Needs[key] {
			if seen[target] || !visit(target) {
				return false
			}
			seen[target] = true
		}
		visiting[key], visited[key] = false, true
		return true
	}
	for key := range run.Facts.Needs {
		if !visit(key) {
			return errors.New("workflow graph has an unknown job, duplicate edge or cycle")
		}
	}
	return nil
}
