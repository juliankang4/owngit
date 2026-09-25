package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
)

func runTailscale(t *testing.T, arguments ...string) (string, error) {
	t.Helper()
	return captureStdout(func() error { return run(append([]string{"tailscale"}, arguments...)) })
}

func tailscaleJSON(t *testing.T, stateDir, fake string) server.TailscaleReport {
	t.Helper()
	output, err := runTailscale(t, "status", "--state-dir", stateDir, "--tailscale", fake, "--json")
	noErr(t, err)
	var report server.TailscaleReport
	noErr(t, json.Unmarshal([]byte(output), &report))
	return report
}

// throughServeTo sends a request to a running server as Tailscale Serve
// passes it on.
func throughServeTo(t *testing.T, base string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, base+"/", nil)
	noErr(t, err)
	request.Host = tailscaletest.Name
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-For", "100.64.0.9")
	response, err := http.DefaultTransport.RoundTrip(request)
	noErr(t, err)
	response.Body.Close()
	return response
}

// "owngit tailscale on" beside a running server changes Tailscale and the
// saved settings, and says that the running server needs a restart; after
// the restart the address is ready. "off" removes what "on" made.
func TestTailscaleCommandBesideARunningServer(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "127.0.0.1:7821")
	noErr(t, err)

	report := tailscaleJSON(t, stateDir, fake.Path)
	if report.On || !report.Installed || report.Name != tailscaletest.Name || report.Endpoint != server.TailscaleEndpointFree || report.Server != state.ServerNotRunning {
		t.Fatalf("before: %+v", report)
	}

	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--tailscale", fake.Path})
	output, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	if err != nil {
		instance.stop()
		t.Fatal(err)
	}
	for _, want := range []string{"Tailscale now answers HTTPS for " + tailscaletest.Name, "http://127.0.0.1:7821", "on, but not ready yet", "Restart OwnGit to finish", "Turning sharing on or off in Settings applies at once"} {
		if !strings.Contains(output, want) {
			instance.stop()
			t.Fatalf("on printed %q, lacking %q", output, want)
		}
	}
	if response := throughServeTo(t, instance.url); response.StatusCode != http.StatusMisdirectedRequest {
		instance.stop()
		t.Fatalf("the running server accepted the name before a restart: %d", response.StatusCode)
	}
	instance.stop()

	instance = startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--tailscale", fake.Path})
	report = tailscaleJSON(t, stateDir, fake.Path)
	response := throughServeTo(t, instance.url)
	if !report.On || !report.Ready || report.URL != "https://"+tailscaletest.Name+"/" {
		instance.stop()
		t.Fatalf("after the restart: %+v", report)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusSeeOther {
		instance.stop()
		t.Fatalf("a request through Serve got %d", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		if !cookie.Secure {
			instance.stop()
			t.Fatalf("cookie %s from a request through Serve is not Secure", cookie.Name)
		}
	}
	if !strings.Contains(instance.log(), "trusting forwarded headers from reverse proxies at 127.0.0.1") {
		instance.stop()
		t.Fatalf("serve does not trust 127.0.0.1:\n%s", instance.log())
	}

	output, err = runTailscale(t, "off", "--state-dir", stateDir, "--tailscale", fake.Path)
	instance.stop()
	noErr(t, err)
	if !strings.Contains(output, "Tailscale no longer answers HTTPS for "+tailscaletest.Name) ||
		strings.Count(strings.ToLower(output), "restart") != 1 || !strings.Contains(output, "Settings applies at once") {
		t.Fatalf("off printed %q", output)
	}
	want := []string{"serve --bg --https=443 http://127.0.0.1:7821", "serve --https=443 --set-path=/ off"}
	if !reflect.DeepEqual(fake.Writes(), want) {
		t.Fatalf("writes=%q, want %q", fake.Writes(), want)
	}
	report = tailscaleJSON(t, stateDir, fake.Path)
	if report.On || report.Endpoint != server.TailscaleEndpointFree {
		t.Fatalf("after off: %+v", report)
	}
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	defer store.Close()
	settings, err := store.NetworkSettings(context.Background())
	noErr(t, err)
	proxies, err := store.TrustedProxies(context.Background())
	noErr(t, err)
	if settings != (state.NetworkSettings{Listen: "127.0.0.1:7821"}) || len(proxies) != 0 {
		t.Fatalf("after off: %+v %v", settings, proxies)
	}
}

// A refusal says what is on the port, and a problem says how to fix it.
func TestTailscaleCommandExplainsRefusals(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}}}},
	}})
	_, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	if err == nil || !strings.Contains(err.Error(), "OwnGit changed nothing") || !strings.Contains(err.Error(), "https://"+tailscaletest.Name+":443/ to http://127.0.0.1:3000") {
		t.Fatalf("err=%v", err)
	}
	if len(fake.Writes()) != 0 {
		t.Fatalf("writes=%q", fake.Writes())
	}
	fake.Update(func(fakeState *tailscaletest.State) { fakeState.Status.CertDomains = nil })
	output, err := runTailscale(t, "status", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if !strings.Contains(output, "Turn on HTTPS Certificates on the DNS page of the Tailscale admin console") {
		t.Fatalf("status printed %q", output)
	}
	if _, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "cannot find the tailscale command") {
		t.Fatalf("missing command: err=%v", err)
	}
	if _, err := runTailscale(t, "off", "--state-dir", stateDir, "--tailscale", fake.Path); err == nil || !strings.Contains(err.Error(), "not on") {
		t.Fatalf("off while off: err=%v", err)
	}
	// A mistyped state directory is refused before Tailscale is changed,
	// as by every other command.
	for _, command := range []string{"on", "status", "off"} {
		missing := filepath.Join(t.TempDir(), "missing")
		if _, err := runTailscale(t, command, "--state-dir", missing, "--tailscale", fake.Path); err == nil {
			t.Errorf("tailscale %s accepted a missing state directory", command)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Errorf("tailscale %s created the missing state directory", command)
		}
	}
	if len(fake.Writes()) != 0 {
		t.Fatalf("writes=%q", fake.Writes())
	}
}

