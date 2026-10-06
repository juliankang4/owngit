package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/webui"
)

// httpsCookiePrefix is the prefix a cookie name carries over HTTPS. A browser
// accepts such a name only from a secure origin, with Path=/ and no Domain
// (RFC 6265bis, section 4.1.3), which is what keeps the HTTPS session and
// form token cookies away from a plain address. Tests spell it out because it
// is the wire contract of cookieNameForScheme.
const httpsCookiePrefix = "__Host-"

// A browser that signed in at this server's HTTPS address can also sign in at
// the same host over plain HTTP. Each address has its own cookie: the HTTPS
// session and form token cookies carry the httpsCookiePrefix and Secure, and
// the plain sign-in gets the plain names without Secure, so a browser stores
// them although a cookie of the same purpose exists for the other address.
// With one name for both, a plain HTTP response cannot replace the secure
// cookie (RFC 6265bis, section 5.4: a cookie received over an insecure
// connection is refused when the store holds a secure cookie of the same
// name), so the sign-in looks successful and the next plain page asks for the
// password again. Signing in at the plain address must also leave the HTTPS
// session alone.
func TestSignInWorksOverHTTPSAndPlainHTTPAtTheSameHost(t *testing.T) {
	fixture := newAPIFixture(t, true)
	secure := httptest.NewTLSServer(fixture.app.Handler())
	t.Cleanup(secure.Close)
	plain := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)
	client.Transport = secure.Client().Transport

	// The HTTPS sign-in page and the sign-in answer set the HTTPS names.
	preauth := setCookie(t, browserGET(t, client, secure.URL+"/login"), httpsCookiePrefix+preauthCookie)
	if !preauth.Secure {
		t.Fatalf("the HTTPS form token cookie %s is not Secure", preauth.Name)
	}
	setCookie(t, postsPassword(t, client, secure.URL, preauth.Value), httpsCookiePrefix+generalCookie)

	// The same browser signs in at the same host over plain HTTP, under the
	// plain names.
	plainPreauth := setCookie(t, browserGET(t, client, plain.URL+"/login"), preauthCookie)
	if plainPreauth.Secure {
		t.Fatalf("the plain HTTP form token cookie %s is Secure", plainPreauth.Name)
	}
	plainSession := setCookie(t, postsPassword(t, client, plain.URL, plainPreauth.Value), generalCookie)
	if plainSession.Secure {
		t.Fatalf("the plain HTTP session cookie %s is Secure", plainSession.Name)
	}

	// Both addresses stay signed in.
	if page := browserGET(t, client, plain.URL+"/repositories/project"); page.status != http.StatusOK {
		t.Fatalf("plain HTTP page after signing in status=%d", page.status)
	}
	if page := browserGET(t, client, secure.URL+"/repositories/project"); page.status != http.StatusOK {
		t.Fatalf("HTTPS page after the plain sign-in status=%d", page.status)
	}
}

// earlierCookieNames are the names OwnGit 1.1.4 and earlier used at both
// address schemes, and upgradeMarkerName is the marker this version sets once
// it has removed them. Tests spell both out because they are the wire
// contract of forgetLegacyCookies.
var earlierCookieNames = []string{"owngit_general", "owngit_admin", "owngit_setup", "owngit_preauth", "owngit_setup_request"}

const upgradeMarkerName = "__Host-owngit_cookie_names"

// browserJar is the cookie store of this test's browser. Every cookie here
// has one host and Path=/, so a map is enough, and it applies the one rule
// the upgrade test is about: a cookie that arrives over plain HTTP is refused
// while the store holds a secure cookie of the same name, host and path (RFC
// 6265bis, section 5.4). Go's cookie jar keeps whichever of the two arrived
// last, so it cannot show the failure this rule causes.
type browserJar struct {
	held map[string]*http.Cookie
}

func newBrowserJar() *browserJar {
	return &browserJar{held: make(map[string]*http.Cookie)}
}

func (jar *browserJar) SetCookies(address *url.URL, cookies []*http.Cookie) {
	now := time.Now()
	for _, cookie := range cookies {
		held, known := jar.held[cookie.Name]
		switch {
		case cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && !cookie.Expires.After(now)):
			// This answer expires the cookie.
			delete(jar.held, cookie.Name)
		case !cookie.Secure && address.Scheme != "https" && known && held.Secure:
			// The browser keeps the secure cookie and drops this one.
		default:
			jar.held[cookie.Name] = cookie
		}
	}
}

