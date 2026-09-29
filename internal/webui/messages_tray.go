package webui

// The panel of the OwnGit icon in the Windows notification area and the
// Linux panel. The icon shows these sentences natively, not in a page.
const (
	MsgTrayRunning       MessageCode = "tray.state.running"
	MsgTrayAttention     MessageCode = "tray.state.attention"
	MsgTrayStopped       MessageCode = "tray.state.stopped"
	MsgTrayUnavailable   MessageCode = "tray.state.unavailable"
	MsgTrayVersion       MessageCode = "tray.version"
	MsgTrayCloneAddress  MessageCode = "tray.clone_address"
	MsgTrayCloneHelp     MessageCode = "tray.clone_help"
	MsgTrayCopy          MessageCode = "tray.copy"
	MsgTrayCopyCommand   MessageCode = "tray.copy_command"
	MsgTrayCopyClone     MessageCode = "tray.copy_clone"
	MsgTrayCopied        MessageCode = "tray.copied"
	MsgTrayCopyFailed    MessageCode = "tray.copy_failed"
	MsgTrayRecent        MessageCode = "tray.recent"
	MsgTrayNoPushes      MessageCode = "tray.no_pushes"
	MsgTrayPushesLater   MessageCode = "tray.pushes_later"
	MsgTrayOpen          MessageCode = "tray.open"
	MsgTrayThisComputer  MessageCode = "tray.this_computer"
	MsgTrayHide          MessageCode = "tray.hide"
	MsgTrayHidePanel     MessageCode = "tray.hide_panel"
	MsgTrayShowPanel     MessageCode = "tray.show_panel"
	MsgTrayQuit          MessageCode = "tray.quit"
	MsgTrayKeepsRunning  MessageCode = "tray.keeps_running"
	MsgTrayHideFailed    MessageCode = "tray.hide_failed"
	MsgTraySetup         MessageCode = "tray.setup"
	MsgTrayUpdate        MessageCode = "tray.update"
	MsgTrayUpdateRun     MessageCode = "tray.update_run"
	MsgTrayUpdateStart   MessageCode = "tray.update_start"
	MsgTrayUpdateRestart MessageCode = "tray.update_restart"
	MsgTrayUpdateGuide   MessageCode = "tray.update_guide"
	MsgTrayMoreFindings  MessageCode = "tray.more_findings"
	MsgTrayRepairRun     MessageCode = "tray.repair_run"
	MsgTrayStartRun      MessageCode = "tray.start_run"
	MsgTrayNoStatus      MessageCode = "tray.no_status"
)

var trayCatalog = map[MessageCode]message{
	MsgTrayRunning:      {en: "Running", ko: "실행 중"},
	MsgTrayAttention:    {en: "Needs attention", ko: "확인 필요"},
	MsgTrayStopped:      {en: "Not running", ko: "실행되지 않음"},
	MsgTrayUnavailable:  {en: "Status unavailable", ko: "상태를 알 수 없음"},
	MsgTrayVersion:      {en: "Version %s", ko: "버전 %s"},
	MsgTrayCloneAddress: {en: "Clone address", ko: "클론 주소"},
	MsgTrayCloneHelp:    {en: "Add the repository name to this address when cloning.", ko: "클론할 때 이 주소 뒤에 저장소 이름을 붙이세요."},
	MsgTrayCopy:         {en: "Copy", ko: "복사"},
	MsgTrayCopyCommand:  {en: "Copy the command", ko: "명령 복사"},
	MsgTrayCopyClone:    {en: "Copy the clone address", ko: "클론 주소 복사"},
	MsgTrayCopied:       {en: "Copied", ko: "복사됨"},
	MsgTrayCopyFailed:   {en: "Not copied", ko: "복사하지 못함"},
	MsgTrayRecent:       {en: "Recent pushes", ko: "최근 푸시"},
	MsgTrayNoPushes:     {en: "No pushes yet.", ko: "아직 푸시가 없습니다."},
	MsgTrayPushesLater:  {en: "Recent pushes appear here while OwnGit runs.", ko: "OwnGit이 실행 중일 때 최근 푸시가 여기에 보입니다."},
	MsgTrayOpen:         {en: "Open dashboard", ko: "대시보드 열기"},
	MsgTrayThisComputer: {en: "This computer", ko: "이 컴퓨터"},
	MsgTrayHide:         {en: "Hide from the notification area", ko: "알림 영역에서 숨기기"},
	MsgTrayHidePanel:    {en: "Hide from the panel", ko: "패널에서 숨기기"},
	MsgTrayShowPanel:    {en: "Open the panel", ko: "패널 열기"},
	MsgTrayQuit:         {en: "Quit the icon", ko: "아이콘 종료"},
	MsgTrayKeepsRunning: {
		en: "OwnGit keeps running either way. Quit closes the icon until you sign in again. Hide keeps it hidden, also after you sign in again, until you turn it on in the dashboard Settings or run \"owngit tray on\".",
		ko: "어느 쪽이든 OwnGit은 계속 실행됩니다. 종료하면 다시 로그인할 때까지 아이콘이 닫힙니다. 숨기면 대시보드 설정에서 다시 켜거나 \"owngit tray on\"을 실행할 때까지 다시 로그인한 뒤에도 숨겨져 있습니다.",
	},
	MsgTrayHideFailed: {en: "The icon could not be hidden: %s", ko: "아이콘을 숨기지 못했습니다: %s"},
	MsgTraySetup:      {en: "Finish setup in the dashboard.", ko: "대시보드에서 설정을 마치세요."},
	MsgTrayUpdate:     {en: "OwnGit %s is available. You are running %s.", ko: "OwnGit %s 버전이 나왔습니다. 지금 쓰는 버전은 %s입니다."},
	MsgTrayUpdateRun:  {en: "To update, run this command:", ko: "업데이트하려면 이 명령을 실행하세요:"},
	MsgTrayUpdateStart: {
		en: "Then start OwnGit again: %s",
		ko: "그다음 OwnGit을 다시 실행하세요: %s",
	},
	MsgTrayUpdateRestart: {en: "Then restart OwnGit.", ko: "그다음 OwnGit을 다시 시작하세요."},
	MsgTrayUpdateGuide:   {en: "The dashboard says how to update this install.", ko: "이 설치를 업데이트하는 방법은 대시보드에 있습니다."},
	MsgTrayMoreFindings:  {en: "The dashboard Settings list %d more.", ko: "나머지 %d건은 대시보드 설정에서 볼 수 있습니다."},
	MsgTrayRepairRun:     {en: "To repair it, run this command:", ko: "고치려면 이 명령을 실행하세요:"},
	MsgTrayStartRun:      {en: "To start OwnGit, run this command:", ko: "OwnGit을 시작하려면 이 명령을 실행하세요:"},
	MsgTrayNoStatus: {
		en: "OwnGit could not report its status just now. The icon asks again in a few seconds.",
		ko: "지금은 OwnGit 상태를 확인하지 못했습니다. 몇 초 뒤에 다시 확인합니다.",
	},
}

func init() {
	for code, entry := range trayCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}
