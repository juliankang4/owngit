package webui

// Settings tabs and the parts every Settings group shares: its save bar,
// its scope line and the controls that apply only in this browser. The
// older Settings messages are in messages.go.
const (
	MsgSettingsTabs            MessageCode = "settings.tabs"
	MsgSettingsTabGeneral      MessageCode = "settings.tab.general"
	MsgSettingsTabAccess       MessageCode = "settings.tab.access"
	MsgSettingsTabNetwork      MessageCode = "settings.tab.network"
	MsgSettingsTabRepositories MessageCode = "settings.tab.repositories"
	MsgSettingsTabStorage      MessageCode = "settings.tab.storage"

	MsgSettingsLeadGeneral      MessageCode = "settings.lead.general"
	MsgSettingsLeadAccess       MessageCode = "settings.lead.access"
	MsgSettingsLeadNetwork      MessageCode = "settings.lead.network"
	MsgSettingsLeadRepositories MessageCode = "settings.lead.repositories"
	MsgSettingsLeadStorage      MessageCode = "settings.lead.storage"

	MsgSettingsSave         MessageCode = "settings.save"
	MsgSettingsUnsaved      MessageCode = "settings.unsaved"
	MsgSettingsUnsavedBar   MessageCode = "settings.unsaved_changes"
	MsgSettingsNothing      MessageCode = "settings.nothing_changed"
	MsgSettingsSendFailed   MessageCode = "settings.send_failed"
	MsgSettingsUnexpected   MessageCode = "settings.unexpected_answer"
	MsgSettingsRisk         MessageCode = "settings.risk"
	MsgSettingsOn           MessageCode = "settings.on"
	MsgSettingsOff          MessageCode = "settings.off"
	MsgSettingsDisplayTitle MessageCode = "settings.display.title"
	MsgSettingsDisplayScope MessageCode = "settings.display.scope"
	MsgSettingsDisplayOrder MessageCode = "settings.display.order"
	MsgSettingsLight        MessageCode = "settings.display.light"
	MsgSettingsDark         MessageCode = "settings.display.dark"
	MsgSettingsSystem       MessageCode = "settings.display.system"

	MsgSettingsUpdateSwitch MessageCode = "settings.update.switch"
	MsgSettingsUpdateScope  MessageCode = "settings.update.scope"

	MsgSettingsTrayTitle       MessageCode = "settings.tray.title"
	MsgSettingsTrayScope       MessageCode = "settings.tray.scope"
	MsgSettingsTraySwitch      MessageCode = "settings.tray.switch"
	MsgSettingsTrayHelp        MessageCode = "settings.tray.help"
	MsgSettingsTrayNoDesktop   MessageCode = "settings.tray.no_desktop"
	MsgSettingsTrayUnreadable  MessageCode = "settings.tray.unreadable"
	MsgSettingsTrayUnavailable MessageCode = "settings.tray.unavailable"

	// Who made a change, as the OwnGit icon lists recent pushes.
	MsgActorAccess        MessageCode = "actor.access"
	MsgActorAdministrator MessageCode = "actor.administrator"

	MsgSettingsAccessScope    MessageCode = "settings.access.scope"
	MsgSettingsAccessMode     MessageCode = "settings.access.mode"
	MsgSettingsAccessModePass MessageCode = "settings.access.mode_password"
	MsgSettingsAccessModeOpen MessageCode = "settings.access.mode_open"
	MsgSettingsAccessNewPass  MessageCode = "settings.access.new_password"
	MsgSettingsAccessKeepHelp MessageCode = "settings.access.keep_help"
	MsgSettingsAdminScope     MessageCode = "settings.admin.scope"
	MsgSettingsTSSwitch       MessageCode = "settings.tailscale.switch"
	MsgSettingsTSScope        MessageCode = "settings.tailscale.scope"
	MsgSettingsTSAgain        MessageCode = "settings.tailscale.again"
	MsgSettingsTSHomeOff      MessageCode = "settings.tailscale.home_needs_sharing"
	MsgSettingsNetMore        MessageCode = "settings.network.more"
	MsgSettingsStorageHidden  MessageCode = "settings.storage.admin_only"
	MsgSettingsReposTitle     MessageCode = "settings.repositories.title"
	MsgSettingsRepoLink       MessageCode = "settings.repositories.link"
	MsgSettingsReposNoneHelp  MessageCode = "settings.repositories.none"

	// The dialog shown when leaving Settings with unsaved changes.
	MsgLeaveTitle        MessageCode = "leave.title"
	MsgLeaveLead         MessageCode = "leave.lead"
	MsgLeaveSave         MessageCode = "leave.save"
	MsgLeaveDiscard      MessageCode = "leave.discard"
	MsgLeaveStay         MessageCode = "leave.stay"
	MsgLeaveEntered      MessageCode = "leave.entered"
	MsgLeaveEmpty        MessageCode = "leave.empty"
	MsgLeavePasswordHelp MessageCode = "leave.password_help"
	MsgLeaveApartTag     MessageCode = "leave.apart_tag"
	MsgLeaveApart        MessageCode = "leave.apart"
	MsgLeaveSaving       MessageCode = "leave.saving"
	MsgLeavePartial      MessageCode = "leave.partial"
)

