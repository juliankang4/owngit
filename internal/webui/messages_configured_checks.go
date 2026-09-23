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

	MsgCCStateTitle     MessageCode = "cc.state.title"
	MsgCCStatePolicy    MessageCode = "cc.state.policy"
	MsgCCStateConsent   MessageCode = "cc.state.consent"
	MsgCCStateRuntime   MessageCode = "cc.state.runtime"
	MsgCCPolicyNone     MessageCode = "cc.state.policy_none"
	MsgCCPolicySaved    MessageCode = "cc.state.policy_saved"
	MsgCCPolicyLegacy   MessageCode = "cc.state.policy_legacy"
	MsgCCConsentOn      MessageCode = "cc.state.consent_on"
	MsgCCConsentOff     MessageCode = "cc.state.consent_off"
	MsgCCConsentCleared MessageCode = "cc.state.consent_cleared"
	MsgCCRuntimeOK      MessageCode = "cc.state.runtime_ok"
	MsgCCRuntimeDown    MessageCode = "cc.state.runtime_down"
	MsgCCRuntimeSep     MessageCode = "cc.state.runtime_separate"
	MsgCCRuntimeWork    MessageCode = "cc.state.runtime_workspace"
	MsgCCRuntimeRestart MessageCode = "cc.state.runtime_restart"
	MsgCCRuntimeOther   MessageCode = "cc.state.runtime_other"
	MsgCCRuntimeRepair  MessageCode = "cc.state.runtime_repair"
	MsgCCPolicyVersion  MessageCode = "cc.state.version"
	MsgCCPolicyDigest   MessageCode = "cc.state.digest"
	MsgCCPolicyUpdated  MessageCode = "cc.state.updated_at"

	MsgCCPolicyTitle    MessageCode = "cc.policy.title"
	MsgCCPolicyHelp     MessageCode = "cc.policy.help"
	MsgCCPolicyKeep     MessageCode = "cc.policy.keep"
	MsgCCExecutor       MessageCode = "cc.policy.executor"
	MsgCCExecHost       MessageCode = "cc.policy.executor.host"
	MsgCCExecHostHelp   MessageCode = "cc.policy.executor.host_help"
	MsgCCExecCont       MessageCode = "cc.policy.executor.container"
	MsgCCExecContHelp   MessageCode = "cc.policy.executor.container_help"
	MsgCCExecRunner     MessageCode = "cc.policy.executor.runner"
	MsgCCExecRunnerHelp MessageCode = "cc.policy.executor.runner_help"
	MsgCCNoFallback     MessageCode = "cc.policy.no_fallback"

	MsgCCEvents     MessageCode = "cc.policy.events"
	MsgCCEventPush  MessageCode = "cc.policy.events.push"
	MsgCCEventPR    MessageCode = "cc.policy.events.pull_request"
	MsgCCEventsHelp MessageCode = "cc.policy.events.help"

	MsgCCLimits      MessageCode = "cc.policy.limits"
	MsgCCTimeout     MessageCode = "cc.policy.timeout"
	MsgCCTimeoutHelp MessageCode = "cc.policy.timeout_help"
	MsgCCOutput      MessageCode = "cc.policy.output"
	MsgCCOutputHelp  MessageCode = "cc.policy.output_help"
	MsgCCQueue       MessageCode = "cc.policy.queue"
	MsgCCActive      MessageCode = "cc.policy.active"
	MsgCCLease       MessageCode = "cc.policy.lease"
	MsgCCLeaseHelp   MessageCode = "cc.policy.lease_help"

	MsgCCSource         MessageCode = "cc.policy.source"
	MsgCCSourceHelp     MessageCode = "cc.policy.source_help"
	MsgCCSrcEntries     MessageCode = "cc.policy.source.entries"
	MsgCCSrcFileBytes   MessageCode = "cc.policy.source.file_bytes"
	MsgCCSrcTotalBytes  MessageCode = "cc.policy.source.total_bytes"
	MsgCCSrcDepth       MessageCode = "cc.policy.source.depth"
	MsgCCSrcPathBytes   MessageCode = "cc.policy.source.path_bytes"
	MsgCCSrcNameBytes   MessageCode = "cc.policy.source.name_bytes"
	MsgCCSrcMetaBytes   MessageCode = "cc.policy.source.metadata_bytes"
	MsgCCSrcDefaultHelp MessageCode = "cc.policy.source.default_help"

	MsgCCContainer      MessageCode = "cc.policy.container"
	MsgCCContainerHelp  MessageCode = "cc.policy.container_help"
	MsgCCImage          MessageCode = "cc.policy.container.image"
	MsgCCImageHelp      MessageCode = "cc.policy.container.image_help"
	MsgCCRuntimeName    MessageCode = "cc.policy.container.runtime"
	MsgCCNetwork        MessageCode = "cc.policy.container.network"
	MsgCCNetworkNone    MessageCode = "cc.policy.container.network_none"
	MsgCCNetworkBridge  MessageCode = "cc.policy.container.network_bridge"
	MsgCCNetworkHelp    MessageCode = "cc.policy.container.network_help"
	MsgCCCPU            MessageCode = "cc.policy.container.cpu"
	MsgCCMemory         MessageCode = "cc.policy.container.memory"
	MsgCCPIDs           MessageCode = "cc.policy.container.pids"
	MsgCCScratch        MessageCode = "cc.policy.container.scratch"
	MsgCCNoDiskQuota    MessageCode = "cc.policy.container.no_disk_quota"
	MsgCCContainerTrust MessageCode = "cc.policy.container.trust"

	MsgCCSave          MessageCode = "cc.policy.save"
	MsgCCSaveHelp      MessageCode = "cc.policy.save_help"
	MsgCCEnable        MessageCode = "cc.consent.enable"
	MsgCCEnableHelp    MessageCode = "cc.consent.enable_help"
	MsgCCDisable       MessageCode = "cc.consent.disable"
	MsgCCDisableHelp   MessageCode = "cc.consent.disable_help"
	MsgCCConsentTitle  MessageCode = "cc.consent.title"
	MsgCCPasswordEach  MessageCode = "cc.password_each_time"
	MsgCCEnableBlocked MessageCode = "cc.consent.blocked"
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
	MsgRTOpen        MessageCode = "runner.open"
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

	MsgCCNumberInvalid MessageCode = "cc.result.invalid_number"

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
	MsgCCTitle: {en: "Configured checks", ko: "설정된 체크"},
	MsgCCTab:   {en: "Configured checks", ko: "설정된 체크"},
	MsgCCIntro: {
		en: "OwnGit runs the commands this repository commits in .owngit/checks.json, after you save an execution policy and turn execution on.",
		ko: "이 저장소가 .owngit/checks.json에 커밋한 명령을 OwnGit이 실행합니다. 실행 정책을 저장하고 실행을 켠 뒤에만 동작합니다.",
	},
	MsgCCOpen: {en: "Configured checks", ko: "설정된 체크"},
	MsgCCManual: {
		en: "This is separate from the manual check helper. Helper tokens and the results a helper reports are unchanged.",
		ko: "수동 체크 에이전트와는 별개입니다. 에이전트 토큰과 에이전트가 보고한 결과는 그대로 유지됩니다.",
	},
	MsgCCAdvisory: {
		en: "Results are information. They never stop you from creating a pull request, viewing changes, or merging.",
		ko: "결과는 참고 정보입니다. PR 생성, 변경 내용 보기, 병합을 막지 않습니다.",
	},
	MsgCCWorkflow: {
		en: "Without a committed .owngit/checks.json at the observed commit, nothing runs and no result is recorded.",
		ko: "해당 커밋에 .owngit/checks.json이 없으면 아무것도 실행하지 않고 결과도 기록하지 않습니다.",
	},

	MsgCCStateTitle:  {en: "Current state", ko: "현재 상태"},
	MsgCCStatePolicy: {en: "Saved policy", ko: "저장된 정책"},
	MsgCCStateConsent: {
		en: "Execution",
		ko: "실행 허용",
	},
	MsgCCStateRuntime: {en: "Runtime", ko: "실행 환경"},
	MsgCCPolicyNone: {
		en: "No policy is saved for this repository.",
		ko: "이 저장소에 저장된 정책이 없습니다.",
	},
	MsgCCPolicySaved: {en: "Saved", ko: "저장됨"},
	MsgCCPolicyLegacy: {
		en: "This policy was restored without its execution settings. Save a complete policy before turning execution on.",
		ko: "이 정책은 실행 설정 없이 복원되었습니다. 실행을 켜기 전에 완전한 정책을 다시 저장하세요.",
	},
	MsgCCConsentOn:  {en: "Enabled", ko: "켜짐"},
	MsgCCConsentOff: {en: "Not enabled", ko: "꺼짐"},
	MsgCCConsentCleared: {
		en: "Changing the policy turns execution off. Turn it on again after checking the new settings.",
		ko: "정책을 바꾸면 실행이 꺼집니다. 새 설정을 확인한 뒤 다시 켜세요.",
	},
	MsgCCRuntimeOK:   {en: "Available", ko: "사용 가능"},
	MsgCCRuntimeDown: {en: "Unavailable", ko: "사용 불가"},
	MsgCCRuntimeSep: {
		en: "An unavailable runtime is not a check result. Ordinary Git, pull requests, and merging keep working.",
		ko: "실행 환경을 쓸 수 없는 것은 체크 결과가 아닙니다. 일반 Git 사용, PR, 병합은 그대로 동작합니다.",
	},
	MsgCCRuntimeWork: {
		en: "OwnGit could not take ownership of its private check workspace.",
		ko: "OwnGit이 체크 전용 작업 폴더의 소유권을 확보하지 못했습니다.",
	},
	MsgCCRuntimeRestart: {
		en: "OwnGit could not reconcile work left by an earlier run.",
		ko: "이전 실행이 남긴 작업을 정리하지 못했습니다.",
	},
	MsgCCRuntimeOther: {
		en: "OwnGit reported a condition this screen has no words for. The recorded code is shown beside it.",
		ko: "이 화면이 설명할 수 없는 상태를 보고했습니다. 기록된 코드를 옆에 표시합니다.",
	},
	MsgCCRuntimeRepair: {
		en: "Repair the environment and restart OwnGit. There is no background retry and no switch to another mode.",
		ko: "환경을 고친 뒤 OwnGit을 다시 시작하세요. 자동 재시도나 다른 모드로의 전환은 없습니다.",
	},
	MsgCCPolicyVersion: {en: "Policy version", ko: "정책 버전"},
	MsgCCPolicyDigest:  {en: "Policy identity", ko: "정책 식별자"},
	MsgCCPolicyUpdated: {en: "Updated", ko: "변경"},

	// -- the policy form ------------------------------------------------
	MsgCCPolicyTitle: {en: "Execution policy", ko: "실행 정책"},
	MsgCCPolicyHelp: {
		en: "These settings belong to you, not to the repository. A committed workflow cannot change the mode, the image, the network, or these limits.",
		ko: "이 설정은 저장소 파일이 아니라 관리자의 것입니다. 커밋된 워크플로 파일은 모드, 이미지, 네트워크, 아래 한도를 바꿀 수 없습니다.",
	},
	MsgCCPolicyKeep: {
		en: "Fields you do not change keep their saved values.",
		ko: "바꾸지 않은 항목은 저장된 값을 그대로 유지합니다.",
	},
	MsgCCExecutor: {en: "Where checks run", ko: "체크 실행 위치"},
	MsgCCExecHost: {en: "This computer, as the OwnGit account", ko: "이 컴퓨터에서 OwnGit 계정으로"},
	MsgCCExecHostHelp: {
		en: "Commands reach everything that account can reach, including OwnGit's own files. This is not a sandbox. Use it only for repositories and commands you trust completely.",
		ko: "명령이 이 계정으로 접근할 수 있는 모든 것에 접근합니다. OwnGit 자체 파일도 포함됩니다. 격리 실행이 아닙니다. 완전히 신뢰하는 저장소와 명령에만 사용하세요.",
	},
	MsgCCExecCont: {en: "Restricted local Docker container", ko: "제한된 로컬 Docker 컨테이너"},
	MsgCCExecContHelp: {
		en: "Commands run in the image you pin, as a nonroot user, with a read-only root filesystem and the CPU, memory, and process limits below. Only the exact source is mounted. What this actually restricts depends on the local Docker daemon, the kernel, and the image you chose.",
		ko: "지정한 이미지에서 비루트 사용자로 실행하며, 루트 파일시스템은 읽기 전용이고 아래의 CPU, 메모리, 프로세스 한도가 적용됩니다. 마운트되는 것은 해당 커밋의 소스뿐입니다. 실제 제한 수준은 로컬 Docker 데몬, 커널, 선택한 이미지에 달려 있습니다.",
	},
	MsgCCExecRunner: {en: "A separately connected runner", ko: "별도로 연결한 러너"},
	MsgCCExecRunnerHelp: {
		en: "A runner you start on another machine claims the work and reports what protection it has. OwnGit relays that report; it does not verify it.",
		ko: "다른 컴퓨터에서 직접 실행한 러너가 작업을 가져가고, 자신이 어떤 보호 상태인지 보고합니다. OwnGit은 그 보고를 전달할 뿐 검증하지 않습니다.",
	},
	MsgCCNoFallback: {
		en: "OwnGit never moves work to another mode. A missing runtime is recorded as unavailable, not as a pass.",
		ko: "OwnGit은 다른 모드로 옮겨 실행하지 않습니다. 실행 환경이 없으면 사용 불가로 기록하며 통과로 처리하지 않습니다.",
	},

	MsgCCEvents:    {en: "When to run", ko: "실행 시점"},
	MsgCCEventPush: {en: "After a push", ko: "푸시 후"},
	MsgCCEventPR:   {en: "On pull request activity", ko: "PR 활동 시"},
	MsgCCEventsHelp: {
		en: "Choose at least one. The committed workflow can narrow these further by branch; it cannot widen them.",
		ko: "하나 이상 선택하세요. 커밋된 워크플로 파일은 브랜치로 범위를 더 좁힐 수 있을 뿐 넓힐 수는 없습니다.",
	},

	MsgCCLimits:  {en: "Limits", ko: "실행 한도"},
	MsgCCTimeout: {en: "Time limit per check (milliseconds)", ko: "체크당 시간 제한 (밀리초)"},
	MsgCCTimeoutHelp: {
		en: "A workflow may ask for less, never more.",
		ko: "워크플로 파일은 더 짧게만 요청할 수 있습니다.",
	},
	MsgCCOutput: {en: "Captured output per check (bytes)", ko: "체크당 출력 저장 한도 (바이트)"},
	MsgCCOutputHelp: {
		en: "Output beyond this limit is cut and the result says so.",
		ko: "이 한도를 넘는 출력은 잘리며 결과에 그 사실을 표시합니다.",
	},
	MsgCCQueue:  {en: "Queued jobs per repository", ko: "저장소당 대기 작업 수"},
	MsgCCActive: {en: "Jobs running at once", ko: "동시에 실행할 작업 수"},
	MsgCCLease:  {en: "Claim lease (milliseconds)", ko: "작업 점유 시간 (밀리초)"},
	MsgCCLeaseHelp: {
		en: "A runner renews its claim within this window.",
		ko: "러너는 이 시간 안에 점유를 갱신합니다.",
	},

	MsgCCSource: {en: "Source snapshot bounds", ko: "소스 스냅샷 한도"},
	MsgCCSourceHelp: {
		en: "The exact committed bytes are copied into a private workspace within these bounds. A repository past them is reported as unavailable rather than partly copied.",
		ko: "해당 커밋의 파일을 이 한도 안에서 전용 작업 폴더로 복사합니다. 한도를 넘는 저장소는 일부만 복사하지 않고 사용 불가로 보고합니다.",
	},
	MsgCCSrcEntries:    {en: "Files", ko: "파일 수"},
	MsgCCSrcFileBytes:  {en: "Bytes per file", ko: "파일당 바이트"},
	MsgCCSrcTotalBytes: {en: "Bytes in total", ko: "전체 바이트"},
	MsgCCSrcDepth:      {en: "Path depth", ko: "경로 깊이"},
	MsgCCSrcPathBytes:  {en: "Bytes per path", ko: "경로당 바이트"},
	MsgCCSrcNameBytes:  {en: "Bytes per name", ko: "이름당 바이트"},
	MsgCCSrcMetaBytes:  {en: "Bytes of listing metadata", ko: "목록 메타데이터 바이트"},
	MsgCCSrcDefaultHelp: {
		en: "Leave a field empty to use OwnGit's default bound.",
		ko: "비워 두면 OwnGit의 기본 한도를 사용합니다.",
	},

	MsgCCContainer: {en: "Container settings", ko: "컨테이너 설정"},
	MsgCCContainerHelp: {
		en: "Used only by the container mode. OwnGit never pulls an image for you.",
		ko: "컨테이너 모드에서만 사용합니다. OwnGit이 이미지를 대신 내려받지 않습니다.",
	},
	MsgCCImage: {en: "Image", ko: "이미지"},
	MsgCCImageHelp: {
		en: "An immutable reference: sha256: followed by 64 hex digits, or a name with @sha256: and 64 hex digits. The image must already be present on this machine.",
		ko: "변하지 않는 참조를 입력하세요. sha256: 뒤에 16진수 64자리, 또는 이름 뒤에 @sha256:과 16진수 64자리를 붙인 형식입니다. 이미지는 이 컴퓨터에 이미 있어야 합니다.",
	},
	MsgCCRuntimeName:   {en: "Runtime", ko: "런타임"},
	MsgCCNetwork:       {en: "Network", ko: "네트워크"},
	MsgCCNetworkNone:   {en: "No network", ko: "네트워크 없음"},
	MsgCCNetworkBridge: {en: "Docker bridge network", ko: "Docker 브리지 네트워크"},
	MsgCCNetworkHelp: {
		en: "Allowing a network is your decision and is recorded with the job.",
		ko: "네트워크 허용은 관리자의 선택이며 작업 기록에 함께 남습니다.",
	},
	MsgCCCPU:     {en: "CPU (millicores)", ko: "CPU (밀리코어)"},
	MsgCCMemory:  {en: "Memory (bytes)", ko: "메모리 (바이트)"},
	MsgCCPIDs:    {en: "Processes", ko: "프로세스 수"},
	MsgCCScratch: {en: "Temporary space (bytes)", ko: "임시 공간 (바이트)"},
	MsgCCNoDiskQuota: {
		en: "The mounted source uses the host volume's disk space. There is no separate total disk quota for it.",
		ko: "마운트된 소스는 호스트 볼륨의 디스크 공간을 사용합니다. 이 영역에 대한 별도의 전체 디스크 할당량은 없습니다.",
	},
	MsgCCContainerTrust: {
		en: "Anyone who can control the local Docker daemon can generally reach this machine as an administrator, whatever a single container allows.",
		ko: "로컬 Docker 데몬을 제어할 수 있는 사람은 컨테이너 설정과 무관하게 이 컴퓨터를 관리자 수준으로 다룰 수 있습니다.",
	},

	MsgCCSave: {en: "Save policy", ko: "정책 저장"},
	MsgCCSaveHelp: {
		en: "Saving stores the settings. It does not start anything.",
		ko: "저장은 설정만 기록합니다. 실행이 시작되지는 않습니다.",
	},
	MsgCCConsentTitle: {en: "Turn execution on or off", ko: "실행 켜고 끄기"},
	MsgCCEnable:       {en: "Turn execution on", ko: "실행 켜기"},
	MsgCCEnableHelp: {
		en: "This binds your permission to the exact policy above. Changing the policy withdraws it.",
		ko: "위에 저장된 정책 그대로에 대해서만 실행을 허용합니다. 정책을 바꾸면 허용이 취소됩니다.",
	},
	MsgCCDisable: {en: "Turn execution off", ko: "실행 끄기"},
	MsgCCDisableHelp: {
		en: "New work stops being claimed. Work already running is asked to stop; OwnGit reports what it can confirm.",
		ko: "새 작업을 더 이상 가져가지 않습니다. 이미 실행 중인 작업에는 중지를 요청하며, OwnGit은 확인된 사실만 보고합니다.",
	},
	MsgCCPasswordEach: {
		en: "Saving, enabling, disabling, cancelling, and rerunning each ask for your current administrator password. Being signed in is not enough.",
		ko: "저장, 실행 켜기와 끄기, 취소, 다시 실행은 매번 현재 관리자 비밀번호를 확인합니다. 로그인만으로는 부족합니다.",
	},
	MsgCCEnableBlocked: {
		en: "Save a complete policy first. There is nothing to enable yet.",
		ko: "먼저 완전한 정책을 저장하세요. 아직 켤 대상이 없습니다.",
	},

	// -- jobs -----------------------------------------------------------
	MsgCCJobsTitle: {en: "Recent jobs", ko: "최근 작업"},
	MsgCCJobsHelp: {
		en: "Each job names the exact commit it was pinned to and the mode that actually applied to it.",
		ko: "각 작업은 어떤 커밋에 고정되었는지와 실제로 적용된 실행 모드를 함께 기록합니다.",
	},
	MsgCCJobsNone: {en: "No job has been recorded for this repository.", ko: "이 저장소에 기록된 작업이 없습니다."},
	MsgCCJobsNoneHelp: {
		en: "A job appears after a matching workflow is committed and execution is on.",
		ko: "조건에 맞는 워크플로 파일이 커밋되고 실행이 켜져 있으면 작업이 나타납니다.",
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
	MsgRTOpen:  {en: "Runner tokens", ko: "러너 토큰"},
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
		en: "Save an execution policy for this repository first. Runner authority is scoped to it.",
		ko: "먼저 이 저장소의 실행 정책을 저장하세요. 러너 권한은 그 정책에 묶입니다.",
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
	MsgCCSaved: {en: "Policy saved. Execution is off.", ko: "정책을 저장했습니다. 실행은 꺼져 있습니다."},
	MsgCCSavedEnabled: {
		en: "Policy saved. It is unchanged, so execution stays on.",
		ko: "정책을 저장했습니다. 내용이 같아서 실행은 켜진 상태로 유지됩니다.",
	},
	MsgCCEnabled: {en: "Execution is on for the saved policy.", ko: "저장된 정책에 대해 실행을 켰습니다."},
	MsgCCDisabled: {
		en: "Execution is off. New work will not be claimed.",
		ko: "실행을 껐습니다. 새 작업을 가져가지 않습니다.",
	},
	MsgCCConsentRevoked: {
		en: "The policy changed, so execution was turned off.",
		ko: "정책이 바뀌어 실행이 꺼졌습니다.",
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
	MsgCCPolicyMissing: {en: "No policy is saved for this repository yet.", ko: "이 저장소에 저장된 정책이 아직 없습니다."},
	MsgCCPolicyStale: {
		en: "The saved policy changed while this page was open. Check the current values before enabling.",
		ko: "이 화면을 열어 둔 사이에 저장된 정책이 바뀌었습니다. 켜기 전에 현재 값을 확인하세요.",
	},
	MsgCCFailed: {en: "The change could not be completed.", ko: "변경을 완료하지 못했습니다."},

	MsgCCNumberInvalid:   {en: "Enter a whole number within the stated range.", ko: "표시된 범위 안의 정수를 입력하세요."},
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
		en: "The total must be at least the per-file limit above it, and within the range shown with this field.",
		ko: "전체 크기는 위의 파일당 제한 이상이며, 이 항목에 안내된 범위 안이어야 합니다.",
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
		en: "This setting does not apply to the selected mode. Clear it or choose the container mode.",
		ko: "선택한 모드에는 적용되지 않는 설정입니다. 비우거나 컨테이너 모드를 선택하세요.",
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
