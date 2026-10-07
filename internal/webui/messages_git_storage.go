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

	MsgNamespacesTitle             MessageCode = "repoadmin.namespaces.title"
	MsgNamespacesHelp              MessageCode = "repoadmin.namespaces.help"
	MsgNamespacesChange            MessageCode = "repoadmin.namespaces.change"
	MsgNamespacesLabel             MessageCode = "repoadmin.namespaces.label"
	MsgNamespacesFieldHelp         MessageCode = "repoadmin.namespaces.field_help"
	MsgNamespacesUnkept            MessageCode = "repoadmin.namespaces.unkept"
	MsgNamespacesSave              MessageCode = "repoadmin.namespaces.save"
	MsgNamespacesSaved             MessageCode = "repoadmin.namespaces.saved"
	MsgNamespacesInvalid           MessageCode = "repoadmin.namespaces.invalid"
	MsgNamespacesUnreadable        MessageCode = "repoadmin.namespaces.unreadable"
	MsgNamespacesChoicesUnreadable MessageCode = "repoadmin.namespaces.choices_unreadable"

	MsgBrowseUnreadable MessageCode = "browse.unreadable_view"

	MsgBrowseTitle           MessageCode = "browse.title"
	MsgBrowseScope           MessageCode = "browse.scope"
	MsgBrowseChange          MessageCode = "browse.change"
	MsgBrowseRaw             MessageCode = "browse.raw"
	MsgBrowseRawHelp         MessageCode = "browse.raw_help"
	MsgBrowseFile            MessageCode = "browse.file"
	MsgBrowseFileHelp        MessageCode = "browse.file_help"
	MsgBrowseCommitPatch     MessageCode = "browse.commit_patch"
	MsgBrowseCommitPatchHelp MessageCode = "browse.commit_patch_help"
	MsgBrowseFilePatch       MessageCode = "browse.file_patch"
	MsgBrowseFilePatchHelp   MessageCode = "browse.file_patch_help"
	MsgBrowseCommitFile      MessageCode = "browse.commit_file"
	MsgBrowseCommitFileHelp  MessageCode = "browse.commit_file_help"
	MsgBrowseCompare         MessageCode = "browse.compare"
	MsgBrowseCompareHelp     MessageCode = "browse.compare_help"
	MsgBrowseCompareTime     MessageCode = "browse.compare_time"
	MsgBrowseCompareTimeHelp MessageCode = "browse.compare_time_help"
	MsgBrowseWarning         MessageCode = "browse.warning"
	MsgBrowseSaved           MessageCode = "browse.saved"

	MsgMaintenanceTitle       MessageCode = "maintenance.title"
	MsgMaintenanceScope       MessageCode = "maintenance.scope"
	MsgMaintenanceChange      MessageCode = "maintenance.change"
	MsgMaintenanceEnabled     MessageCode = "maintenance.enabled"
	MsgMaintenanceEnabledHelp MessageCode = "maintenance.enabled_help"
	MsgMaintenanceOn          MessageCode = "maintenance.choice.on"
	MsgMaintenanceOff         MessageCode = "maintenance.choice.off"
	MsgMaintenanceStart       MessageCode = "maintenance.window_start"
	MsgMaintenanceEnd         MessageCode = "maintenance.window_end"
	MsgMaintenanceWindowHelp  MessageCode = "maintenance.window_help"
	MsgMaintenanceIdle        MessageCode = "maintenance.idle"
	MsgMaintenanceIdleHelp    MessageCode = "maintenance.idle_help"
	MsgMaintenanceCommand     MessageCode = "maintenance.command"
	MsgMaintenanceCommandHelp MessageCode = "maintenance.command_help"
	MsgMaintenanceFull        MessageCode = "maintenance.full_repack"
	MsgMaintenanceFullHelp    MessageCode = "maintenance.full_repack_help"
	MsgMaintenancePacks       MessageCode = "maintenance.pack_threshold"
	MsgMaintenancePacksHelp   MessageCode = "maintenance.pack_threshold_help"
	MsgMaintenanceWarning     MessageCode = "maintenance.warning"
	MsgMaintenanceSaved       MessageCode = "maintenance.saved"
	MsgMaintenanceWindowSame  MessageCode = "maintenance.window_same"

	MsgCleanupTitle       MessageCode = "cleanup.title"
	MsgCleanupChange      MessageCode = "cleanup.change"
	MsgCleanupScope       MessageCode = "cleanup.scope"
	MsgCleanupEnabled     MessageCode = "cleanup.enabled"
	MsgCleanupEnabledHelp MessageCode = "cleanup.enabled_help"
	MsgCleanupOn          MessageCode = "cleanup.choice.on"
	MsgCleanupOff         MessageCode = "cleanup.choice.off"
	MsgCleanupGrace       MessageCode = "cleanup.grace"
	MsgCleanupGraceHelp   MessageCode = "cleanup.grace_help"
	MsgCleanupWarning     MessageCode = "cleanup.warning"
	MsgCleanupNotPurge    MessageCode = "cleanup.not_purge"
	MsgCleanupSavedOn     MessageCode = "cleanup.saved_on"
	MsgCleanupSavedOff    MessageCode = "cleanup.saved_off"
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

	MsgNamespacesTitle: {en: "Other ref namespaces", ko: "다른 ref 이름공간"},
	MsgNamespacesHelp: {
		en: "Pushes may change branches and tags, and refs under the namespaces listed here, such as refs/notes/ for Git notes. None is listed by default. A change applies to pushes that start after you save.",
		ko: "푸시로는 브랜치와 태그, 그리고 여기에 적은 이름공간 아래의 ref를 바꿀 수 있습니다. 예를 들어 Git 노트는 refs/notes/입니다. 기본값은 비어 있고, 저장한 뒤 시작하는 푸시부터 적용됩니다.",
	},
	MsgNamespacesChange: {en: "Change the namespaces", ko: "이름공간 바꾸기"},
	MsgNamespacesLabel:  {en: "Namespaces, one per line", ko: "이름공간(한 줄에 하나씩)"},
	MsgNamespacesFieldHelp: {
		en: "Each starts with refs/ and ends with /, such as refs/notes/. Branches, tags, OwnGit's own refs/owngit/ and namespaces that differ only in letter case or lie inside another are refused. At most 32.",
		ko: "refs/로 시작하고 /로 끝나야 합니다(예: refs/notes/). 브랜치, 태그, OwnGit이 쓰는 refs/owngit/, 대소문자만 다르거나 다른 이름공간 안에 들어가는 이름공간은 받지 않습니다. 32개까지 적을 수 있습니다.",
	},
	MsgNamespacesUnkept: {
		en: "Overwritten or deleted refs in these namespaces have no kept history.",
		ko: "이 이름공간에서 덮어쓰거나 지운 ref는 이전 기록이 보관되지 않습니다.",
	},
	MsgNamespacesSave: {en: "Save namespaces", ko: "이름공간 저장"},
	MsgNamespacesSaved: {
		en: "Saved. Pushes that start from now on follow the new namespaces.",
		ko: "저장했습니다. 이제부터 시작하는 푸시는 새 이름공간을 따릅니다.",
	},
	MsgNamespacesInvalid: {
		en: "Write each namespace as refs/name/, outside branches, tags and refs/owngit/, not differing from another only in letter case or lying inside another, at most 32.",
		ko: "이름공간은 refs/이름/ 형식으로 적어 주세요. 브랜치, 태그, refs/owngit/ 밖이어야 하고, 다른 이름공간과 대소문자만 다르거나 그 안에 들어가면 안 되며, 32개까지입니다.",
	},
	MsgNamespacesUnreadable: {
		en: "The saved namespaces cannot be read, so pushes to this repository are refused. Save the list again to replace them.",
		ko: "저장된 이름공간을 읽을 수 없어 지금은 이 저장소로 푸시할 수 없습니다. 목록을 다시 저장하면 바뀝니다.",
	},
	MsgNamespacesChoicesUnreadable: {
		en: "Nothing was saved because the repository's kept history and default branch protection cannot be read. Save them above first.",
		ko: "저장소의 기록 보관과 기본 브랜치 보호 설정을 읽을 수 없어 저장하지 않았습니다. 위에서 먼저 그 설정을 저장해 주세요.",
	},

	MsgBrowseUnreadable: {
		en: "This view is not shown because the saved browsing limits cannot be read. An administrator can set them again under Settings, Repositories, or with owngit settings set and the --browse- options.",
		ko: "저장된 보기 한도를 읽을 수 없어 이 화면을 보여 주지 않습니다. 관리자가 설정의 저장소 탭이나 owngit settings set의 --browse- 옵션으로 다시 정하면 됩니다.",
	},

	MsgBrowseTitle: {en: "Browsing limits", ko: "보기 한도"},
	MsgBrowseScope: {
		en: "How much of a file, a diff or a pull request comparison one page reads. New limits apply to pages opened after you save; a page still stops at its own time limit.",
		ko: "한 페이지가 파일, 차이(diff), 풀 리퀘스트 비교를 얼마나 읽을지 정합니다. 저장한 뒤 여는 페이지부터 새 한도를 쓰며, 페이지는 여전히 자체 시간 한도에서 멈춥니다.",
	},
	MsgBrowseChange:      {en: "Change the limits", ko: "한도 바꾸기"},
	MsgBrowseRaw:         {en: "Raw file download", ko: "원본 파일 내려받기"},
	MsgBrowseRawHelp:     {en: "A larger file is not downloaded from the file view; clone the repository to get it. From 1 MB to 256 MB; the default is 10 MB.", ko: "이보다 큰 파일은 파일 화면에서 내려받을 수 없으니 저장소를 클론해서 받으세요. 1 MB부터 256 MB까지 정할 수 있고 기본값은 10 MB입니다."},
	MsgBrowseFile:        {en: "File view", ko: "파일 보기"},
	MsgBrowseFileHelp:    {en: "How much of one file the file view shows; the rest is offered as a download. From 64 KB to 64 MB; the default is 2 MB.", ko: "파일 화면에 한 파일을 얼마나 보여 줄지 정합니다. 나머지는 내려받기로 제공합니다. 64 KB부터 64 MB까지 정할 수 있고 기본값은 2 MB입니다."},
	MsgBrowseCommitPatch: {en: "Commit diff", ko: "커밋 차이"},
	MsgBrowseCommitPatchHelp: {
		en: "How much diff text a commit page reads for all its files. From 64 KB to 64 MB; the default is 2 MB.",
		ko: "커밋 페이지가 모든 파일의 차이를 합쳐 얼마나 읽을지 정합니다. 64 KB부터 64 MB까지 정할 수 있고 기본값은 2 MB입니다.",
	},
	MsgBrowseFilePatch: {en: "One file's diff alone", ko: "한 파일만의 차이"},
	MsgBrowseFilePatchHelp: {
		en: "How much diff text the page of one changed file reads. From 64 KB to 64 MB; the default is 8 MB.",
		ko: "바뀐 파일 하나만 여는 페이지가 차이를 얼마나 읽을지 정합니다. 64 KB부터 64 MB까지 정할 수 있고 기본값은 8 MB입니다.",
	},
	MsgBrowseCommitFile: {en: "One file within a commit", ko: "커밋 안의 파일 하나"},
	MsgBrowseCommitFileHelp: {
		en: "A file whose diff is larger is listed on the commit page with a link to its diff alone. From 16 KB to 16 MB; the default is 256 KB.",
		ko: "차이가 이보다 큰 파일은 커밋 페이지에 목록으로만 나오고, 그 파일만의 차이로 가는 링크가 붙습니다. 16 KB부터 16 MB까지 정할 수 있고 기본값은 256 KB입니다.",
	},
	MsgBrowseCompare: {en: "Pull request comparison", ko: "풀 리퀘스트 비교"},
	MsgBrowseCompareHelp: {
		en: "How much diff text a pull request page reads. From 64 KB to 64 MB; the default is 8 MB.",
		ko: "풀 리퀘스트 페이지가 차이를 얼마나 읽을지 정합니다. 64 KB부터 64 MB까지 정할 수 있고 기본값은 8 MB입니다.",
	},
	MsgBrowseCompareTime: {en: "Comparison time", ko: "비교 시간"},
	MsgBrowseCompareTimeHelp: {
		en: "How long reading that comparison may take before the page shows what it read as incomplete. From 5 seconds to 1 minute; the default is 20 seconds.",
		ko: "비교를 읽는 데 쓸 수 있는 시간입니다. 넘으면 읽은 데까지를 불완전한 결과로 보여 줍니다. 5초부터 1분까지 정할 수 있고 기본값은 20초입니다.",
	},
	MsgBrowseWarning: {
		en: "Larger views use more memory and may delay other pages.",
		ko: "보기 한도를 키우면 메모리를 더 쓰고 다른 페이지가 늦어질 수 있습니다.",
	},
	MsgBrowseSaved: {en: "Saved. Pages opened from now on use the new limits.", ko: "저장했습니다. 이제부터 여는 페이지에 새 한도가 적용됩니다."},

	MsgMaintenanceTitle: {en: "Repository maintenance", ko: "저장소 유지 관리"},
	MsgMaintenanceScope: {
		en: "OwnGit packs a repository's loose objects and refs once it has been idle after a change, and consolidates repositories with many packs in a daily window. It never removes history. A change applies from the next maintenance on; one already running finishes.",
		ko: "OwnGit은 저장소가 바뀐 뒤 한동안 쓰이지 않으면 흩어진 객체와 ref를 묶고, 팩이 많은 저장소는 매일 정한 시간대에 하나로 합칩니다. 기록은 지우지 않습니다. 바꾼 설정은 다음 유지 관리부터 적용되고, 이미 진행 중인 작업은 그대로 끝납니다.",
	},
	MsgMaintenanceChange:      {en: "Change maintenance", ko: "유지 관리 설정 바꾸기"},
	MsgMaintenanceEnabled:     {en: "Maintenance", ko: "유지 관리"},
	MsgMaintenanceEnabledHelp: {en: "The default is On.", ko: "기본값은 켬입니다."},
	MsgMaintenanceOn:          {en: "On", ko: "켬"},
	MsgMaintenanceOff:         {en: "Off", ko: "끔"},
	MsgMaintenanceStart:       {en: "Daily window from", ko: "매일 시작 시각"},
	MsgMaintenanceEnd:         {en: "until", ko: "끝 시각"},
	MsgMaintenanceWindowHelp: {
		en: "Local time of the computer that runs OwnGit, in whole hours; a window may pass midnight. The default is 03:00 until 05:00.",
		ko: "OwnGit을 실행하는 컴퓨터의 현지 시각이며 한 시간 단위입니다. 자정을 넘겨도 됩니다. 기본값은 03:00부터 05:00까지입니다.",
	},
	MsgMaintenanceIdle: {en: "Idle time before it starts", ko: "시작 전 대기 시간"},
	MsgMaintenanceIdleHelp: {
		en: "How long a repository must go unused before it is maintained. From 1 minute to 24 hours; the default is 5 minutes.",
		ko: "저장소가 이만큼 쓰이지 않아야 유지 관리를 시작합니다. 1분부터 24시간까지 정할 수 있고 기본값은 5분입니다.",
	},
	MsgMaintenanceCommand: {en: "Time for each step", ko: "단계별 시간"},
	MsgMaintenanceCommandHelp: {
		en: "How long each ordinary maintenance step may take. From 1 minute to 24 hours; the default is 30 minutes.",
		ko: "보통 유지 관리의 각 단계에 쓸 수 있는 최대 시간입니다. 1분부터 24시간까지 정할 수 있고 기본값은 30분입니다.",
	},
	MsgMaintenanceFull: {en: "Time for consolidation", ko: "합치기 시간"},
	MsgMaintenanceFullHelp: {
		en: "How long rewriting all of a repository's packs into one may take. From 1 minute to 24 hours; the default is 2 hours.",
		ko: "저장소의 모든 팩을 하나로 다시 쓰는 데 쓸 수 있는 최대 시간입니다. 1분부터 24시간까지 정할 수 있고 기본값은 2시간입니다.",
	},
	MsgMaintenancePacks: {en: "Packs before consolidation", ko: "합치기 전 팩 수"},
	MsgMaintenancePacksHelp: {
		en: "The daily window consolidates a repository with more packs than this. From 2 to 1000; the default is 20.",
		ko: "팩이 이보다 많은 저장소를 매일 시간대에 하나로 합칩니다. 2부터 1000까지 정할 수 있고 기본값은 20입니다.",
	},
	MsgMaintenanceWarning: {
		en: "Turning maintenance off can slow Git and use more space; a wider window can keep the computer busy while you work.",
		ko: "유지 관리를 끄면 Git이 느려지고 공간을 더 쓸 수 있습니다. 시간대를 넓히면 작업하는 동안 컴퓨터가 바쁠 수 있습니다.",
	},
	MsgMaintenanceSaved:      {en: "Saved. The next maintenance follows the new choices.", ko: "저장했습니다. 다음 유지 관리부터 새 설정을 따릅니다."},
	MsgMaintenanceWindowSame: {en: "Choose an end that differs from the start.", ko: "시작 시각과 다른 끝 시각을 고르세요."},

	MsgCleanupTitle: {en: "Unused object cleanup", ko: "쓰지 않는 객체 정리"},
	MsgCleanupScope: {
		en: "Removes Git objects that no branch, tag or other ref reaches, such as leftovers of rewritten pushes that were not kept. It runs once a night in the maintenance window while maintenance is on, and rewrites each repository's packs.",
		ko: "어떤 브랜치, 태그, 다른 ref로도 닿지 않는 Git 객체를 지웁니다. 예를 들어 덮어쓴 푸시에서 보관하지 않은 나머지입니다. 유지 관리가 켜져 있으면 매일 유지 관리 시간대에 한 번 돌며, 저장소마다 팩을 다시 씁니다.",
	},
	MsgCleanupChange:      {en: "Change cleanup", ko: "정리 설정 바꾸기"},
	MsgCleanupEnabled:     {en: "Cleanup", ko: "정리"},
	MsgCleanupEnabledHelp: {en: "The default is Off, which removes nothing.", ko: "기본값은 끔이며, 끄면 아무것도 지우지 않습니다."},
	MsgCleanupOn:          {en: "On", ko: "켬"},
	MsgCleanupOff:         {en: "Off", ko: "끔"},
	MsgCleanupGrace:       {en: "Grace period in days", ko: "유예 기간(일)"},
	MsgCleanupGraceHelp: {
		en: "Only unreachable objects older than this are removed. From 2 to 365 days; the default is 14.",
		ko: "이보다 오래된 닿지 않는 객체만 지웁니다. 2일부터 365일까지 정할 수 있고 기본값은 14일입니다.",
	},
	MsgCleanupWarning: {
		en: "Unreachable objects older than this may be removed for good. Kept history and other refs still keep their objects.",
		ko: "이보다 오래된 닿지 않는 객체는 영구히 지워질 수 있습니다. 보관된 기록과 다른 ref가 가리키는 객체는 그대로 남습니다.",
	},
	MsgCleanupNotPurge: {
		en: "This does not remove kept history or anything a ref still reaches, so it cannot take a pushed secret out of a repository.",
		ko: "보관된 기록이나 ref가 아직 가리키는 것은 지우지 않으므로, 이미 푸시한 비밀 정보를 저장소에서 없애는 용도로는 쓸 수 없습니다.",
	},
	MsgCleanupSavedOn:  {en: "Saved. The next nightly maintenance removes unreachable objects older than the grace period.", ko: "저장했습니다. 다음 유지 관리 시간대부터 유예 기간보다 오래된 닿지 않는 객체를 지웁니다."},
	MsgCleanupSavedOff: {en: "Saved. Cleanup is off and removes nothing.", ko: "저장했습니다. 정리를 껐으며 아무것도 지우지 않습니다."},
}

func init() {
	registerMessages(gitStorageCatalog)
}
