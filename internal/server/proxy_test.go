package server

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/requestctx"
	"owngit/internal/testfixture"
	"owngit/internal/webui"
)

// proxiedOwnGit is an OwnGit server behind an in-process HTTPS reverse proxy
// that OwnGit trusts. The proxy passes the Host through and appends the
// client address to X-Forwarded-For, like the documented nginx and Caddy
// setups. Like nginx without an X-Forwarded-Host line, it passes a client's
// own X-Forwarded-Host through unchanged.
type proxiedOwnGit struct {
	app          *App
	proxy        *httptest.Server
	gzipRequests atomic.Int64
}

// testClientHeader stands in for the client address the proxy saw, since
// every test client connects from 127.0.0.1. The proxy removes it.
const testClientHeader = "X-Test-Client"

func newProxiedOwnGit(t *testing.T) *proxiedOwnGit {
	t.Helper()
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	noErr(t, err)
	accessHash, err := auth.HashPassword("shared-password")
	noErr(t, err)
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(context.Background(), canonical, "password", accessHash, adminHash, false))
	app.Repositories.SetRoot(canonical)
	return behindProxy(t, app)
}

// behindProxy puts app behind the proxy and trusts it.
func behindProxy(t *testing.T, app *App) *proxiedOwnGit {
	t.Helper()
	app.Requests = requestctx.Resolver{
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		HostAllowed:    app.Hosts.Allows,
	}
	fixture := &proxiedOwnGit{app: app}
	handler := app.Handler()
	backend := serve(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Content-Encoding") == "gzip" {
			fixture.gzipRequests.Add(1)
		}
		handler.ServeHTTP(writer, request)
	}))
	backendURL, err := url.Parse(backend.URL)
	noErr(t, err)
	fixture.proxy = httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(proxied *httputil.ProxyRequest) {
		proxied.SetURL(backendURL)
		proxied.Out.Host = proxied.In.Host
		proxied.SetXForwarded()
		if forwardedHost := proxied.In.Header.Values("X-Forwarded-Host"); len(forwardedHost) > 0 {
			proxied.Out.Header["X-Forwarded-Host"] = forwardedHost
		}
		if client := proxied.In.Header.Get(testClientHeader); client != "" {
			list := append(proxied.In.Header.Values("X-Forwarded-For"), client)
			proxied.Out.Header.Set("X-Forwarded-For", strings.Join(list, ", "))
		}
		proxied.Out.Header.Del(testClientHeader)
	}})
	t.Cleanup(fixture.proxy.Close)
	return fixture
}

// browser returns a client that trusts the proxy's certificate, keeps
// cookies and does not follow redirects.
func (fixture *proxiedOwnGit) browser(t *testing.T) (*http.Client, http.CookieJar) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	noErr(t, err)
	client := fixture.proxy.Client()
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client, jar
}

func (fixture *proxiedOwnGit) send(t *testing.T, client *http.Client, method, path string, values url.Values, origin string, headers ...string) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	request, err := http.NewRequest(method, fixture.proxy.URL+path, body)
	noErr(t, err)
	if values != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Add(headers[index], headers[index+1])
	}
	response, err := client.Do(request)
	noErr(t, err)
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	noErr(t, err)
	return response, string(content)
}

func secureCookie(t *testing.T, response *http.Response, name string) {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			if !cookie.Secure {
				t.Fatalf("cookie %s is not Secure behind an HTTPS proxy", name)
			}
			return
		}
	}
	t.Fatalf("response sets no %s cookie", name)
}

