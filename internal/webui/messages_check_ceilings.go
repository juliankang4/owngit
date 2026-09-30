package webui

// The check ceilings on the Settings Repositories tab: this computer's upper
// bounds for what a repository's check policy may choose.
const (
	MsgCeilingsTitle       MessageCode = "ceilings.title"
	MsgCeilingsScope       MessageCode = "ceilings.scope"
	MsgCeilingsChange      MessageCode = "ceilings.change"
	MsgCeilingsTimeout     MessageCode = "ceilings.timeout"
	MsgCeilingsTimeoutHelp MessageCode = "ceilings.timeout_help"
	MsgCeilingsOutput      MessageCode = "ceilings.output"
	MsgCeilingsOutputHelp  MessageCode = "ceilings.output_help"
	MsgCeilingsQueue       MessageCode = "ceilings.queue"
	MsgCeilingsQueueHelp   MessageCode = "ceilings.queue_help"
	MsgCeilingsActive      MessageCode = "ceilings.active"
	MsgCeilingsActiveHelp  MessageCode = "ceilings.active_help"
	MsgCeilingsCPU         MessageCode = "ceilings.cpu"
	MsgCeilingsCPUHelp     MessageCode = "ceilings.cpu_help"
	MsgCeilingsMemory      MessageCode = "ceilings.memory"
	MsgCeilingsMemoryHelp  MessageCode = "ceilings.memory_help"
	MsgCeilingsPIDs        MessageCode = "ceilings.pids"
	MsgCeilingsPIDsHelp    MessageCode = "ceilings.pids_help"
	MsgCeilingsScratch     MessageCode = "ceilings.scratch"
	MsgCeilingsScratchHelp MessageCode = "ceilings.scratch_help"
	MsgCeilingsSource      MessageCode = "ceilings.source"
	MsgCeilingsSourceHelp  MessageCode = "ceilings.source_help"
	MsgCeilingsWarning     MessageCode = "ceilings.warning"
	MsgCeilingsSaved       MessageCode = "ceilings.saved"
	MsgCeilingsAbove       MessageCode = "ceilings.above"

	// MsgCCFieldCeiling is a check policy value above a ceiling, and
	// MsgCCPolicyAboveCeilings the checks page of a saved policy above one.
	MsgCCFieldCeiling        MessageCode = "cc.result.field_ceiling"
	MsgCCPolicyAboveCeilings MessageCode = "cc.policy.above_ceilings"
	MsgCCRunAboveCeilings    MessageCode = "cc.result.run_above_ceilings"
	MsgCCCeilingsUnreadable  MessageCode = "cc.result.ceilings_unreadable"
)

