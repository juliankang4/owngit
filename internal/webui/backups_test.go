package webui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// A running backup shows its own message as its own line below the state
// list, in the text style and wrapping as text: a finished backup whose
// record the state store has not saved yet says so there. The message is
// shown escaped, in either language, and only when there is one.
func TestRunningBackupShowsItsMessageBelowTheState(t *testing.T) {
	r := newRenderer(t)
	render := func(lang Lang, run *BackupRunInfo) string {
		var out bytes.Buffer
		data := struct {
			Page   SettingsPage
			Lang   Lang
			Chrome Chrome
		}{Page: SettingsPage{Backups: BackupsInfo{Visible: true, Configured: true, Running: run}}, Lang: lang}
		noErr(t, r.templates["settings"].ExecuteTemplate(&out, "setBackups", data))
		return out.String()
	}
	// The Running now cell alone, which is narrow: the message must not sit
	// in it, where a sentence breaks inside a word.
	runningCell := func(page string) string {
		at := strings.Index(page, `data-en="Running now"`)
		if at < 0 {
			t.Fatalf("the page names no running backup:\n%s", page)
		}
		rest := page[at:]
		return rest[:strings.Index(rest, "</dd>")+len("</dd>")]
	}
	started := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	run := &BackupRunInfo{Status: "running", StartedAt: started, Message: "The backup finished: succeeded <safe>"}
	want := `<p class="bkmsg" dir="auto">The backup finished: succeeded &lt;safe&gt;</p>`
	for _, lang := range []Lang{LangEN, LangKO} {
		page := render(lang, run)
		if !strings.Contains(page, want) {
			t.Fatalf("%s: the running backup's message is missing from its own line:\n%s", lang, page)
		}
		if cell := runningCell(page); strings.Contains(cell, "finished") {
			t.Fatalf("%s: the message is inside the running-now cell: %s", lang, cell)
		}
		if quiet := render(lang, &BackupRunInfo{Status: "running", StartedAt: started}); strings.Contains(quiet, "bkmsg") {
			t.Fatalf("%s: a running backup without a message shows one:\n%s", lang, quiet)
		}
	}
	// A message with a language pair keeps both, in the same place and
	// style.
	localized := &BackupRunInfo{Status: "running", StartedAt: started, Message: "English <alias>", MessageEN: "English <alias>", MessageKO: "한국어 <별칭>"}
	page := render(LangKO, localized)
	localizedWant := `<p class="bkmsg" dir="auto"><span data-en="English &lt;alias&gt;" data-ko="한국어 &lt;별칭&gt;">한국어 &lt;별칭&gt;</span></p>`
	if !strings.Contains(page, localizedWant) {
		t.Fatalf("the localized message lacks the escaped language pair:\n%s", page)
	}
}

// When where a backup will be could not be checked, the restore part names
// the backup and the problem and nothing else: no step to stop OwnGit,
// rename its folders or start it again, since no command could follow. A
// guide with a command shows every step.
func TestUncheckedRestoreDoesNotInviteFolderMoves(t *testing.T) {
	r := newRenderer(t)
	guide := BackupRestore{
		StateDir: "/synthetic/state", MovedState: "/synthetic/state.before-restore",
		RepositoryRoot: "/synthetic/repositories", MovedRepositories: "/synthetic/repositories.before-restore",
	}
	render := func(t *testing.T, lang Lang, guide BackupRestore) string {
		t.Helper()
		var out bytes.Buffer
		data := struct {
			Lang Lang
			R    *BackupRestore
			ID   string
			Open bool
		}{lang, &guide, "restore", true}
		noErr(t, r.templates["settings"].ExecuteTemplate(&out, "backupRestore", data))
		return out.String()
	}
	for _, lang := range []Lang{LangEN, LangKO} {
		t.Run(string(lang), func(t *testing.T) {
			unchecked := guide
			unchecked.Unchecked, unchecked.Problem = "/synthetic/alias/uploads/b", "stat: permission denied"
			page := render(t, lang, unchecked)
			if !strings.Contains(page, "/synthetic/alias/uploads/b") || !strings.Contains(page, "stat: permission denied") {
				t.Fatalf("the unchecked backup or its problem is missing:\n%s", page)
			}
			for _, step := range []string{`class="bksteps"`, "/synthetic/state.before-restore", "/synthetic/repositories.before-restore", "owngit restore"} {
				if strings.Contains(page, step) {
					t.Fatalf("an unchecked restore shows %q:\n%s", step, page)
				}
			}

			ready := guide
			ready.Command = "owngit restore --input /synthetic/backups/b --verify"
			page = render(t, lang, ready)
			for _, step := range []string{`class="bksteps"`, "/synthetic/state.before-restore", "/synthetic/repositories.before-restore", ready.Command} {
				if !strings.Contains(page, step) {
					t.Fatalf("a restore with a command lacks %q:\n%s", step, page)
				}
			}
		})
	}
}
