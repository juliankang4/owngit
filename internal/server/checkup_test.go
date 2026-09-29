package server

import (
	"context"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/webui"
)

// The checkup on the General tab shows each finding with the command that
// repairs it, only to a confirmed administrator, since findings hold this
// computer's paths. The checkup runs only for that administrator.
func TestSettingsCheckupIsForTheAdministrator(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", fixturePasswordHash(t, "admin-password"), true))
	app.Repositories.SetRoot(canonical)
	program := `C:\Users\you\Downloads\owngit & co\owngit.exe`
	runs := 0
	app.Diagnose = func(context.Context) []webui.Finding {
		runs++
		return []webui.Finding{{Code: webui.MsgDoctorWindowsRule, Args: []string{program}, Repair: "owngit service install"}, {Code: webui.MsgDoctorWindowsPublic}}
	}
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)

	body, _ := dashboardGET(t, client, server.URL+"/settings")
	if runs != 0 || strings.Contains(body, "owngit.exe") || !strings.Contains(body, "only the administrator sees it here") || !strings.Contains(body, `href="/admin/login?next=`) {
		t.Errorf("visitor: checkup ran %d times, page:\n%s", runs, body)
	}

	browserGET(t, client, server.URL+"/admin/login")
	browserForm(t, client, server.URL+"/admin/login", url.Values{"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "admin_password": {"admin-password"}, "next": {"/settings"}}, server.URL)
	body, _ = dashboardGET(t, client, server.URL+"/settings")
	if runs != 1 {
		t.Errorf("administrator: checkup ran %d times", runs)
	}
	for _, want := range []string{
		html.EscapeString(webui.Finding{Code: webui.MsgDoctorWindowsRule, Args: []string{program}}.Sentence(webui.LangEN)),
		html.EscapeString(webui.Text(webui.LangEN, webui.MsgDoctorWindowsPublic)),
		`value="owngit service install"`, `data-copy="checkup-repair-0"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("administrator page lacks %q", want)
		}
	}
	if strings.Contains(body, "checkup-repair-1") {
		t.Error("a finding without a repair has a command field")
	}

	// Other tabs do not run it.
	dashboardGET(t, client, server.URL+"/settings/network")
	if runs != 1 {
		t.Errorf("the Network tab ran the checkup")
	}
	app.Diagnose = func(context.Context) []webui.Finding { return nil }
	if body, _ = dashboardGET(t, client, server.URL+"/settings"); !strings.Contains(body, "OwnGit found no problem on this computer.") {
		t.Error("no clean line without findings")
	}
}
