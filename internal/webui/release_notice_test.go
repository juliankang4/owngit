package webui

import (
	"strings"
	"testing"
)

// The release notice carries both languages, so an in-place language switch
// rewrites it like every other sentence, and it links only to the URLs the
// backend supplied.
func TestReleaseNoticeRendersInBothLanguages(t *testing.T) {
	r := newRenderer(t)
	notice := &ReleaseNotice{
		Version: "1.0.3", Current: "1.0.2",
		NotesURL:   "https://github.com/juliankang4/owngit/releases/tag/v1.0.3",
		GuideURL:   "https://github.com/juliankang4/owngit#install",
		DismissURL: "/release-notice/dismiss",
	}
	for _, lang := range Langs() {
		out := render(t, r, OverviewPage{Chrome: fullChrome(lang), Release: notice})
		for _, want := range []string{
			`data-en="OwnGit 1.0.3 is available. You are running 1.0.2."`,
			`data-ko="OwnGit 1.0.3 버전이 나왔습니다. 지금 쓰는 버전은 1.0.2입니다."`,
			`href="` + notice.NotesURL + `"`, `href="` + notice.GuideURL + `"`,
			`name="version" value="1.0.3"`, `name="csrf" value="csrf-token-value"`,
			wantText(lang, MsgReleaseDismiss),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: notice lacks %q", lang, want)
			}
		}
	}
	if out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN)}); strings.Contains(out, "data-release-notice") {
		t.Error("a dashboard without a release shows a notice")
	}
}

func TestUpdateCheckSettingStates(t *testing.T) {
	r := newRenderer(t)
	cases := []struct {
		info  UpdateCheckInfo
		want  []MessageCode
		lacks []MessageCode
		value string
	}{
		{UpdateCheckInfo{Enabled: true}, []MessageCode{MsgSettingsUpdateOnNow, MsgSettingsUpdateTurnOff}, []MessageCode{MsgSettingsUpdateForced}, "off"},
		{UpdateCheckInfo{}, []MessageCode{MsgSettingsUpdateOffNow, MsgSettingsUpdateTurnOn}, []MessageCode{MsgSettingsUpdateForced}, "on"},
		{UpdateCheckInfo{Enabled: true, ForcedOff: true}, []MessageCode{MsgSettingsUpdateForced, MsgSettingsUpdateSavedOn}, []MessageCode{MsgSettingsUpdateOnNow}, "off"},
		{UpdateCheckInfo{ForcedOff: true}, []MessageCode{MsgSettingsUpdateForced, MsgSettingsUpdateSavedOff}, []MessageCode{MsgSettingsUpdateOffNow}, "on"},
	}
	for _, tc := range cases {
		for _, lang := range Langs() {
			out := render(t, r, SettingsPage{Chrome: fullChrome(lang), SubmitURL: "/settings", AccessMode: AccessOpen, UpdateCheck: tc.info})
			for _, code := range tc.want {
				if !strings.Contains(out, wantText(lang, code)) {
					t.Errorf("%+v/%s: lacks %s", tc.info, lang, code)
				}
			}
			for _, code := range tc.lacks {
				if strings.Contains(out, wantText(lang, code)) {
					t.Errorf("%+v/%s: shows %s", tc.info, lang, code)
				}
			}
			form := formFor(t, out, ActionSetUpdateCheck)
			if !strings.Contains(form, `name="update_check" value="`+tc.value+`"`) || !strings.Contains(form, `name="admin_password"`) {
				t.Errorf("%+v/%s: the form does not submit %q with the administrator password", tc.info, lang, tc.value)
			}
		}
	}
}
