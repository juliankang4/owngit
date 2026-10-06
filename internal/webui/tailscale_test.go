package webui

import (
	"fmt"
	"html"
	"regexp"
	"slices"
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
		off := render(t, r, allPages(lang)["settings-network"])
		for _, want := range []string{
			`id="grp-tailscale"`, text(MsgTSTitle), text(MsgTSOff), text(MsgSettingsTSSwitch), text(MsgTSHome), text(MsgTSMacApp),
			`name="action" value="save_tailscale"`, `name="home_network"`, `aria-describedby="ts-home-help"`, `id="ts-home-help"`,
			// The switch that turns sharing on is described by the notice.
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
		if sw := switchTag(t, off, "ts-switch"); strings.Contains(sw, " checked") || strings.Contains(sw, " disabled") {
			t.Errorf("%s: the switch of sharing that is off is not an unticked, usable switch: %s", lang, sw)
		}

		on := render(t, r, allPages(lang)["settings-tailscale"])
		for _, want := range []string{
			text(MsgTSWaiting), text(MsgTSAddress), text(MsgTSOffNote), text(MsgTSOffUnready), text(MsgTSChanged),
			text(TailscaleProblemCode("logged_out")), text(TailscaleWaitCode("restart")), text(TailscaleWaitCode("endpoint")),
			`id="ts-url"`, `value="https://owngit.tail0000.ts.net/"`, `data-copy="ts-url"`, "https://owngit.tail0000.ts.net/git/project.git",
			"https://owngit.tail0000.ts.net:443/ to http://localhost:3000", `name="action" value="save_tailscale"`,
		} {
			if !strings.Contains(on, want) {
				t.Errorf("%s: the Tailscale block while on lacks %q", lang, want)
			}
		}
		if sw := switchTag(t, on, "ts-switch"); !strings.Contains(sw, " checked") || strings.Contains(sw, " disabled") ||
			strings.Contains(on, "data-group-open") || strings.Contains(on, text(MsgTSReady)) {
			t.Errorf("%s: sharing that is not ready is shown as ready or offered again: %s", lang, sw)
		}
	}
}

// switchTag returns the input tag of the switch with the given ID.
func switchTag(t *testing.T, out, id string) string {
	t.Helper()
	at := strings.Index(out, `id="`+id+`"`)
	if at < 0 {
		t.Fatalf("no switch %s", id)
	}
	start := strings.LastIndex(out[:at], "<")
	return out[start : start+strings.Index(out[start:], ">")]
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
		out := render(t, r, SettingsPage{Chrome: chrome, Tab: SettingsNetwork, SubmitURL: "/settings/network"})
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

// Plain HTTP that came over the tailnet to this computer's Tailscale
// address is shown as encrypted by Tailscale, with what the page can tell,
// and the page does not ask to accept plain HTTP for it.
func TestConnectionOverTheTailnetNamesTailscale(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		chrome := fullChrome(lang)
		chrome.Connection = Connection{Tailnet: true, Host: "100.64.0.7:7654"}
		out := render(t, r, SettingsPage{Chrome: chrome, Tab: SettingsNetwork, SubmitURL: "/settings/network"})
		for _, want := range []string{wantText(lang, MsgConnTailnet), wantText(lang, MsgConnTailnetNote), "conn--secure"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the page lacks %q", lang, want)
			}
		}
		for _, unwanted := range []string{wantText(lang, MsgConnPlain), wantText(lang, MsgConnPlainDetail), wantText(lang, MsgConnTailscale), `value="acknowledge_insecure"`} {
			if strings.Contains(out, unwanted) {
				t.Errorf("%s: a tailnet connection shows %q", lang, unwanted)
			}
		}
		setup := render(t, r, SetupPage{Chrome: chrome, Stage: SetupWizard, SubmitURL: "/setup"})
		chrome.Connection = Connection{Host: "192.168.1.5:7654"}
		lan := render(t, r, SetupPage{Chrome: chrome, Stage: SetupWizard, SubmitURL: "/setup"})
		if strings.Contains(setup, `name="insecure_ack"`) || !strings.Contains(lan, `name="insecure_ack"`) {
			t.Errorf("%s: setup asks to accept plain HTTP over the tailnet, or not on the home network", lang)
		}
	}
	// It names Tailscale, not OwnGit, and claims nothing about the device
	// beyond the tailnet.
	if text := Text(LangEN, MsgConnTailnetNote); !strings.Contains(text, "tailnet device that sent it") || !strings.Contains(text, "plain HTTP") {
		t.Errorf("note %q", text)
	}
}