func TestBrowserAndGitThroughATrustedReverseProxy(t *testing.T) {
	fixture := newProxiedOwnGit(t)
	origin := fixture.proxy.URL
	client, jar := fixture.browser(t)

	response, _ := fixture.send(t, client, http.MethodGet, "/login", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login page status=%d", response.StatusCode)
	}
	secureCookie(t, response, preauthCookie)
	csrf := cookieValue(t, jar, origin, preauthCookie)
	response, _ = fixture.send(t, client, http.MethodPost, "/login", url.Values{"csrf": {csrf}, "password": {"shared-password"}, "next": {"/"}}, origin)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status=%d", response.StatusCode)
	}
	secureCookie(t, response, generalCookie)
	session, ok, err := fixture.app.Store.Session(context.Background(), cookieValue(t, jar, origin, generalCookie), "general", fixture.app.now())
	if err != nil || !ok {
		t.Fatalf("login created no session: %v", err)
	}
	// The proxy, not OwnGit, encrypted the browser's connection, and the
	// indicator says so.
	if _, body := fixture.send(t, client, http.MethodGet, "/", nil, ""); !strings.Contains(body, "conn--secure") ||
		!strings.Contains(body, enText(webui.MsgConnProxy)) || strings.Contains(body, enText(webui.MsgConnEncrypted)) {
		t.Fatal("the connection indicator does not say that the proxy in front of OwnGit encrypted the connection")
	}
	if response, body := fixture.send(t, client, http.MethodGet, "/settings", nil, ""); response.StatusCode != http.StatusOK || strings.Contains(body, webui.ActionAcknowledgeInsecure) {
		t.Fatal("Settings asks to acknowledge plain HTTP for an HTTPS request through the proxy")
	}

	// The browser's Origin is the proxy's https origin; a plain-HTTP Origin
	// for the same Host is refused.
	create := url.Values{"csrf": {session.CSRF}, "name": {"project"}}
	if response, _ := fixture.send(t, client, http.MethodPost, "/repositories", create, strings.Replace(origin, "https:", "http:", 1)); response.StatusCode != http.StatusForbidden {
		t.Fatalf("plain-HTTP Origin status=%d, want 403", response.StatusCode)
	}
	if response, _ := fixture.send(t, client, http.MethodPost, "/repositories", create, origin); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("create repository status=%d", response.StatusCode)
	}
	if _, body := fixture.send(t, client, http.MethodGet, "/repositories/project", nil, ""); !strings.Contains(body, origin+"/git/project.git") {
		t.Fatalf("repository page does not show the clone address %s/git/project.git", origin)
	}

	// Git clone and push over the proxy. Cloning 64 branches makes Git gzip
	// its upload-pack request (QA-001).
	git := fixture.gitClient(t)
	remote := origin + "/git/project.git"
	source := filepath.Join(t.TempDir(), "source")
	git(t, "", "init", "-q", "--initial-branch=main", source)
	git(t, source, "commit", "-q", "--allow-empty", "-m", "first")
	for index := 0; index < 63; index++ {
		git(t, source, "branch", fmt.Sprintf("b/%02d", index))
	}
	git(t, source, "push", "-q", remote, "refs/heads/*:refs/heads/*")
	clone := filepath.Join(t.TempDir(), "clone")
	before := fixture.gzipRequests.Load()
	git(t, "", "clone", "-q", remote, clone)
	if fixture.gzipRequests.Load() == before {
		t.Fatal("the clone sent no gzip request body through the proxy")
	}
	if branches := git(t, clone, "branch", "-r"); strings.Count(branches, "origin/b/") != 63 {
		t.Fatalf("clone has %d of 63 branches", strings.Count(branches, "origin/b/"))
	}
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "second")
	git(t, clone, "push", "-q", "origin", "HEAD:refs/heads/main")
	if local, served := git(t, clone, "rev-parse", "HEAD"), git(t, clone, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(served, local) {
		t.Fatalf("pushed %s, server has %q", local, served)
	}
}

