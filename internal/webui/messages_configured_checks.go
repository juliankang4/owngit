package webui

// Text for the configured-check policy, job, and runner-token screens.
//
// These entries are merged into the shared catalog at startup, exactly like
// the evidence entries, so Text, Has, MissingMessages, and the client-side
// bundle all see one catalog.
//
// Wording rule for this file: it describes what OwnGit actually does. Host
// commands run with the service account's access, container limits are the
// ones the local Docker daemon enforces, and a runner reports its own
// protection. None of those is called a sandbox, and no sentence here turns an
// advisory check into something that holds a merge.

// The configured-check policy screen.
const (
	MsgCCTitle    MessageCode = "cc.title"
	MsgCCTab      MessageCode = "cc.tab"
	MsgCCIntro    MessageCode = "cc.intro"
	MsgCCOpen     MessageCode = "cc.open"
	MsgCCManual   MessageCode = "cc.manual_helper"
	MsgCCAdvisory MessageCode = "cc.advisory"
	MsgCCWorkflow MessageCode = "cc.workflow"

	// The status summary.
	MsgCCStateTitle     MessageCode = "cc.state.title"
	MsgCCStatePolicy    MessageCode = "cc.state.policy"
	MsgCCStateConsent   MessageCode = "cc.state.consent"
	MsgCCStateRuntime   MessageCode = "cc.state.runtime"
	MsgCCStateWhere     MessageCode = "cc.state.where"
	MsgCCStateWhen      MessageCode = "cc.state.when"
	MsgCCStateFile      MessageCode = "cc.state.file"
	MsgCCNotChosen      MessageCode = "cc.state.not_chosen"
	MsgCCPolicyNone     MessageCode = "cc.state.policy_none"
	MsgCCPolicySaved    MessageCode = "cc.state.policy_saved"
	MsgCCPolicyLegacy   MessageCode = "cc.state.policy_legacy"
	MsgCCConsentOn      MessageCode = "cc.state.consent_on"
	MsgCCConsentOff     MessageCode = "cc.state.consent_off"
	MsgCCConsentPaused  MessageCode = "cc.state.consent_paused"
	MsgCCRuntimeOK      MessageCode = "cc.state.runtime_ok"
	MsgCCRuntimeDown    MessageCode = "cc.state.runtime_down"
	MsgCCRuntimeSep     MessageCode = "cc.state.runtime_separate"
	MsgCCRuntimeWork    MessageCode = "cc.state.runtime_workspace"
	MsgCCRuntimeRestart MessageCode = "cc.state.runtime_restart"
	MsgCCRuntimeOther   MessageCode = "cc.state.runtime_other"
	MsgCCRuntimeRepair  MessageCode = "cc.state.runtime_repair"
	MsgCCPolicyVersion  MessageCode = "cc.state.version"

	MsgCCFileFound      MessageCode = "cc.file.found"
	MsgCCFileMissing    MessageCode = "cc.file.missing"
	MsgCCFileInvalid    MessageCode = "cc.file.invalid"
	MsgCCFileNoCommits  MessageCode = "cc.file.no_commits"
	MsgCCFileUnreadable MessageCode = "cc.file.unreadable"
	MsgCCFileChecksOne  MessageCode = "cc.file.checks_one"
	MsgCCFileChecksMany MessageCode = "cc.file.checks_many"

	MsgCCNextLabel   MessageCode = "cc.next.label"
	MsgCCNextRepair  MessageCode = "cc.next.repair"
	MsgCCNextSave    MessageCode = "cc.next.save"
	MsgCCNextResave  MessageCode = "cc.next.resave"
	MsgCCNextFile    MessageCode = "cc.next.file"
	MsgCCNextFixFile MessageCode = "cc.next.fix_file"
	MsgCCNextEvents  MessageCode = "cc.next.events"
	MsgCCNextEnable  MessageCode = "cc.next.enable"
	MsgCCNextRunner  MessageCode = "cc.next.runner"
	MsgCCNextNone    MessageCode = "cc.next.none"
	// Neutral next steps for a prerequisite that is not known to hold.
	MsgCCNextFileUnknown   MessageCode = "cc.next.file_unknown"
	MsgCCNextRunnerUnknown MessageCode = "cc.next.runner_unknown"
	MsgCCNextRunnerStart   MessageCode = "cc.next.runner_start"
	// Container mode never gets the plain promise: whether Docker runs and the
	// image is present is known only when a job starts.
	MsgCCNextContainerStart  MessageCode = "cc.next.container_start"
	MsgCCNextContainerFailed MessageCode = "cc.next.container_failed"
	MsgCCNextUnknown         MessageCode = "cc.next.unknown"

	// The steps.
	MsgCCStepWhere  MessageCode = "cc.step.where"
	MsgCCStepWhen   MessageCode = "cc.step.when"
	MsgCCStepFile   MessageCode = "cc.step.file"
	MsgCCStepSave   MessageCode = "cc.step.save"
	MsgCCStepSwitch MessageCode = "cc.step.switch"
	MsgCCStepN      MessageCode = "cc.step.n"

	MsgCCPolicyHelp     MessageCode = "cc.policy.help"
	MsgCCExecHost       MessageCode = "cc.policy.executor.host"
	MsgCCExecHostHelp   MessageCode = "cc.policy.executor.host_help"
	MsgCCExecCont       MessageCode = "cc.policy.executor.container"
	MsgCCExecContHelp   MessageCode = "cc.policy.executor.container_help"
	MsgCCExecRunner     MessageCode = "cc.policy.executor.runner"
	MsgCCExecRunnerHelp MessageCode = "cc.policy.executor.runner_help"
	MsgCCNoFallback     MessageCode = "cc.policy.no_fallback"

	MsgCCRunnerWhat      MessageCode = "cc.runner.what"
	MsgCCRunnerLink      MessageCode = "cc.runner.link"
	MsgCCRunnerNeedsSave MessageCode = "cc.runner.needs_save"
	MsgCCRunnerOnly      MessageCode = "cc.runner.only"

	MsgCCEventPush MessageCode = "cc.policy.events.push"
	// Short event names for the status summary.
	MsgCCStateEventPush MessageCode = "cc.state.event_push"
	MsgCCStateEventPR   MessageCode = "cc.state.event_pull_request"
	MsgCCEventPR        MessageCode = "cc.policy.events.pull_request"
	MsgCCEventsHelp     MessageCode = "cc.policy.events.help"

	// The check file step.
	MsgCCFileExample     MessageCode = "cc.file.example"
	MsgCCFileCopy        MessageCode = "cc.file.copy"
	MsgCCFileCopied      MessageCode = "cc.file.copied"
	MsgCCFileCopyFailed  MessageCode = "cc.file.copy_failed"
	MsgCCFileKeyVersion  MessageCode = "cc.file.key_version"
	MsgCCFileKeyEvents   MessageCode = "cc.file.key_events"
	MsgCCFileKeyBranches MessageCode = "cc.file.key_branches"
	MsgCCFileKeyChecks   MessageCode = "cc.file.key_checks"
	MsgCCFileKeyLimits   MessageCode = "cc.file.key_limits"
	MsgCCFileKeysTitle   MessageCode = "cc.file.keys_title"

	MsgCCAdvanced     MessageCode = "cc.advanced"
	MsgCCAdvancedHelp MessageCode = "cc.advanced_help"

	MsgCCLimits      MessageCode = "cc.policy.limits"
	MsgCCTimeout     MessageCode = "cc.policy.timeout"
	MsgCCTimeoutHelp MessageCode = "cc.policy.timeout_help"
	MsgCCOutput      MessageCode = "cc.policy.output"
	MsgCCOutputHelp  MessageCode = "cc.policy.output_help"
	MsgCCQueue       MessageCode = "cc.policy.queue"
	MsgCCQueueHelp   MessageCode = "cc.policy.queue_help"
	MsgCCActive      MessageCode = "cc.policy.active"
	MsgCCLease       MessageCode = "cc.policy.lease"
	MsgCCLeaseHelp   MessageCode = "cc.policy.lease_help"

	MsgCCSource        MessageCode = "cc.policy.source"
	MsgCCSourceHelp    MessageCode = "cc.policy.source_help"
	MsgCCSrcEntries    MessageCode = "cc.policy.source.entries"
	MsgCCSrcFileBytes  MessageCode = "cc.policy.source.file_bytes"
	MsgCCSrcTotalBytes MessageCode = "cc.policy.source.total_bytes"
	MsgCCSrcDepth      MessageCode = "cc.policy.source.depth"
	MsgCCSrcPathBytes  MessageCode = "cc.policy.source.path_bytes"
	MsgCCSrcNameBytes  MessageCode = "cc.policy.source.name_bytes"
	MsgCCSrcMetaBytes  MessageCode = "cc.policy.source.metadata_bytes"
	MsgCCSrcMetaHelp   MessageCode = "cc.policy.source.metadata_help"

	MsgCCContainer       MessageCode = "cc.policy.container"
	MsgCCContainerOnly   MessageCode = "cc.policy.container_only"
	MsgCCContainerHelp   MessageCode = "cc.policy.container_help"
	MsgCCContainerLimits MessageCode = "cc.policy.container_limits"
	MsgCCImage           MessageCode = "cc.policy.container.image"
	MsgCCImageHelp       MessageCode = "cc.policy.container.image_help"
	MsgCCRuntimeName     MessageCode = "cc.policy.container.runtime"
	MsgCCNetwork         MessageCode = "cc.policy.container.network"
	MsgCCNetworkNone     MessageCode = "cc.policy.container.network_none"
	MsgCCNetworkBridge   MessageCode = "cc.policy.container.network_bridge"
	MsgCCNetworkHelp     MessageCode = "cc.policy.container.network_help"
	MsgCCCPU             MessageCode = "cc.policy.container.cpu"
	MsgCCMemory          MessageCode = "cc.policy.container.memory"
	MsgCCPIDs            MessageCode = "cc.policy.container.pids"
	MsgCCScratch         MessageCode = "cc.policy.container.scratch"
	MsgCCNoDiskQuota     MessageCode = "cc.policy.container.no_disk_quota"
	MsgCCContainerTrust  MessageCode = "cc.policy.container.trust"

	// Units and amounts. An amount carries %s for the number.
	MsgCCUnitWord      MessageCode = "cc.unit.word"
	MsgCCUnitSeconds   MessageCode = "cc.unit.seconds"
	MsgCCUnitMinutes   MessageCode = "cc.unit.minutes"
	MsgCCUnitHours     MessageCode = "cc.unit.hours"
	MsgCCUnitBytes     MessageCode = "cc.unit.bytes"
	MsgCCUnitKB        MessageCode = "cc.unit.kb"
	MsgCCUnitMB        MessageCode = "cc.unit.mb"
	MsgCCUnitGB        MessageCode = "cc.unit.gb"
	MsgCCUnitCores     MessageCode = "cc.unit.cores"
	MsgCCAmountSecond  MessageCode = "cc.amount.second"
	MsgCCAmountSeconds MessageCode = "cc.amount.seconds"
	MsgCCAmountMinute  MessageCode = "cc.amount.minute"
	MsgCCAmountMinutes MessageCode = "cc.amount.minutes"
	MsgCCAmountHour    MessageCode = "cc.amount.hour"
	MsgCCAmountHours   MessageCode = "cc.amount.hours"
	MsgCCAmountByte    MessageCode = "cc.amount.byte"
	MsgCCAmountBytes   MessageCode = "cc.amount.bytes"
	MsgCCAmountKB      MessageCode = "cc.amount.kb"
	MsgCCAmountMB      MessageCode = "cc.amount.mb"
	MsgCCAmountGB      MessageCode = "cc.amount.gb"
	MsgCCAmountCore    MessageCode = "cc.amount.core"
	MsgCCAmountCores   MessageCode = "cc.amount.cores"
	MsgCCDefaultIs     MessageCode = "cc.default_is"
	MsgCCSizeNote      MessageCode = "cc.size_note"

	MsgCCSave          MessageCode = "cc.policy.save"
	MsgCCSaveHelp      MessageCode = "cc.policy.save_help"
	MsgCCEnable        MessageCode = "cc.consent.enable"
	MsgCCEnableHelp    MessageCode = "cc.consent.enable_help"
	MsgCCDisable       MessageCode = "cc.consent.disable"
	MsgCCDisableHelp   MessageCode = "cc.consent.disable_help"
	MsgCCPasswordEach  MessageCode = "cc.password_each_time"
	MsgCCEnableBlocked MessageCode = "cc.consent.blocked"
	MsgCCEnableLegacy  MessageCode = "cc.consent.blocked_legacy"
)

