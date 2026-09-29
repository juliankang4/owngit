package webui

// Server-wide policies on the Settings tabs: each group's title, what it
// applies to and from when, and the words of its choices.
const (
	// MsgPolicyUnreadable is shown in a group whose saved value cannot be
	// read.
	MsgPolicyUnreadable MessageCode = "policy.unreadable"
	MsgPolicyAdminOnly  MessageCode = "policy.admin_only"

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

	MsgInitialBranchTitle   MessageCode = "initial_branch.title"
	MsgInitialBranchScope   MessageCode = "initial_branch.scope"
	MsgInitialBranchLabel   MessageCode = "initial_branch.label"
	MsgInitialBranchHelp    MessageCode = "initial_branch.help"
	MsgInitialBranchInvalid MessageCode = "initial_branch.invalid"
	MsgInitialBranchSaved   MessageCode = "initial_branch.saved"

	MsgTransferTitle    MessageCode = "transfer.title"
	MsgTransferScope    MessageCode = "transfer.scope"
	MsgTransferSize     MessageCode = "transfer.size"
	MsgTransferSizeHelp MessageCode = "transfer.size_help"
	MsgTransferTime     MessageCode = "transfer.time"
	MsgTransferTimeHelp MessageCode = "transfer.time_help"
	MsgTransferHigher   MessageCode = "transfer.higher"
	MsgTransferSaved    MessageCode = "transfer.saved"

	MsgCheckLogsTitle      MessageCode = "check_logs.title"
	MsgCheckLogsScope      MessageCode = "check_logs.scope"
	MsgCheckLogsLabel      MessageCode = "check_logs.label"
	MsgCheckLogsHelp       MessageCode = "check_logs.help"
	MsgCheckLogsSaved      MessageCode = "check_logs.saved"
	MsgCheckLogs7d         MessageCode = "check_logs.choice.7d"
	MsgCheckLogs30d        MessageCode = "check_logs.choice.30d"
	MsgCheckLogs90d        MessageCode = "check_logs.choice.90d"
	MsgCheckLogs365d       MessageCode = "check_logs.choice.365d"
	MsgCheckLogsIndefinite MessageCode = "check_logs.choice.indefinite"

	MsgKeptHistoryTitle      MessageCode = "kept_history.title"
	MsgKeptHistoryScope      MessageCode = "kept_history.scope"
	MsgKeptHistoryLabel      MessageCode = "kept_history.label"
	MsgKeptHistoryHelp       MessageCode = "kept_history.help"
	MsgKeptHistoryOffWarning MessageCode = "kept_history.off_warning"
	MsgKeptHistoryOn         MessageCode = "kept_history.choice.on"
	MsgKeptHistoryOff        MessageCode = "kept_history.choice.off"
	MsgKeptHistorySaved      MessageCode = "kept_history.saved"
	MsgKeptHistorySavedOff   MessageCode = "kept_history.saved_off"

	// What stops while a saved setting cannot be read, and how to fix it.
	MsgSessionUnreadableSignIn MessageCode = "session.unreadable_sign_in"
	MsgBranchUnreadableCreate  MessageCode = "initial_branch.unreadable_create"
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
		en: "The longer it lasts, the longer someone using a signed-in browser can use the dashboard without the password.",
		ko: "길게 할수록 로그인된 브라우저를 쓰는 사람이 비밀번호 없이 대시보드를 쓸 수 있는 시간도 길어집니다.",
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

	MsgInitialBranchTitle: {en: "New repositories", ko: "새 저장소"},
	MsgInitialBranchScope: {
		en: "Repositories created after you save, in the dashboard, on the command line or through the API. Existing repositories keep their branches, and an import takes its source's default branch. Change one repository's default branch on its own settings page.",
		ko: "저장한 뒤 대시보드, 명령줄, API로 만드는 저장소에 적용됩니다. 이미 있는 저장소의 브랜치는 그대로이고, 가져온 저장소는 원본의 기본 브랜치를 씁니다. 저장소 하나의 기본 브랜치는 그 저장소의 설정 페이지에서 바꿉니다.",
	},
	MsgInitialBranchLabel: {en: "Initial branch", ko: "첫 브랜치"},
	MsgInitialBranchHelp: {
		en: "The branch a new repository starts on, which becomes its default branch. Up to 100 letters, digits, \"-\", \"_\", \".\" and \"/\". The default is main.",
		ko: "새 저장소의 첫 브랜치로, 그 저장소의 기본 브랜치가 됩니다. 영문자, 숫자, \"-\", \"_\", \".\", \"/\"로 100자까지 씁니다. 기본값은 main입니다.",
	},
	MsgInitialBranchInvalid: {
		en: "OwnGit cannot use this branch name. Use up to 100 letters, digits, \"-\", \"_\", \".\" and \"/\", in a name Git accepts, such as main or trunk.",
		ko: "이 브랜치 이름은 쓸 수 없습니다. main이나 trunk처럼 Git이 받아들이는 이름을 영문자, 숫자, \"-\", \"_\", \".\", \"/\"로 100자까지 쓰세요.",
	},
	MsgInitialBranchSaved: {
		en: "Saved. Repositories created from now on start on this branch.",
		ko: "저장했습니다. 이제부터 만드는 저장소는 이 브랜치로 시작합니다.",
	},

	MsgTransferTitle: {en: "Git transfers", ko: "Git 전송"},
	MsgTransferScope: {
		en: "Clones, fetches, pushes and archive downloads that start after you save. Transfers already running keep their limits.",
		ko: "저장한 뒤 시작하는 클론, 가져오기(fetch), 푸시, 압축 파일 내려받기에 적용됩니다. 이미 진행 중인 전송은 원래 한도를 그대로 씁니다.",
	},
	MsgTransferSize: {en: "Largest transfer", ko: "최대 전송 크기"},
	MsgTransferSizeHelp: {
		en: "The most one transfer may receive and, separately, the most it may send: from 1 MB to 64 GB (1 GB is 1024 MB). The default is 4 GB. A larger push is refused, and a larger clone or fetch is cut off.",
		ko: "전송 하나가 받을 수 있는 최대 크기이자, 따로 보낼 수 있는 최대 크기입니다. 1 MB부터 64 GB까지 정할 수 있고(1 GB는 1024 MB) 기본값은 4 GB입니다. 이보다 큰 푸시는 거부되고, 이보다 큰 클론이나 가져오기(fetch)는 중간에 끊깁니다.",
	},
	MsgTransferTime: {en: "Longest transfer", ko: "최대 전송 시간"},
	MsgTransferTimeHelp: {
		en: "How long one transfer may take: from 1 minute to 24 hours. The default is 30 minutes.",
		ko: "전송 하나에 걸릴 수 있는 최대 시간입니다. 1분부터 24시간까지 정할 수 있고 기본값은 30분입니다.",
	},
	MsgTransferHigher: {
		en: "Higher limits let large or slow transfers keep the server busy for longer and use more disk space.",
		ko: "한도를 높이면 크거나 느린 전송이 서버를 더 오래 붙잡고 디스크도 더 많이 씁니다.",
	},
	MsgTransferSaved: {
		en: "Saved. Transfers that start from now on use the new limits.",
		ko: "저장했습니다. 이제부터 시작하는 전송에 새 한도가 적용됩니다.",
	},

	MsgCheckLogsTitle: {en: "Raw check logs", ko: "체크 원본 로그"},
	MsgCheckLogsScope: {
		en: "The full output of project checks, counted from when each check started. A new choice applies at once to every raw log kept now: a log it no longer keeps cannot be opened, and the next cleanup, when OwnGit starts or a check result arrives, deletes it. Check results, summaries and excerpts are always kept.",
		ko: "프로젝트 체크의 전체 출력에 적용되며, 각 체크가 시작된 때부터 기간을 셉니다. 새로 고른 기간은 지금 보관 중인 원본 로그 전체에 바로 적용됩니다. 기간이 지난 로그는 더는 열 수 없고, OwnGit이 시작하거나 체크 결과가 들어올 때 하는 다음 정리에서 지워집니다. 체크 결과, 요약, 발췌는 언제나 남습니다.",
	},
	MsgCheckLogsLabel: {en: "Keep raw logs", ko: "원본 로그 보관"},
	MsgCheckLogsHelp: {
		en: "A shorter time deletes older raw logs, which are then no longer available. A longer time, or keeping them indefinitely, uses more space in the state database.",
		ko: "기간을 줄이면 오래된 원본 로그가 지워져 더는 볼 수 없습니다. 기간을 늘리거나 계속 보관하면 상태 데이터베이스가 공간을 더 씁니다.",
	},
	MsgCheckLogsSaved: {
		en: "Saved. Every raw log kept now follows the new choice.",
		ko: "저장했습니다. 지금 보관 중인 원본 로그 모두에 새 기간이 적용됩니다.",
	},
	MsgCheckLogs7d:         {en: "7 days", ko: "7일"},
	MsgCheckLogs30d:        {en: "30 days", ko: "30일"},
	MsgCheckLogs90d:        {en: "90 days", ko: "90일"},
	MsgCheckLogs365d:       {en: "1 year", ko: "1년"},
	MsgCheckLogsIndefinite: {en: "Keep indefinitely", ko: "계속 보관"},

	MsgKeptHistoryTitle: {en: "Kept history", ko: "보관된 기록"},
	MsgKeptHistoryScope: {
		en: "Every repository that follows the server setting, which is every repository whose own Settings tab does not choose otherwise. A change applies to pushes and imports that start after you save.",
		ko: "서버 설정을 따르는 모든 저장소에 적용됩니다. 저장소의 설정 탭에서 따로 정하지 않았다면 모두 서버 설정을 따릅니다. 저장한 뒤 시작하는 푸시와 가져오기부터 적용됩니다.",
	},
	MsgKeptHistoryLabel: {en: "Overwritten and deleted history", ko: "덮어쓰거나 지운 기록"},
	MsgKeptHistoryHelp: {
		en: "When a force push, an import or a deletion replaces the commits of a branch or tag, OwnGit keeps them as kept history, which you can browse and restore. The default is Keep.",
		ko: "강제 푸시나 가져오기, 삭제로 브랜치나 태그의 커밋이 바뀌면 OwnGit은 이전 커밋을 보관된 기록으로 남겨 두고, 이 기록은 둘러보거나 되돌릴 수 있습니다. 기본값은 보관입니다.",
	},
	MsgKeptHistoryOffWarning: {
		en: "With Do not keep, commits that a force push, an import or a deletion replaces after you save are not kept, so they are not listed in kept history and cannot be restored from it. A commit that is no longer kept may still open by its full commit ID. History kept before stays and can still be restored; nothing is deleted.",
		ko: "보관하지 않음을 고르면, 저장한 뒤 강제 푸시나 가져오기, 삭제로 바뀐 커밋은 보관되지 않아 보관된 기록에 나오지 않고 그 기록에서 되돌릴 수도 없습니다. 보관되지 않은 커밋도 전체 커밋 ID로는 열릴 수 있습니다. 이미 보관된 기록은 그대로 남아 있어 계속 되돌릴 수 있고, 지워지는 것은 없습니다.",
	},
	MsgKeptHistoryOn:  {en: "Keep", ko: "보관"},
	MsgKeptHistoryOff: {en: "Do not keep", ko: "보관하지 않음"},
	MsgKeptHistorySaved: {
		en: "Saved. From now on, repositories that follow this setting keep overwritten and deleted history.",
		ko: "저장했습니다. 이제부터 이 설정을 따르는 저장소는 덮어쓰거나 지운 기록을 보관합니다.",
	},
	MsgKeptHistorySavedOff: {
		en: "Saved. From now on, repositories that follow this setting do not keep overwritten or deleted history. History already kept stays.",
		ko: "저장했습니다. 이제부터 이 설정을 따르는 저장소는 덮어쓰거나 지운 기록을 보관하지 않습니다. 이미 보관된 기록은 그대로 남습니다.",
	},

	MsgPolicyAdminOnly: {
		en: "Only an administrator sees the saved value and changes it.",
		ko: "저장된 값은 관리자로 확인한 뒤에 보고 바꿀 수 있습니다.",
	},
	MsgSessionUnreadableSignIn: {
		en: "Nobody can sign in with the shared password because the saved sign-in length cannot be read. An administrator can set it again with owngit settings set --session.",
		ko: "저장된 로그인 유지 시간을 읽을 수 없어 지금은 공용 비밀번호로 로그인할 수 없습니다. 관리자가 owngit settings set --session으로 다시 정하면 됩니다.",
	},
	MsgBranchUnreadableCreate: {
		en: "The repository was not created because the saved initial branch for new repositories cannot be read. Set it again under Settings, Repositories, or with owngit settings set --initial-branch.",
		ko: "새 저장소의 첫 브랜치로 저장된 값을 읽을 수 없어 저장소를 만들지 않았습니다. 설정의 저장소 탭이나 owngit settings set --initial-branch로 다시 정해 주세요.",
	},
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

// CheckLogChoices lists how long raw check logs can be kept, shortest
// first. Their values are those of state.CheckLogRetention.
func CheckLogChoices() []PolicyChoice {
	return []PolicyChoice{
		{"7d", MsgCheckLogs7d}, {"30d", MsgCheckLogs30d}, {"90d", MsgCheckLogs90d},
		{"365d", MsgCheckLogs365d}, {"indefinite", MsgCheckLogsIndefinite},
	}
}

// KeptHistoryChoices lists the server-wide kept history choices, Keep
// first. Their values are those the settings API uses.
func KeptHistoryChoices() []PolicyChoice {
	return []PolicyChoice{{"on", MsgKeptHistoryOn}, {"off", MsgKeptHistoryOff}}
}

func init() {
	for code, entry := range policiesCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}