// gitClient runs the system Git with no system or user configuration. It
// trusts the proxy's certificate and reads the synthetic shared password from
// an owner-only credential file.
func (fixture *proxiedOwnGit) gitClient(t *testing.T) func(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	home := t.TempDir()
	emptyConfig := filepath.Join(home, "gitconfig")
	noErr(t, os.WriteFile(emptyConfig, nil, 0o600))
	certificate := filepath.Join(home, "proxy.pem")
	noErr(t, os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.proxy.Certificate().Raw}), 0o600))
	credentials := filepath.Join(home, "credentials")
	proxyURL, err := url.Parse(fixture.proxy.URL)
	noErr(t, err)
	proxyURL.User = url.UserPassword("owngit", "shared-password")
	noErr(t, os.WriteFile(credentials, []byte(proxyURL.String()+"\n"), 0o600))
	options := []string{
		"-c", "http.sslCAInfo=" + certificate, "-c", "http.schannelUseSSLCAInfo=true", "-c", "http.schannelCheckRevoke=false",
		"-c", "credential.helper=", "-c", "credential.helper=store --file=" + filepath.ToSlash(credentials),
	}
	environment := testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+emptyConfig,
		"HOME="+home, "GIT_AUTHOR_NAME=Proxy Test", "GIT_AUTHOR_EMAIL=proxy@example.invalid",
		"GIT_COMMITTER_NAME=Proxy Test", "GIT_COMMITTER_EMAIL=proxy@example.invalid"))
	return func(t *testing.T, directory string, arguments ...string) string {
		t.Helper()
		command := exec.Command("git", append(append([]string{}, options...), arguments...)...)
		command.Dir = directory
		command.Env = environment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
}

// Clients behind one trusted proxy lock out independently, and a client
// cannot move its failures elsewhere by sending its own X-Forwarded-For.
func TestClientsBehindATrustedProxyLockOutSeparately(t *testing.T) {
	fixture := newProxiedOwnGit(t)
	client, _ := fixture.browser(t)
	discovery := "/git/project.git/info/refs?service=git-upload-pack"
	if _, err := fixture.app.Repositories.Create(context.Background(), "project", ""); err != nil {
		t.Fatal(err)
	}
	gitStatus := func(password, clientAddress string, extra ...string) int {
		request, err := http.NewRequest(http.MethodGet, fixture.proxy.URL+discovery, nil)
		noErr(t, err)
		request.SetBasicAuth("owngit", password)
		request.Header.Set(testClientHeader, clientAddress)
		for index := 0; index+1 < len(extra); index += 2 {
			request.Header.Add(extra[index], extra[index+1])
		}
		response, err := client.Do(request)
		noErr(t, err)
		response.Body.Close()
		return response.StatusCode
	}
	for attempt := 1; attempt <= 4; attempt++ {
		if status := gitStatus("wrong-password", "198.51.100.1"); status != http.StatusUnauthorized {
			t.Fatalf("wrong password %d status=%d", attempt, status)
		}
		// This client claims a new address each time; the proxy appends the
		// one it saw, and only that one counts.
		spoofed := fmt.Sprintf("203.0.113.%d", attempt)
		if status := gitStatus("wrong-password", "198.51.100.3", "X-Forwarded-For", spoofed); status != http.StatusUnauthorized {
			t.Fatalf("spoofing client attempt %d status=%d", attempt, status)
		}
	}
	for _, locked := range []string{"198.51.100.1", "198.51.100.3"} {
		if status := gitStatus("shared-password", locked); status == http.StatusOK {
			t.Fatalf("client %s was not locked out", locked)
		}
		if err := fixture.app.Auth.VerifyCredential(context.Background(), "general", "shared-password", locked); !errors.Is(err, auth.ErrRateLimited) {
			t.Fatalf("client %s: %v, want the rate limit", locked, err)
		}
	}
	if status := gitStatus("shared-password", "198.51.100.2"); status != http.StatusOK {
		t.Fatalf("another client behind the same proxy status=%d, want 200", status)
	}
	for _, address := range []string{"127.0.0.1", "203.0.113.1", "203.0.113.4"} {
		if err := fixture.app.Auth.VerifyCredential(context.Background(), "general", "shared-password", address); err != nil {
			t.Fatalf("address %s was charged: %v", address, err)
		}
	}
}

