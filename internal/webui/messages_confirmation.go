package webui

// When the administrator password is asked: the choice in Access, what the
// forms and the administrator sign-in say under it, and the indication on
// every page while the check is off.
const (
	MsgConfirmTitle     MessageCode = "confirm.title"
	MsgConfirmScope     MessageCode = "confirm.scope"
	MsgConfirmLabel     MessageCode = "confirm.label"
	MsgConfirmHelp      MessageCode = "confirm.help"
	MsgConfirmNeverRisk MessageCode = "confirm.never_risk"
	MsgConfirmOpenRisk  MessageCode = "confirm.never_open_risk"
	MsgConfirmAck       MessageCode = "confirm.ack"
	MsgConfirmAckNeeded MessageCode = "confirm.ack_needed"
	MsgConfirmLastTime  MessageCode = "confirm.last_time"
	MsgConfirmSaved     MessageCode = "confirm.saved"
	MsgConfirmTurnedOff MessageCode = "confirm.turned_off"
	MsgConfirmUnknown   MessageCode = "confirm.unknown"

	MsgConfirmEvery MessageCode = "confirm.choice.every"
	MsgConfirm30m   MessageCode = "confirm.choice.30m"
	MsgConfirm1h    MessageCode = "confirm.choice.1h"
	MsgConfirm8h    MessageCode = "confirm.choice.8h"
	MsgConfirm1d    MessageCode = "confirm.choice.1d"
	MsgConfirm7d    MessageCode = "confirm.choice.7d"
	MsgConfirm30d   MessageCode = "confirm.choice.30d"
	MsgConfirmNever MessageCode = "confirm.choice.never"

	// What typing the password now does, under each choice. Shown beside
	// the password field of a form and on the administrator sign-in.
	MsgConfirmAfterEvery MessageCode = "confirm.after.every"
	MsgConfirmAfter30m   MessageCode = "confirm.after.30m"
	MsgConfirmAfter1h    MessageCode = "confirm.after.1h"
	MsgConfirmAfter8h    MessageCode = "confirm.after.8h"
	MsgConfirmAfter1d    MessageCode = "confirm.after.1d"
	MsgConfirmAfter7d    MessageCode = "confirm.after.7d"
	MsgConfirmAfter30d   MessageCode = "confirm.after.30d"

	// A save that needs no password says why.
	MsgConfirmRemembered MessageCode = "confirm.remembered"
	MsgConfirmCheckOff   MessageCode = "confirm.check_off"

	// The Settings page heading, the sidebar and the indication on every
	// page. The sidebar sentences take the time the confirmation ends.
	MsgConfirmIntroRemembered MessageCode = "confirm.intro_remembered"
	MsgConfirmIntroOff        MessageCode = "confirm.intro_off"
	MsgConfirmUntil           MessageCode = "confirm.until"
	MsgConfirmPagesUntil      MessageCode = "confirm.pages_until"
	MsgConfirmOffBadge        MessageCode = "confirm.off_badge"
	MsgConfirmAdminBody       MessageCode = "confirm.admin_body"
)

