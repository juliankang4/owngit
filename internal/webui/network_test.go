package webui

import (
	"html"
	"strings"
	"testing"
)

// The Network block compares the next start with the running server in
// words: every value carries its column name, and a value that waits for a
// restart says so next to it, so neither the header row nor colour is needed
// to read the state.
func TestNetworkSettingsStateIsWrittenOut(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, allPages(lang)["settings"])
		text := func(code MessageCode) string { return html.EscapeString(Text(lang, code)) }
		if got := strings.Count(out, `class="netrow__pending"`); got != 2 {
			t.Errorf("%s: pending rows=%d, want 2", lang, got)
		}
		if got := strings.Count(out, text(MsgNetPending)); got < 2 {
			t.Errorf("%s: pending rows do not say %q", lang, Text(lang, MsgNetPending))
		}
		// Four rows, two values each, each named.
		if got := strings.Count(out, `class="netrow__k"`); got != 8 {
			t.Errorf("%s: named values=%d, want 8", lang, got)
		}
		for _, code := range []MessageCode{MsgNetRestart, MsgNetOptionNote, MsgNetFromOption, MsgNetPlainHTTP, MsgNetHTTPSProxy, MsgNetChange, MsgNetSave, MsgNetNoBaseURL} {
			if !strings.Contains(out, text(code)) {
				t.Errorf("%s: the Network block lacks %q", lang, Text(lang, code))
			}
		}
		// Untranslated values keep their exact text in both languages.
		for _, value := range []string{"[::]:8080", "http://owngit.local:8080", "nas.lan", "owngit network reset", `placeholder="localhost:7654"`} {
			if !strings.Contains(out, value) {
				t.Errorf("%s: the Network block lacks %q", lang, value)
			}
		}
		// Help text is attached to its field.
		for _, id := range []string{"net-listen-help", "net-base-help", "net-hosts-help", "net-proxies-help"} {
			if !strings.Contains(out, `aria-describedby="`+id+`"`) || !strings.Contains(out, `id="`+id+`"`) {
				t.Errorf("%s: %s is not attached to its field", lang, id)
			}
		}
	}
}
