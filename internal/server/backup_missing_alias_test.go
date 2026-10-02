package server

import (
	"fmt"
	"html"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/backups"
	"owngit/internal/recovery"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func TestMissingBackupAliasesAreLocalizedWithoutClaimingConversion(t *testing.T) {
	fixture := newAPIFixture(t, false)
	remote, err := fixture.app.Repositories.Path("project")
	noErr(t, err)
	alias := "refs/heads/<missing>'&"
	apiRunGit(t, remote, "--git-dir", ".", "symbolic-ref", alias, "refs/heads/future")
	apiRunGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/chained-missing", alias)
	apiRunGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/cycle-a", "refs/heads/cycle-b")
	apiRunGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/cycle-b", "refs/heads/cycle-a")
	server, client, jar := openBrowser(t, fixture)
	browserAdminSessionFor(t, fixture, server.URL, jar, "missing-alias-backup")
	response := adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/backups/schedule", map[string]string{"destination": filepath.Join(t.TempDir(), "backups"), "scheduled": "off", "verify": "on"}, "admin-password")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("schedule status: %d", response.StatusCode)
	}
	_, err = fixture.app.Backups.StartNow()
	noErr(t, err)
	status := waitForBackups(t, fixture.app)
	if status.LastRun == nil || status.LastRun.Status != state.BackupSucceeded || !strings.Contains(status.LastRun.Message, recovery.MissingAliasBranchNotice) || strings.Contains(status.LastRun.Message, recovery.AliasBranchNotice) {
		t.Fatalf("unresolved aliases: %+v", status.LastRun)
	}
	message := status.LastRun.Message
	if len(message) > state.MaxBackupRunMessage {
		t.Fatalf("stored summary exceeds the message limit: %d bytes", len(message))
	}
	entries := []string{
		"project: " + alias + " -> refs/heads/future. " + (recovery.AliasBranch{Name: alias, Target: "refs/heads/future"}).ReconnectCommand(),
		"project: refs/heads/chained-missing -> " + alias + ". " + (recovery.AliasBranch{Name: "refs/heads/chained-missing", Target: alias}).ReconnectCommand(),
		"project: refs/heads/cycle-a -> refs/heads/cycle-b.",
		"project: refs/heads/cycle-b -> refs/heads/cycle-a.",
	}
	var displayed []string
	for _, entry := range entries {
		prefix, _, _ := strings.Cut(entry, " -> ")
		if strings.Contains(message, prefix) {
			if !strings.Contains(message, entry) {
				t.Fatalf("summary cut an alias entry: %s", message)
			}
			displayed = append(displayed, entry)
		}
	}
	omitted := len(entries) - len(displayed)
	if len(displayed) == 0 || omitted == 0 || !strings.HasSuffix(message, fmt.Sprintf(recovery.OmittedAliasNotice, omitted)) {
		t.Fatalf("summary lacks complete entries and its omitted count: %s", message)
	}
	if strings.Contains(message, "git symbolic-ref -- 'refs/heads/cycle-") {
		t.Fatalf("cycle notice recreates the cycle: %s", message)
	}
	if webui.Text(webui.LangEN, webui.MsgBackupUnresolvedAliasBranches) != recovery.UnresolvedAliasBranchNotice {
		t.Fatal("unresolved-alias English catalog text differs from the stored notice")
	}
	if webui.Text(webui.LangEN, webui.MsgBackupMissingAliasBranches) != recovery.MissingAliasBranchNotice {
		t.Fatal("missing-alias English catalog text differs from the stored notice")
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, server.URL+webui.SettingsTabURL(webui.SettingsStorage)+"?lang="+string(lang))
		if !strings.Contains(page.body, `">`+html.EscapeString(webui.Text(lang, webui.MsgBackupMissingAliasBranches))) {
			t.Fatalf("%s page lacks the selected missing-alias guidance", lang)
		}
		for _, entry := range displayed {
			if !strings.Contains(page.body, html.EscapeString(entry)) {
				t.Fatalf("%s page lacks the complete displayed entry: %s", lang, entry)
			}
		}
		marker := fmt.Sprintf(webui.Text(lang, webui.MsgBackupOmittedAliases), omitted)
		if !strings.Contains(page.body, "\n"+html.EscapeString(marker)+"</span>") {
			t.Fatalf("%s page lacks the selected omission count and log pointer", lang)
		}
		if strings.Contains(page.body, "<missing>") || strings.Contains(page.body, recovery.AliasBranchNotice) {
			t.Fatalf("%s page misstates or fails to escape an unresolved alias", lang)
		}
	}
}

func TestOmittedBackupAliasMarkerIsLocalizedAfterWarnings(t *testing.T) {
	warnings := "The backup is complete, but another warning remains."
	marker := fmt.Sprintf(recovery.OmittedAliasNotice, 12)
	run := &backups.RunView{Message: warnings + "\n" + marker}
	info := (&App{}).backupRun(run, state.Settings{})
	if info.MessageEN != run.Message || info.MessageKO != warnings+"\n"+fmt.Sprintf(webui.Text(webui.LangKO, webui.MsgBackupOmittedAliases), 12) {
		t.Fatalf("marker-only summary lost its warnings or localization: %+v", info)
	}
	if webui.Text(webui.LangEN, webui.MsgBackupOmittedAliases) != recovery.OmittedAliasNotice {
		t.Fatal("omission English catalog text differs from the stored notice")
	}
}

func TestUnresolvedOnlyBackupAliasesAreLocalized(t *testing.T) {
	fixture := newAPIFixture(t, false)
	remote, err := fixture.app.Repositories.Path("project")
	noErr(t, err)
	apiRunGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/a", "refs/heads/b")
	apiRunGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/b", "refs/heads/a")
	server, client, jar := openBrowser(t, fixture)
	browserAdminSessionFor(t, fixture, server.URL, jar, "cycle-alias-backup")
	response := adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/backups/schedule", map[string]string{"destination": filepath.Join(t.TempDir(), "backups"), "scheduled": "off", "verify": "on"}, "admin-password")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("schedule status: %d", response.StatusCode)
	}
	_, err = fixture.app.Backups.StartNow()
	noErr(t, err)
	status := waitForBackups(t, fixture.app)
	if status.LastRun == nil || status.LastRun.Status != state.BackupSucceeded || !strings.Contains(status.LastRun.Message, recovery.UnresolvedAliasBranchNotice) {
		t.Fatalf("cycle-only backup lacks its notice: %+v", status.LastRun)
	}
	if strings.Contains(status.LastRun.Message, recovery.MissingAliasBranchNotice) || strings.Contains(status.LastRun.Message, recovery.AliasBranchNotice) || strings.Contains(status.LastRun.Message, "git symbolic-ref") {
		t.Fatalf("cycle notice misstates the target or recreates the cycle: %s", status.LastRun.Message)
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, server.URL+webui.SettingsTabURL(webui.SettingsStorage)+"?lang="+string(lang))
		if !strings.Contains(page.body, `">`+html.EscapeString(webui.Text(lang, webui.MsgBackupUnresolvedAliasBranches))) || !strings.Contains(page.body, "project: refs/heads/a -&gt; refs/heads/b.") || !strings.Contains(page.body, "project: refs/heads/b -&gt; refs/heads/a.") {
			t.Fatalf("%s page lacks cycle-only guidance and targets", lang)
		}
	}
}