var settingsCatalog = map[MessageCode]message{
	MsgSettingsTabs:            {en: "Settings sections", ko: "설정 메뉴"},
	MsgSettingsTabGeneral:      {en: "General", ko: "일반"},
	MsgSettingsTabAccess:       {en: "Access", ko: "접근 권한"},
	MsgSettingsTabNetwork:      {en: "Network", ko: "네트워크"},
	MsgSettingsTabRepositories: {en: "Repositories", ko: "저장소"},
	MsgSettingsTabStorage:      {en: "Storage & recovery", ko: "보관과 복구"},

	MsgSettingsLeadGeneral: {
		en: "How OwnGit looks in this browser, and new-release notices.",
		ko: "이 브라우저에서 보이는 화면과 새 릴리스 알림을 정합니다.",
	},
	MsgSettingsLeadAccess: {
		en: "Who can use OwnGit, the administrator password, and when it is asked.",
		ko: "누가 OwnGit을 쓸 수 있는지, 관리자 비밀번호를 언제 물을지 정하고 관리자 비밀번호를 바꿉니다.",
	},
	MsgSettingsLeadNetwork: {
		en: "How this computer and other devices reach OwnGit.",
		ko: "이 컴퓨터와 다른 기기가 OwnGit에 접속하는 방법을 정합니다.",
	},
	MsgSettingsLeadRepositories: {
		en: "Default branch, imports, checks, credentials and deletion are set per repository, on each repository's settings page.",
		ko: "기본 브랜치, 가져오기, 체크, 자격 증명, 삭제는 저장소마다 따로 정합니다. 각 저장소의 설정 페이지에서 바꿀 수 있습니다.",
	},
	MsgSettingsLeadStorage: {
		en: "Where OwnGit keeps your repositories, and how long it keeps raw check logs.",
		ko: "OwnGit이 저장소를 보관하는 곳과 체크 원본 로그를 보관하는 기간입니다.",
	},

	MsgSettingsSave:       {en: "Save", ko: "저장"},
	MsgSettingsUnsaved:    {en: "Not saved", ko: "저장 안 됨"},
	MsgSettingsUnsavedBar: {en: "Unsaved changes", ko: "저장하지 않은 변경"},
	MsgSettingsNothing: {
		en: "Nothing was changed, so nothing was saved.",
		ko: "바뀐 내용이 없어 저장하지 않았습니다.",
	},
	MsgSettingsSendFailed: {
		en: "The change could not be sent, so it was not saved. Check the connection to OwnGit and save again.",
		ko: "변경 내용을 보내지 못해 저장하지 않았습니다. OwnGit에 연결되어 있는지 확인하고 다시 저장하세요.",
	},
	MsgSettingsUnexpected: {
		en: "OwnGit answered in a way this page does not expect. Reload the page to see the current settings.",
		ko: "OwnGit이 이 페이지가 예상하지 못한 응답을 보냈습니다. 페이지를 새로 고쳐 현재 설정을 확인하세요.",
	},
	MsgSettingsRisk: {en: "Risk:", ko: "위험:"},
	MsgSettingsOn:   {en: "On", ko: "켜짐"},
	MsgSettingsOff:  {en: "Off", ko: "꺼짐"},

	MsgSettingsDisplayTitle: {en: "Display in this browser", ko: "이 브라우저의 화면 설정"},
	MsgSettingsDisplayScope: {
		en: "This browser only. A choice applies at once and never asks for the administrator password. The buttons at the top of the page do the same.",
		ko: "이 브라우저에만 적용되며 고르는 즉시 바뀝니다. 관리자 비밀번호는 묻지 않고, 페이지 위쪽 버튼으로도 바꿀 수 있습니다.",
	},
	MsgSettingsDisplayOrder: {en: "Repository list order", ko: "저장소 목록 정렬"},
	MsgSettingsLight:        {en: "Light", ko: "라이트 모드"},
	MsgSettingsDark:         {en: "Dark", ko: "다크 모드"},
	MsgSettingsSystem:       {en: "Match the system", ko: "시스템 설정 따르기"},

	MsgSettingsUpdateSwitch: {en: "Check for new releases once a day", ko: "새 릴리스를 하루에 한 번 확인"},
	MsgSettingsUpdateScope: {
		en: "Whole server. Applies as soon as you save.",
		ko: "서버 전체 설정이며 저장하면 바로 적용됩니다.",
	},

	MsgSettingsTrayTitle: {en: "OwnGit icon", ko: "OwnGit 아이콘"},
	MsgSettingsTrayScope: {
		en: "The computer that runs OwnGit, not the computer this browser is on.",
		ko: "이 브라우저가 있는 컴퓨터가 아니라 OwnGit이 실행되는 컴퓨터에 적용됩니다.",
	},
	MsgSettingsTraySwitch: {en: "Show the OwnGit icon in the menu bar, notification area or panel", ko: "메뉴 막대, 알림 영역 또는 패널에 OwnGit 아이콘 표시"},
	MsgSettingsTrayHelp: {
		en: "Turn it on to show the icon again after it was hidden. Hiding the icon never stops OwnGit: Git and the dashboard keep working. On that computer, \"owngit tray on\" and \"owngit tray off\" do the same.",
		ko: "숨긴 아이콘을 다시 보이게 하려면 켜세요. 아이콘을 숨겨도 OwnGit은 멈추지 않으며 Git과 대시보드는 그대로 동작합니다. 그 컴퓨터에서 \"owngit tray on\"과 \"owngit tray off\"로도 바꿀 수 있습니다.",
	},
	MsgSettingsTrayNoDesktop: {
		en: "That computer has no desktop session now, so no icon shows there. It follows this setting once it has one.",
		ko: "그 컴퓨터에는 지금 데스크톱 세션이 없어 아이콘이 보이지 않습니다. 데스크톱 세션이 생기면 이 설정을 따릅니다.",
	},
	MsgSettingsTrayUnavailable: {
		en: "This kind of install has no OwnGit icon, because OwnGit runs as its own service account, not as the account that signs in at the desktop.",
		ko: "이 설치 방식에는 OwnGit 아이콘이 없습니다. OwnGit이 데스크톱에 로그인하는 계정이 아니라 전용 서비스 계정으로 실행되기 때문입니다.",
	},
	MsgActorAccess:        {en: "Shared access", ko: "공용 접근"},
	MsgActorAdministrator: {en: "Administrator", ko: "관리자"},
	MsgSettingsTrayUnreadable: {
		en: "OwnGit could not read whether the icon is hidden. The OwnGit log says why. Saving sets it again.",
		ko: "아이콘을 숨겼는지 읽지 못했습니다. 이유는 OwnGit 로그에 있으며, 저장하면 다시 설정됩니다.",
	},

	MsgSettingsAccessScope: {
		en: "Git and the dashboard, for everyone; there are no individual accounts. Applies as soon as you save. Turning the shared password on or changing it signs out everyone who is not confirmed as administrator; they sign in again with the new password.",
		ko: "Git과 대시보드에 함께 적용되며 사람마다 계정을 따로 두지는 않습니다. 저장하면 바로 바뀝니다. 공용 비밀번호를 켜거나 바꾸면 관리자로 확인되지 않은 사람은 모두 로그아웃되고, 새 비밀번호로 다시 로그인합니다.",
	},
	MsgSettingsAccessMode:     {en: "Who can read and push", ko: "읽기와 푸시 권한"},
	MsgSettingsAccessModePass: {en: "Only people with the shared password", ko: "공용 비밀번호를 아는 사람만"},
	MsgSettingsAccessModeOpen: {en: "Anyone who can reach OwnGit, without a password", ko: "OwnGit에 접속할 수 있는 누구나 (비밀번호 없음)"},
	MsgSettingsAccessNewPass:  {en: "New shared password", ko: "새 공용 비밀번호"},
	MsgSettingsAccessKeepHelp: {
		en: "Leave it empty to keep the current shared password.",
		ko: "비워 두면 지금 공용 비밀번호를 그대로 씁니다.",
	},
	MsgSettingsAdminScope: {
		en: "Needed to change settings, delete repositories and manage credentials. Applies as soon as you save.",
		ko: "설정 변경, 저장소 삭제, 자격 증명 관리에 필요합니다. 저장하면 바로 적용됩니다.",
	},
	MsgSettingsTSSwitch: {en: "Share OwnGit on my tailnet", ko: "내 tailnet에 OwnGit 공유"},
	MsgSettingsTSScope: {
		en: "Turning sharing on or off applies at once, without a restart.",
		ko: "공유를 켜거나 끄면 다시 시작하지 않아도 바로 적용됩니다.",
	},
	MsgSettingsTSAgain: {
		en: "Sharing is on, but turning it on did not finish. Save to turn it on again.",
		ko: "공유가 켜져 있지만 켜는 작업이 끝나지 않았습니다. 저장하면 다시 켭니다.",
	},
	MsgSettingsTSHomeOff: {
		en: "Sharing is off, so nothing was saved. The home network choice is used only when you turn sharing on. To change where OwnGit listens, use This computer's address in Network above.",
		ko: "공유가 꺼져 있어 아무것도 저장하지 않았습니다. 홈 네트워크 선택은 공유를 켤 때만 쓰입니다. OwnGit이 연결을 받는 곳을 바꾸려면 위쪽 네트워크의 이 컴퓨터의 주소를 쓰세요.",
	},
	MsgSettingsNetMore: {en: "Proxy and host details", ko: "프록시와 호스트 세부 설정"},
	MsgSettingsStorageHidden: {
		en: "Only administrators see where the repositories are kept.",
		ko: "저장소를 보관하는 위치는 관리자에게만 보입니다.",
	},
	MsgSettingsReposTitle: {en: "Settings for each repository", ko: "저장소별 설정"},
	// Value: the repository name.
	MsgSettingsRepoLink: {en: "%s settings", ko: "%s 설정"},
	MsgSettingsReposNoneHelp: {
		en: "No repositories yet. Each new repository gets its own settings page.",
		ko: "아직 저장소가 없습니다. 저장소를 만들면 저장소마다 설정 페이지가 생깁니다.",
	},

	MsgLeaveTitle:   {en: "You have unsaved changes", ko: "저장하지 않은 변경 사항이 있습니다"},
	MsgLeaveLead:    {en: "If you leave now, these changes are lost:", ko: "이대로 나가면 아래 변경 사항이 사라집니다."},
	MsgLeaveSave:    {en: "Save and leave", ko: "저장하고 나가기"},
	MsgLeaveDiscard: {en: "Discard and leave", ko: "저장하지 않고 나가기"},
	MsgLeaveStay:    {en: "Stay", ko: "계속 편집"},
	// A password is never shown, only that one was typed.
	MsgLeaveEntered: {en: "entered", ko: "입력됨"},
	MsgLeaveEmpty:   {en: "(empty)", ko: "(비어 있음)"},
	MsgLeavePasswordHelp: {
		en: "Needed only to save. Leaving without saving does not ask for it.",
		ko: "저장할 때만 필요합니다. 저장하지 않고 나가면 묻지 않습니다.",
	},
	// Marks a group whose save sends a page, such as one that asks for a
	// password, when more than one such group holds a change.
	MsgLeaveApartTag: {en: "Save separately", ko: "따로 저장"},
	MsgLeaveApart: {
		en: "Groups marked Save separately reload the page when saved, so they cannot be saved on the way out. Choose Stay and save each one with its own Save button, or leave without saving.",
		ko: "‘따로 저장’ 표시가 있는 항목은 저장할 때 페이지를 새로 불러오므로 나가면서 함께 저장할 수 없습니다. 계속 편집을 누르고 항목마다 저장 버튼으로 저장하거나 저장하지 않고 나가세요.",
	},
	MsgLeaveSaving: {en: "Saving…", ko: "저장하는 중…"},
	MsgLeavePartial: {
		en: "Not everything could be saved, so you are still on this page. Saved groups say so; the others keep your changes.",
		ko: "모두 저장하지 못해 이 페이지에 그대로 있습니다. 저장한 항목에는 저장했다는 안내가 보이고 나머지 항목에는 입력한 내용이 남아 있습니다.",
	},
}

func init() {
	for code, entry := range settingsCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}
