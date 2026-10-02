package server

import (
	"html"
	"net/http"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS == "windows" {
		alias = "refs/heads/notice'&한글"
	}
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
		if strings.Contains(page.body, alias) || strings.Contains(page.body, "<script>notice</script>") {
			t.Fatal("alias was rendered as HTML instead of text")
		}
		if !strings.Contains(page.body, `">`+html.EscapeString(webui.Text(lang, webui.MsgBackupAliasBranches))) {
			t.Fatalf("%s page did not select its alias explanation", lang)
		}
		if !strings.Contains(page.body, `data-en="`+html.EscapeString(recovery.AliasBranchNotice)) || !strings.Contains(page.body, `data-ko="`+html.EscapeString(webui.Text(webui.LangKO, webui.MsgBackupAliasBranches))) {
			t.Fatal("the alias notice cannot switch language without reloading")
		}
	}
}