func (jar *browserJar) Cookies(address *url.URL) []*http.Cookie {
	cookies := make([]*http.Cookie, 0, len(jar.held))
	for _, cookie := range jar.held {
		if cookie.Secure && address.Scheme != "https" {
			continue
		}
		cookies = append(cookies, cookie)
	}
	return cookies
}

// A browser that signed in at the HTTPS address of OwnGit 1.1.4 or earlier
// holds secure cookies under the names of that version. This version reads
// other names there, but the earlier cookies stay, and while they do the
// browser refuses the plain cookie of the same name that the plain address
// needs (RFC 6265bis, section 5.4): the plain sign-in looks successful and
// the next plain page asks for the password again. So the first secure answer
// of this version expires the earlier names once and sets a marker. The plain
// address works from then on, and the marker keeps a later secure answer from
// deleting the plain session.
func TestUpgradeFromTheEarlierCookieNamesAllowsThePlainAddress(t *testing.T) {
	fixture := newAPIFixture(t, true)
	secure := httptest.NewTLSServer(fixture.app.Handler())
	t.Cleanup(secure.Close)
	plain := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)
	client.Transport = secure.Client().Transport
	browser := newBrowserJar()
	client.Jar = browser
	// What the earlier version left at the secure address: the session
	// cookies name live sessions of the server, so the cleanup has to end
	// them, not only expire the cookies.
	ctx := context.Background()
	earlierSessions := map[string]string{}
	for _, name := range []string{generalCookie, adminCookie} {
		token := "earlier-" + name
		kind := strings.TrimPrefix(name, "owngit_")
		noErr(t, fixture.store.CreateSession(ctx, token, kind, "earlier-csrf", 1, fixture.app.now().Add(time.Hour)))
		earlierSessions[name] = token
		browser.held[name] = &http.Cookie{Name: name, Value: token, Path: "/", Secure: true,
			Expires: time.Now().Add(12 * time.Hour)}
	}
	for _, name := range []string{setupCookie, preauthCookie, approvalCookie} {
		browser.held[name] = &http.Cookie{Name: name, Value: "earlier-" + name, Path: "/", Secure: true,
			Expires: time.Now().Add(12 * time.Hour)}
	}
	// A token cookie the browser also holds at the plain address, which the
	// cleanup forgets with the earlier names.
	browser.held["owngit_general"].Value = earlierSessions[generalCookie]

	// Until the secure address is opened, the browser holds the plain
	// cookies back, so a plain sign-in cannot even finish: the form token of
	// the sign-in page is refused too, and the form is refused as a forgery.
	plainPreauth := setCookie(t, browserGET(t, client, plain.URL+"/login"), preauthCookie)
	answer := browserForm(t, client, plain.URL+"/login", url.Values{"csrf": {plainPreauth.Value}, "password": {"shared-password"}, "next": {"/"}}, plain.URL)
	if answer.status != http.StatusForbidden {
		t.Fatalf("a plain sign-in while the secure cookies of the earlier version are held status=%d, want %d", answer.status, http.StatusForbidden)
	}
	if page := browserGET(t, client, plain.URL+"/repositories/project"); page.status == http.StatusOK {
		t.Fatal("a browser that holds the secure cookie of the earlier version is signed in at the plain address, so this test no longer shows the failure the upgrade repairs")
	}

	// The first secure answer expires every earlier name and marks the
	// browser as upgraded.
	page := browserGET(t, client, secure.URL+"/login")
	for _, name := range earlierCookieNames {
		cookie := setCookie(t, page, name)
		if cookie.Value != "" || cookie.Expires.After(time.Now()) {
			t.Fatalf("the secure answer does not expire %s: %+v", name, cookie)
		}
	}
	if marker := setCookie(t, page, upgradeMarkerName); !marker.Secure {
		t.Fatalf("the marker cookie %s is not Secure", marker.Name)
	}
	for _, name := range earlierCookieNames {
		if held := browser.held[name]; held != nil {
			t.Fatalf("the browser still holds %s=%q", name, held.Value)
		}
	}
	for name, token := range earlierSessions {
		kind := strings.TrimPrefix(name, "owngit_")
		if _, ok, err := fixture.store.Session(ctx, token, kind, fixture.app.now()); err != nil || ok {
			t.Fatalf("the cleanup left the %s session %s: ok=%v err=%v", kind, token, ok, err)
		}
	}

	// The plain address signs in now, and its page opens. A later secure
	// visit leaves that session alone.
	plainPreauth = setCookie(t, browserGET(t, client, plain.URL+"/login"), preauthCookie)
	setCookie(t, postsPassword(t, client, plain.URL, plainPreauth.Value), generalCookie)
	if page := browserGET(t, client, plain.URL+"/repositories/project"); page.status != http.StatusOK {
		t.Fatalf("plain HTTP page after the upgrade signed in status=%d", page.status)
	}
	browserGET(t, client, secure.URL+"/login")
	if held := browser.held[generalCookie]; held == nil || held.Secure {
		t.Fatalf("the later secure visit left the plain session cookie as %+v", held)
	}
	if page := browserGET(t, client, plain.URL+"/repositories/project"); page.status != http.StatusOK {
		t.Fatalf("plain HTTP page after a later secure visit status=%d", page.status)
	}
}

