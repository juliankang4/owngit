package webui

import (
	"bytes"
	"strings"
	"testing"
)

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
