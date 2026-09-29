package webui

// Server-wide policies on the Settings tabs: each group's title, what it
// applies to and from when, and the words of its choices.
const (
	// MsgPolicyUnreadable is shown in a group whose saved value cannot be
	// read.
	MsgPolicyUnreadable MessageCode = "policy.unreadable"

	MsgSessionTitle MessageCode = "session.title"
	MsgSessionScope MessageCode = "session.scope"
	MsgSessionLabel MessageCode = "session.label"
	MsgSessionHelp  MessageCode = "session.help"
	MsgSessionSaved MessageCode = "session.saved"
	MsgSession1h    MessageCode = "session.choice.1h"
	MsgSession8h    MessageCode = "session.choice.8h"
	MsgSession12h   MessageCode = "session.choice.12h"
	MsgSession1d    MessageCode = "session.choice.1d"
	MsgSession7d    MessageCode = "session.choice.7d"
	MsgSession30d   MessageCode = "session.choice.30d"
)

var policiesCatalog = map[MessageCode]message{
	MsgPolicyUnreadable: {
		en: "The saved value cannot be read, so OwnGit cannot use this setting. The form shows the default; save it or another value to replace the saved one.",
		ko: "저장된 값을 읽을 수 없어 OwnGit이 이 설정을 쓰지 못합니다. 양식에는 기본값이 보이며, 이 값이나 다른 값을 저장하면 저장된 값이 바뀝니다.",
	},

	MsgSessionTitle: {en: "Stay signed in", ko: "로그인 유지"},
	MsgSessionScope: {
		en: "Sign-ins with the shared password, in every browser. A new time applies to sign-ins after you save; browsers already signed in keep the end they have. Without a shared password nobody signs in, so it has no effect.",
		ko: "공용 비밀번호로 로그인하는 모든 브라우저에 적용됩니다. 저장한 뒤 로그인할 때부터 새 시간을 쓰고, 이미 로그인한 브라우저는 원래 끝나는 시각을 그대로 씁니다. 공용 비밀번호를 쓰지 않으면 로그인할 일이 없으니 이 설정도 쓰이지 않습니다.",
	},
	MsgSessionLabel: {en: "A sign-in lasts", ko: "로그인 유지 시간"},
	MsgSessionHelp: {
		en: "The longer it lasts, the longer someone using a signed-in browser can read and push without the password.",
		ko: "길게 할수록 로그인된 브라우저를 쓰는 사람이 비밀번호 없이 저장소를 읽고 푸시할 수 있는 시간도 길어집니다.",
	},
	MsgSessionSaved: {
		en: "Saved. Sign-ins from now on last the new time.",
		ko: "저장했습니다. 이제부터 로그인하면 새 시간만큼 유지됩니다.",
	},
	MsgSession1h:  {en: "1 hour", ko: "1시간"},
	MsgSession8h:  {en: "8 hours", ko: "8시간"},
	MsgSession12h: {en: "12 hours", ko: "12시간"},
	MsgSession1d:  {en: "1 day", ko: "1일"},
	MsgSession7d:  {en: "7 days", ko: "7일"},
	MsgSession30d: {en: "30 days", ko: "30일"},
}

// PolicyChoice is one choice of a policy drawn as a menu.
type PolicyChoice struct {
	Value string
	Label MessageCode
}

// SessionChoices lists the lengths of a sign-in, shortest first. Their
// values are those of state.GeneralSession.
func SessionChoices() []PolicyChoice {
	return []PolicyChoice{
		{"1h", MsgSession1h}, {"8h", MsgSession8h}, {"12h", MsgSession12h},
		{"1d", MsgSession1d}, {"7d", MsgSession7d}, {"30d", MsgSession30d},
	}
}

func init() {
	for code, entry := range policiesCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}
