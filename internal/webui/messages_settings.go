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
	MsgSettingsNetMore        MessageCode = "settings.network.more"
	MsgSettingsStorageHidden  MessageCode = "settings.storage.admin_only"
	MsgSettingsReposTitle     MessageCode = "settings.repositories.title"
	MsgSettingsRepoLink       MessageCode = "settings.repositories.link"
	MsgSettingsReposNoneHelp  MessageCode = "settings.repositories.none"
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
		en: "Who can use OwnGit, and the administrator password.",
		ko: "누가 OwnGit을 쓸 수 있는지 정하고 관리자 비밀번호를 바꿉니다.",
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
		en: "Where OwnGit keeps your repositories.",
		ko: "OwnGit이 저장소를 보관하는 곳입니다.",
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

	MsgSettingsAccessScope: {
		en: "Git and the dashboard, for everyone; there are no individual accounts. Applies as soon as you save, and a new shared password signs out everyone signed in with the shared password.",
		ko: "Git과 대시보드에 함께 적용되며 사람마다 계정을 따로 두지는 않습니다. 저장하면 바로 바뀌고, 공용 비밀번호를 새로 정하면 공용 비밀번호로 로그인한 사람은 모두 로그아웃됩니다.",
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
}

func init() {
	for code, entry := range settingsCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}