// The earlier name a plain sign-in of this version writes is the same string,
// so the first secure answer of a browser also ends and forgets the session
// that sign-in created at the plain address. The browser forgets a session
// only after the server ended it.
func TestTheCookieCleanupEndsTheSessionItForgets(t *testing.T) {
	fixture := newAPIFixture(t, true)
	secure := httptest.NewTLSServer(fixture.app.Handler())
	t.Cleanup(secure.Close)
	plain := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)
	client.Transport = secure.Client().Transport
	ctx := context.Background()

	// A sign-in at the plain address, before this browser opened the secure
	// address.
	preauth := setCookie(t, browserGET(t, client, plain.URL+"/login"), preauthCookie)
	token := setCookie(t, postsPassword(t, client, plain.URL, preauth.Value), generalCookie).Value
	if _, ok, err := fixture.store.Session(ctx, token, "general", fixture.app.now()); err != nil || !ok {
		t.Fatalf("the plain sign-in left no general session: ok=%v err=%v", ok, err)
	}

	// The first secure answer runs the one-time cleanup: it ends the session
	// the name carries before the browser forgets the name.
	page := browserGET(t, client, secure.URL+"/login")
	if _, ok, err := fixture.store.Session(ctx, token, "general", fixture.app.now()); err != nil || ok {
		t.Fatalf("the cleanup left the session it forgot: ok=%v err=%v", ok, err)
	}
	expiry := setCookie(t, page, generalCookie)
	if expiry.Value != "" || expiry.Expires.After(time.Now()) {
		t.Fatalf("the cleanup does not expire %s: %+v", generalCookie, expiry)
	}
}