// Job list and job detail.
const (
	MsgCCJobsTitle       MessageCode = "cc.jobs.title"
	MsgCCJobsHelp        MessageCode = "cc.jobs.help"
	MsgCCJobsNone        MessageCode = "cc.jobs.none"
	MsgCCJobsNoneHelp    MessageCode = "cc.jobs.none_help"
	MsgCCJobsUnavailable MessageCode = "cc.jobs.unavailable"
	MsgCCJobsTruncated   MessageCode = "cc.jobs.truncated"
	MsgCCJobNotFound     MessageCode = "cc.jobs.not_found"
	MsgCCJobBack         MessageCode = "cc.jobs.back"
	MsgCCJobOpen         MessageCode = "cc.jobs.open"

	MsgCCJobTrigger    MessageCode = "cc.job.trigger"
	MsgCCJobRef        MessageCode = "cc.job.ref"
	MsgCCJobSource     MessageCode = "cc.job.source"
	MsgCCJobExecutor   MessageCode = "cc.job.executor"
	MsgCCJobWorkflow   MessageCode = "cc.job.workflow"
	MsgCCJobConfig     MessageCode = "cc.job.configuration"
	MsgCCJobPolicy     MessageCode = "cc.job.policy_version"
	MsgCCJobAdmitted   MessageCode = "cc.job.admitted_at"
	MsgCCJobStarted    MessageCode = "cc.job.started_at"
	MsgCCJobFinished   MessageCode = "cc.job.finished_at"
	MsgCCJobIdentifier MessageCode = "cc.job.identifier"
	MsgCCJobPR         MessageCode = "cc.job.pull_request"
	MsgCCJobCommands   MessageCode = "cc.job.commands"
	MsgCCJobNoAttempt  MessageCode = "cc.job.no_attempt"
	MsgCCJobUnreadable MessageCode = "cc.jobs.unreadable"
	MsgCCAttemptGone   MessageCode = "cc.job.attempt_missing"
	MsgCCAttemptUnread MessageCode = "cc.job.attempt_unreadable"
	MsgCCChecksGone    MessageCode = "cc.job.commands_missing"
	MsgCCChecksUnread  MessageCode = "cc.job.commands_unreadable"
	MsgCCJobFullOID    MessageCode = "cc.job.source_full"
	MsgCCJobBaseOID    MessageCode = "cc.job.base"
	MsgCCCopyHint      MessageCode = "cc.job.copy_hint"
	MsgCCJobLog        MessageCode = "cc.job.log"
	MsgCCJobLogCut     MessageCode = "cc.job.log_display_truncated"
	MsgCCJobCancelAsk  MessageCode = "cc.job.cancel_requested"

	MsgCCJobStatePending     MessageCode = "cc.job.state.pending"
	MsgCCJobStateClaimed     MessageCode = "cc.job.state.claimed"
	MsgCCJobStateStarted     MessageCode = "cc.job.state.started"
	MsgCCJobStatePassed      MessageCode = "cc.job.state.passed"
	MsgCCJobStateFailed      MessageCode = "cc.job.state.failed"
	MsgCCJobStateError       MessageCode = "cc.job.state.error"
	MsgCCJobStateCancelled   MessageCode = "cc.job.state.cancelled"
	MsgCCJobStateIncomplete  MessageCode = "cc.job.state.incomplete"
	MsgCCJobStateUnavailable MessageCode = "cc.job.state.unavailable"
	MsgCCJobStateAmbiguous   MessageCode = "cc.job.state.ambiguous"
	MsgCCJobStateInterrupted MessageCode = "cc.job.state.interrupted"

	MsgCCTriggerPush MessageCode = "cc.trigger.push"
	MsgCCTriggerPR   MessageCode = "cc.trigger.pull_request"

	MsgCCCancel     MessageCode = "cc.job.cancel"
	MsgCCCancelHelp MessageCode = "cc.job.cancel_help"
	MsgCCRerun      MessageCode = "cc.job.rerun"
	MsgCCRerunHelp  MessageCode = "cc.job.rerun_help"
)

