package webui

import (
	"fmt"
	"html/template"
)

// The access policies on the Settings tabs: whether deleting a repository
// asks for its name, the login attempt limits, and whether a link from
// another site keeps the shared sign-in.
const (
	MsgDeleteNameTitle            MessageCode = "delete_name.title"
	MsgDeleteNameScope            MessageCode = "delete_name.scope"
	MsgDeleteNameLabel            MessageCode = "delete_name.label"
	MsgDeleteNameHelp             MessageCode = "delete_name.help"
	MsgDeleteNameOffWarning       MessageCode = "delete_name.off_warning"
	MsgDeleteNameOn               MessageCode = "delete_name.choice.on"
	MsgDeleteNameOff              MessageCode = "delete_name.choice.off"
	MsgDeleteNameSaved            MessageCode = "delete_name.saved"
	MsgDeleteNameSavedOff         MessageCode = "delete_name.saved_off"
	MsgDeleteNamePageOff          MessageCode = "delete_name.page_off"
	MsgDeleteNameUnreadableDelete MessageCode = "delete_name.unreadable_delete"

	MsgLoginLimitsTitle       MessageCode = "login_limits.title"
	MsgLoginLimitsScope       MessageCode = "login_limits.scope"
	MsgLoginLimitsSummary     MessageCode = "login_limits.summary"
	MsgLoginLimitsChange      MessageCode = "login_limits.change"
	MsgLoginLimitsAttempts    MessageCode = "login_limits.attempts"
	MsgLoginLimitsAttemptsHlp MessageCode = "login_limits.attempts_help"
	MsgLoginLimitsWindow      MessageCode = "login_limits.window"
	MsgLoginLimitsWindowHelp  MessageCode = "login_limits.window_help"
	MsgLoginLimitsPause       MessageCode = "login_limits.pause"
	MsgLoginLimitsPauseHelp   MessageCode = "login_limits.pause_help"
	MsgLoginLimitsWarning     MessageCode = "login_limits.warning"
	MsgLoginLimitsProxy       MessageCode = "login_limits.proxy"
	MsgLoginLimitsSaved       MessageCode = "login_limits.saved"
	MsgLoginLimitsSavedLooser MessageCode = "login_limits.saved_looser"
	MsgLoginLimitsInvalid     MessageCode = "login_limits.invalid"
	MsgLoginLimitsUnreadable  MessageCode = "login_limits.unreadable_sign_in"

	MsgCrossSiteTitle         MessageCode = "cross_site.title"
	MsgCrossSiteScope         MessageCode = "cross_site.scope"
	MsgCrossSiteLabel         MessageCode = "cross_site.label"
	MsgCrossSiteHelp          MessageCode = "cross_site.help"
	MsgCrossSiteLaxWarning    MessageCode = "cross_site.lax_warning"
	MsgCrossSiteStrict        MessageCode = "cross_site.choice.strict"
	MsgCrossSiteLax           MessageCode = "cross_site.choice.lax"
	MsgCrossSiteSaved         MessageCode = "cross_site.saved"
	MsgCrossSiteSavedLax      MessageCode = "cross_site.saved_lax"
	MsgCrossSiteUnreadableIn  MessageCode = "cross_site.unreadable_sign_in"
	MsgLoginLimitsSummaryNone MessageCode = "login_limits.summary_unreadable"
)

