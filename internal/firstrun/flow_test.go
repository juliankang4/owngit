package firstrun

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// syncBuffer is terminal output shared between the flow and the test.
type syncBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

type harness struct {
	t       *testing.T
	app     *server.App
	store   *state.Store
	out     *syncBuffer
	input   chan []byte
	flow    *flow
	started int
	base    string
}

func newApp(t *testing.T) (*server.App, *state.Store, string) {
	t.Helper()
	base := t.TempDir()
	store, err := state.Open(context.Background(), filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(base, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks()}
	gitHandler, err := githttp.New(runner, manager, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := webui.New()
	if err != nil {
		t.Fatal(err)
	}
	app := &server.App{
		Store: store, Auth: &auth.Manager{Store: store, SessionLife: time.Hour, AdminSessionLife: time.Minute},
		Repositories: manager, GitHTTP: gitHandler, Renderer: renderer, Hosts: server.NewHostPolicy(),
		SuggestedRepositoryRoot: filepath.Join(base, "suggested"), GitVersion: "git version test", HTTPBackendFound: true,
		Approvals: server.NewSetupApprovals(),
	}
	t.Cleanup(app.StopBackground)
	return app, store, base
}

// newHarness prepares a flow over a real server App, as serve builds it,
// with synthetic Tailscale values and color off.
func newHarness(t *testing.T, listen string, tailscale Tailscale) *harness {
	t.Helper()
	app, store, base := newApp(t)
	h := &harness{t: t, app: app, store: store, out: &syncBuffer{}, input: make(chan []byte, 64), base: base}
	app.OnSetupComplete = func() { h.started++ }
	con := &console{ctx: context.Background(), input: h.input, finished: app.Approvals.Done()}
	s := &screen{out: h.out, columns: func() (int, bool) { return 80, true }}
	con.echo = s.write
	found := make(chan Tailscale, 1)
	found <- tailscale
	h.flow = &flow{
		ctx: context.Background(), lang: webui.LangEN, screen: s, console: con, app: app,
		origin: "http://127.0.0.1:7654", listen: listen, suggested: app.SuggestedRepositoryRoot, tailscale: found,
		stateDir: "/tmp/owngit state",
	}
	h.flow.network, h.flow.otherDevices = networkReach(listen, h.flow.origin)
	return h
}

func (h *harness) send(answers ...string) {
	for _, answer := range answers {
		h.input <- []byte(answer)
	}
}

// run answers every question from the script; after the script the input
// ends as if the terminal closed.
func (h *harness) run(answers ...string) error {
	h.send(answers...)
	close(h.input)
	return h.flow.run()
}

func (h *harness) waitFor(text string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(h.out.String(), text) {
		if time.Now().After(deadline) {
			h.t.Fatalf("output never contained %q:\n%s", text, h.out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForCount waits until the output contains text at least count times.
func (h *harness) waitForCount(text string, count int) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(h.out.String(), text) < count {
		if time.Now().After(deadline) {
			h.t.Fatalf("output never contained %q %d times:\n%s", text, count, h.out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) settings() state.Settings {
	settings, err := h.store.Settings(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return settings
}

func TestTerminalSetupUsesTheServerRules(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{State: TailscaleMissing})
	folder := filepath.Join(h.base, "repositories")
	err := h.run("1\r", "1\r",
		"relative\r", folder+"\r",
		"2\r", "short\r", "correct horse\r", "correct horsE\r", "correct horse\r", "correct horse\r",
		"correct horse\r", "battery staple 9\r", "battery staple 9\r",
		"\r", "1\r")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, h.out)
	}
	out := h.out.String()
	for _, want := range []string{
		"[x] Enter a full path, for example /Users/you/git.", "[ok] OwnGit can use this folder.",
		"[x] Use at least 8 characters.", "[x] The two entries are different. Type the password again.",
		"[ok] Shared password entered.", "[x] Use a different password from the shared access password.",
		"[ok] Administrator password entered.", "[i] Tailscale was not found on this computer.",
		"Connection               this computer only", "+- [ok] Setup complete ", "Dashboard   http://127.0.0.1:7654/",
		"+- Repository folder ", " 1/5 -+", " 5/5 -+",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	for _, secret := range []string{"short", "correct horse", "correct horsE", "battery staple"} {
		if strings.Contains(out, secret) {
			t.Errorf("a password was shown: %q", secret)
		}
	}
	settings := h.settings()
	canonical, _ := filepath.EvalSymlinks(folder)
	if !settings.Initialized || settings.AccessMode != "password" || settings.RepositoryRoot != canonical || settings.InsecureHTTPAccepted {
		t.Fatalf("settings=%+v", settings)
	}
	for kind, password := range map[string]string{"access": "correct horse", "admin": "battery staple 9"} {
		hash, _ := h.store.PasswordHash(context.Background(), kind)
		if !auth.CheckPassword(hash, password) {
			t.Fatalf("%s password was not saved", kind)
		}
	}
	if h.started != 1 {
		t.Fatalf("setup completion ran %d times", h.started)
	}
}

func TestNetworkListenAsksForThePlainHTTPAcknowledgement(t *testing.T) {
	h := newHarness(t, "0.0.0.0:7654", Tailscale{State: TailscaleRunning, IPv4: "100.64.0.7", Name: "my-mac.tail0000.ts.net"})
	err := h.run("2\r", "1\r", "\r", "1\r", "admin-password-1\r", "admin-password-1\r", "\r",
		"n\r", "maybe\r", "y\r", "1\r")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, h.out)
	}
	out := h.out.String()
	for _, want := range []string{
		"+- [!] 암호화되지 않는 연결 ", "5/6", "[x] OwnGit이 이 연결을 암호화하지 않는다는 점을 확인해 주세요.", "--listen", "127.0.0.1:7654",
		"[x] y 또는 n을 입력하세요.", "일반 HTTP, 암호화 안 됨", "[ok] 이 컴퓨터에서 Tailscale이 실행 중입니다.",
		"\nowngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654 --state-dir '/tmp/owngit state'\n",
		"백그라운드 서비스의 옵션",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	settings := h.settings()
	if !settings.Initialized || settings.AccessMode != "open" || !settings.InsecureHTTPAccepted {
		t.Fatalf("settings=%+v", settings)
	}
}

// A listen address from the saved network settings applies at every start,
// so the way back to this computer only is a saved loopback address, not a
// one-run --listen option.
func TestSavedNetworkListenSuggestsSavingALocalAddress(t *testing.T) {
	for _, test := range []struct {
		lang, first string
		want        []string
	}{
		{"en", "1\r", []string{"[x] Confirm that you understand OwnGit is not encrypting this connection.", "saved network settings"}},
		{"ko", "2\r", []string{"[x] OwnGit이 이 연결을 암호화하지 않는다는 점을 확인해 주세요.", "저장된 네트워크 설정"}},
	} {
		h := newHarness(t, "0.0.0.0:7700", Tailscale{})
		h.flow.listenSaved = true
		err := h.run(test.first, "1\r", "\r", "1\r", "admin-password-1\r", "admin-password-1\r", "\r", "n\r", "y\r", "1\r")
		if err != nil {
			t.Fatalf("%s run: %v\n%s", test.lang, err, h.out)
		}
		out := h.out.String()
		for _, want := range append(test.want, "\nowngit network set --listen 127.0.0.1:7700 --base-url '' --state-dir '/tmp/owngit state'\n") {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q", test.lang, want)
			}
		}
		if strings.Contains(out, "--listen 127.0.0.1:7700.") || strings.Contains(out, "--listen 127.0.0.1:7700 옵션") {
			t.Errorf("%s: a saved address was answered with a one-run --listen option:\n%s", test.lang, out)
		}
		if settings := h.settings(); !settings.Initialized || !settings.InsecureHTTPAccepted {
			t.Fatalf("%s settings=%+v", test.lang, settings)
		}
	}
}

func TestLanguageCanBeSwitchedWithL(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{})
	h.run("1\r", "l\r", "ㅣ\r")
	out := h.out.String()
	if !strings.Contains(out, "[L] English") || strings.Count(out, "+- Set up OwnGit ") != 2 || !strings.Contains(out, "+- OwnGit 설치 ") {
		t.Fatalf("language switch output:\n%s", out)
	}
}

func TestCtrlCStopsWithoutSavingAnything(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{})
	folder := filepath.Join(h.base, "repositories")
	err := h.run("1\r", "1\r", folder+"\r", "2\r", "correct horse\r", "\x03")
	if err != errStopped {
		t.Fatalf("err=%v", err)
	}
	out := h.out.String()
	if !strings.Contains(out, "+- [!] Setup stopped ") || !strings.Contains(out, "Nothing was saved. OwnGit is still not set up.") ||
		!strings.Contains(out, "To continue, run owngit serve again.") {
		t.Fatalf("stop card missing:\n%s", out)
	}
	if h.settings().Initialized || h.started != 0 {
		t.Fatal("setup was saved")
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatal("the repository folder was created before setup finished")
	}
}

func TestClosedTerminalStopsSetup(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{})
	if err := h.run("1\r"); err != errStopped {
		t.Fatalf("err=%v", err)
	}
}

// A browser (here the setup file flow) finishes setup while the terminal is
// asking; the terminal reports what was saved and ends.
func TestSetupFinishedElsewhereEndsTheTerminalFlow(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{})
	h.send("1\r", "1\r")
	result := make(chan error, 1)
	go func() { result <- h.flow.run() }()
	h.waitFor("  Folder")
	root := filepath.Join(h.base, "elsewhere")
	if notices, err := h.app.CompleteSetup(context.Background(), server.SetupAnswers{StoragePath: root, AccessMode: "open", AdminPassword: "admin-password-1", InsecureAccepted: true}, true); len(notices) != 0 || err != nil {
		t.Fatalf("notices=%v err=%v", notices, err)
	}
	if err := <-result; err != nil {
		t.Fatalf("err=%v", err)
	}
	out := h.out.String()
	if !strings.Contains(out, "Answers received from the browser:") || !strings.Contains(out, "[ok] Access                   Anyone on this network") ||
		!strings.Contains(out, "+- [ok] Setup complete ") {
		t.Fatalf("output:\n%s", out)
	}
}

type browser struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, client: &http.Client{Jar: jar}}
}

