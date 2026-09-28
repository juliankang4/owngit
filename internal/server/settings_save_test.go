package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
)

// A settings change that could not be saved changes nothing: this browser
// keeps its sessions, the page says the change was not saved, and the cause
// is logged once.
func TestSettingsChangeThatWasNotSavedKeepsThisBrowserSignedIn(t *testing.T) {
	for _, check := range []struct {
		action string
		values url.Values
	}{
		{webui.ActionChangeAccessPassword, url.Values{"access_password": {"shared-password-new"}}},
		{webui.ActionDisableAccessPassword, url.Values{}},
		{webui.ActionChangeAdminPassword, url.Values{"new_admin_password": {"admin-password-new"}}},
	} {
		t.Run(check.action, func(t *testing.T) {
			fixture := newAPIFixture(t, true)
			ctx := context.Background()
			settings, err := fixture.store.Settings(ctx)
			noErr(t, err)
			noErr(t, fixture.store.CreateSession(ctx, "kept-general", "general", "kept-general-csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
			server := serve(t, fixture.app.Handler())
			client, jar := newBrowserClient(t)
			parsed, _ := url.Parse(server.URL)
			jar.SetCookies(parsed, []*http.Cookie{{Name: generalCookie, Value: "kept-general", Path: "/"}})
			browserAdminSessionFor(t, fixture, server.URL, jar, "kept-admin")
			refuseWrites(t, fixture.store, "refuse_password_insert", "INSERT ON passwords")
			refuseWrites(t, fixture.store, "refuse_password_delete", "DELETE ON passwords")
			serverLog := captureServerLog(t)

			values := check.values
			values.Set("csrf", "kept-general-csrf")
			values.Set("action", check.action)
			values.Set("admin_password", "admin-password")
			result := browserForm(t, client, server.URL+"/settings", values, server.URL)
			if result.status != http.StatusServiceUnavailable || !strings.Contains(result.body, enText(webui.MsgSettingsNotSaved)) {
				t.Fatalf("status=%d body=%s", result.status, result.body)
			}
			for _, cookie := range result.header.Values("Set-Cookie") {
				if strings.HasPrefix(cookie, generalCookie+"=;") || strings.HasPrefix(cookie, adminCookie+"=;") {
					t.Fatalf("a change that was not saved ended a session: %s", cookie)
				}
			}
			checkLoggedSteps(t, check.action, loggedFailures(serverLog, 0), "settings save")
			for _, session := range []struct{ token, kind string }{{"kept-general", "general"}, {"kept-admin-session", "admin"}} {
				if _, ok, err := fixture.store.Session(ctx, session.token, session.kind, time.Now()); err != nil || !ok {
					t.Fatalf("%s session ok=%v err=%v", session.kind, ok, err)
				}
			}
		})
	}
}

// A network change that could not be saved says so and leaves the plain
// HTTP acknowledgement it carried unrecorded, as it leaves the address.
func TestNetworkChangeThatWasNotSavedRecordsNoAcknowledgement(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash := fixturePasswordHash(t, "admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, false))
	app.Repositories.SetRoot(canonical)
	client, base, csrf, body := networkSettingsClient(t, app)
	revision := formValue(t, body, "network_revision")
	noErr(t, store.Exec(context.Background(), `CREATE TRIGGER refuse_listen BEFORE INSERT ON metadata WHEN NEW.key='network_listen' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`))
	serverLog := captureServerLog(t)
	values := saveNetworkForm(csrf, revision, "admin-password", map[string]string{"listen": "0.0.0.0:7797", "insecure_ack": "1"})
	if result := browserForm(t, client, base+"/settings", values, base); result.status != http.StatusServiceUnavailable || !strings.Contains(result.body, enText(webui.MsgSettingsNotSaved)) {
		t.Fatalf("status=%d body=%s", result.status, result.body)
	}
	checkLoggedSteps(t, "network save", loggedFailures(serverLog, 0), "network settings save")
	current, err := store.Settings(context.Background())
	noErr(t, err)
	if settings, _, _ := savedNetwork(t, store); settings.Listen != "" || current.InsecureHTTPAccepted {
		t.Fatalf("a change that was not saved left listen=%q acknowledged=%v", settings.Listen, current.InsecureHTTPAccepted)
	}
}

// Turning Tailscale sharing on for the home network, when its settings save
// fails, leaves the plain HTTP acknowledgement unrecorded too.
func TestTailscaleChangeThatWasNotSavedRecordsNoAcknowledgement(t *testing.T) {
	ctx := context.Background()
	homeNetwork := true
	unacknowledged, store, root := newTestApp(t)
	noErr(t, store.CompleteSetup(ctx, root, "open", "", "synthetic-admin-hash", false))
	app, _ := withTailscale(t, unacknowledged, tailscaletest.State{Status: tailscaletest.Running()})
	noErr(t, store.Exec(ctx, `CREATE TRIGGER refuse_listen BEFORE INSERT ON metadata WHEN NEW.key='network_listen' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`))
	if _, err := app.Tailscale.On(ctx, &homeNetwork, 0); err == nil {
		t.Fatal("turning sharing on succeeded although its settings save was refused")
	}
	settings, err := store.Settings(ctx)
	noErr(t, err)
	if settings.InsecureHTTPAccepted {
		t.Fatal("a Tailscale change that was not saved recorded the plain HTTP acknowledgement")
	}
}

// A sharing change whose settings could not be saved says so, and says that
// Tailscale may already have the change whenever Tailscale accepted a write
// or removal, or shows it: its settings save failed, or Tailscale could not
// be read back. The cause is logged once either way.
func TestTailscaleChangeThatWasNotSavedSaysWhetherTailscaleChanged(t *testing.T) {
	const readFailure = "synthetic serve status failure"
	for _, check := range []struct {
		name, action string
		onFirst      bool
		// The change fails through a trigger in the state, or through the
		// fake's ServeReadErrorAfterWrite.
		trigger string
		fake    tailscaletest.State
		// written is whether Tailscale is changed before the failure.
		written bool
		want    webui.MessageCode
	}{
		{name: "on, before the endpoint is written", action: webui.ActionTailscaleOn,
			trigger: `CREATE TRIGGER refuse BEFORE INSERT ON metadata WHEN NEW.key='tailscale_serve' AND NOT json_extract(NEW.value,'$.confirmed') BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
			want:    webui.MsgSettingsNotSaved},
		{name: "on, after the endpoint is written", action: webui.ActionTailscaleOn,
			trigger: `CREATE TRIGGER refuse BEFORE UPDATE ON metadata WHEN NEW.key='tailscale_serve' AND json_extract(NEW.value,'$.confirmed') BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
			written: true, want: webui.MsgTSNotSavedAhead},
		{name: "on, when the written endpoint cannot be read back", action: webui.ActionTailscaleOn,
			fake:    tailscaletest.State{ServeReadErrorAfterWrite: readFailure},
			written: true, want: webui.MsgTSNotSavedAhead},
		{name: "off, after the endpoint is removed", action: webui.ActionTailscaleOff, onFirst: true,
			trigger: `CREATE TRIGGER refuse BEFORE DELETE ON metadata WHEN OLD.key='tailscale_serve' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
			written: true, want: webui.MsgTSNotSavedAhead},
		{name: "off, when the removal cannot be read back", action: webui.ActionTailscaleOff, onFirst: true,
			fake:    tailscaletest.State{ServeReadErrorAfterWrite: readFailure},
			written: true, want: webui.MsgTSNotSavedAhead},
	} {
		t.Run(check.name, func(t *testing.T) {
			app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
			ctx := context.Background()
			if check.onFirst {
				_, err := app.Tailscale.On(ctx, nil, 0)
				noErr(t, err)
			}
			client, base, csrf, _ := networkSettingsClient(t, app)
			if check.trigger != "" {
				noErr(t, app.Store.Exec(ctx, check.trigger))
			}
			fake.Update(func(s *tailscaletest.State) {
				s.ServeReadErrorAfterWrite, s.Wrote = check.fake.ServeReadErrorAfterWrite, false
			})
			writes := len(fake.Writes())
			serverLog := captureServerLog(t)
			result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, check.action, "admin-password", false), base)
			if result.status != http.StatusServiceUnavailable || !strings.Contains(result.body, enText(check.want)) {
				t.Fatalf("status=%d body=%s", result.status, result.body)
			}
			if written := len(fake.Writes()) > writes; written != check.written {
				t.Fatalf("Tailscale written=%v, want %v: %q", written, check.written, fake.Writes()[writes:])
			}
			lines := loggedFailures(serverLog, 0)
			checkLoggedSteps(t, check.name, lines, "Tailscale sharing change")
			if check.want == webui.MsgTSNotSavedAhead && !strings.Contains(strings.Join(lines, "\n"), ErrTailscaleAhead.Error()) {
				t.Errorf("the log does not say that Tailscale may be ahead: %q", lines)
			}
		})
	}
}

// A problem the owner can fix, such as a permission Tailscale refused, is
// answered as refused and not logged. A failure Tailscale did not explain may
// pass on another try, so it is unavailable and its cause is logged.
func TestTailscaleFailureThatMayPassOnAnotherTryIsUnavailable(t *testing.T) {
	for _, check := range []struct {
		name, writeError string
		status           int
		logged           []string
	}{
		{"a refused permission", "Access denied: serve config denied", http.StatusConflict, nil},
		{"an unexplained failure", "synthetic unexplained failure", http.StatusServiceUnavailable, []string{"Tailscale sharing change"}},
	} {
		t.Run(check.name, func(t *testing.T) {
			app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), WriteError: check.writeError})
			client, base, csrf, _ := networkSettingsClient(t, app)
			serverLog := captureServerLog(t)
			result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", false), base)
			if result.status != check.status || strings.Contains(result.body, enText(webui.MsgTSNotSavedAhead)) {
				t.Fatalf("status=%d, want %d; body=%s", result.status, check.status, result.body)
			}
			checkLoggedSteps(t, check.name, loggedFailures(serverLog, 0), check.logged...)
		})
	}
}

// Only a timeout, an unreadable answer and an unexplained failure may pass on
// another try; every other problem is one the owner can fix.
func TestTailscaleRefusedOnlyWhenTheOwnerCanFixIt(t *testing.T) {
	for _, problem := range []string{
		string(tailscale.KindNotInstalled), string(tailscale.KindNotRunning), string(tailscale.KindLoggedOut),
		string(tailscale.KindStopped), string(tailscale.KindNeedsApproval), string(tailscale.KindMagicDNSOff),
		string(tailscale.KindHTTPSOff), string(tailscale.KindHTTPSUnavailable), string(tailscale.KindPermission),
		TailscaleProblemTaken, TailscaleProblemOtherPort, TailscaleProblemUnrecorded,
		TailscaleProblemChanged, TailscaleProblemListenOption, TailscaleProblemNotOn, TailscaleProblemServeChanged,
		TailscaleProblemOwnersEndpoint,
	} {
		if !(&TailscaleError{Problem: problem}).Refused() {
			t.Errorf("%s is not answered as refused", problem)
		}
	}
	for _, problem := range []string{string(tailscale.KindTimeout), string(tailscale.KindUnreadable), string(tailscale.KindFailed), TailscaleProblemReadBack} {
		if (&TailscaleError{Problem: problem}).Refused() {
			t.Errorf("%s is answered as refused", problem)
		}
	}
}

// Tailscale may have a change when a read back shows it, or when the read
// back failed and the change command succeeded or failed without a refusal
// (Refused). A read back that shows no change, or a command Tailscale
// refused, rules it out. The timeout is the value tailscale.Command returns
// at its time limit, which no test here waits for.
func TestTailscaleMayHaveTheChangeWhenItShowsItOrItsOutcomeIsUnknown(t *testing.T) {
	timedOut := &tailscale.Error{Kind: tailscale.KindTimeout}
	refused := &tailscale.Error{Kind: tailscale.KindPermission}
	failed := &tailscale.Error{Kind: tailscale.KindFailed, Detail: "synthetic failure"}
	unreadable := &tailscale.Error{Kind: tailscale.KindUnreadable}
	readFailed := &tailscale.Error{Kind: tailscale.KindFailed, Detail: "synthetic read failure"}
	for _, check := range []struct {
		name               string
		changeErr, readErr error
		shown, want        bool
	}{
		{"accepted and shown", nil, nil, true, true},
		{"accepted and not kept", nil, nil, false, false},
		{"accepted and not read back", nil, readFailed, false, true},
		{"timed out and shown", timedOut, nil, true, true},
		{"timed out and not shown", timedOut, nil, false, false},
		{"timed out and not read back", timedOut, readFailed, false, true},
		{"refused and shown", refused, nil, true, true},
		{"refused and not read back", refused, readFailed, false, false},
		{"failed and not read back", failed, readFailed, false, true},
		{"unreadable and not read back", unreadable, readFailed, false, true},
	} {
		if got := tailscaleMayHave(check.changeErr, check.readErr, check.shown); got != check.want {
			t.Errorf("%s: may have=%v, want %v", check.name, got, check.want)
		}
	}
}

// Turning sharing off reads Tailscale back after a failed removal, as
// turning on does after a failed write: a change command that failed after
// Tailscale kept the change is marked as possibly ahead, also when it cannot
// be read back, and one Tailscale refused, which a read back shows
// unchanged, is not.
func TestTailscaleChangeThatFailedIsMarkedByWhatTheReadBackShows(t *testing.T) {
	for _, check := range []struct {
		name    string
		on      bool
		fake    func(*tailscaletest.State)
		problem string
		marked  bool
	}{
		{"on, the write fails after it took effect", true, func(s *tailscaletest.State) { s.WriteErrorAfterChange = "synthetic interrupted write" }, string(tailscale.KindFailed), true},
		{"off, the removal fails after it took effect", false, func(s *tailscaletest.State) { s.WriteErrorAfterChange = "synthetic interrupted removal" }, string(tailscale.KindFailed), true},
		{"off, the removal fails and cannot be read back", false, func(s *tailscaletest.State) {
			// Wrote is still set by turning on; only the removal counts.
			s.WriteErrorAfterChange, s.ServeReadErrorAfterWrite, s.Wrote = "synthetic interrupted removal", "synthetic serve status failure", false
		}, string(tailscale.KindFailed), true},
		{"off, the removal is refused", false, func(s *tailscaletest.State) { s.WriteError = "Access denied: serve config denied" }, string(tailscale.KindPermission), false},
	} {
		t.Run(check.name, func(t *testing.T) {
			app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
			ctx := context.Background()
			if !check.on {
				_, err := app.Tailscale.On(ctx, nil, 0)
				noErr(t, err)
			}
			fake.Update(check.fake)
			reads := len(fake.Calls())
			var err error
			if check.on {
				_, err = app.Tailscale.On(ctx, nil, 0)
			} else {
				_, err = app.Tailscale.Off(ctx)
			}
			var refusal *TailscaleError
			if !errors.As(err, &refusal) || refusal.Problem != check.problem || errors.Is(err, ErrTailscaleAhead) != check.marked {
				t.Fatalf("err=%v, want problem %s marked=%v", err, check.problem, check.marked)
			}
			if calls := fake.Calls()[reads:]; calls[len(calls)-1] != "serve status --json" {
				t.Fatalf("the failed change was not read back: %q", calls)
			}
		})
	}
}

// A change Tailscale accepted and did not keep is unavailable, not refused:
// it may pass on another try, so it is answered 503 and its cause logged,
// with its explanation kept.
func TestTailscaleChangeThatWasNotKeptIsUnavailable(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), IgnoreWrites: true})
	client, base, csrf, _ := networkSettingsClient(t, app)
	serverLog := captureServerLog(t)
	result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", false), base)
	if result.status != http.StatusServiceUnavailable || !strings.Contains(result.body, enText(webui.TailscaleProblemCode(TailscaleProblemReadBack))) || strings.Contains(result.body, enText(webui.MsgTSNotSavedAhead)) {
		t.Fatalf("status=%d body=%s", result.status, result.body)
	}
	checkLoggedSteps(t, "a change not kept", loggedFailures(serverLog, 0), "Tailscale sharing change")
}