// Runner tokens.
const (
	MsgRTTitle       MessageCode = "runner.title"
	MsgRTIntro       MessageCode = "runner.intro"
	MsgRTScope       MessageCode = "runner.scope"
	MsgRTNotPassword MessageCode = "runner.not_password"
	MsgRTNeedPolicy  MessageCode = "runner.need_policy"

	MsgRTListTitle MessageCode = "runner.list.title"
	MsgRTNone      MessageCode = "runner.list.none"
	MsgRTLabel     MessageCode = "runner.label"
	MsgRTLabelHelp MessageCode = "runner.label_help"
	MsgRTIssue     MessageCode = "runner.issue"
	MsgRTIssueHelp MessageCode = "runner.issue_help"
	MsgRTRevoke    MessageCode = "runner.revoke"
	MsgRTRevHelp   MessageCode = "runner.revoke_help"
	MsgRTActive    MessageCode = "runner.active"
	MsgRTRevoked   MessageCode = "runner.revoked"
	MsgRTCreatedAt MessageCode = "runner.created_at"
	MsgRTLastUsed  MessageCode = "runner.last_used"
	MsgRTNeverUsed MessageCode = "runner.never_used"
	MsgRTRevokedAt MessageCode = "runner.revoked_at"
	MsgRTGen       MessageCode = "runner.generation"

	MsgRTTokenTitle MessageCode = "runner.token.title"
	MsgRTTokenOnce  MessageCode = "runner.token.once"
	MsgRTTokenLabel MessageCode = "runner.token.label"
	MsgRTTokenStore MessageCode = "runner.token.store"

	MsgRTConnectTitle MessageCode = "runner.connect.title"
	MsgRTConnectHelp  MessageCode = "runner.connect.help"
	MsgRTConnectTLS   MessageCode = "runner.connect.tls"
	MsgRTConnectLocal MessageCode = "runner.connect.local_only"
	MsgRTConnectNoSvc MessageCode = "runner.connect.no_service"
)

// Results of the configured-check forms.
const (
	MsgCCSaved              MessageCode = "cc.result.saved"
	MsgCCSavedEnabled       MessageCode = "cc.result.saved_enabled"
	MsgCCEnabled            MessageCode = "cc.result.enabled"
	MsgCCDisabled           MessageCode = "cc.result.disabled"
	MsgCCConsentRevoked     MessageCode = "cc.result.consent_revoked"
	MsgCCJobCancelled       MessageCode = "cc.result.job_cancelled"
	MsgCCJobAlreadyFinished MessageCode = "cc.result.job_already_finished"
	MsgCCJobRerunQueued     MessageCode = "cc.result.job_rerun"
	MsgCCJobRerunExisting   MessageCode = "cc.result.job_rerun_existing"
	MsgCCJobRefused         MessageCode = "cc.result.job_refused"
	MsgCCJobMissing         MessageCode = "cc.result.job_missing"
	MsgCCPolicyRefused      MessageCode = "cc.result.policy_refused"
	MsgCCPolicyMissing      MessageCode = "cc.result.policy_missing"
	MsgCCPolicyStale        MessageCode = "cc.result.policy_stale"
	MsgCCFailed             MessageCode = "cc.result.failed"

	MsgCCNumberInvalid  MessageCode = "cc.result.invalid_number"
	MsgCCNumberFraction MessageCode = "cc.result.fraction"
	MsgCCNumberWhole    MessageCode = "cc.result.whole_number"
	MsgCCNumberCores    MessageCode = "cc.result.cores_decimals"

	// Refusals the backend reported against one field. They say what to do and
	// never restate a bound: the accepted range is already printed beside the
	// field from this same catalog, so one source describes it.
	MsgCCFieldRange         MessageCode = "cc.result.field_range"
	MsgCCTotalBytesRange    MessageCode = "cc.result.total_bytes_range"
	MsgCCFieldRequired      MessageCode = "cc.result.field_required"
	MsgCCFieldUnknown       MessageCode = "cc.result.field_unknown"
	MsgCCFieldFormat        MessageCode = "cc.result.field_format"
	MsgCCFieldNotApplicable MessageCode = "cc.result.field_not_applicable"
	MsgCCFieldDuplicate     MessageCode = "cc.result.field_duplicate"
	MsgCCImageRequired      MessageCode = "cc.result.image_required"
	MsgCCRuntimeUnsupported MessageCode = "cc.result.runtime_unsupported"
	MsgCCEventsInvalid      MessageCode = "cc.result.invalid_events"
	MsgCCExecutorInvalid    MessageCode = "cc.result.invalid_executor"
	MsgCCImageInvalid       MessageCode = "cc.result.invalid_image"
	MsgCCNetworkInvalid     MessageCode = "cc.result.invalid_network"

	MsgRTIssued       MessageCode = "runner.result.issued"
	MsgRTRevokedDone  MessageCode = "runner.result.revoked"
	MsgRTNotFound     MessageCode = "runner.result.not_found"
	MsgRTLabelInvalid MessageCode = "runner.result.invalid_label"
	MsgRTFailed       MessageCode = "runner.result.failed"
	MsgRTExisting     MessageCode = "runner.result.existing"
)

