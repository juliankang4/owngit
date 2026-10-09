package webui

func workflowState(status string) stateLabel {
	switch status {
	case "passed":
		return known("wf.state.passed", "check", "ok")
	case "failed":
		return known("wf.state.failed", "error", "bad")
	case "error":
		return known("wf.state.error", "warning", "bad")
	case "cancelled":
		return known("wf.state.cancelled", "stop", "warn")
	case "incomplete":
		return known("wf.state.incomplete", "info", "warn")
	case "unavailable":
		return known("wf.state.unavailable", "slash", "warn")
	case "interrupted":
		return known("wf.state.interrupted", "stop", "warn")
	case "ambiguous":
		return known("wf.state.ambiguous", "warning", "warn")
	case "skipped":
		return known("wf.state.skipped", "minus", "quiet")
	case "not_run":
		return known("wf.state.not_run", "slash", "warn")
	case "refused":
		return known("wf.state.refused", "slash", "warn")
	case "partial":
		return known("wf.state.partial", "info", "warn")
	case "running", "started", "claimed":
		return known("wf.state.running", "clock", "quiet")
	case "queued", "pending":
		return known("wf.state.queued", "clock", "quiet")
	case "waiting":
		return known("wf.state.waiting", "clock", "quiet")
	case "":
		return known(MsgCheckStateAbsent, "minus", "quiet")
	default:
		return unrecognised
	}
}

func verdictState(verdict string) stateLabel {
	switch verdict {
	case VerdictRuns:
		return known("wf.verdict.runs", "check", "quiet")
	case VerdictNoted:
		return known("wf.verdict.noted", "info", "quiet")
	case VerdictPartial:
		return known("wf.verdict.partial", "info", "warn")
	case VerdictRefused:
		return known("wf.state.refused", "slash", "warn")
	case VerdictNeverFits:
		return known("wf.verdict.never_fits", "warning", "warn")
	case VerdictUnknown:
		return known("wf.verdict.unknown", "info", "quiet")
	default:
		return unrecognised
	}
}

func switchState(s WorkflowSwitch) stateLabel {
	switch {
	case s.On():
		return known("wf.status.on", "check", "ok")
	case s.RunWorkflows:
		return known("wf.status.consent", "clock", "warn")
	default:
		return known("wf.status.off", "minus", "quiet")
	}
}

func workflowCode(code string) WorkflowMessage { return WorkflowMessage{Code: code} }

func forSecret(pendingAction, pendingName, rowName string, notices []Notice) []Notice {
	if pendingAction != ActionRemoveSecret || pendingName == "" || pendingName != rowName {
		return nil
	}
	return notices
}

func workflowsTitle(view string) MessageCode {
	switch view {
	case WorkflowViewRuns:
		return "wf.runs.title"
	case WorkflowViewRun:
		return "wf.run.title"
	case WorkflowViewJob:
		return "wf.job.title"
	case WorkflowViewDispatch:
		return "wf.dispatch.title"
	default:
		return "wf.files.title"
	}
}
