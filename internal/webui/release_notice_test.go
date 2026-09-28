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
	}{
		{UpdateCheckInfo{Enabled: true}, []MessageCode{MsgSettingsUpdateSwitch, MsgSettingsUpdateHelp}, []MessageCode{MsgSettingsUpdateForced}},
		{UpdateCheckInfo{}, []MessageCode{MsgSettingsUpdateSwitch, MsgSettingsUpdateHelp}, []MessageCode{MsgSettingsUpdateForced}},
		{UpdateCheckInfo{Enabled: true, ForcedOff: true}, []MessageCode{MsgSettingsUpdateForced, MsgSettingsUpdateSavedOn}, []MessageCode{MsgSettingsUpdateSavedOff}},
		{UpdateCheckInfo{ForcedOff: true}, []MessageCode{MsgSettingsUpdateForced, MsgSettingsUpdateSavedOff}, []MessageCode{MsgSettingsUpdateSavedOn}},
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
			// The switch shows the saved value, which stays editable while
			// the start option overrides it, and is saved with the
			// administrator password.
			form := formFor(t, out, ActionSetUpdateCheck)
			checked := `name="update_check" value="on" data-saved="off" aria-describedby`
			if tc.info.Enabled {
				checked = `name="update_check" value="on" data-saved="on" checked aria-describedby`
			}
			if !strings.Contains(strings.Join(strings.Fields(form), " "), checked) || !strings.Contains(form, `name="admin_password"`) {
				t.Errorf("%+v/%s: the switch is not %q with the administrator password", tc.info, lang, checked)
			}
		}
	}
}