func (b *browser) do(method, path string, values url.Values) string {
	b.t.Helper()
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	request, _ := http.NewRequest(method, b.base+path, body)
	if values != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", b.base)
	}
	response, err := b.client.Do(request)
	if err != nil {
		b.t.Fatal(err)
	}
	defer response.Body.Close()
	content, _ := io.ReadAll(response.Body)
	return string(content)
}

var (
	csrfField  = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	codeOnPage = regexp.MustCompile(`class="approvalcode"[^>]*>([^<]+)<`)
)

func field(t *testing.T, pattern *regexp.Regexp, page string) string {
	t.Helper()
	match := pattern.FindStringSubmatch(page)
	if match == nil {
		t.Fatalf("page lacks %s:\n%s", pattern, page)
	}
	return match[1]
}

func TestWebPathApprovesOneBrowserInTheTerminal(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{})
	web := httptest.NewServer(h.app.Handler())
	defer web.Close()
	opened := ""
	h.flow.openBrowser = func(address string) error { opened = address; return nil }
	h.send("1\r", "2\r")
	result := make(chan error, 1)
	go func() { result <- h.flow.run() }()
	h.waitFor("Press T to set up here instead.")
	if opened != "http://127.0.0.1:7654/setup" {
		t.Fatalf("opened %q", opened)
	}

	// A browser whose code the owner does not recognize is rejected.
	stranger := newBrowser(t, web.URL)
	page := stranger.do(http.MethodGet, "/setup", nil)
	stranger.do(http.MethodPost, "/setup/approval", url.Values{"csrf": {field(t, csrfField, page)}})
	h.waitFor("+- A browser wants to set up OwnGit ")
	h.waitFor("Approve this browser? [y/N]: ")
	h.send("n\r")
	h.waitFor("[i] Rejected. That browser cannot continue setup.")

	owner := newBrowser(t, web.URL)
	page = owner.do(http.MethodGet, "/setup", nil)
	page = owner.do(http.MethodPost, "/setup/approval", url.Values{"csrf": {field(t, csrfField, page)}})
	code := field(t, codeOnPage, page)
	spaced := strings.Join(strings.Split(code, ""), " ")
	h.waitFor(spaced)
	h.send("y\r")
	h.waitFor("[ok] Approved. Continue in the browser.")
	page = owner.do(http.MethodGet, "/setup", nil)
	if !strings.Contains(page, `name="storage_path"`) {
		t.Fatalf("the approved browser does not see the setup form:\n%s", page)
	}
	root := filepath.Join(h.base, "web-repositories")
	owner.do(http.MethodPost, "/setup", url.Values{
		"csrf": {field(t, csrfField, page)}, "storage_path": {root}, "access_mode": {"open"},
		"admin_password": {"admin-password-1"}, "insecure_ack": {"1"},
	})
	if err := <-result; err != nil {
		t.Fatalf("err=%v", err)
	}
	h.waitFor("+- [ok] Setup complete ")
	if !h.settings().Initialized || h.started != 1 {
		t.Fatal("web setup did not complete once")
	}
	if strings.Contains(stranger.do(http.MethodGet, "/setup", nil), `name="storage_path"`) {
		t.Fatal("the rejected browser reached the setup form")
	}
}

