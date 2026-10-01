package webui

import (
	"bytes"
	"html"
	"strings"
	"testing"
)

func TestBackupResultMessagesKeepPlainHTMLUnlessLocalized(t *testing.T) {
	r := newRenderer(t)
	render := func(lang Lang, run BackupRunInfo) string {
		var out bytes.Buffer
		data := struct {
			Page   SettingsPage
			Lang   Lang
			Chrome Chrome
		}{Page: SettingsPage{Backups: BackupsInfo{Visible: true, Configured: true, Runs: []BackupRunInfo{run}}}, Lang: lang}
		noErr(t, r.templates["settings"].ExecuteTemplate(&out, "setBackups", data))
		return out.String()
	}
	plain := BackupRunInfo{Status: "succeeded", Message: "original result <safe>"}
	wantPlain := `<p class="bkmsg mono" dir="auto">original result &lt;safe&gt;</p>`
	for _, lang := range []Lang{LangEN, LangKO} {
		if page := render(lang, plain); !strings.Contains(page, wantPlain) {
			t.Fatalf("plain message HTML changed: %s", page)
		}
	}
	localized := plain
	localized.MessageEN, localized.MessageKO = "English <alias>", "한국어 <별칭>"
	for _, lang := range []Lang{LangEN, LangKO} {
		chosen := localized.MessageEN
		if lang == LangKO {
			chosen = localized.MessageKO
		}
		want := `<p class="bkmsg" dir="auto"><span data-en="` + html.EscapeString(localized.MessageEN) + `" data-ko="` + html.EscapeString(localized.MessageKO) + `">` + html.EscapeString(chosen) + `</span></p>`
		if page := render(lang, localized); !strings.Contains(page, want) {
			t.Fatalf("localized message lacks the escaped language pair: %s", page)
		}
	}
}