var checkCeilingsCatalog = map[MessageCode]message{
	MsgCeilingsTitle: {en: "Check ceilings", ko: "체크 상한"},
	MsgCeilingsScope: {
		en: "The most a repository's check policy may choose on this computer. A ceiling never changes a saved policy. A policy above a lowered ceiling starts no new checks until you raise the ceiling or lower the policy; checks already queued run with their own limits.",
		ko: "저장소의 체크 설정이 이 컴퓨터에서 고를 수 있는 최댓값입니다. 상한을 바꿔도 저장된 설정은 바뀌지 않습니다. 상한을 낮춰 설정이 그보다 커지면 상한을 올리거나 설정을 줄일 때까지 새 체크를 시작하지 않으며, 이미 대기 중인 체크는 원래 한도로 실행됩니다.",
	},
	MsgCeilingsChange:  {en: "Change the ceilings", ko: "상한 바꾸기"},
	MsgCeilingsTimeout: {en: "Time for one check", ko: "체크 하나의 시간"},
	MsgCeilingsTimeoutHelp: {
		en: "From 1 second to 168 hours (7 days); the default is 24 hours.",
		ko: "1초부터 168시간(7일)까지 정할 수 있고 기본값은 24시간입니다.",
	},
	MsgCeilingsOutput: {en: "Output of one check", ko: "체크 하나의 출력"},
	MsgCeilingsOutputHelp: {
		en: "How much one check may print. A check that prints more is stopped and ends as incomplete, and OwnGit stores only the first part of any output. From 1 KB to 1 GB; the default is 64 MB.",
		ko: "체크 하나가 출력할 수 있는 양입니다. 이보다 많이 출력한 체크는 멈추고 완료되지 않은 것으로 끝나며, OwnGit은 어떤 출력이든 앞부분만 저장합니다. 1 KB부터 1 GB까지 정할 수 있고 기본값은 64 MB입니다.",
	},
	MsgCeilingsQueue: {en: "Checks waiting per repository", ko: "저장소당 대기 체크 수"},
	MsgCeilingsQueueHelp: {
		en: "From 1 to 10000; the default is 1000.",
		ko: "1부터 10000까지 정할 수 있고 기본값은 1000입니다.",
	},
	MsgCeilingsActive: {en: "Checks running at once per repository", ko: "저장소당 동시 실행 체크 수"},
	MsgCeilingsActiveHelp: {
		en: "From 1 to 1000; the default is 100.",
		ko: "1부터 1000까지 정할 수 있고 기본값은 100입니다.",
	},
	MsgCeilingsCPU: {en: "Container CPUs", ko: "컨테이너 CPU"},
	MsgCeilingsCPUHelp: {
		en: "From 0.1 to 1024; the default is 64.",
		ko: "0.1부터 1024까지 정할 수 있고 기본값은 64입니다.",
	},
	MsgCeilingsMemory: {en: "Container memory", ko: "컨테이너 메모리"},
	MsgCeilingsMemoryHelp: {
		en: "From 64 MB to 1024 GB; the default is 64 GB.",
		ko: "64 MB부터 1024 GB까지 정할 수 있고 기본값은 64 GB입니다.",
	},
	MsgCeilingsPIDs: {en: "Container processes", ko: "컨테이너 프로세스 수"},
	MsgCeilingsPIDsHelp: {
		en: "From 16 to 65536; the default is 4096.",
		ko: "16부터 65536까지 정할 수 있고 기본값은 4096입니다.",
	},
	MsgCeilingsScratch: {en: "Container scratch space", ko: "컨테이너 임시 공간"},
	MsgCeilingsScratchHelp: {
		en: "From 1 MB to 1024 GB; the default is 16 GB.",
		ko: "1 MB부터 1024 GB까지 정할 수 있고 기본값은 16 GB입니다.",
	},
	MsgCeilingsSource: {en: "Source copied for a check", ko: "체크용으로 복사하는 소스"},
	MsgCeilingsSourceHelp: {
		en: "The total size of the files copied into a check's workspace. From 1 byte to 1024 GB; the default is 4 GB.",
		ko: "체크 작업 공간에 복사하는 파일의 전체 크기입니다. 1바이트부터 1024 GB까지 정할 수 있고 기본값은 4 GB입니다.",
	},
	MsgCeilingsWarning: {
		en: "Repositories you enable can request more resources.",
		ko: "체크를 켠 저장소가 이 컴퓨터의 자원을 더 많이 쓰도록 요청할 수 있습니다.",
	},
	MsgCeilingsSaved: {
		en: "Saved. Check policies saved and checks queued from now on use the new ceilings.",
		ko: "저장했습니다. 이제부터 저장하는 체크 설정과 대기열에 들어가는 체크에 새 상한이 적용됩니다.",
	},
	MsgCeilingsAbove: {
		en: "These repositories' check policies are above the ceilings and start no new checks:",
		ko: "다음 저장소의 체크 설정이 상한보다 커서 새 체크를 시작하지 않습니다:",
	},

	MsgCCFieldCeiling: {
		en: "This is more than this computer's check ceiling allows. An administrator can raise the ceiling under Settings, Repositories, Check ceilings.",
		ko: "이 컴퓨터의 체크 상한보다 큰 값입니다. 관리자가 설정의 저장소 탭에 있는 체크 상한에서 상한을 올릴 수 있습니다.",
	},
	MsgCCPolicyAboveCeilings: {
		en: "This policy is above this computer's check ceilings, so no new check starts. Lower its limits into the ranges shown, or raise the ceilings under Settings, Repositories, Check ceilings.",
		ko: "이 설정이 이 컴퓨터의 체크 상한보다 커서 새 체크를 시작하지 않습니다. 한도를 안내된 범위 안으로 줄이거나 설정의 저장소 탭에 있는 체크 상한을 올려 주세요.",
	},
	MsgCCRunAboveCeilings: {
		en: "Nothing was queued because this policy is above this computer's check ceilings.",
		ko: "이 설정이 이 컴퓨터의 체크 상한보다 커서 대기열에 넣지 않았습니다.",
	},
	MsgCCCeilingsUnreadable: {
		en: "The saved check ceilings cannot be read, so no check policy can be saved and no new check starts. An administrator can set them again under Settings, Repositories, Check ceilings.",
		ko: "저장된 체크 상한을 읽을 수 없어 체크 설정을 저장할 수 없고 새 체크도 시작하지 않습니다. 관리자가 설정의 저장소 탭에 있는 체크 상한에서 다시 정하면 됩니다.",
	},
}

func init() {
	for code, entry := range checkCeilingsCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}