func TestSwitchingToTheTerminalEndsTheBrowserPath(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{})
	web := httptest.NewServer(h.app.Handler())
	defer web.Close()
	h.send("1\r", "2\r")
	result := make(chan error, 1)
	go func() { result <- h.flow.run() }()
	h.waitFor("Press T to set up here instead.")
	owner := newBrowser(t, web.URL)
	page := owner.do(http.MethodGet, "/setup", nil)
	owner.do(http.MethodPost, "/setup/approval", url.Values{"csrf": {field(t, csrfField, page)}})
	h.waitFor("Approve this browser? [y/N]: ")
	h.send("y\r")
	h.waitFor("Waiting for the browser to finish setup.")
	if page := owner.do(http.MethodGet, "/setup", nil); !strings.Contains(page, `name="storage_path"`) {
		t.Fatal("no setup form after approval")
	}
	h.send("ㅅ")
	h.waitFor("  Folder")
	if page := owner.do(http.MethodGet, "/setup", nil); strings.Contains(page, `name="storage_path"`) {
		t.Fatal("the browser kept its setup session after switching to the terminal")
	}
	h.send("\x03")
	if err := <-result; err != errStopped {
		t.Fatalf("err=%v", err)
	}
}

// After one browser was approved, the terminal still shows a new request:
// the owner may have moved to another browser. Approving it ends the first
// browser's setup session.
func TestANewRequestIsShownAfterAnEarlierApproval(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{})
	web := httptest.NewServer(h.app.Handler())
	defer web.Close()
	h.send("1\r", "2\r")
	result := make(chan error, 1)
	go func() { result <- h.flow.run() }()
	h.waitFor("Press T to set up here instead.")
	approve := func(b *browser) {
		t.Helper()
		page := b.do(http.MethodGet, "/setup", nil)
		page = b.do(http.MethodPost, "/setup/approval", url.Values{"csrf": {field(t, csrfField, page)}})
		h.waitFor(strings.Join(strings.Split(field(t, codeOnPage, page), ""), " "))
		approved := strings.Count(h.out.String(), "[ok] Approved. Continue in the browser.")
		h.send("y\r")
		// The browser may ask before the terminal has recorded the answer.
		h.waitForCount("[ok] Approved. Continue in the browser.", approved+1)
		if page := b.do(http.MethodGet, "/setup", nil); !strings.Contains(page, `name="storage_path"`) {
			t.Fatalf("no setup form after approval:\n%s", page)
		}
	}
	first, second := newBrowser(t, web.URL), newBrowser(t, web.URL)
	approve(first)
	h.waitFor("Waiting for the browser to finish setup.")
	replaces := "Approving ends the setup already open in the browser you approved before."
	if strings.Contains(h.out.String(), replaces) {
		t.Fatal("the first request carried the warning")
	}
	approve(second)
	if !strings.Contains(h.out.String(), replaces) {
		t.Fatalf("the second request lacks the warning:\n%s", h.out.String())
	}
	if strings.Count(h.out.String(), "[ok] Approved. Continue in the browser.") != 2 {
		t.Fatalf("output:\n%s", h.out.String())
	}
	if page := first.do(http.MethodGet, "/setup", nil); strings.Contains(page, `name="storage_path"`) {
		t.Fatal("the first browser kept its setup session")
	}
	h.send("\x03")
	if err := <-result; err != errStopped {
		t.Fatalf("err=%v", err)
	}
}

func TestNetworkReach(t *testing.T) {
	for _, c := range []struct {
		listen, origin string
		network        bool
		other          string
	}{
		{"127.0.0.1:7654", "http://127.0.0.1:7654", false, ""},
		{"[::1]:7654", "http://[::1]:7654", false, ""},
		{"0.0.0.0:7654", "http://127.0.0.1:7654", true, ""},
		{"192.0.2.5:7654", "http://192.0.2.5:7654", true, ""},
		{"192.0.2.5:7654", "http://127.0.0.1:7654", true, "http://192.0.2.5:7654/"},
		{"192.0.2.5:7654", "http://gitbox.internal:7654", true, ""},
	} {
		network, other := networkReach(c.listen, c.origin)
		if network != c.network || other != c.other {
			t.Errorf("%s %s: %v %q", c.listen, c.origin, network, other)
		}
	}
}
