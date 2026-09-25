package webui

import (
	"html"
	"strings"
	"testing"
)

// The Tailscale block states what turning sharing on records in public,
// keeps the home network closed unless ticked, and shows the address with
// what it still waits for.
func TestTailscaleBlockSaysWhatSharingDoes(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		text := func(code MessageCode) string { return html.EscapeString(Text(lang, code)) }
		off := render(t, r, allPages(lang)["settings"])
		for _, want := range []string{
			`id="tailscale"`, text(MsgTSTitle), text(MsgTSOff), text(MsgTSTurnOn), text(MsgTSTurnOnButton), text(MsgTSHome), text(MsgTSMacApp),
			`name="action" value="tailscale_on"`, `name="home_network"`, `aria-describedby="ts-home-help"`, `id="ts-home-help"`,
			// The button that turns sharing on is described by the notice.
			`id="ts-log"`, `aria-describedby="ts-log"`,
			// The notice names this computer's name as it will appear in
			// the certificate log, and the help names both listen
			// addresses.
			"owngit.tail0000.ts.net", "[::]:8080", "localhost:8080",
		} {
			if !strings.Contains(off, want) {
				t.Errorf("%s: the Tailscale block while off lacks %q", lang, want)
			}
		}
		if strings.Contains(off, `name="home_network" value="1" checked`) {
			t.Errorf("%s: the home network is ticked for an owner who listens on this computer only", lang)
		}
		if strings.Contains(off, `value="tailscale_off"`) {
			t.Errorf("%s: the block offers to turn off sharing that is off", lang)
		}

		on := render(t, r, allPages(lang)["settings-tailscale"])
		for _, want := range []string{
			text(MsgTSWaiting), text(MsgTSAddress), text(MsgTSTurnOff), text(MsgTSTurnOffBtn), text(MsgTSChanged),
			text(TailscaleProblemCode("logged_out")), text(TailscaleWaitCode("restart")), text(TailscaleWaitCode("endpoint")),
			`id="ts-url"`, `value="https://owngit.tail0000.ts.net/"`, `data-copy="ts-url"`, "https://owngit.tail0000.ts.net/git/project.git",
			"https://owngit.tail0000.ts.net:443/ to http://localhost:3000", `name="action" value="tailscale_off"`,
		} {
			if !strings.Contains(on, want) {
				t.Errorf("%s: the Tailscale block while on lacks %q", lang, want)
			}
		}
		if strings.Contains(on, `value="tailscale_on"`) || strings.Contains(on, text(MsgTSReady)) {
			t.Errorf("%s: sharing that is not ready is shown as ready or offered again", lang)
		}
	}
}

// The certificate log notice says that only the fact that the address was
// opened is recorded, not code or other content, and the name can be
// changed.
func TestTailscaleCertificateNoticeSaysWhatIsAndIsNotRecorded(t *testing.T) {
	en := Text(LangEN, MsgTSCertLog)
	for _, part := range []string{"public certificate log", "Only the fact that the address was opened is recorded", "not your code, repositories, passwords", "Tailscale admin console"} {
		if !strings.Contains(en, part) {
			t.Errorf("English notice lacks %q: %q", part, en)
		}
	}
	ko := Text(LangKO, MsgTSCertLog)
	for _, part := range []string{"공개 인증서 로그", "주소를 열었다는 기록만 남을 뿐", "기록되지 않습니다", "관리 콘솔"} {
		if !strings.Contains(ko, part) {
			t.Errorf("Korean notice lacks %q: %q", part, ko)
		}
	}
	if home := Text(LangEN, MsgTSHome); !strings.Contains(home, "not encrypted") {
		t.Errorf("the home network choice does not say it is not encrypted: %q", home)
	}
	if home := Text(LangKO, MsgTSHome); !strings.Contains(home, "암호화되지 않음") {
		t.Errorf("the home network choice does not say it is not encrypted: %q", home)
	}
}

// A request through OwnGit's Tailscale endpoint says who encrypted it:
// Tailscale on this computer, not OwnGit.
func TestConnectionThroughTailscaleNamesTailscale(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		chrome := fullChrome(lang)
		chrome.Connection = Connection{Encrypted: true, Tailscale: true, Host: "owngit.tail0000.ts.net"}
		out := render(t, r, SettingsPage{Chrome: chrome, SubmitURL: "/settings"})
		if !strings.Contains(out, wantText(lang, MsgConnTailscaleOn)) || !strings.Contains(out, "conn--secure") {
			t.Errorf("%s: the indicator does not say Tailscale on this computer encrypted the request", lang)
		}
		if strings.Contains(out, wantText(lang, MsgConnEncrypted)) {
			t.Errorf("%s: a request encrypted by Tailscale is reported as encrypted by OwnGit", lang)
		}
	}
	if text := Text(LangEN, MsgConnTailscaleOn); !strings.Contains(text, "Tailscale") || !strings.Contains(text, "this computer") {
		t.Errorf("indicator text %q", text)
	}
	if text := Text(LangKO, MsgConnTailscaleOn); !strings.Contains(text, "Tailscale") || !strings.Contains(text, "이 컴퓨터") {
		t.Errorf("indicator text %q", text)
	}
}
