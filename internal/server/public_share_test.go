package server

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// The public share address answers share links and the files their pages
// load, and nothing else: every other path is the same plain not found,
// whatever the request carries. A link works there as on OwnGit's own
// address, and revoking it ends it on both.
func TestPublicShareAddressAnswersShareLinksOnly(t *testing.T) {
	serverLog := captureServerLog(t)
	fixture := newAPIFixture(t, true)
	private := serve(t, fixture.app.Handler())
	public := serve(t, fixture.app.PublicShareHandler())
	browse := createShare(t, private.URL, "project", map[string]any{"label": "Recruiter"})
	clone := createShare(t, private.URL, "project", map[string]any{"label": "Contractor", "scope": "clone"})
	secret := strings.TrimPrefix(browse.URL, private.URL+"/share/")
	cloneSecret := strings.TrimPrefix(clone.URL, private.URL+"/share/")

	get := func(target string, header http.Header) (int, string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, public.URL+target, nil)
		noErr(t, err)
		for name, values := range header {
			request.Header[name] = values
		}
		request.Host = "share.example.test"
		response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
		noErr(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		noErr(t, err)
		return response.StatusCode, string(body)
	}
	_, notFound := get("/", nil)
	withFunnel := http.Header{"Tailscale-Funnel-Request": {"?1"}}
	withAdmin := http.Header{"Authorization": {"Basic " + basicCredentials("admin", "admin-password")}}
	for _, path := range []string{
		"/", "/login", "/setup", "/setup/redeem", "/settings", "/activity", "/new", "/import",
		"/repositories/project", "/repositories/project/share-links", "/project",
		"/api/v1/repositories", "/api/v1/repositories/project/share-links", "/api/v1/settings",
		"/git/project.git/info/refs?service=git-upload-pack", "/git/project.git/info/refs?service=git-receive-pack",
		HealthPath, TrayStatusPath, TrayEventsPath,
		"/assets/", "/assets/owngit.css.map", "/assets/owngit.js.map", "/assets/fonts/PRETENDARD-LICENSE.txt", "/assets/../settings",
		"/share", "/sharex", "/share/x/../../settings",
	} {
		for name, header := range map[string]http.Header{"plain": nil, "Funnel": withFunnel, "administrator": withAdmin} {
			status, body := get(path, header)
			if status != http.StatusNotFound || body != notFound {
				t.Errorf("%s %s: status=%d body=%q", name, path, status, body)
			}
		}
	}
	if notFound != "404 page not found\n" {
		t.Fatalf("not found body=%q", notFound)
	}
	for _, path := range publicAssets {
		if status, _ := get(path, withFunnel); status != http.StatusOK {
			t.Errorf("%s status=%d", path, status)
		}
	}

	// A link opens there, and every page it links to answers there too.
	opened := browse
	opened.URL = public.URL + "/share/" + secret
	client, home := openShare(t, opened)
	overview := browserGET(t, client, home)
	if overview.status != http.StatusOK || !strings.Contains(overview.body, "API fixture") {
		t.Fatalf("public overview status=%d", overview.status)
	}
	linked := regexp.MustCompile(`(?:href|src|action)="(/[^"]*|\?[^"]*)"`)
	for _, page := range []string{home, home + "/code", home + "/commits"} {
		body := browserGET(t, client, page).body
		for _, match := range linked.FindAllStringSubmatch(body, -1) {
			target := html.UnescapeString(match[1])
			if strings.HasPrefix(target, "?") {
				target = strings.TrimPrefix(page, public.URL) + target
			}
			if answer := browserGET(t, client, public.URL+target); answer.status >= 400 {
				t.Errorf("%s links to %s, which answers %d", page, target, answer.status)
			}
		}
	}

	// A clone link clones with the public address, and a push is refused.
	openedClone := clone
	openedClone.URL = public.URL + "/share/" + cloneSecret
	cloneClient, cloneHome := openShare(t, openedClone)
	if page := browserGET(t, cloneClient, cloneHome); !strings.Contains(page.body, public.URL+"/share/"+clone.ShareLink.ID+".git") || strings.Contains(page.body, private.URL) {
		t.Fatal("the public page does not show the public Git address")
	}
	remote := strings.Replace(public.URL, "://", "://visitor:"+cloneSecret+"@", 1) + "/share/" + clone.ShareLink.ID + ".git"
	directory := filepath.Join(t.TempDir(), "clone")
	apiRunGit(t, "", "clone", "-q", remote, directory)
	apiRunGit(t, directory, "-c", "user.name=Share Test", "-c", "user.email=share-test@example.invalid", "commit", "-q", "--allow-empty", "-m", "refused")
	if output, err := gitCombined(directory, "push", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("a push through the public address was accepted:\n%s", output)
	}

	// Revoking a link ends it on both addresses, for pages and for Git.
	for _, link := range []createdShare{browse, clone} {
		response := adminAPIRequest(t, http.MethodPost, private.URL+"/api/v1/repositories/project/share-links/"+link.ShareLink.ID+"/revoke", nil, "admin-password")
		response.Body.Close()
	}
	for _, target := range []string{home, public.URL + "/share/" + secret, private.URL + "/share/" + secret} {
		if page := browserGET(t, client, target); page.status != http.StatusNotFound {
			t.Errorf("revoked %s status=%d", target, page.status)
		}
	}
	if output, err := gitCombined(directory, "fetch", "origin"); err == nil {
		t.Fatalf("a revoked link fetched through the public address:\n%s", output)
	}
	if strings.Contains(serverLog.String(), secret) || strings.Contains(serverLog.String(), cloneSecret) {
		t.Fatal("the server log holds a share link's secret")
	}
}

