package webui

import (
	"html/template"
	"strconv"
)

// Presentation helpers for the configured-check screens.
//
// The mapping from a recorded value to a word lives here rather than in a
// template, so the markup names a value and this file decides what it means.
// Every unrecognised value resolves to the shared unrecognised label, never to
// a pass and never to a blank.

// jobState names one recorded job state.
//
// Waiting, claimed, and started are quiet: they report what the record says
// without implying this package can see a live process. Ambiguous and
// interrupted are warnings, because neither is a result.
func jobState(status string) stateLabel {
	switch status {
	case JobPassed:
		return known(MsgCCJobStatePassed, "check", "ok")
	case JobFailed:
		return known(MsgCCJobStateFailed, "error", "bad")
	case JobError:
		return known(MsgCCJobStateError, "warning", "bad")
	case JobCancelled:
		return known(MsgCCJobStateCancelled, "stop", "warn")
	case JobIncomplete:
		return known(MsgCCJobStateIncomplete, "info", "warn")
	case JobUnavailable:
		return known(MsgCCJobStateUnavailable, "slash", "warn")
	case JobAmbiguous:
		return known(MsgCCJobStateAmbiguous, "warning", "warn")
	case JobInterrupted:
		return known(MsgCCJobStateInterrupted, "stop", "warn")
	case JobPending:
		return known(MsgCCJobStatePending, "clock", "quiet")
	case JobClaimed:
		return known(MsgCCJobStateClaimed, "clock", "quiet")
	case JobStarted:
		return known(MsgCCJobStateStarted, "clock", "quiet")
	default:
		return unrecognised
	}
}

// consentState names whether execution is on.
//
// It is deliberately not a pass or a failure: it is a permission, and its two
// values are "enabled" and "not enabled" rather than good and bad.
func consentState(policy CheckPolicyView) stateLabel {
	if policy.ConsentActive {
		return known(MsgCCConsentOn, "check", "ok")
	}
	return known(MsgCCConsentOff, "minus", "quiet")
}

// policyState names whether a usable policy is stored. A legacy policy is
// stored history that cannot be enabled, which is its own state.
func policyState(policy CheckPolicyView) stateLabel {
	switch {
	case !policy.Saved:
		return known(MsgCCPolicyNone, "minus", "quiet")
	case policy.Legacy:
		return known(MsgCCPolicyLegacy, "warning", "warn")
	default:
		return known(MsgCCPolicySaved, "check", "quiet")
	}
}

// runtimeState names what the backend observed about the environment.
func runtimeState(runtime CheckRuntimeView) stateLabel {
	if runtime.Available {
		return known(MsgCCRuntimeOK, "check", "ok")
	}
	return known(MsgCCRuntimeDown, "slash", "warn")
}

// runtimeReason explains a closed runtime. An unrecognised code says so and
// the code itself is rendered as data beside it, so a new backend condition is
// never swallowed.
func runtimeReason(code string) MessageCode {
	switch code {
	case RuntimeWorkspaceUnavailable:
		return MsgCCRuntimeWork
	case RuntimeRestartUnavailable:
		return MsgCCRuntimeRestart
	default:
		return MsgCCRuntimeOther
	}
}

// executorName names one execution mode.
func executorName(executor string) MessageCode {
	switch executor {
	case ExecutorHost:
		return MsgCCExecHost
	case ExecutorContainer:
		return MsgCCExecCont
	case ExecutorExternalRunner:
		return MsgCCExecRunner
	default:
		return MsgEvidenceUnknownState
	}
}

// triggerName names the event that admitted a job.
func triggerName(trigger string) MessageCode {
	switch trigger {
	case CheckEventPush:
		return MsgCCTriggerPush
	case CheckEventPullRequest:
		return MsgCCTriggerPR
	default:
		return MsgEvidenceUnknownState
	}
}

// networkName names a container network selection.
func networkName(network string) MessageCode {
	switch network {
	case ContainerNetworkNone:
		return MsgCCNetworkNone
	case ContainerNetworkBridge:
		return MsgCCNetworkBridge
	default:
		return MsgEvidenceUnknownState
	}
}

// Action constants exposed to the templates, so the markup and the handler
// compare the same strings.
func savePolicyAction() string { return ActionSaveCheckPolicy }

// fieldRange renders the accepted range of one numeric field.
//
// The numbers are the backend's, passed through the view adapter. A field
// whose floor is another setting says so and still states its maximum, because
// a moving floor is no reason to leave the ceiling enforced but unstated. Only
// a field the backend publishes nothing for renders nothing.
func fieldRange(lang Lang, ranges map[string]FieldRange, field string) template.HTML {
	bounds, present := ranges[field]
	if !present || !bounds.Known {
		return ""
	}
	high := strconv.FormatInt(bounds.Max, 10)
	if bounds.MinLabel != "" {
		return biText(lang,
			"Accepted range: at least \""+Text(LangEN, bounds.MinLabel)+"\", up to "+high,
			"\ud5c8\uc6a9 \ubc94\uc704: \""+Text(LangKO, bounds.MinLabel)+"\" \uc774\uc0c1, \ucd5c\ub300 "+high)
	}
	low := strconv.FormatInt(bounds.Min, 10)
	return biText(lang,
		"Accepted range: "+low+" to "+high,
		"\ud5c8\uc6a9 \ubc94\uc704: "+low+" ~ "+high)
}
func enableChecksAction() string { return ActionEnableChecks }
func disableChecksAction() string {
	return ActionDisableChecks
}
func cancelJobAction() string    { return ActionCancelCheckJob }
func rerunJobAction() string     { return ActionRerunCheckJob }
func issueRunnerAction() string  { return ActionIssueRunnerToken }
func revokeRunnerAction() string { return ActionRevokeRunnerToken }
func hostExecutor() string       { return ExecutorHost }
func containerExecutor() string  { return ExecutorContainer }
func runnerExecutor() string     { return ExecutorExternalRunner }
func noneNetwork() string        { return ContainerNetworkNone }
func bridgeNetwork() string      { return ContainerNetworkBridge }
func pushEvent() string          { return CheckEventPush }
func pullRequestEvent() string   { return CheckEventPullRequest }

// forRunnerCredential keeps a refused revocation on the row it was typed
// into. Every row submits the same action, so the action alone cannot say
// which one failed.
func forRunnerCredential(pendingAction, pendingID, rowID string, notices []Notice) []Notice {
	if pendingAction != ActionRevokeRunnerToken || pendingID == "" || pendingID != rowID {
		return nil
	}
	return notices
}