// After the computer is renamed, "off" takes back OwnGit's settings and
// says how to remove the address under the old name, which only that name
// can remove, and "on" then uses the new name.
func TestTailscaleCommandAfterARename(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	fake.Update(func(s *tailscaletest.State) {
		s.Status.Self.DNSName, s.Status.CertDomains = "newbox.tail0000.ts.net.", []string{"newbox.tail0000.ts.net"}
	})
	output, err := runTailscale(t, "status", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if !strings.Contains(output, "Turn sharing on again to use the new name") {
		t.Fatalf("status printed %q", output)
	}
	output, err = runTailscale(t, "off", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	for _, want := range []string{"no longer named " + tailscaletest.Name, "rename the computer back", "https://" + tailscaletest.Name + ":443/"} {
		if !strings.Contains(output, want) {
			t.Errorf("off printed %q, lacking %q", output, want)
		}
	}
	if len(fake.Writes()) != 1 {
		t.Fatalf("off wrote to Tailscale after the rename: %q", fake.Writes())
	}
	output, err = runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if !strings.Contains(output, "Tailscale now answers HTTPS for newbox.tail0000.ts.net") || !strings.Contains(output, "under a name this computer had before") {
		t.Fatalf("on after the rename printed %q", output)
	}
}

// "on" shows the certificate log notice, in the words of the Settings page,
// before it asks Tailscale to serve the address, and its JSON carries it.
// A refusal before any write shows no notice.
func TestTailscaleOnShowsTheCertificateLogNotice(t *testing.T) {
	notice := fmt.Sprintf(webui.Text(webui.LangEN, webui.MsgTSCertLog), tailscaletest.Name)
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), WriteError: "Access denied: serve config denied"})
	output, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	if err == nil || !strings.Contains(output, notice) {
		t.Fatalf("a failed write: err=%v output=%q", err, output)
	}
	fake.Update(func(s *tailscaletest.State) { s.WriteError = "" })
	output, err = runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path, "--json")
	noErr(t, err)
	var result struct {
		On             bool   `json:"on"`
		CertificateLog string `json:"certificate_log"`
	}
	noErr(t, json.Unmarshal([]byte(output), &result))
	if !result.On || result.CertificateLog != notice {
		t.Fatalf("JSON: %+v", result)
	}

	taken := initializedState(t, false)
	busy := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}}}},
	}})
	if output, err := runTailscale(t, "on", "--state-dir", taken, "--tailscale", busy.Path); err == nil || strings.Contains(output, "certificate log") {
		t.Fatalf("a refusal: err=%v output=%q", err, output)
	}
}