// Setup through a trusted HTTPS proxy needs no plain-HTTP acknowledgement,
// and its cookies are Secure.
func TestSetupThroughATrustedHTTPSProxy(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	fixture := behindProxy(t, app)
	origin := fixture.proxy.URL
	client, jar := fixture.browser(t)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	fixture.send(t, client, http.MethodGet, "/setup", nil, "")
	response, _ := fixture.send(t, client, http.MethodPost, "/setup/redeem", url.Values{
		"csrf": {cookieValue(t, jar, origin, preauthCookie)}, "token": {"synthetic-owner-token"},
	}, origin)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeem status=%d", response.StatusCode)
	}
	secureCookie(t, response, setupCookie)
	if _, page := fixture.send(t, client, http.MethodGet, "/setup", nil, ""); strings.Contains(page, `name="insecure_ack"`) {
		t.Fatal("the setup form asks to acknowledge plain HTTP for an HTTPS request through the proxy")
	}
	session, ok, err := store.Session(context.Background(), cookieValue(t, jar, origin, setupCookie), "setup", time.Now())
	if err != nil || !ok {
		t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	response, _ = fixture.send(t, client, http.MethodPost, "/setup", url.Values{
		"csrf": {session.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"}, "admin_password": {"admin-password-one"},
	}, origin)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup without the plain-HTTP acknowledgement status=%d", response.StatusCode)
	}
	settings, err := store.Settings(context.Background())
	if err != nil || !settings.Initialized || settings.InsecureHTTPAccepted {
		t.Fatalf("settings=%+v err=%v, want setup complete without a plain-HTTP acknowledgement", settings, err)
	}
}

// hostStatus sends a GET through the proxy with the given raw Host and
// headers and returns the status and body.
func (fixture *proxiedOwnGit) hostStatus(t *testing.T, path, host string, headers ...string) (int, string) {
	t.Helper()
	client, _ := fixture.browser(t)
	request, err := http.NewRequest(http.MethodGet, fixture.proxy.URL+path, nil)
	noErr(t, err)
	request.Host = host
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Add(headers[index], headers[index+1])
	}
	response, err := client.Do(request)
	noErr(t, err)
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	noErr(t, err)
	return response.StatusCode, string(content)
}

// A trusted proxy that passes a client's X-Forwarded-Host through must not
// let that header widen the Host check, which is the DNS rebinding defense.
// The forwarded value is used only when the raw Host also passes.
func TestForwardedHostNeverWidensTheHostCheck(t *testing.T) {
	app := newProxiedOwnGit(t).app
	app.Hosts = NewHostPolicy("git.example.internal")
	fixture := behindProxy(t, app)
	if _, err := fixture.app.Repositories.Create(context.Background(), "project", ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/login", "/setup", "/api/v1/repositories", "/git/project.git/info/refs?service=git-upload-pack"} {
		for _, forwarded := range []string{"localhost", "127.0.0.1", "git.example.internal"} {
			if status, _ := fixture.hostStatus(t, path, "rebind.attacker.example", "X-Forwarded-Host", forwarded); status != http.StatusMisdirectedRequest {
				t.Errorf("GET %s with Host rebind.attacker.example and X-Forwarded-Host %s: status=%d, want 421", path, forwarded, status)
			}
		}
	}
	// Between two accepted Hosts the forwarded one may be used.
	if status, _ := fixture.hostStatus(t, "/login", "localhost", "X-Forwarded-Host", "git.example.internal"); status != http.StatusOK {
		t.Fatalf("accepted Host with an accepted forwarded Host: status=%d", status)
	}
}

// Before setup, an unknown raw Host with a forwarded accepted Host is still
// an unknown Host: it gets only the setup-link redemption page, as without
// the header, and nothing else.
func TestForwardedHostCannotOpenSetupForAnUnknownHost(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	app.Version = "9.9.9-test"
	fixture := behindProxy(t, app)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	for _, path := range []string{"/", "/login", "/setup/approval", "/api/v1/repositories"} {
		if status, _ := fixture.hostStatus(t, path, "evil.example", "X-Forwarded-Host", "localhost"); status != http.StatusMisdirectedRequest {
			t.Errorf("GET %s from an unknown Host with X-Forwarded-Host localhost: status=%d, want 421", path, status)
		}
	}
	status, page := fixture.hostStatus(t, "/setup", "evil.example", "X-Forwarded-Host", "localhost")
	if status != http.StatusOK || !strings.Contains(page, `action="/setup/redeem"`) {
		t.Fatalf("GET /setup from an unknown Host: status=%d, want the redemption page", status)
	}
	for _, secret := range []string{"git version test", "9.9.9-test", repositoryRoot, "OwnGit-Repositories", `name="storage_path"`} {
		if strings.Contains(page, secret) {
			t.Fatalf("the redemption page for an unknown Host reveals %q", secret)
		}
	}
}
