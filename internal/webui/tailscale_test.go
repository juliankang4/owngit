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
// opened is recorded, not code or other content, that the name can be
// changed before turning sharing on, and that a rename does not take an
// issued name out of the log.
func TestTailscaleCertificateNoticeSaysWhatIsAndIsNotRecorded(t *testing.T) {
	en := Text(LangEN, MsgTSCertLog)
	for _, part := range []string{"public certificate log", "Only the fact that the address was opened is recorded", "not your code, repositories, passwords", "Tailscale admin console before turning sharing on", "stay in the log, even after a rename"} {
		if !strings.Contains(en, part) {
			t.Errorf("English notice lacks %q: %q", part, en)
		}
	}
	ko := Text(LangKO, MsgTSCertLog)
	for _, part := range []string{"공개 인증서 로그", "주소를 열었다는 기록만 남을 뿐", "기록되지 않습니다", "공유를 켜기 전에 Tailscale 관리 콘솔에서", "이름을 바꿔도 로그에 남습니다"} {
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
		if !strings.Contains(out, wantText(lang, MsgConnTailscaleNote)) || strings.Contains(out, wantText(lang, MsgConnNoProof)) {
			t.Errorf("%s: Settings does not say that OwnGit sees HTTP from Tailscale on this computer", lang)
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

// What is on the port is described in the page's language, with the
// addresses kept as they are.
func TestTailscalePortListIsTranslated(t *testing.T) {
	r := newRenderer(t)
	uses := []TailscaleUse{
		{Kind: "proxy", Address: "https://owngit.tail0000.ts.net:443/", Target: "http://localhost:3000"},
		{Kind: "funnel", Address: "owngit.tail0000.ts.net:443"},
		{Kind: "tcp_forward", Address: "443", Target: "localhost:22"},
	}
	page := SettingsPage{Chrome: fullChrome(LangKO), SubmitURL: "/settings",
		Tailscale: TailscaleInfo{Found: uses, FoundNote: MsgTSTaken}}
	out := render(t, r, page)
	for _, want := range []string{
		">https://owngit.tail0000.ts.net:443/에서 http://localhost:3000(으)로 전달<",
		">owngit.tail0000.ts.net:443의 Funnel(공개 인터넷에 열림)<",
		">포트 443의 TCP 전달(localhost:22)<",
		`data-en="https://owngit.tail0000.ts.net:443/ to http://localhost:3000"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the Korean list lacks %q", want)
		}
	}
	for _, use := range uses {
		for _, lang := range Langs() {
			if text := TailscaleUseText(lang, use); strings.Contains(text, "%!") || !strings.Contains(text, use.Address) || !strings.Contains(text, use.Target) {
				t.Errorf("%s %s: %q", lang, use.Kind, text)
			}
		}
	}
}

// What Tailscale keeps under an earlier name is shown with how to remove
// it.
func TestTailscaleStaleAddressIsExplained(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		page := SettingsPage{Chrome: fullChrome(lang), SubmitURL: "/settings",
			Tailscale: TailscaleInfo{Stale: []TailscaleUse{{Kind: "proxy", Address: "https://oldbox.tail0000.ts.net:443/", Target: "http://127.0.0.1:7654"}}}}
		out := render(t, r, page)
		if !strings.Contains(out, wantText(lang, MsgTSStale)) || !strings.Contains(out, "https://oldbox.tail0000.ts.net:443/") ||
			!strings.Contains(out, "tailscale serve --https=443 --set-path=/ off") {
			t.Errorf("%s: the earlier name's address is not explained", lang)
		}
	}
}

// The page after turning off through the tailnet address is complete on
// its own: it follows the language and appearance and loads nothing else.
func TestTailscaleOffPageStandsAlone(t *testing.T) {
	r := newRenderer(t)
	for _, tc := range []struct {
		page   TailscaleOffPage
		scheme string
	}{
		{TailscaleOffPage{Lang: LangEN, Appearance: AppearanceSystem, Local: "http://localhost:7654/"}, `content="light dark"`},
		{TailscaleOffPage{Lang: LangKO, Appearance: AppearanceLight, Local: "http://localhost:7654/"}, `content="light"`},
	} {
		var out strings.Builder
		if err := r.RenderTailscaleOff(&out, tc.page); err != nil {
			t.Fatal(err)
		}
		body := out.String()
		for _, want := range []string{`<html lang="` + string(tc.page.Lang) + `">`, tc.scheme, Text(tc.page.Lang, MsgTSTurnedOff),
			Text(tc.page.Lang, "settings.tailscale.off_away"), `<a href="http://localhost:7654/settings">http://localhost:7654/</a>`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the page lacks %q", tc.page.Lang, want)
			}
		}
		for _, needs := range []string{"<script", "<link", "<img", "src=", "url("} {
			if strings.Contains(body, needs) {
				t.Errorf("%s: the page loads %q", tc.page.Lang, needs)
			}
		}
	}
}