// configuredCheckCatalog holds the text for the screens above.
var configuredCheckCatalog = map[MessageCode]message{
	// -- the screen itself ---------------------------------------------
	MsgCCTitle: {en: "Automatic checks", ko: "자동 체크"},
	MsgCCTab:   {en: "Automatic checks", ko: "자동 체크"},
	MsgCCOpen:  {en: "Automatic checks", ko: "자동 체크"},
	MsgCCIntro: {
		en: "When you push or open a pull request, OwnGit runs the commands in your repository's check file and shows whether each one passed or failed.",
		ko: "푸시하거나 PR을 열면 OwnGit이 저장소의 체크 파일에 적힌 명령을 실행하고, 각 명령이 통과했는지 실패했는지 보여 줍니다.",
	},
	MsgCCAdvisory: {
		en: "Results are advice only. They never block a push, a pull request, or a merge.",
		ko: "결과는 참고용입니다. 푸시, PR, 병합을 막지 않습니다.",
	},
	MsgCCManual: {
		en: "This is separate from the check helper you run yourself. Helper tokens and the results a helper reports are not affected.",
		ko: "직접 실행하는 체크 에이전트와는 별개입니다. 에이전트 토큰과 에이전트가 보고한 결과는 영향을 받지 않습니다.",
	},
	MsgCCWorkflow: {
		en: "Checks run only for commits that contain the file .owngit/checks.json. Commit a file like this one, then replace the command with the one you use to test your project.",
		ko: "체크는 .owngit/checks.json 파일이 들어 있는 커밋에서만 실행됩니다. 아래와 같은 파일을 커밋하고, 명령을 프로젝트를 테스트할 때 쓰는 명령으로 바꾸세요.",
	},

	// -- status ----------------------------------------------------------
	MsgCCStateTitle:   {en: "Status", ko: "현재 상태"},
	MsgCCStateConsent: {en: "Checks", ko: "체크 실행"},
	MsgCCStateWhere:   {en: "Where", ko: "실행할 곳"},
	MsgCCStateWhen:    {en: "When", ko: "실행할 때"},
	MsgCCStateFile:    {en: "Check file", ko: "체크 파일"},
	MsgCCStatePolicy:  {en: "Settings", ko: "설정"},
	MsgCCStateRuntime: {en: "Check environment", ko: "체크 실행 환경"},
	MsgCCNotChosen:    {en: "Not chosen yet", ko: "아직 고르지 않음"},
	MsgCCPolicyNone:   {en: "Not saved yet", ko: "아직 저장하지 않음"},
	MsgCCPolicySaved:  {en: "Saved", ko: "저장됨"},
	MsgCCPolicyLegacy: {
		en: "These settings were restored from an older version and are incomplete. Save them again before turning checks on.",
		ko: "이 설정은 이전 버전에서 복원되어 일부가 빠져 있습니다. 체크를 켜기 전에 다시 저장하세요.",
	},
	MsgCCStateEventPush: {en: "Push", ko: "푸시"},
	MsgCCStateEventPR:   {en: "Pull request", ko: "PR"},
	MsgCCConsentOn:      {en: "On: new commits are checked", ko: "켜짐: 새 커밋을 체크합니다"},
	MsgCCConsentOff:     {en: "Off: nothing runs", ko: "꺼짐: 아무것도 실행하지 않습니다"},
	MsgCCConsentPaused:  {en: "On, but paused until the check environment works", ko: "켜져 있지만 체크 환경이 준비될 때까지 멈춤"},
	MsgCCRuntimeOK:      {en: "Available", ko: "사용 가능"},
	MsgCCRuntimeDown:    {en: "Unavailable", ko: "사용 불가"},
	MsgCCRuntimeSep: {
		en: "This is not a check result. Git, pull requests, and merging keep working.",
		ko: "체크 결과가 아닙니다. Git 사용, PR, 병합은 그대로 동작합니다.",
	},
	MsgCCRuntimeWork: {
		en: "OwnGit could not take ownership of its private check workspace.",
		ko: "OwnGit이 체크 전용 작업 폴더의 소유권을 확보하지 못했습니다.",
	},
	MsgCCRuntimeRestart: {
		en: "OwnGit could not clean up work left by an earlier run.",
		ko: "이전 실행이 남긴 작업을 정리하지 못했습니다.",
	},
	MsgCCRuntimeOther: {
		en: "OwnGit reported a condition this screen has no words for. The recorded code is shown beside it.",
		ko: "이 화면이 설명할 수 없는 상태를 보고했습니다. 기록된 코드를 옆에 표시합니다.",
	},
	MsgCCRuntimeRepair: {
		en: "Fix the cause and restart OwnGit. OwnGit does not retry on its own or run the checks somewhere else.",
		ko: "원인을 고친 뒤 OwnGit을 다시 시작하세요. OwnGit은 스스로 다시 시도하거나 다른 곳에서 체크를 실행하지 않습니다.",
	},
	MsgCCPolicyVersion: {en: "Version", ko: "버전"},

	MsgCCFileFound:      {en: "Found on the default branch", ko: "기본 브랜치에 있음"},
	MsgCCFileMissing:    {en: "Not found on the default branch", ko: "기본 브랜치에 없음"},
	MsgCCFileInvalid:    {en: "Found, but OwnGit cannot use it", ko: "있지만 사용할 수 없음"},
	MsgCCFileNoCommits:  {en: "The repository has no commits yet", ko: "저장소에 아직 커밋이 없음"},
	MsgCCFileUnreadable: {en: "Could not be checked just now", ko: "지금은 확인하지 못함"},
	MsgCCFileChecksOne:  {en: "%s check", ko: "체크 %s개"},
	MsgCCFileChecksMany: {en: "%s checks", ko: "체크 %s개"},

	MsgCCNextLabel: {en: "Next step", ko: "다음 할 일"},
	MsgCCNextRepair: {
		en: "Fix the check environment problem described above, then restart OwnGit.",
		ko: "위에 설명한 체크 실행 환경 문제를 고친 뒤 OwnGit을 다시 시작하세요.",
	},
	MsgCCNextSave: {
		en: "Choose where and when checks run, then save the settings in step 4.",
		ko: "실행할 곳과 때를 고른 뒤 4단계에서 설정을 저장하세요.",
	},
	MsgCCNextResave: {
		en: "Save the settings again in step 4. They were restored from an older version.",
		ko: "4단계에서 설정을 다시 저장하세요. 이전 버전에서 복원된 설정입니다.",
	},
	MsgCCNextFile: {
		en: "Commit a check file to the repository, as shown in step 3.",
		ko: "3단계의 예시처럼 저장소에 체크 파일을 커밋하세요.",
	},
	MsgCCNextFixFile: {
		en: "Fix the check file. OwnGit's reason is shown under the check file status above.",
		ko: "체크 파일을 고치세요. 사용할 수 없는 이유는 위의 체크 파일 상태 아래에 있습니다.",
	},
	MsgCCNextEvents: {
		en: "The check file and step 2 have no event in common, so nothing will run. Turn on the same event in both.",
		ko: "체크 파일과 2단계에 함께 켜진 이벤트가 없어 아무것도 실행되지 않습니다. 양쪽에서 같은 이벤트를 켜세요.",
	},
	MsgCCNextEnable: {
		en: "Turn checks on in step 5.",
		ko: "5단계에서 체크를 켜세요.",
	},
	MsgCCNextRunner: {
		en: "Create a runner token and start a runner on the computer that should run the checks.",
		ko: "러너 토큰을 만들고, 체크를 실행할 컴퓨터에서 러너를 시작하세요.",
	},
	MsgCCNextNone: {
		en: "Nothing. Checks run on the next matching push or pull request.",
		ko: "없습니다. 조건에 맞는 다음 푸시나 PR에서 체크가 실행됩니다.",
	},
	MsgCCNextContainerStart: {
		en: "If Docker is running on this computer and the image is already pulled, checks run on the next matching push or pull request. OwnGit can only tell when a job starts, and a job that cannot use Docker is recorded as unavailable.",
		ko: "이 컴퓨터에서 Docker가 실행 중이고 이미지를 미리 받아 두었다면, 조건에 맞는 다음 푸시나 PR에서 체크가 실행됩니다. OwnGit은 작업을 시작할 때에만 이를 알 수 있으며, Docker를 쓸 수 없는 작업은 사용 불가로 기록됩니다.",
	},
	MsgCCNextContainerFailed: {
		en: "The last container check could not run. Open that job in the list below to see why. If the cause was Docker or the image, fix it, then run the job again from its page.",
		ko: "가장 최근 컨테이너 체크를 실행하지 못했습니다. 아래 목록에서 그 작업을 열어 이유를 확인하세요. 원인이 Docker나 이미지였다면 그 문제를 고친 뒤, 작업 페이지에서 다시 실행하세요.",
	},
	MsgCCNextUnknown: {
		en: "Could not determine the next step for the selected place.",
		ko: "선택한 실행 위치에 대한 다음 할 일을 확인할 수 없습니다.",
	},
	MsgCCNextFileUnknown: {
		en: "Could not determine. OwnGit could not read the check file on the default branch, so it cannot tell whether checks will run. Reload this page to try again.",
		ko: "확인할 수 없습니다. 기본 브랜치의 체크 파일을 읽지 못해 체크가 실행될지 알 수 없습니다. 페이지를 다시 불러와 보세요.",
	},
	MsgCCNextRunnerUnknown: {
		en: "Could not determine. OwnGit could not read the runner tokens for this repository, so it cannot tell whether a runner can connect. Reload this page to try again.",
		ko: "확인할 수 없습니다. 이 저장소의 러너 토큰을 읽지 못해 러너가 연결할 수 있는지 알 수 없습니다. 페이지를 다시 불러와 보세요.",
	},
	MsgCCNextRunnerStart: {
		en: "If no runner is running yet, start one on the other computer with a runner token from this repository. OwnGit cannot see whether a runner is running, and checks run only while one is connected.",
		ko: "아직 러너를 시작하지 않았다면, 이 저장소의 러너 토큰으로 다른 컴퓨터에서 러너를 시작하세요. OwnGit은 러너가 실행 중인지 알 수 없으며, 체크는 러너가 연결되어 있을 때만 실행됩니다.",
	},

	// -- steps -----------------------------------------------------------
	MsgCCStepWhere:  {en: "Where checks run", ko: "체크를 실행할 곳"},
	MsgCCStepWhen:   {en: "When checks run", ko: "체크를 실행할 때"},
	MsgCCStepFile:   {en: "Add a check file to the repository", ko: "저장소에 체크 파일 추가"},
	MsgCCStepSave:   {en: "Save the settings", ko: "설정 저장"},
	MsgCCStepSwitch: {en: "Turn checks on or off", ko: "체크 켜고 끄기"},
	MsgCCStepN:      {en: "Step %s", ko: "%s단계"},

	MsgCCPolicyHelp: {
		en: "These settings are kept by OwnGit, not in the repository, so a commit cannot change where checks run or raise these limits.",
		ko: "이 설정은 저장소가 아니라 OwnGit에 보관되므로, 커밋으로 실행할 곳을 바꾸거나 한도를 높일 수 없습니다.",
	},
	MsgCCExecHost: {en: "This computer", ko: "이 컴퓨터"},
	MsgCCExecHostHelp: {
		en: "Commands run directly on this computer with the OwnGit account's access, which includes OwnGit's own files. This is not a sandbox. Use it only for repositories and commands you trust completely.",
		ko: "명령이 이 컴퓨터에서 OwnGit 계정 권한으로 바로 실행되며, OwnGit 자체 파일에도 접근할 수 있습니다. 격리된 환경이 아닙니다. 완전히 믿을 수 있는 저장소와 명령에만 사용하세요.",
	},
	MsgCCExecCont: {en: "A Docker container on this computer", ko: "이 컴퓨터의 Docker 컨테이너"},
	MsgCCExecContHelp: {
		en: "Commands run inside a Docker image you choose, as a non-administrator user, with limits on CPU, memory, and processes. Only the commit's files are shared with the container. How much this restricts a command depends on your Docker setup and the image.",
		ko: "직접 고른 Docker 이미지 안에서 관리자가 아닌 사용자로 실행하며, CPU, 메모리, 프로세스 수를 제한합니다. 컨테이너에는 해당 커밋의 파일만 공유됩니다. 실제로 얼마나 제한되는지는 Docker 설정과 이미지에 달려 있습니다.",
	},
	MsgCCExecRunner: {en: "Another computer (runner)", ko: "다른 컴퓨터 (러너)"},
	MsgCCExecRunnerHelp: {
		en: "A runner you start on another computer picks up the jobs and runs them there. It reports how it is protected, and OwnGit passes that report on without checking it.",
		ko: "다른 컴퓨터에서 시작한 러너가 작업을 가져가 그 컴퓨터에서 실행합니다. 러너가 알린 보호 상태는 OwnGit이 확인하지 않고 그대로 전달합니다.",
	},
	MsgCCNoFallback: {
		en: "OwnGit never switches to another place on its own. If the chosen place is unavailable, the job is recorded as unavailable, never as passed.",
		ko: "OwnGit은 실행할 곳을 스스로 바꾸지 않습니다. 고른 곳을 쓸 수 없으면 작업은 사용 불가로 기록되며 통과로 처리되지 않습니다.",
	},

	MsgCCRunnerWhat: {
		en: "A runner is a small OwnGit program you start on another computer. It collects check jobs from this server, runs them on that computer, and sends back the results.",
		ko: "러너는 다른 컴퓨터에서 실행하는 작은 OwnGit 프로그램입니다. 이 서버에서 체크 작업을 가져가 그 컴퓨터에서 실행하고 결과를 돌려보냅니다.",
	},
	MsgCCRunnerLink: {en: "Create a runner token", ko: "러너 토큰 만들기"},
	MsgCCRunnerNeedsSave: {
		en: "Save these settings first. A runner token works only once settings are saved.",
		ko: "먼저 이 설정을 저장하세요. 러너 토큰은 설정을 저장한 뒤에만 쓸 수 있습니다.",
	},
	MsgCCRunnerOnly: {
		en: "Used only when checks run on another computer.",
		ko: "다른 컴퓨터에서 실행할 때만 사용합니다.",
	},

	MsgCCEventPush: {en: "After a push", ko: "푸시한 뒤"},
	MsgCCEventPR:   {en: "When a pull request is opened or updated", ko: "PR을 열거나 업데이트할 때"},
	MsgCCEventsHelp: {
		en: "Choose at least one. The check file has to turn on the same event, and it can narrow it to certain branches.",
		ko: "하나 이상 고르세요. 체크 파일에도 같은 이벤트가 켜져 있어야 하며, 체크 파일에서 특정 브랜치로 좁힐 수 있습니다.",
	},

	// -- the check file ------------------------------------------------
	MsgCCFileExample:    {en: "Example check file", ko: "체크 파일 예시"},
	MsgCCFileCopy:       {en: "Copy", ko: "복사"},
	MsgCCFileCopied:     {en: "Copied.", ko: "복사했습니다."},
	MsgCCFileCopyFailed: {en: "Copying did not work here. Select the text and copy it.", ko: "여기서는 복사하지 못했습니다. 텍스트를 선택해 복사하세요."},
	MsgCCFileKeysTitle:  {en: "What the fields mean", ko: "항목 설명"},
	MsgCCFileKeyVersion: {en: "Always 1.", ko: "항상 1입니다."},
	MsgCCFileKeyEvents: {
		en: "When to run. push runs after a push, and pull_request runs when a pull request is opened or updated. Leave one out to skip it.",
		ko: "언제 실행할지 정합니다. push는 푸시한 뒤, pull_request는 PR을 열거나 업데이트할 때 실행합니다. 필요 없는 항목은 빼세요.",
	},
	MsgCCFileKeyBranches: {
		en: "Optional, inside an event. For example {\"branches\": [\"main\"]} runs only for main. A name ending in * matches every branch that starts with it.",
		ko: "선택 사항이며 이벤트 안에 씁니다. 예를 들어 {\"branches\": [\"main\"]}은 main에서만 실행합니다. *로 끝나는 이름은 그 이름으로 시작하는 모든 브랜치에 해당합니다.",
	},
	MsgCCFileKeyLimits: {
		en: "Optional. Asks for a different time or output limit for this file, such as {\"limits\": {\"timeout_ms\": 1200000}}. Times are in milliseconds and sizes in bytes. Without it, a check gets 10 minutes and keeps 64 KB of output, and it never gets more than the maximums in step 4.",
		ko: "선택 사항입니다. 이 파일의 시간이나 출력 한도를 따로 요청하며, 예를 들어 {\"limits\": {\"timeout_ms\": 1200000}}처럼 씁니다. 시간은 밀리초, 크기는 바이트 단위입니다. 쓰지 않으면 체크마다 10분이 주어지고 출력은 64 KB까지 보관하며, 4단계의 최대값을 넘지 않습니다.",
	},
	MsgCCFileKeyChecks: {
		en: "The commands to run, each with a short name. Each command runs in a copy of the commit's files, and it passes when it exits with code 0.",
		ko: "실행할 명령 목록이며 각각 짧은 이름을 붙입니다. 명령은 커밋 파일의 복사본에서 실행되며, 종료 코드 0으로 끝나면 통과입니다.",
	},

	// -- limits ----------------------------------------------------------
	MsgCCAdvanced: {en: "Advanced limits", ko: "고급 한도"},
	MsgCCAdvancedHelp: {
		en: "The starting values suit most projects. A field that shows a default uses it when left empty.",
		ko: "처음 채워진 값은 대부분의 프로젝트에 맞습니다. 기본값이 표시된 칸은 비워 두면 그 값을 씁니다.",
	},
	MsgCCSizeNote: {
		en: "Sizes count 1 KB as 1024 bytes.",
		ko: "크기는 1 KB를 1024바이트로 계산합니다.",
	},

	MsgCCLimits:  {en: "Each job", ko: "작업마다"},
	MsgCCTimeout: {en: "Longest time a check may run", ko: "체크당 최대 실행 시간"},
	MsgCCTimeoutHelp: {
		en: "A check gets 10 minutes unless its check file asks for a different time under limits, and never more than this.",
		ko: "체크 파일의 limits에서 다른 시간을 요청하지 않으면 체크마다 10분이 주어지며, 이 값을 넘지 않습니다.",
	},
	MsgCCOutput: {en: "Most output a check may keep", ko: "체크당 최대 보관 출력"},
	MsgCCOutputHelp: {
		en: "A check keeps 64 KB of output unless its check file asks for more under limits, and never more than this. Output past the limit is cut off, and the result says so.",
		ko: "체크 파일의 limits에서 더 요청하지 않으면 체크마다 출력을 64 KB까지 보관하며, 이 값을 넘지 않습니다. 한도를 넘는 출력은 잘리며, 결과에 그 사실을 표시합니다.",
	},
	MsgCCQueue: {en: "Unfinished jobs allowed", ko: "쌓아 둘 수 있는 미완료 작업"},
	MsgCCQueueHelp: {
		en: "Counts jobs that are waiting or running. Past this number, new jobs are not added.",
		ko: "대기 중이거나 실행 중인 작업을 셉니다. 이 수를 넘으면 새 작업을 추가하지 않습니다.",
	},
	MsgCCActive: {en: "Jobs running at the same time", ko: "동시에 실행할 작업 수"},
	MsgCCLease:  {en: "Check-in window", ko: "응답 대기 시간"},
	MsgCCLeaseHelp: {
		en: "Whatever runs a job has to report progress within this time. If it goes silent for longer, OwnGit stops waiting for that job.",
		ko: "작업을 실행하는 쪽은 이 시간 안에 진행 상황을 알려야 합니다. 더 오래 응답이 없으면 OwnGit은 그 작업을 더 기다리지 않습니다.",
	},

	MsgCCSource: {en: "Files copied for a check", ko: "체크용으로 복사하는 파일"},
	MsgCCSourceHelp: {
		en: "Before a check runs, OwnGit copies the commit's files into a private folder. A commit over any of these limits is not checked and is reported as unavailable.",
		ko: "체크를 실행하기 전에 OwnGit은 커밋의 파일을 전용 폴더로 복사합니다. 한도를 하나라도 넘는 커밋은 체크하지 않고 사용 불가로 보고합니다.",
	},
	MsgCCSrcEntries:    {en: "Files and folders", ko: "파일과 폴더 수"},
	MsgCCSrcFileBytes:  {en: "Largest file", ko: "가장 큰 파일"},
	MsgCCSrcTotalBytes: {en: "All files together", ko: "전체 파일 크기"},
	MsgCCSrcDepth:      {en: "Folder depth", ko: "폴더 깊이"},
	MsgCCSrcPathBytes:  {en: "Longest path, in bytes", ko: "가장 긴 경로 (바이트)"},
	MsgCCSrcNameBytes:  {en: "Longest file name, in bytes", ko: "가장 긴 파일 이름 (바이트)"},
	MsgCCSrcMetaBytes:  {en: "File list size", ko: "파일 목록 크기"},
	MsgCCSrcMetaHelp: {
		en: "How much of Git's file listing OwnGit reads for one commit.",
		ko: "커밋 하나의 파일 목록을 읽을 때 쓰는 최대 크기입니다.",
	},

	MsgCCContainer: {en: "Container settings", ko: "컨테이너 설정"},
	MsgCCContainerOnly: {
		en: "Used only when checks run in a Docker container.",
		ko: "Docker 컨테이너에서 실행할 때만 사용합니다.",
	},
	MsgCCContainerHelp: {
		en: "OwnGit never downloads an image. Pull it on this computer first.",
		ko: "OwnGit은 이미지를 내려받지 않습니다. 먼저 이 컴퓨터에 받아 두세요.",
	},
	MsgCCContainerLimits: {en: "Container resources", ko: "컨테이너 자원"},
	MsgCCImage:           {en: "Docker image", ko: "Docker 이미지"},
	MsgCCImageHelp: {
		en: "Enter the image's fixed digest so it cannot change later: sha256: followed by 64 hex characters, or a name followed by @sha256: and 64 hex characters.",
		ko: "나중에 바뀌지 않도록 이미지의 고정 다이제스트를 입력하세요. sha256: 뒤에 16진수 64자리, 또는 이름 뒤에 @sha256:과 16진수 64자리를 붙인 형식입니다.",
	},
	MsgCCRuntimeName:   {en: "Runtime", ko: "런타임"},
	MsgCCNetwork:       {en: "Network", ko: "네트워크"},
	MsgCCNetworkNone:   {en: "No network access", ko: "네트워크 사용 안 함"},
	MsgCCNetworkBridge: {en: "Allow network access (Docker bridge)", ko: "네트워크 허용 (Docker 브리지)"},
	MsgCCNetworkHelp: {
		en: "Whether commands can reach the network. The choice is recorded with each job.",
		ko: "명령이 네트워크에 접근할 수 있는지 정합니다. 이 선택은 작업마다 기록됩니다.",
	},
	MsgCCCPU:     {en: "CPU", ko: "CPU"},
	MsgCCMemory:  {en: "Memory", ko: "메모리"},
	MsgCCPIDs:    {en: "Processes", ko: "프로세스 수"},
	MsgCCScratch: {en: "Temporary space (/tmp)", ko: "임시 공간 (/tmp)"},
	MsgCCNoDiskQuota: {
		en: "The copied files sit on this computer's disk, which the container reaches directly, and have no disk quota of their own beyond the file limits above.",
		ko: "복사한 파일은 컨테이너가 직접 쓰는 이 컴퓨터의 디스크에 있으며, 위의 파일 한도 외에 별도의 디스크 할당량은 없습니다.",
	},
	MsgCCContainerTrust: {
		en: "Anyone who can control the local Docker daemon can generally reach this machine as an administrator, whatever a single container allows.",
		ko: "로컬 Docker 데몬을 제어할 수 있는 사람은 컨테이너 설정과 무관하게 이 컴퓨터를 관리자 수준으로 다룰 수 있습니다.",
	},

	MsgCCUnitWord:      {en: "unit", ko: "단위"},
	MsgCCUnitSeconds:   {en: "seconds", ko: "초"},
	MsgCCUnitMinutes:   {en: "minutes", ko: "분"},
	MsgCCUnitHours:     {en: "hours", ko: "시간"},
	MsgCCUnitBytes:     {en: "bytes", ko: "바이트"},
	MsgCCUnitKB:        {en: "KB", ko: "KB"},
	MsgCCUnitMB:        {en: "MB", ko: "MB"},
	MsgCCUnitGB:        {en: "GB", ko: "GB"},
	MsgCCUnitCores:     {en: "cores", ko: "코어"},
	MsgCCAmountSecond:  {en: "%s second", ko: "%s초"},
	MsgCCAmountSeconds: {en: "%s seconds", ko: "%s초"},
	MsgCCAmountMinute:  {en: "%s minute", ko: "%s분"},
	MsgCCAmountMinutes: {en: "%s minutes", ko: "%s분"},
	MsgCCAmountHour:    {en: "%s hour", ko: "%s시간"},
	MsgCCAmountHours:   {en: "%s hours", ko: "%s시간"},
	MsgCCAmountByte:    {en: "%s byte", ko: "%s바이트"},
	MsgCCAmountBytes:   {en: "%s bytes", ko: "%s바이트"},
	MsgCCAmountKB:      {en: "%s KB", ko: "%s KB"},
	MsgCCAmountMB:      {en: "%s MB", ko: "%s MB"},
	MsgCCAmountGB:      {en: "%s GB", ko: "%s GB"},
	MsgCCAmountCore:    {en: "%s core", ko: "%s코어"},
	MsgCCAmountCores:   {en: "%s cores", ko: "%s코어"},
	MsgCCDefaultIs:     {en: "Default: %s", ko: "기본값: %s"},

	// -- saving and switching -------------------------------------------
	MsgCCSave: {en: "Save settings", ko: "설정 저장"},
	MsgCCSaveHelp: {
		en: "Saving does not turn checks on. Saving changed settings turns checks off until you turn them on again in step 5.",
		ko: "저장한다고 체크가 켜지지는 않습니다. 설정을 바꿔 저장하면 5단계에서 다시 켤 때까지 체크가 꺼집니다.",
	},
	MsgCCEnable: {en: "Turn checks on", ko: "체크 켜기"},
	MsgCCEnableHelp: {
		en: "Checks run only with the settings saved above. Changing them later turns checks off again.",
		ko: "위에 저장한 설정 그대로만 실행합니다. 나중에 설정을 바꾸면 체크가 다시 꺼집니다.",
	},
	MsgCCDisable: {en: "Turn checks off", ko: "체크 끄기"},
	MsgCCDisableHelp: {
		en: "No new jobs start. Jobs already running are asked to stop, and OwnGit reports only what it can confirm.",
		ko: "새 작업을 시작하지 않습니다. 실행 중인 작업에는 중지를 요청하며, OwnGit은 확인된 사실만 보고합니다.",
	},
	MsgCCPasswordEach: {
		en: "Changing check settings is a security change, so OwnGit asks for the administrator password every time, even when you are signed in.",
		ko: "체크 설정 변경은 보안 설정이라 로그인한 상태여도 매번 관리자 비밀번호를 확인합니다.",
	},
	MsgCCEnableBlocked: {
		en: "Save the settings in step 4 first.",
		ko: "먼저 4단계에서 설정을 저장하세요.",
	},
	MsgCCEnableLegacy: {
		en: "Save the settings again in step 4 first. They were restored from an older version.",
		ko: "먼저 4단계에서 설정을 다시 저장하세요. 이전 버전에서 복원된 설정입니다.",
	},

	// -- jobs -----------------------------------------------------------
	MsgCCJobsTitle: {en: "Recent jobs", ko: "최근 작업"},
	MsgCCJobsHelp: {
		en: "Each job names the exact commit it was pinned to and the mode that actually applied to it.",
		ko: "각 작업은 어떤 커밋에 고정되었는지와 실제로 적용된 실행 모드를 함께 기록합니다.",
	},
	MsgCCJobsNone: {en: "No job has been recorded for this repository.", ko: "이 저장소에 기록된 작업이 없습니다."},
	MsgCCJobsNoneHelp: {
		en: "A job appears after a matching push or pull request, once the check file is committed and checks are on.",
		ko: "체크 파일이 커밋되고 체크가 켜진 뒤, 조건에 맞는 푸시나 PR이 있으면 작업이 나타납니다.",
	},
	MsgCCJobsUnavailable: {
		en: "The job records could not be read, so this list is not a statement that no job exists.",
		ko: "작업 기록을 읽지 못했습니다. 작업이 없다는 뜻은 아닙니다.",
	},
	MsgCCJobsTruncated: {en: "Only the most recent jobs are listed.", ko: "가장 최근 작업만 표시합니다."},
	MsgCCJobNotFound:   {en: "That job does not exist in this repository.", ko: "이 저장소에 그런 작업이 없습니다."},
	MsgCCJobUnreadable: {
		en: "That job's record could not be read. This does not mean the job is absent, and it is not a check result.",
		ko: "이 작업의 기록을 읽지 못했습니다. 작업이 없다는 뜻이 아니며 체크 결과도 아닙니다.",
	},
	MsgCCAttemptGone: {
		en: "This job registered a run, but its record is no longer stored.",
		ko: "이 작업은 실행을 등록했지만 그 기록이 더 이상 저장되어 있지 않습니다.",
	},
	MsgCCAttemptUnread: {
		en: "This job registered a run, but its record could not be read. Nothing here says whether it passed.",
		ko: "이 작업은 실행을 등록했지만 그 기록을 읽지 못했습니다. 통과 여부는 여기서 알 수 없습니다.",
	},
	MsgCCChecksGone: {
		en: "The configuration version this job captured is no longer stored, so its commands cannot be listed.",
		ko: "이 작업이 사용한 설정 버전이 더 이상 저장되어 있지 않아 명령을 표시할 수 없습니다.",
	},
	MsgCCChecksUnread: {
		en: "The commands this job captured could not be read. This does not mean it defined none.",
		ko: "이 작업이 사용한 명령을 읽지 못했습니다. 명령이 없었다는 뜻은 아닙니다.",
	},
	MsgCCJobFullOID: {en: "Full commit", ko: "전체 커밋 ID"},
	MsgCCJobBaseOID: {en: "Compared against", ko: "비교 기준"},
	MsgCCCopyHint: {
		en: "Select the value to copy it.",
		ko: "값을 선택하면 복사할 수 있습니다.",
	},
	MsgCCJobBack: {en: "Back to jobs", ko: "작업 목록으로"},
	MsgCCJobOpen: {en: "Open", ko: "열기"},

	MsgCCJobTrigger:    {en: "Trigger", ko: "실행 계기"},
	MsgCCJobRef:        {en: "Ref", ko: "참조"},
	MsgCCJobSource:     {en: "Commit", ko: "커밋"},
	MsgCCJobExecutor:   {en: "Mode", ko: "실행 모드"},
	MsgCCJobWorkflow:   {en: "Workflow file", ko: "워크플로 파일"},
	MsgCCJobConfig:     {en: "Check configuration", ko: "체크 설정"},
	MsgCCJobPolicy:     {en: "Policy version", ko: "정책 버전"},
	MsgCCJobAdmitted:   {en: "Accepted", ko: "접수"},
	MsgCCJobStarted:    {en: "Started", ko: "시작"},
	MsgCCJobFinished:   {en: "Finished", ko: "종료"},
	MsgCCJobIdentifier: {en: "Job", ko: "작업"},
	MsgCCJobPR:         {en: "Pull request", ko: "PR"},
	MsgCCJobCommands:   {en: "Commands", ko: "명령"},
	MsgCCJobNoAttempt: {
		en: "No run was registered for this job.",
		ko: "이 작업에 등록된 실행 기록이 없습니다.",
	},
	MsgCCJobLog: {en: "Recorded output", ko: "기록된 출력"},
	MsgCCJobLogCut: {
		en: "Only part of the recorded log is shown here.",
		ko: "기록된 로그의 일부만 여기에 표시합니다.",
	},
	MsgCCJobCancelAsk: {
		en: "A cancellation was recorded. That is a request, not proof that the work stopped.",
		ko: "취소 요청이 기록되었습니다. 요청일 뿐 실제로 멈췄다는 증거는 아닙니다.",
	},

	MsgCCJobStatePending:     {en: "Waiting to be claimed", ko: "대기 중"},
	MsgCCJobStateClaimed:     {en: "Claimed, not started", ko: "가져감, 아직 시작 안 함"},
	MsgCCJobStateStarted:     {en: "Start recorded", ko: "시작 기록됨"},
	MsgCCJobStatePassed:      {en: "Checks passed", ko: "체크 통과"},
	MsgCCJobStateFailed:      {en: "Checks failed", ko: "체크 실패"},
	MsgCCJobStateError:       {en: "A check could not finish", ko: "체크를 끝내지 못했습니다"},
	MsgCCJobStateCancelled:   {en: "Cancelled", ko: "취소됨"},
	MsgCCJobStateIncomplete:  {en: "Incomplete", ko: "완료되지 않음"},
	MsgCCJobStateUnavailable: {en: "The environment was unavailable", ko: "실행 환경을 사용할 수 없었습니다"},
	MsgCCJobStateAmbiguous: {
		en: "It is not known whether this ran",
		ko: "실행 여부를 확인할 수 없습니다",
	},
	MsgCCJobStateInterrupted: {en: "Interrupted before start", ko: "시작 전에 중단됨"},

	MsgCCTriggerPush: {en: "Push", ko: "푸시"},
	MsgCCTriggerPR:   {en: "Pull request", ko: "PR"},

	MsgCCCancel: {en: "Cancel", ko: "취소"},
	MsgCCCancelHelp: {
		en: "Records a cancellation. OwnGit reports what it can actually confirm about stopping the work.",
		ko: "취소를 기록합니다. 작업 중지에 대해서는 실제로 확인된 사실만 보고합니다.",
	},
	MsgCCRerun: {en: "Run again", ko: "다시 실행"},
	MsgCCRerunHelp: {
		en: "Queues one new job for the same commit and the same recorded commands.",
		ko: "같은 커밋과 같은 명령으로 새 작업을 하나 대기열에 넣습니다.",
	},

	// -- runner tokens ---------------------------------------------------
	MsgRTTitle: {en: "Runner tokens", ko: "러너 토큰"},
	MsgRTIntro: {
		en: "A runner you start on another machine authenticates with one of these tokens.",
		ko: "다른 컴퓨터에서 직접 실행하는 러너가 이 토큰으로 인증합니다.",
	},
	MsgRTScope: {
		en: "A token works only for this repository and only for claiming and reporting configured-check work. You can revoke it at any time.",
		ko: "이 토큰은 이 저장소의 설정된 체크 작업을 가져가고 결과를 보고할 때만 쓸 수 있습니다. 언제든 취소할 수 있습니다.",
	},
	MsgRTNotPassword: {
		en: "A token is not a password and not a repository sign-in. It cannot change settings, read other repositories, or act as an administrator.",
		ko: "토큰은 비밀번호도 저장소 로그인도 아닙니다. 설정을 바꾸거나 다른 저장소를 읽거나 관리자로 동작할 수 없습니다.",
	},
	MsgRTNeedPolicy: {
		en: "Save the automatic check settings for this repository first. A runner token works only for saved settings.",
		ko: "먼저 이 저장소의 자동 체크 설정을 저장하세요. 러너 토큰은 저장된 설정에만 쓸 수 있습니다.",
	},

	MsgRTListTitle: {en: "Issued tokens", ko: "발급된 토큰"},
	MsgRTNone:      {en: "No runner token has been issued for this repository.", ko: "이 저장소에 발급된 러너 토큰이 없습니다."},
	MsgRTLabel:     {en: "Label", ko: "이름"},
	MsgRTLabelHelp: {
		en: "Name the machine that will run the checks, so you know what you are revoking later.",
		ko: "나중에 무엇을 취소하는지 알 수 있도록 체크를 실행할 컴퓨터 이름을 적어 두세요.",
	},
	MsgRTIssue: {en: "Issue a token", ko: "토큰 발급"},
	MsgRTIssueHelp: {
		en: "The value is shown once, right after it is issued.",
		ko: "토큰 값은 발급 직후 한 번만 표시됩니다.",
	},
	MsgRTRevoke: {en: "Revoke", ko: "취소"},
	MsgRTRevHelp: {
		en: "The runner stops being able to claim or report with it. An unstarted claim is interrupted; results already recorded stay.",
		ko: "러너는 이 토큰으로 작업을 가져가거나 보고할 수 없게 됩니다. 아직 시작하지 않은 점유는 중단되고, 이미 기록된 결과는 남습니다.",
	},
	MsgRTActive:    {en: "Active", ko: "사용 중"},
	MsgRTRevoked:   {en: "Revoked", ko: "취소됨"},
	MsgRTCreatedAt: {en: "Issued", ko: "발급"},
	MsgRTLastUsed:  {en: "Last used", ko: "마지막 사용"},
	MsgRTNeverUsed: {en: "Never used", ko: "사용한 적 없음"},
	MsgRTRevokedAt: {en: "Revoked", ko: "취소"},
	MsgRTGen:       {en: "Generation", ko: "세대"},

	MsgRTTokenTitle: {en: "Copy this now", ko: "지금 토큰을 복사하세요"},
	MsgRTTokenOnce: {
		en: "This is the only time OwnGit shows this token. It keeps only a verifier, so leaving this page loses the value for good.",
		ko: "OwnGit이 이 토큰을 보여주는 것은 이번 한 번뿐입니다. 검증용 값만 저장하므로 이 화면을 떠나면 값은 복구할 수 없습니다.",
	},
	MsgRTTokenLabel: {en: "Runner token", ko: "러너 토큰"},
	MsgRTTokenStore: {
		en: "Save it to a file on the runner machine and pass that file with --token-file. Do not type it on a command line.",
		ko: "러너 컴퓨터의 파일에 저장한 뒤 --token-file로 그 파일을 전달하세요. 명령줄에 직접 입력하지 마세요.",
	},

	MsgRTConnectTitle: {en: "Connecting the runner", ko: "러너 연결 방법"},
	MsgRTConnectHelp: {
		en: "Run this on the machine that will execute the checks. It stays in the foreground and runs the repository's commands as that machine's account.",
		ko: "체크를 실행할 컴퓨터에서 아래 명령을 실행하세요. 포그라운드로 계속 실행되며, 저장소의 명령을 그 컴퓨터의 계정 권한으로 실행합니다.",
	},
	MsgRTConnectTLS: {
		en: "Runner commands require HTTPS. For a private certificate authority add --ca-file, which extends the trusted roots without turning certificate or host name checking off.",
		ko: "러너 명령은 HTTPS를 요구합니다. 사설 인증기관을 쓴다면 --ca-file을 추가하세요. 인증서와 호스트 이름 검증을 끄지 않고 신뢰 루트만 넓힙니다.",
	},
	MsgRTConnectLocal: {
		en: "Plain HTTP is accepted only for a loopback address you confirm with --accept-insecure-http, which is for local testing rather than another machine.",
		ko: "일반 HTTP는 --accept-insecure-http로 직접 확인한 루프백 주소에서만 허용됩니다. 다른 컴퓨터 연결이 아니라 로컬 테스트용입니다.",
	},
	MsgRTConnectNoSvc: {
		en: "OwnGit does not install a service or start a runner for you.",
		ko: "OwnGit이 서비스를 설치하거나 러너를 대신 실행하지 않습니다.",
	},

	// -- results ---------------------------------------------------------
	// Two results, because saving a changed policy clears consent while an
	// unchanged resubmission leaves it exactly as it was.
	//
	// The changed-policy sentence reports the resulting state without claiming
	// it was already that state. Saving a changed policy revokes an active
	// consent, so "still off" would be false for exactly the operator whose
	// running checks just stopped being authorized.
	MsgCCSaved: {en: "Settings saved. Checks are off.", ko: "설정을 저장했습니다. 체크는 꺼져 있습니다."},
	MsgCCSavedEnabled: {
		en: "Settings saved. Nothing changed, so checks stay on.",
		ko: "설정을 저장했습니다. 바뀐 내용이 없어 체크는 켜진 상태로 유지됩니다.",
	},
	MsgCCEnabled: {en: "Checks are on for the saved settings.", ko: "저장된 설정으로 체크를 켰습니다."},
	MsgCCDisabled: {
		en: "Checks are off. No new jobs will start.",
		ko: "체크를 껐습니다. 새 작업을 시작하지 않습니다.",
	},
	MsgCCConsentRevoked: {
		en: "The settings changed, so checks were turned off.",
		ko: "설정이 바뀌어 체크가 꺼졌습니다.",
	},
	MsgCCJobCancelled: {en: "A cancellation was recorded.", ko: "취소를 기록했습니다."},
	// A cancel that arrives after the work finished is recorded, but it stops
	// nothing and changes no result. Reporting it as a cancellation would
	// leave the reader expecting the outcome to change.
	MsgCCJobAlreadyFinished: {
		en: "This job had already finished, so the request was recorded but no result changed.",
		ko: "이 작업은 이미 끝난 상태여서 요청만 기록되고 결과는 바뀌지 않았습니다.",
	},
	MsgCCJobRerunQueued: {
		en: "A new job was queued for the same commit.",
		ko: "같은 커밋으로 새 작업을 대기열에 넣었습니다.",
	},
	MsgCCJobRerunExisting: {
		en: "A rerun is already outstanding, so no second one was queued.",
		ko: "이미 진행 중인 재실행이 있어 새로 추가하지 않았습니다.",
	},
	MsgCCJobRefused: {
		en: "The job is not in a state that allows this.",
		ko: "이 작업은 지금 그 동작을 할 수 있는 상태가 아닙니다.",
	},
	MsgCCJobMissing: {en: "That job does not exist in this repository.", ko: "이 저장소에 그런 작업이 없습니다."},
	MsgCCPolicyRefused: {
		en: "OwnGit refused these settings. The reason is shown beside the field it belongs to.",
		ko: "이 설정을 저장하지 못했습니다. 이유는 해당 항목 옆에 표시됩니다.",
	},
	MsgCCPolicyMissing: {en: "No settings are saved for this repository yet.", ko: "이 저장소에 저장된 설정이 아직 없습니다."},
	MsgCCPolicyStale: {
		en: "The saved settings changed while this page was open. Check the current values before turning checks on.",
		ko: "이 화면을 열어 둔 사이에 저장된 설정이 바뀌었습니다. 체크를 켜기 전에 현재 값을 확인하세요.",
	},
	MsgCCFailed: {en: "The change could not be completed.", ko: "변경을 완료하지 못했습니다."},

	MsgCCNumberInvalid: {en: "Enter a number, such as 10 or 1.5.", ko: "10이나 1.5처럼 숫자로 입력하세요."},
	MsgCCNumberFraction: {
		en: "This amount is not a whole number of milliseconds or bytes. Use fewer decimal places or a smaller unit.",
		ko: "밀리초나 바이트 단위로 나누어떨어지지 않는 값입니다. 소수 자릿수를 줄이거나 더 작은 단위를 고르세요.",
	},
	MsgCCNumberWhole: {en: "Enter a whole number.", ko: "정수로 입력하세요."},
	MsgCCNumberCores: {
		en: "CPU is set in steps of a thousandth of a core. Use at most three decimal places, such as 1.5 or 0.25.",
		ko: "CPU는 1000분의 1코어 단위로 정합니다. 1.5나 0.25처럼 소수 셋째 자리까지만 쓰세요.",
	},
	MsgCCEventsInvalid:   {en: "Choose at least one event.", ko: "실행 시점을 하나 이상 선택하세요."},
	MsgCCExecutorInvalid: {en: "Choose where checks run.", ko: "체크를 실행할 위치를 선택하세요."},
	MsgCCImageInvalid: {
		en: "Enter an immutable image reference with a sha256 digest.",
		ko: "sha256 다이제스트가 포함된, 변하지 않는 이미지 참조를 입력하세요.",
	},
	MsgCCNetworkInvalid: {en: "Choose a network option.", ko: "네트워크 옵션을 선택하세요."},

	MsgCCFieldRange: {
		en: "This value is outside the accepted range shown with this field.",
		ko: "이 항목에 안내된 허용 범위를 벗어난 값입니다.",
	},
	// This field's floor moves with the file limit above it, so the message
	// names that relationship. The maximum is shown with the field, like every
	// other numeric limit.
	MsgCCTotalBytesRange: {
		en: "The total must be at least the largest file size above it, and within the range shown with this field.",
		ko: "전체 파일 크기는 위의 가장 큰 파일 크기 이상이며, 이 항목에 안내된 범위 안이어야 합니다.",
	},
	MsgCCFieldRequired: {en: "This field is required.", ko: "이 항목은 필수입니다."},
	MsgCCFieldUnknown: {
		en: "This value is not one this version accepts.",
		ko: "이 버전이 받아들이는 값이 아닙니다.",
	},
	MsgCCFieldFormat: {
		en: "This value is not written in an accepted form.",
		ko: "허용되는 형식으로 작성된 값이 아닙니다.",
	},
	MsgCCFieldNotApplicable: {
		en: "This setting applies only to a Docker container. Clear it or choose the container under \"Where checks run\".",
		ko: "Docker 컨테이너에만 쓰는 설정입니다. 비우거나 \"체크를 실행할 곳\"에서 컨테이너를 고르세요.",
	},
	MsgCCFieldDuplicate: {en: "This value repeats an earlier one.", ko: "앞서 입력한 값과 중복됩니다."},
	MsgCCImageRequired: {
		en: "The container mode needs an image. Enter an immutable reference.",
		ko: "컨테이너 모드에는 이미지가 필요합니다. 변하지 않는 참조를 입력하세요.",
	},
	MsgCCRuntimeUnsupported: {
		en: "This release runs containers only through the local Docker runtime.",
		ko: "이 버전은 로컬 Docker 런타임으로만 컨테이너를 실행합니다.",
	},

	MsgRTIssued:      {en: "The runner token was issued.", ko: "러너 토큰을 발급했습니다."},
	MsgRTRevokedDone: {en: "The runner token was revoked.", ko: "러너 토큰을 취소했습니다."},
	MsgRTNotFound: {
		en: "That token was not found, or it was already revoked.",
		ko: "그 토큰을 찾지 못했거나 이미 취소되었습니다.",
	},
	MsgRTLabelInvalid: {
		en: "Enter a single-line label between 1 and 100 UTF-8 bytes.",
		ko: "한 줄 이름을 UTF-8 기준 100바이트 이내로 입력하세요. 한글만 쓰면 최대 33자입니다.",
	},
	MsgRTFailed: {en: "The runner token could not be issued.", ko: "러너 토큰을 발급하지 못했습니다."},
	MsgRTExisting: {
		en: "This request was already handled, so the existing token was kept and no new value was created.",
		ko: "이미 처리된 요청이어서 기존 토큰을 유지했고 새 값을 만들지 않았습니다.",
	},
}

// init merges the entries above into the shared catalog. A duplicate code is a
// mistake in this package, so it fails at startup rather than letting one
// definition silently win.
func init() {
	for code, entry := range configuredCheckCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}