var accessPoliciesCatalog = map[MessageCode]message{
	MsgDeleteNameTitle: {en: "Deleting a repository", ko: "저장소 삭제"},
	MsgDeleteNameScope: {
		en: "The Delete page of every repository, from the next deletion after you save.",
		ko: "모든 저장소의 삭제 페이지에 적용되며, 저장한 뒤 하는 삭제부터 적용됩니다.",
	},
	MsgDeleteNameLabel: {en: "Repository name", ko: "저장소 이름"},
	MsgDeleteNameHelp: {
		en: "Whether the Delete page asks you to type the repository's name. It always shows the repository and asks what happens to its files and for the administrator password as Settings requires. The default is Ask for the name.",
		ko: "삭제 페이지에서 저장소 이름을 직접 입력하게 할지 정합니다. 어느 쪽이든 삭제 페이지는 저장소를 보여 주고, 파일을 어떻게 할지와 설정에 따른 관리자 비밀번호를 묻습니다. 기본값은 이름 입력 요구입니다.",
	},
	MsgDeleteNameOffWarning: {
		en: "With Do not ask, a mistaken deletion no longer needs the repository name. Check the repository shown on the Delete page before you delete it.",
		ko: "이름을 묻지 않으면 실수로 삭제할 때도 저장소 이름을 입력할 필요가 없습니다. 삭제하기 전에 삭제 페이지에 보이는 저장소가 맞는지 확인하세요.",
	},
	MsgDeleteNameOn:  {en: "Ask for the name", ko: "이름 입력 요구"},
	MsgDeleteNameOff: {en: "Do not ask", ko: "묻지 않음"},
	MsgDeleteNameSaved: {
		en: "Saved. Deleting a repository asks for its name.",
		ko: "저장했습니다. 저장소를 삭제할 때 이름을 입력해야 합니다.",
	},
	MsgDeleteNameSavedOff: {
		en: "Saved. Deleting a repository no longer asks for its name, so a mistaken deletion no longer needs it.",
		ko: "저장했습니다. 이제 저장소를 삭제할 때 이름을 묻지 않으므로, 실수로 삭제할 때도 이름이 필요 없습니다.",
	},
	MsgDeleteNamePageOff: {
		en: "Settings turn off typing the name here. Check that this is %s before you delete it.",
		ko: "설정에 따라 여기서는 이름을 입력하지 않습니다. 삭제하기 전에 이 저장소가 %s인지 확인하세요.",
	},
	MsgDeleteNameUnreadableDelete: {
		en: "Nothing was deleted because the saved choice whether deleting asks for the repository name cannot be read. Set it again under Settings, Repositories, or with owngit settings set --delete-requires-name.",
		ko: "삭제할 때 저장소 이름을 물을지 저장된 값을 읽을 수 없어 아무것도 삭제하지 않았습니다. 설정의 저장소 탭이나 owngit settings set --delete-requires-name으로 다시 정해 주세요.",
	},

	MsgLoginLimitsTitle: {en: "Login attempts", ko: "로그인 시도"},
	MsgLoginLimitsScope: {
		en: "Wrong passwords from one address, counted apart for the shared password and the administrator password, in the dashboard, Git and the API. New limits apply to wrong passwords after you save; an address paused now stays paused until its pause ends.",
		ko: "한 주소에서 틀린 비밀번호에 적용되며, 공용 비밀번호와 관리자 비밀번호를 따로 셉니다. 대시보드, Git, API 모두 해당합니다. 저장한 뒤 틀린 비밀번호부터 새 한도를 쓰고, 이미 멈춘 주소는 원래 끝나는 때까지 멈춰 있습니다.",
	},
	MsgLoginLimitsSummary: {
		en: "%[1]s wrong passwords within %[2]s pause the address for %[3]s.",
		ko: "%[2]s 안에 %[1]s번 틀리면 그 주소를 %[3]s 동안 멈춥니다.",
	},
	MsgLoginLimitsSummaryNone: {en: "The saved limits cannot be read.", ko: "저장된 한도를 읽을 수 없습니다."},
	MsgLoginLimitsChange:      {en: "Change the limits", ko: "한도 바꾸기"},
	MsgLoginLimitsAttempts:    {en: "Wrong passwords", ko: "틀린 비밀번호 횟수"},
	MsgLoginLimitsAttemptsHlp: {
		en: "How many wrong passwords within the window pause the address: from 1 to 100. The default is 4.",
		ko: "기간 안에 몇 번 틀리면 그 접속 주소의 로그인 시도를 일시 차단할지 정합니다. 1부터 100까지 정할 수 있고 기본값은 4입니다.",
	},
	MsgLoginLimitsWindow: {en: "Within", ko: "세는 기간"},
	MsgLoginLimitsWindowHelp: {
		en: "How long wrong passwords are counted together: from 1 minute to 24 hours. The default is 10 minutes.",
		ko: "틀린 비밀번호를 함께 세는 기간입니다. 1분부터 24시간까지 정할 수 있고 기본값은 10분입니다.",
	},
	MsgLoginLimitsPause: {en: "Pause", ko: "멈추는 시간"},
	MsgLoginLimitsPauseHelp: {
		en: "How long the address cannot sign in afterwards, even with the right password: from 1 minute to 24 hours. The default is 15 minutes.",
		ko: "그 뒤로 그 주소에서는 맞는 비밀번호로도 로그인할 수 없는 시간입니다. 1분부터 24시간까지 정할 수 있고 기본값은 15분입니다.",
	},
	MsgLoginLimitsWarning: {
		en: "More attempts, a shorter window or a shorter pause lets people who can reach OwnGit try more passwords.",
		ko: "횟수를 늘리거나 기간이나 멈추는 시간을 줄이면 OwnGit에 접속할 수 있는 사람이 비밀번호를 더 많이 시도할 수 있습니다.",
	},
	MsgLoginLimitsProxy: {
		en: "If people who reach OwnGit through the same proxy are paused together, add that proxy under Network, Trusted proxies first, so each person's own address is counted.",
		ko: "같은 프록시를 거쳐 접속하는 사람들이 함께 멈춘다면, 먼저 네트워크 탭의 신뢰하는 프록시에 그 프록시를 추가하세요. 그러면 사람마다 자기 주소로 셉니다.",
	},
	MsgLoginLimitsSaved: {
		en: "Saved. Wrong passwords from now on are counted with the new limits.",
		ko: "저장했습니다. 이제부터 틀린 비밀번호는 새 한도로 셉니다.",
	},
	MsgLoginLimitsSavedLooser: {
		en: "Saved. Wrong passwords from now on are counted with the new limits, which let people who can reach OwnGit try more passwords than the defaults.",
		ko: "저장했습니다. 이제부터 틀린 비밀번호는 새 한도로 셉니다. 기본값보다 느슨해서 OwnGit에 접속할 수 있는 사람이 비밀번호를 더 많이 시도할 수 있습니다.",
	},
	MsgLoginLimitsInvalid: {
		en: "Enter a number from 1 to 100.",
		ko: "1부터 100까지의 숫자를 입력하세요.",
	},
	MsgLoginLimitsUnreadable: {
		en: "The password was not accepted, and the attempt could not be counted, because the saved login attempt limits cannot be read. An administrator can set all three again under Settings, Access, or with owngit settings set --login-attempts 4 --login-window 10m --login-pause 15m (the defaults).",
		ko: "저장된 로그인 시도 한도를 읽을 수 없어 비밀번호를 받아들이지 않았고 이번 시도도 세지 못했습니다. 관리자가 설정의 접근 권한 탭에서 세 값을 모두 다시 정하거나 owngit settings set --login-attempts 4 --login-window 10m --login-pause 15m(기본값)을 실행하면 됩니다.",
	},

	MsgCrossSiteTitle: {en: "Links from other sites", ko: "다른 사이트의 링크"},
	MsgCrossSiteScope: {
		en: "Sign-ins with the shared password. A new choice applies to sign-ins after you save, and to this browser at once. The administrator confirmation is never kept on a link from another site.",
		ko: "공용 비밀번호로 한 로그인에 적용됩니다. 저장한 뒤 하는 로그인부터, 그리고 이 브라우저에는 바로 적용됩니다. 관리자 확인은 다른 사이트의 링크로 열 때 언제나 유지되지 않습니다.",
	},
	MsgCrossSiteLabel: {en: "A link from another site", ko: "다른 사이트에서 연 링크"},
	MsgCrossSiteHelp: {
		en: "When you open an OwnGit link from a chat, webmail or another site, Require fresh navigation shows the sign-in page, or the page without your sign-in, until you open it again from OwnGit. The default is Require fresh navigation.",
		ko: "채팅이나 웹메일, 다른 사이트에서 OwnGit 링크를 열면, 새로 열기 요구에서는 OwnGit 안에서 다시 열 때까지 로그인 페이지나 로그인하지 않은 화면이 보입니다. 기본값은 새로 열기 요구입니다.",
	},
	MsgCrossSiteLaxWarning: {
		en: "With Keep the sign-in, opening a link from another site can use this browser's shared sign-in. Forms sent from another site are still refused.",
		ko: "로그인 유지를 고르면 다른 사이트의 링크를 열 때 이 브라우저의 공용 로그인이 쓰일 수 있습니다. 다른 사이트에서 보낸 양식은 여전히 거부됩니다.",
	},
	MsgCrossSiteStrict: {en: "Require fresh navigation", ko: "새로 열기 요구"},
	MsgCrossSiteLax:    {en: "Keep the sign-in", ko: "로그인 유지"},
	MsgCrossSiteSaved: {
		en: "Saved. A link from another site opens without the shared sign-in.",
		ko: "저장했습니다. 다른 사이트의 링크는 공용 로그인 없이 열립니다.",
	},
	MsgCrossSiteSavedLax: {
		en: "Saved. A link from another site keeps the shared sign-in, so opening it can use this browser's sign-in.",
		ko: "저장했습니다. 다른 사이트의 링크도 공용 로그인을 유지하므로, 그 링크를 열 때 이 브라우저의 로그인이 쓰일 수 있습니다.",
	},
	MsgCrossSiteUnreadableIn: {
		en: "Nobody can sign in with the shared password because the saved choice for links from other sites cannot be read. An administrator can set it again under Settings, Access, or with owngit settings set --cross-site-links.",
		ko: "다른 사이트의 링크에 대해 저장된 값을 읽을 수 없어 지금은 공용 비밀번호로 로그인할 수 없습니다. 관리자가 설정의 접근 권한 탭이나 owngit settings set --cross-site-links로 다시 정하면 됩니다.",
	},
}

