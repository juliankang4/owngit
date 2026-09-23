package webui

import (
	"html/template"
	"strconv"
	"strings"
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

// fieldRange renders the accepted range of one numeric field in the units a
// person reads, such as "1 second to 24 hours".
//
// The numbers are the backend's, passed through the view adapter; only their
// spelling changes here. A field whose floor is another setting says so and
// still states its maximum, because a moving floor is no reason to leave the
// ceiling enforced but unstated. Only a field the backend publishes nothing
// for renders nothing.
func fieldRange(lang Lang, ranges map[string]FieldRange, field string) template.HTML {
	bounds, present := ranges[field]
	if !present || !bounds.Known {
		return ""
	}
	highEN, highKO := LimitText(LangEN, field, bounds.Max), LimitText(LangKO, field, bounds.Max)
	if bounds.MinLabel != "" {
		return biText(lang,
			"Allowed: at least \""+Text(LangEN, bounds.MinLabel)+"\", up to "+highEN,
			"\ud5c8\uc6a9 \ubc94\uc704: \""+Text(LangKO, bounds.MinLabel)+"\" \uc774\uc0c1, \ucd5c\ub300 "+highKO)
	}
	return biText(lang,
		"Allowed: "+LimitText(LangEN, field, bounds.Min)+" to "+highEN,
		"\ud5c8\uc6a9 \ubc94\uc704: "+LimitText(LangKO, field, bounds.Min)+" ~ "+highKO)
}

// LimitText writes a stored value of one policy field the way the screen
// states it in running text, such as "10 minutes" or "64 MB". A field this
// package has no control for is written as a plain number.
func LimitText(lang Lang, field string, value int64) string {
	kind := LimitCount
	if limit, known := PolicyLimitFor(field); known {
		kind = limit.Kind
	}
	return humanLimit(lang, kind, value)
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

// CheckFileExample is the minimal check file the settings screen offers to
// copy. A test parses it with the real workflow parser, so the page never
// offers a file OwnGit would refuse.
const CheckFileExample = `{
  "version": 1,
  "events": {
    "push": {},
    "pull_request": {}
  },
  "checks": [
    {"name": "test", "command": "make test"}
  ]
}
`

func checkFileExample() string { return CheckFileExample }

// checksRunning answers the first status question: will a matching push or
// pull request actually run checks now?
//
// Permission on its own is not an answer. A permitted policy whose runtime is
// closed runs nothing, so it says "on, but paused" rather than "on".
func checksRunning(p ConfiguredChecksPage) stateLabel {
	switch {
	case !p.Policy.Saved || !p.Policy.ConsentActive:
		return known(MsgCCConsentOff, "minus", "quiet")
	case !p.Runtime.Available:
		return known(MsgCCConsentPaused, "warning", "warn")
	default:
		return known(MsgCCConsentOn, "check", "ok")
	}
}

// checkFileState names what the default branch holds at the check file path.
// A lookup that failed says so and is never reported as a missing file.
func checkFileState(file CheckFileView) stateLabel {
	switch file.State {
	case CheckFileFound:
		return known(MsgCCFileFound, "check", "ok")
	case CheckFileMissing:
		return known(MsgCCFileMissing, "minus", "quiet")
	case CheckFileNoCommits:
		return known(MsgCCFileNoCommits, "minus", "quiet")
	case CheckFileInvalid:
		return known(MsgCCFileInvalid, "warning", "warn")
	case CheckFileUnreadable:
		return known(MsgCCFileUnreadable, "info", "quiet")
	default:
		return stateLabel{}
	}
}

// checkFileCount renders "3 checks" in both languages.
func checkFileCount(lang Lang, n int) template.HTML {
	en := MsgCCFileChecksMany
	if n == 1 {
		en = MsgCCFileChecksOne
	}
	number := formatNumber(n)
	return biText(lang,
		strings.ReplaceAll(Text(LangEN, en), "%s", number),
		strings.ReplaceAll(Text(LangKO, en), "%s", number))
}

// nextCheckStep is the one thing the owner still has to do, in the order the
// page asks for it. It only uses facts the page already holds.
//
// "Nothing left to do" is a promise that the next matching push runs checks,
// so it is said only when every prerequisite is known to hold. A fact that
// could not be read (an unreadable check file, an unknown token count) yields
// a neutral "could not tell" instead of either an instruction or that promise.
// In runner mode even a known token is not evidence that a runner is running,
// and in container mode nothing here shows that Docker runs or the image is
// present, so the last step in both says what still has to be true rather
// than promising execution. Only host mode, whose prerequisites the page does
// hold, ends with the promise.
func nextCheckStep(p ConfiguredChecksPage) MessageCode {
	runner := p.Policy.Executor == ExecutorExternalRunner
	switch {
	case !p.Runtime.Available:
		return MsgCCNextRepair
	case !p.Policy.Saved:
		return MsgCCNextSave
	case p.Policy.Legacy:
		return MsgCCNextResave
	case p.CheckFile.State == CheckFileMissing || p.CheckFile.State == CheckFileNoCommits:
		return MsgCCNextFile
	case p.CheckFile.State == CheckFileInvalid:
		return MsgCCNextFixFile
	case p.CheckFile.Found() && !sharesEvent(p.Policy.AllowedEvents, p.CheckFile.Events):
		return MsgCCNextEvents
	case !p.Policy.ConsentActive:
		return MsgCCNextEnable
	case !p.CheckFile.Found():
		return MsgCCNextFileUnknown
	case runner && !p.RunnerTokensKnown:
		return MsgCCNextRunnerUnknown
	case runner && p.ActiveRunnerTokens == 0:
		return MsgCCNextRunner
	case runner:
		return MsgCCNextRunnerStart
	case p.Policy.Executor == ExecutorContainer && latestJobUnavailable(p):
		return MsgCCNextContainerFailed
	case p.Policy.Executor == ExecutorContainer:
		return MsgCCNextContainerStart
	case p.Policy.Executor == ExecutorHost:
		return MsgCCNextNone
	default:
		return MsgCCNextUnknown
	}
}

// latestJobUnavailable reports whether the newest recorded job ran under the
// current settings in a container and could not run. The job records why, and
// the reason may be Docker, the image, or something else entirely (a commit
// over a file limit), so the hint built on this names no cause and sends the
// reader to the job.
func latestJobUnavailable(p ConfiguredChecksPage) bool {
	if len(p.Jobs) == 0 {
		return false
	}
	job := p.Jobs[0]
	return job.Status == JobUnavailable && job.Executor == ExecutorContainer && job.PolicyVersion == p.Policy.Version
}

func sharesEvent(allowed, file []string) bool {
	for _, event := range file {
		if containsEvent(allowed, event) {
			return true
		}
	}
	return false
}

// stepNumber renders "Step 3" for a screen reader, in both languages. The
// visible badge shows only the digit.
func stepNumber(lang Lang, n int) template.HTML {
	number := strconv.Itoa(n)
	return biText(lang,
		strings.ReplaceAll(Text(LangEN, MsgCCStepN), "%s", number),
		strings.ReplaceAll(Text(LangKO, MsgCCStepN), "%s", number))
}

// limitsOpen reports whether the advanced limits start expanded: only when a
// refusal points into them, so the reader lands on the field that needs work.
func limitsOpen(notices []Notice) bool {
	for _, notice := range notices {
		if _, known := PolicyLimitFor(notice.Field); known {
			return true
		}
	}
	return false
}