// With another control server than Tailscale's, the refusal does not
// point to a Tailscale admin console page that does not exist there, and
// the plain HTTP notice names private networks in general.
func TestPrivateNetworkWording(t *testing.T) {
	for _, lang := range Langs() {
		text := Text(lang, TailscaleProblemCode("https_unavailable"))
		if strings.Contains(text, "admin console") || strings.Contains(text, "관리 콘솔") || !strings.Contains(text, "Headscale") {
			t.Errorf("%s: %q", lang, text)
		}
		hint := Text(lang, MsgConnTailscale)
		for _, name := range []string{"Tailscale", "NetBird", "WireGuard"} {
			if !strings.Contains(hint, name) {
				t.Errorf("%s: the plain HTTP notice does not name %s: %q", lang, name, hint)
			}
		}
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
	page := SettingsPage{Chrome: fullChrome(LangKO), Tab: SettingsNetwork, SubmitURL: "/settings/network",
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
// it: both ways to rename the computer back, and the command with the port
// of the address shown.
func TestTailscaleStaleAddressIsExplained(t *testing.T) {
	r := newRenderer(t)
	port := map[Lang]string{LangEN: "PORT", LangKO: "포트"}
	for _, lang := range Langs() {
		page := SettingsPage{Chrome: fullChrome(lang), Tab: SettingsNetwork, SubmitURL: "/settings/network",
			Tailscale: TailscaleInfo{Stale: []TailscaleUse{{Kind: "proxy", Address: "https://oldbox.tail0000.ts.net:8443/", Target: "http://127.0.0.1:7654"}}}}
		out := render(t, r, page)
		if !strings.Contains(out, wantText(lang, MsgTSStale)) || !strings.Contains(out, "https://oldbox.tail0000.ts.net:8443/") {
			t.Errorf("%s: the earlier name's address is not explained", lang)
		}
		text := Text(lang, MsgTSStale)
		for _, want := range []string{"tailscale serve --https=" + port[lang] + " --set-path=/ off", "tailscale set --hostname=", "Tailscale"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: the explanation lacks %q: %q", lang, want, text)
			}
		}
	}
	if text := Text(LangEN, MsgTSStale); !strings.Contains(text, "admin console or with") {
		t.Errorf("the explanation does not name both ways to rename: %q", text)
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

// A Korean particle after a port number or another value would have to
// follow how the value is read (8443 and 10000 take 을, 7652 takes 를), so
// the Tailscale messages put none there. The note about passed ports says
// "port" for one and "ports" for several.
func TestTailscaleMessagesFitAnyPort(t *testing.T) {
	particle := regexp.MustCompile(`%(\[\d\])?[sdv](을|를|이|가|은|는|와|과|로|으로)`)
	for _, catalog := range []map[MessageCode]message{tailscaleCatalog, tailscaleBlockCatalog} {
		for code, text := range catalog {
			if match := particle.FindString(text.ko); match != "" {
				t.Errorf("%s: Korean %q has a particle right after a value", code, match)
			}
		}
	}
	for _, test := range []struct {
		passed       []string
		port, en, ko string
	}{
		{[]string{"443"}, "8443", "on HTTPS port 443 of this computer, so sharing uses port 8443", "HTTPS 포트 443에서"},
		{[]string{"443", "8443"}, "10000", "on each of these HTTPS ports of this computer: 443, 8443. Sharing uses port 10000", "HTTPS 포트 443, 8443에서"},
	} {
		code := TailscalePortNote(len(test.passed))
		list := strings.Join(test.passed, ", ")
		en, ko := fmt.Sprintf(Text(LangEN, code), list, test.port), fmt.Sprintf(Text(LangKO, code), list, test.port)
		if !strings.Contains(en, test.en) || !strings.Contains(ko, test.ko) || !strings.Contains(ko, test.port+" 포트를 쓰고") {
			t.Errorf("%d passed ports:\nEN %s\nKO %s", len(test.passed), en, ko)
		}
	}
}

// A viewer who is not the administrator gets a Tailscale problem without its
// detail (TailscaleProblemBrief), so a message that ends before the detail
// has a complete sentence for that viewer. Only the refusals named here end
// before their detail without one: the administrator alone gets them, from
// a change on the Settings page or the command line, always with the
// detail. Any other problem, such as a new kind of Tailscale failure, needs
// its brief.
func TestAViewerGetsACompleteSentenceForEveryTailscaleProblem(t *testing.T) {
	administratorOnly := []string{"port_taken", "owners_endpoint", "replace_changed", "not_replaceable", "endpoint_changed", "listen_option", "unrecorded"}
	for code := range catalog {
		problem, ok := strings.CutPrefix(string(code), "tailscale.problem.")
		if !ok || strings.HasSuffix(problem, "_brief") || strings.HasSuffix(problem, "_listed") || slices.Contains(administratorOnly, problem) {
			continue
		}
		for _, lang := range Langs() {
			if text := strings.TrimSpace(Text(lang, TailscaleProblemBrief(code))); strings.HasSuffix(text, ":") {
				t.Errorf("%s: a viewer gets %q, which ends before the detail it does not see; add %s_brief", code, text, code)
			}
		}
	}
	for _, problem := range administratorOnly {
		if !Has(MessageCode("tailscale.problem." + problem)) {
			t.Errorf("tailscale.problem.%s is named but not in the catalog", problem)
		}
	}
}

// Each port another service uses is listed with its own form, which sends
// exactly that port and the digest of what is listed and asks for the
// administrator password. A port OwnGit never replaces gets the reason and
// no form.
func TestTailscaleOccupiedPortsOfferTheirReplacement(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		page := SettingsPage{Chrome: fullChrome(lang), Tab: SettingsNetwork, SubmitURL: "/settings/network",
			Tailscale: TailscaleInfo{CanTurnOn: true, Name: "owngit.tail0000.ts.net", PortMode: "auto", FoundNote: MsgTSTakenBelow, Occupied: []TailscaleOccupied{
				{Port: "443", Address: "https://owngit.tail0000.ts.net/", Replaceable: true, Digest: "d443",
					Uses: []TailscaleUse{{Kind: "proxy", Address: "https://owngit.tail0000.ts.net:443/api", Target: "http://localhost:4000"}}},
				{Port: "8443", Address: "https://owngit.tail0000.ts.net:8443/", Digest: "d8443",
					Uses: []TailscaleUse{{Kind: "funnel", Address: "owngit.tail0000.ts.net:8443"}}},
			}}}
		out := render(t, r, page)
		replace := out[strings.Index(out, `class="tsreplace"`):]
		replace = replace[:strings.Index(replace, "</section>")]
		for _, want := range []string{
			`name="tailscale_https_port" value="443"`, `name="replace_digest" value="d443"`, `name="tailscale" value="on"`,
			`name="admin_password" type="password" required`, "localhost:4000", strings.Split(Text(lang, MsgTSReplaceNotAllowed), `"`)[0],
			strings.Split(Text(lang, MsgTSReplaceWarning), "%s")[0],
		} {
			if !strings.Contains(replace, want) {
				t.Errorf("%s: the replacement lacks %q", lang, want)
			}
		}
		if strings.Count(replace, "<form") != 1 || strings.Contains(replace, "d8443") {
			t.Errorf("%s: a Funnel port is offered for replacement", lang)
		}
		if !strings.Contains(out, `name="tailscale_port" data-saved="auto"`) || strings.Index(out, `id="ts-port-mode"`) > strings.Index(out, `class="tsreplace"`) {
			t.Errorf("%s: a free port is not the first choice", lang)
		}
	}
}
