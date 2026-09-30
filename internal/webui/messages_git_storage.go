package webui

// The Git and storage policies on the Settings tabs and a repository's
// Settings tab: how many Git transfers run at once and how long they wait,
// extra ref namespaces, the budgets of file, diff and comparison views,
// repository maintenance and unused object cleanup.
const (
	MsgTransferAdvanced         MessageCode = "transfer.advanced"
	MsgTransferSlotsScope       MessageCode = "transfer.slots_scope"
	MsgTransferPerRepository    MessageCode = "transfer.per_repository"
	MsgTransferPerRepositoryHlp MessageCode = "transfer.per_repository_help"
	MsgTransferExtraSlots       MessageCode = "transfer.extra_slots"
	MsgTransferExtraSlotsHelp   MessageCode = "transfer.extra_slots_help"
	MsgTransferIdle             MessageCode = "transfer.idle"
	MsgTransferIdleHelp         MessageCode = "transfer.idle_help"
	MsgTransferQueue            MessageCode = "transfer.queue"
	MsgTransferQueueHelp        MessageCode = "transfer.queue_help"
	MsgTransferSlotsWarning     MessageCode = "transfer.slots_warning"
	MsgTransferSavedLooser      MessageCode = "transfer.saved_looser"
)

var gitStorageCatalog = map[MessageCode]message{
	MsgTransferAdvanced: {en: "Transfers at once and waiting", ko: "동시 전송과 대기"},
	MsgTransferSlotsScope: {
		en: "Transfers that ask for a slot after you save. A lower number never stops a transfer already running: new transfers wait until enough of them have ended.",
		ko: "저장한 뒤 자리를 요청하는 전송에 적용됩니다. 수를 낮춰도 이미 진행 중인 전송은 멈추지 않고, 새 전송은 충분히 끝날 때까지 기다립니다.",
	},
	MsgTransferPerRepository: {en: "Transfers per repository", ko: "저장소당 동시 전송"},
	MsgTransferPerRepositoryHlp: {
		en: "How many clones, fetches, pushes and archive downloads of one repository run at once: from 1 to 32. The default is 4.",
		ko: "한 저장소에서 동시에 진행하는 클론, 가져오기(fetch), 푸시, 압축 파일 내려받기의 수입니다. 1부터 32까지 정할 수 있고 기본값은 4입니다.",
	},
	MsgTransferExtraSlots: {en: "Extra slots for other repositories", ko: "다른 저장소용 추가 자리"},
	MsgTransferExtraSlotsHelp: {
		en: "Slots beyond the number per repository that only a repository with no transfer running may take, so one busy repository never makes the others wait. OwnGit runs at most both numbers added together at once. From 0 to 32; the default is 1.",
		ko: "저장소당 수를 넘는 추가 자리로, 진행 중인 전송이 없는 저장소만 쓸 수 있습니다. 그래서 한 저장소가 바빠도 다른 저장소는 기다리지 않습니다. OwnGit은 두 수를 더한 만큼까지만 동시에 전송합니다. 0부터 32까지 정할 수 있고 기본값은 1입니다.",
	},
	MsgTransferIdle: {en: "Idle limit", ko: "무응답 한도"},
	MsgTransferIdleHelp: {
		en: "A transfer whose client sends or accepts no data for this long is stopped; time Git spends working does not count. From 10 seconds to 1 hour; the default is 1 minute.",
		ko: "클라이언트가 이 시간 동안 데이터를 보내지도 받지도 않으면 전송을 멈춥니다. Git이 작업하는 시간은 세지 않습니다. 10초부터 1시간까지 정할 수 있고 기본값은 1분입니다.",
	},
	MsgTransferQueue: {en: "Wait for a slot", ko: "자리 대기 시간"},
	MsgTransferQueueHelp: {
		en: "How long a transfer waits for a free slot before it is told the server is busy and to try again shortly. From 5 seconds to 10 minutes; the default is 90 seconds.",
		ko: "전송이 빈 자리를 기다리는 최대 시간입니다. 그동안 자리가 나지 않으면 서버가 바쁘니 잠시 뒤 다시 시도하라고 알립니다. 5초부터 10분까지 정할 수 있고 기본값은 90초입니다.",
	},
	MsgTransferSlotsWarning: {
		en: "Transfers can hold slots longer and make other clients wait.",
		ko: "전송이 자리를 더 오래 차지해 다른 클라이언트가 기다릴 수 있습니다.",
	},
	MsgTransferSavedLooser: {
		en: "Saved. Transfers that start from now on use the new limits. Transfers can hold slots longer and make other clients wait.",
		ko: "저장했습니다. 이제부터 시작하는 전송에 새 한도가 적용됩니다. 전송이 자리를 더 오래 차지해 다른 클라이언트가 기다릴 수 있습니다.",
	},
}

func init() {
	for code, entry := range gitStorageCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}