var confirmationCatalog = map[MessageCode]message{
	MsgConfirmTitle: {en: "Administrator password check", ko: "관리자 비밀번호 확인"},
	MsgConfirmScope: {
		en: "Settings, repository settings, deletion, imports, checks and credentials in this dashboard all follow this. Time counts from when the password was last typed in this browser; moving between pages does not extend it. A shorter time applies at once, also to browsers already confirmed, counted from when they typed the password. Signing out, End, or changing the administrator password ends a remembered check. The command line and the API always ask, for reads as well as changes.",
		ko: "이 대시보드의 설정, 저장소 설정, 삭제, 가져오기, 체크, 자격 증명 관리가 모두 이 규칙을 따릅니다. 이 브라우저에서 비밀번호를 마지막으로 입력한 때부터 시간을 재며, 페이지를 옮겨 다녀도 늘어나지 않습니다. 시간을 줄이면 이미 확인된 브라우저에도 비밀번호를 입력한 때부터 따져 바로 적용됩니다. 로그아웃하거나 종료를 누르거나 관리자 비밀번호를 바꾸면 기억해 둔 확인도 끝납니다. 명령줄과 API는 조회와 변경 모두 언제나 비밀번호를 묻습니다.",
	},
	MsgConfirmLabel: {en: "Ask for the administrator password", ko: "관리자 비밀번호 묻기"},
	MsgConfirmHelp: {
		en: "The longer the time, the longer anyone using this browser can make administrator changes, including deleting repositories and issuing credentials. “Do not ask” applies to every browser.",
		ko: "시간이 길수록 이 브라우저를 쓰는 사람이 저장소 삭제와 자격 증명 발급을 포함한 관리자 작업을 할 수 있는 시간도 길어집니다. ‘묻지 않기’는 모든 브라우저에 적용됩니다.",
	},
	MsgConfirmNeverRisk: {
		en: "Anyone who can access this dashboard can change settings, delete repositories, issue credentials, and turn on automatic checks without the administrator password. When anyone can reach OwnGit without a password, no sign-in is required.",
		ko: "이 대시보드에 들어올 수 있는 사람이면 누구나 관리자 비밀번호 없이 설정을 바꾸고, 저장소를 삭제하고, 자격 증명을 발급하고, 자동 체크를 켤 수 있습니다. 누구나 접속할 수 있게 열어 둔 상태라면 로그인도 필요 없습니다.",
	},
	MsgConfirmOpenRisk: {
		en: "Access is open right now, so this applies to anyone who can reach OwnGit.",
		ko: "지금은 누구나 접속할 수 있는 상태라서 OwnGit에 접속하는 모든 사람에게 해당합니다.",
	},
	MsgConfirmAck: {
		en: "I understand that anyone who can open this dashboard can then make administrator changes.",
		ko: "이 대시보드를 열 수 있는 사람이면 누구나 관리자 작업을 할 수 있게 된다는 점을 확인했습니다.",
	},
	MsgConfirmAckNeeded: {
		en: "Tick the box to confirm that you understand the risk.",
		ko: "위험을 이해했다면 확인란에 체크하세요.",
	},
	MsgConfirmLastTime: {
		en: "Turning the check off asks for the password one last time.",
		ko: "확인을 끄려면 비밀번호를 마지막으로 한 번 입력해야 합니다.",
	},
	MsgConfirmSaved: {
		en: "Saved. A shorter time applies at once, also to browsers already confirmed; a longer one applies from the next time the password is typed.",
		ko: "저장했습니다. 시간을 줄이면 이미 확인된 브라우저에도 바로 적용되고, 늘리면 다음에 비밀번호를 입력할 때부터 적용됩니다.",
	},
	MsgConfirmTurnedOff: {
		en: "The administrator password check is off. Anyone who can open the dashboard can make administrator changes until you turn it back on here.",
		ko: "관리자 비밀번호 확인을 껐습니다. 여기서 다시 켤 때까지 대시보드를 열 수 있는 사람은 누구나 관리자 작업을 할 수 있습니다.",
	},
	MsgConfirmUnknown: {
		en: "The saved choice was not recognized, perhaps saved by a newer OwnGit, so every change asks for the password. Choose one and save it to replace it.",
		ko: "저장된 선택을 알아볼 수 없어(새 버전의 OwnGit이 저장했을 수 있습니다) 변경할 때마다 비밀번호를 묻습니다. 하나를 골라 저장하면 바뀝니다.",
	},

	MsgConfirmEvery: {en: "Every time", ko: "매번 묻기"},
	MsgConfirm30m:   {en: "Again after 30 minutes", ko: "30분 뒤에 다시 묻기"},
	MsgConfirm1h:    {en: "Again after 1 hour", ko: "1시간 뒤에 다시 묻기"},
	MsgConfirm8h:    {en: "Again after 8 hours", ko: "8시간 뒤에 다시 묻기"},
	MsgConfirm1d:    {en: "Again after 1 day", ko: "하루 뒤에 다시 묻기"},
	MsgConfirm7d:    {en: "Again after 7 days", ko: "7일 뒤에 다시 묻기"},
	MsgConfirm30d:   {en: "Again after 30 days", ko: "30일 뒤에 다시 묻기"},
	MsgConfirmNever: {en: "Do not ask", ko: "묻지 않기"},

	MsgConfirmAfterEvery: {
		en: "Asked for every change, as set in Access.",
		ko: "접근 권한 설정에 따라 변경할 때마다 묻습니다.",
	},
	MsgConfirmAfter30m: {en: "After this, this browser does not ask again for 30 minutes.", ko: "입력하면 이 브라우저에서 30분 동안 다시 묻지 않습니다."},
	MsgConfirmAfter1h:  {en: "After this, this browser does not ask again for 1 hour.", ko: "입력하면 이 브라우저에서 1시간 동안 다시 묻지 않습니다."},
	MsgConfirmAfter8h:  {en: "After this, this browser does not ask again for 8 hours.", ko: "입력하면 이 브라우저에서 8시간 동안 다시 묻지 않습니다."},
	MsgConfirmAfter1d:  {en: "After this, this browser does not ask again for 1 day.", ko: "입력하면 이 브라우저에서 하루 동안 다시 묻지 않습니다."},
	MsgConfirmAfter7d:  {en: "After this, this browser does not ask again for 7 days.", ko: "입력하면 이 브라우저에서 7일 동안 다시 묻지 않습니다."},
	MsgConfirmAfter30d: {en: "After this, this browser does not ask again for 30 days.", ko: "입력하면 이 브라우저에서 30일 동안 다시 묻지 않습니다."},

	MsgConfirmRemembered: {
		en: "Confirmed as administrator, so this saves without the password.",
		ko: "관리자로 확인되어 비밀번호 없이 저장됩니다.",
	},
	MsgConfirmCheckOff: {
		en: "The password check is off, so this saves without it.",
		ko: "비밀번호 확인이 꺼져 있어 바로 저장됩니다.",
	},

	MsgConfirmIntroRemembered: {
		en: "You are confirmed as administrator, so changes save without the password for now. The display settings under General apply to this browser only.",
		ko: "관리자로 확인되어 지금은 비밀번호 없이 저장됩니다. 일반 탭의 화면 설정은 이 브라우저에만 적용됩니다.",
	},
	MsgConfirmIntroOff: {
		en: "The administrator password check is off, so changes save without it. You can turn it back on in Access.",
		ko: "관리자 비밀번호 확인이 꺼져 있어 비밀번호 없이 저장됩니다. 접근 권한 탭에서 다시 켤 수 있습니다.",
	},
	// Value: when the confirmation ends, a clock time today or a date.
	MsgConfirmUntil: {en: "Confirmed as administrator until %s", ko: "%s까지 관리자로 확인됨"},
	// Value: as for MsgConfirmUntil. Under Every time the confirmation only
	// opens the administrator pages.
	MsgConfirmPagesUntil: {en: "Administrator pages open until %s", ko: "%s까지 관리자 페이지 열림"},
	MsgConfirmOffBadge:   {en: "Administrator password check off", ko: "관리자 비밀번호 확인 꺼짐"},
	MsgConfirmAdminBody: {
		en: "Administrator pages and changes need the administrator password.",
		ko: "관리자 페이지를 열고 설정을 바꾸려면 관리자 비밀번호가 필요합니다.",
	},
}

// AdminConfirmChoice is one choice of "Ask for the administrator password".
type AdminConfirmChoice struct {
	Value string
	Label MessageCode
}

// AdminConfirmChoices lists the choices in the order Access shows them.
// Their values are those of state.AdminConfirmation.
func AdminConfirmChoices() []AdminConfirmChoice {
	return []AdminConfirmChoice{
		{"every", MsgConfirmEvery}, {"30m", MsgConfirm30m}, {"1h", MsgConfirm1h}, {"8h", MsgConfirm8h},
		{"1d", MsgConfirm1d}, {"7d", MsgConfirm7d}, {"30d", MsgConfirm30d}, {"never", MsgConfirmNever},
	}
}

// confirmAfter is what typing the administrator password does under
// choice, or "" when nothing needs saying.
func confirmAfter(choice string) MessageCode {
	return map[string]MessageCode{
		"every": MsgConfirmAfterEvery, "30m": MsgConfirmAfter30m, "1h": MsgConfirmAfter1h, "8h": MsgConfirmAfter8h,
		"1d": MsgConfirmAfter1d, "7d": MsgConfirmAfter7d, "30d": MsgConfirmAfter30d,
	}[choice]
}

func init() {
	registerMessages(confirmationCatalog)
}