// OwnGit's own address keeps refusing requests that come through Tailscale
// Funnel, share links included.
func TestPrivateAddressStillRefusesFunnelForShareLinks(t *testing.T) {
	fixture := newAPIFixture(t, true)
	private := serve(t, fixture.app.Handler())
	created := createShare(t, private.URL, "project", map[string]any{"label": "Recruiter"})
	request, err := http.NewRequest(http.MethodGet, created.URL, nil)
	noErr(t, err)
	request.Header.Set("Tailscale-Funnel-Request", "?1")
	if answer := browserRequest(t, &http.Client{}, request); answer.status != http.StatusForbidden {
		t.Fatalf("a Funnel request on the private address status=%d", answer.status)
	}
}

func basicCredentials(user, password string) string {
	request := &http.Request{Header: http.Header{}}
	request.SetBasicAuth(user, password)
	return strings.TrimPrefix(request.Header.Get("Authorization"), "Basic ")
}

// The Network settings save the public share address with the same checks
// as "owngit network set", say what it opens, and show what the running
// server does with it.
func TestNetworkSettingsSaveThePublicShareAddress(t *testing.T) {
	app := newConfiguredApp(t)
	app.RunningRecordLive = true
	runningRecord(t, app, state.RunningNetwork{
		Listen: DefaultListenAddress, Address: DefaultListenAddress, ListenSource: "default", BaseURLSource: "default",
		PublicShareListen: "127.0.0.1:7655", PublicShareURL: "https://old.example.test", PublicShareError: "address already in use",
	})
	client, base, csrf, body := networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.MsgPublicShareFailed)) || !strings.Contains(body, "address already in use") {
		t.Fatal("the page does not say why the public address could not listen")
	}
	revision := formValue(t, body, "network_revision")
	for name, fields := range map[string]map[string]string{
		"half set":      {"public_share_listen": "127.0.0.1:7655"},
		"own port":      {"public_share_listen": "0.0.0.0:7654", "public_share_url": "https://share.example.test"},
		"URL with path": {"public_share_listen": "127.0.0.1:7655", "public_share_url": "https://share.example.test/x"},
		"bad listen":    {"public_share_listen": "share", "public_share_url": "https://share.example.test"},
	} {
		result := browserForm(t, client, base+"/settings", saveNetworkForm(csrf, revision, "admin-password", fields), base)
		if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, `id="network-public_share_listen-note"`) || !strings.Contains(result.body, enText(webui.MsgPublicShareInvalid)) {
			t.Errorf("%s: status=%d", name, result.status)
		}
	}
	if settings, _, _ := savedNetwork(t, app.Store); settings != (state.NetworkSettings{}) {
		t.Fatalf("a refused save changed the settings: %+v", settings)
	}
	result := browserForm(t, client, base+"/settings", saveNetworkForm(csrf, revision, "admin-password", map[string]string{
		"public_share_listen": "0.0.0.0:7655", "public_share_url": "HTTPS://Share.example.test", "insecure_ack": "1",
	}), base)
	if result.status != http.StatusSeeOther {
		t.Fatalf("save status=%d body=%s", result.status, result.body)
	}
	if settings, _, _ := savedNetwork(t, app.Store); settings.PublicShareListen != "0.0.0.0:7655" || settings.PublicShareURL != "https://Share.example.test" {
		t.Fatalf("saved %+v", settings)
	}
	body, _ = dashboardGET(t, client, base+"/settings/network?notice=network_saved")
	for _, code := range []webui.MessageCode{webui.MsgPublicShareWarnOn, webui.MsgPublicShareWarnDirect, webui.MsgPublicShareWarnProxy, webui.MsgNetRestart} {
		if !strings.Contains(body, enText(code)) {
			t.Errorf("the page does not say %q", enText(code))
		}
	}
}

// A new link names its public address too while the running server has
// one, in the dashboard and in the owner API.
func TestNewShareLinkNamesItsPublicAddress(t *testing.T) {
	fixture := newAPIFixture(t, true)
	fixture.app.PublicShareURL = "https://share.example.test"
	private := serve(t, fixture.app.Handler())
	response := adminAPIRequest(t, http.MethodPost, private.URL+"/api/v1/repositories/project/share-links", map[string]any{"label": "Contractor", "scope": "clone"}, "admin-password")
	defer response.Body.Close()
	var created struct {
		ShareLink struct {
			ID string `json:"id"`
		} `json:"share_link"`
		URL            string `json:"url"`
		PublicURL      string `json:"public_url"`
		PublicCloneURL string `json:"public_clone_url"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&created))
	secret := strings.TrimPrefix(created.URL, private.URL+"/share/")
	if created.PublicURL != "https://share.example.test/share/"+secret || created.PublicCloneURL != "https://share.example.test/share/"+created.ShareLink.ID+".git" {
		t.Fatalf("created=%+v", created)
	}
}