// Signing out at the secure address ends this browser's sessions of both
// address schemes: the request carries the cookie of each, so the plain
// address is signed out too, and its session row is gone. Signing out at a
// plain address cannot end the session of the secure address, whose cookie
// never reached it: that session stays, and the sign-in page says so.
func TestSignOutEndsTheSignInsItCanReach(t *testing.T) {
	fixture := newAPIFixture(t, true)
	// The secure address is the base URL, so a plain sign-out knows that a
	// sign-in it cannot end may exist.
	fixture.app.Network = NewLiveNetwork(LiveNetworkConfig{BaseURL: "https://git.example.internal", Hosts: fixture.app.Hosts})
	secure := httptest.NewTLSServer(fixture.app.Handler())
	t.Cleanup(secure.Close)
	plain := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)
	client.Transport = secure.Client().Transport
	ctx := context.Background()

	csrfOf := func(token, kind string) string {
		t.Helper()
		session, ok, err := fixture.store.Session(ctx, token, kind, fixture.app.now())
		if err != nil || !ok {
			t.Fatalf("no %s session for %s: ok=%v err=%v", kind, token, ok, err)
		}
		return session.CSRF
	}
	// preauthToken opens the sign-in page at path and returns the form token
	// of this address: the cookie the answer set, or the valid one the browser
	// already holds, which the page reuses without sending a new one.
	preauthToken := func(address, path, name string) string {
		t.Helper()
		answer := browserGET(t, client, address+path)
		if answer.status != http.StatusOK {
			t.Fatalf("sign-in page at %s%s status=%d location=%q", address, path, answer.status, answer.header.Get("Location"))
		}
		for _, line := range answer.header.Values("Set-Cookie") {
			if cookie, err := http.ParseSetCookie(line); err == nil && cookie.Name == name && cookie.Value != "" {
				return cookie.Value
			}
		}
		parsed, err := url.Parse(address)
		if err != nil {
			t.Fatalf("address %s: %v", address, err)
		}
		for _, cookie := range client.Jar.Cookies(parsed) {
			if cookie.Name == name {
				return cookie.Value
			}
		}
		t.Fatalf("no %s cookie from %s%s and none in the jar", name, address, path)
		return ""
	}
	signIn := func(address, preauthName, sessionName string) string {
		t.Helper()
		csrf := preauthToken(address, "/login", preauthName)
		return setCookie(t, postsPassword(t, client, address, csrf), sessionName).Value
	}
	adminSignIn := func(address, preauthName, adminName string) string {
		t.Helper()
		csrf := preauthToken(address, "/admin/login", preauthName)
		answer := browserForm(t, client, address+"/admin/login", url.Values{
			"csrf": {csrf}, "admin_password": {"admin-password"}, "next": {"/"}}, address)
		if answer.status != http.StatusSeeOther {
			t.Fatalf("administrator sign-in at %s status=%d", address, answer.status)
		}
		return setCookie(t, answer, adminName).Value
	}

	// The secure sign-out ends both sign-ins.
	secureToken := signIn(secure.URL, httpsCookiePrefix+preauthCookie, httpsCookiePrefix+generalCookie)
	plainToken := signIn(plain.URL, preauthCookie, generalCookie)
	answer := browserForm(t, client, secure.URL+"/logout", url.Values{"csrf": {csrfOf(secureToken, "general")}}, secure.URL)
	if answer.status != http.StatusSeeOther {
		t.Fatalf("sign-out at the secure address status=%d", answer.status)
	}
	for _, name := range []string{httpsCookiePrefix + generalCookie, generalCookie, httpsCookiePrefix + adminCookie, adminCookie} {
		cleared := setCookie(t, answer, name)
		if cleared.Value != "" || cleared.Expires.After(time.Now()) {
			t.Fatalf("the sign-out does not clear %s: %+v", name, cleared)
		}
	}
	for _, session := range []struct{ token, kind string }{{secureToken, "general"}, {plainToken, "general"}} {
		if _, ok, err := fixture.store.Session(ctx, session.token, session.kind, fixture.app.now()); err != nil || ok {
			t.Fatalf("the secure sign-out left the %s session %s: ok=%v err=%v", session.kind, session.token, ok, err)
		}
	}

	// The plain sign-out ends its own sign-in only, and says that the secure
	// address keeps its own.
	secureToken = signIn(secure.URL, httpsCookiePrefix+preauthCookie, httpsCookiePrefix+generalCookie)
	plainToken = signIn(plain.URL, preauthCookie, generalCookie)
	// The sign-in page of the address that kept its sign-in says so. The
	// notice value of the redirect and the catalog code are spelled out:
	// they are the contract of this sign-out.
	const (
		secureKeptNotice = "logout_secure_kept"
		adminKeptNotice  = "admin_logout_secure_kept"
	)
	answer = browserForm(t, client, plain.URL+"/logout", url.Values{"csrf": {csrfOf(plainToken, "general")}}, plain.URL)
	if answer.status != http.StatusSeeOther || !strings.Contains(answer.header.Get("Location"), "notice="+secureKeptNotice) {
		t.Fatalf("sign-out at the plain address status=%d location=%q", answer.status, answer.header.Get("Location"))
	}
	if _, ok, _ := fixture.store.Session(ctx, plainToken, "general", fixture.app.now()); ok {
		t.Fatal("the plain sign-out left its own session")
	}
	if _, ok, err := fixture.store.Session(ctx, secureToken, "general", fixture.app.now()); err != nil || !ok {
		t.Fatalf("the plain sign-out ended the session of the secure address: ok=%v err=%v", ok, err)
	}
	sentence := webui.Text(webui.LangEN, webui.MessageCode("login."+secureKeptNotice))
	if page := browserGET(t, client, plain.URL+"/login?notice="+secureKeptNotice); !strings.Contains(page.body, sentence) {
		t.Fatalf("the sign-in page does not say %q", sentence)
	}

	// The administrator-only sign-out at the plain address ends the plain
	// confirmation, keeps the confirmation of the secure address, and says
	// that the dashboard cannot end it here.
	plainToken = signIn(plain.URL, preauthCookie, generalCookie)
	secureAdmin := adminSignIn(secure.URL, httpsCookiePrefix+preauthCookie, httpsCookiePrefix+adminCookie)
	plainAdmin := adminSignIn(plain.URL, preauthCookie, adminCookie)
	answer = browserForm(t, client, plain.URL+"/admin/logout", url.Values{"csrf": {csrfOf(plainToken, "general")}}, plain.URL)
	if answer.status != http.StatusSeeOther || !strings.Contains(answer.header.Get("Location"), "notice="+adminKeptNotice) {
		t.Fatalf("administrator sign-out at the plain address status=%d location=%q", answer.status, answer.header.Get("Location"))
	}
	if _, ok, _ := fixture.store.Session(ctx, plainAdmin, "admin", fixture.app.now()); ok {
		t.Fatal("the administrator sign-out left its own confirmation")
	}
	if _, ok, err := fixture.store.Session(ctx, secureAdmin, "admin", fixture.app.now()); err != nil || !ok {
		t.Fatalf("the administrator sign-out ended the confirmation of the secure address: ok=%v err=%v", ok, err)
	}
	adminSentence := webui.Text(webui.LangEN, webui.MessageCode("admin.secure_kept"))
	if page := browserGET(t, client, plain.URL+"/?notice="+adminKeptNotice); !strings.Contains(page.body, adminSentence) {
		t.Fatalf("the dashboard does not say %q", adminSentence)
	}

	// A reader who holds no plain general sign-in cannot open the dashboard,
	// which would send them to the sign-in page without the notice. The
	// sign-in page carries the sentence itself, so this reader reads it too.
	plainAdmin = adminSignIn(plain.URL, preauthCookie, adminCookie)
	noErr(t, fixture.store.DeleteSession(ctx, plainToken, "general"))
	answer = browserForm(t, client, plain.URL+"/admin/logout", url.Values{"csrf": {csrfOf(plainAdmin, "admin")}}, plain.URL)
	if answer.status != http.StatusSeeOther || answer.header.Get("Location") != "/login?notice="+adminKeptNotice {
		t.Fatalf("administrator sign-out without a plain sign-in status=%d location=%q", answer.status, answer.header.Get("Location"))
	}
	if _, ok, _ := fixture.store.Session(ctx, plainAdmin, "admin", fixture.app.now()); ok {
		t.Fatal("the administrator sign-out without a plain sign-in left its confirmation")
	}
	page := browserGET(t, client, plain.URL+answer.header.Get("Location"))
	if page.status != http.StatusOK || !strings.Contains(page.body, adminSentence) {
		t.Fatalf("the sign-in page after the administrator sign-out status=%d, saying %q: %v", page.status, adminSentence, strings.Contains(page.body, adminSentence))
	}
}

// postsPassword sends the shared-password form with csrf and returns the
// answer.
func postsPassword(t *testing.T, client *http.Client, address, csrf string) browserHTTPResult {
	t.Helper()
	result := browserForm(t, client, address+"/login", url.Values{"csrf": {csrf}, "password": {"shared-password"}, "next": {"/"}}, address)
	if result.status != http.StatusSeeOther {
		t.Fatalf("sign-in at %s status=%d", address, result.status)
	}
	return result
}

// setCookie returns the cookie of an answer named exactly name, one name for
// each scheme, and says which names the answer did set.
func setCookie(t *testing.T, result browserHTTPResult, name string) *http.Cookie {
	t.Helper()
	var set []string
	for _, line := range result.header.Values("Set-Cookie") {
		cookie, err := http.ParseSetCookie(line)
		if err != nil {
			continue
		}
		if cookie.Name == name {
			return cookie
		}
		set = append(set, cookie.Name)
	}
	t.Fatalf("the answer set no %s cookie, only %s", name, strings.Join(set, ", "))
	return nil
}
