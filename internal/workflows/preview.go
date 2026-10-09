package workflows

import (
	"strings"

	"owngit/internal/actions"
	"owngit/internal/state"
)

func inputDependent(value any) bool {
	switch value := value.(type) {
	case string:
		return strings.Contains(value, "${{") && (strings.Contains(value, "inputs.") || strings.Contains(value, "inputs["))
	case []any:
		for _, child := range value {
			if inputDependent(child) {
				return true
			}
		}
	case map[string]any:
		for _, child := range value {
			if inputDependent(child) {
				return true
			}
		}
	case actions.Concurrency:
		return inputDependent([]any{value.Group, value.CancelInProgress})
	}
	return false
}

func previewContext(workflow *actions.Workflow, id, ref, oid string) (actions.PlanContext, bool) {
	context := actions.PlanContext{GitHub: actions.GitHubContext{SHA: oid, Ref: "refs/heads/" + ref, RefName: ref, RefType: "branch", Repository: id, EventName: "push"}}
	trigger, dispatch := workflow.Events[state.ActionsEventDispatch]
	if !dispatch {
		return context, false
	}
	context.GitHub.EventName = state.ActionsEventDispatch
	typed, eventInputs, err := actions.DispatchInputs(trigger.Inputs, nil)
	if err == nil {
		context.Inputs, context.GitHub.Event.Inputs = typed, eventInputs
		return context, false
	}
	context.Inputs = map[string]any{}
	for name, input := range trigger.Inputs {
		context.Inputs[name] = input.Default
	}
	return context, true
}

func jobAdmissionDependsOnInputs(job actions.JobDefinition) bool {
	return inputDependent([]any{job.Matrix, job.Name, job.If, job.TimeoutMinutes, job.ContinueOnError, job.Concurrency, job.FailFast, job.MaxParallel})
}

func unresolvedPreview(code string, dependent bool) bool {
	return dependent && (code == "workflow.wrong_type" || code == "workflow.limit" || code == "workflow.too_many_jobs")
}
