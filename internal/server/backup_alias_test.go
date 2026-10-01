package server

import (
	"html"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/recovery"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func TestBackupAliasNoticeIsLocalizedAndEscaped(t *testing.T) {
	fixture := newAPIFixture(t, false)
	remote, err := fixture.app.Repositories.Path("project")
	noErr(t, err)
	alias := "refs/heads/<script>notice</script>'&"
	apiRunGit(t, remote, "--git-dir", ".", "symbolic-ref", alias, "refs/heads/main")
	server, client, jar := openBrowser(t, fixture)
	browserAdminSessionFor(t, fixture, server.URL, jar, "alias-backup")
	response := adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/backups/schedule", map[string]string{"destination": filepath.Join(t.TempDir(), "backups"), "scheduled": "off", "verify": "on"}, "admin-password")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("schedule status %d", response.StatusCode)
	}
	_, err = fixture.app.Backups.StartNow()
	noErr(t, err)
	status := waitForBackups(t, fixture.app)
	if status.LastRun == nil || status.LastRun.Status != state.BackupSucceeded || status.LastRun.Verification != state.BackupVerifyPassed {
		t.Fatalf("backup: %+v", status.LastRun)
	}
	if webui.Text(webui.LangEN, webui.MsgBackupAliasBranches) != recovery.AliasBranchNotice {
		t.Fatal("the stored alias notice and English catalog text differ")
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, server.URL+webui.SettingsTabURL(webui.SettingsStorage)+"?lang="+string(lang))
		for _, want := range []string{html.EscapeString(webui.Text(lang, webui.MsgBackupAliasBranches)), html.EscapeString("project: " + alias + " -> refs/heads/main"), "git symbolic-ref"} {
			if !strings.Contains(page.body, want) {
				t.Fatalf("%s page lacks %q", lang, want)
			}
		}
		if strings.Contains(page.body, "<script>notice</script>") {
			t.Fatal("alias was rendered as HTML instead of text")
		}
		if lang == webui.LangKO && strings.Contains(page.body, recovery.AliasBranchNotice) {
			t.Fatal("the Korean page kept the English alias explanation")
		}
	}
}