// DeleteNameChoices lists whether deleting asks for the name, Ask first.
// Their values are those the settings API uses.
func DeleteNameChoices() []PolicyChoice {
	return []PolicyChoice{{"on", MsgDeleteNameOn}, {"off", MsgDeleteNameOff}}
}

// CrossSiteChoices lists the cross-site link choices, the default first.
// Their values are those of state.CrossSiteLinks.
func CrossSiteChoices() []PolicyChoice {
	return []PolicyChoice{{"strict", MsgCrossSiteStrict}, {"lax", MsgCrossSiteLax}}
}

// biLoginLimits summarizes the login attempt limits p shows, such as "4
// wrong passwords within 10 minutes pause the address for 15 minutes".
func biLoginLimits(lang Lang, p Policies) template.HTML {
	window, windowErr := ParseLimit(LimitDuration, p.LoginWindow)
	pause, pauseErr := ParseLimit(LimitDuration, p.LoginPause)
	if windowErr != nil || pauseErr != nil || p.LoginAttempts == "" {
		return biText(lang, Text(LangEN, MsgLoginLimitsSummaryNone), Text(LangKO, MsgLoginLimitsSummaryNone))
	}
	text := func(lang Lang) string {
		return fmt.Sprintf(Text(lang, MsgLoginLimitsSummary), p.LoginAttempts, humanLimit(lang, LimitDuration, window), humanLimit(lang, LimitDuration, pause))
	}
	return biText(lang, text(LangEN), text(LangKO))
}

func init() {
	registerMessages(accessPoliciesCatalog)
}
