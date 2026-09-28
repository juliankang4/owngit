package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
)

// everyPortTaken is a Serve configuration in which another program answers
// HTTPS on each port OwnGit would choose.
func everyPortTaken() tailscale.ServeConfig {
	config := tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{}, Web: map[string]tailscale.WebServer{}}
	for _, port := range []string{"443", "8443", "10000"} {
		config.TCP[port] = tailscale.TCPHandler{HTTPS: true}
		config.Web[tailscaletest.Name+":"+port] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}}}
	}
	return config
}

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
	if len(fake.Writes()) != 2 || len(fake.State().Serve.Web) != 0 {
		t.Fatalf("writes=%q, serve %+v", fake.Writes(), fake.State().Serve)
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

// A refusal says what is on the ports, and a problem says how to fix it.
func TestTailscaleCommandExplainsRefusals(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: everyPortTaken()})
	_, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	if err == nil || !strings.Contains(err.Error(), "OwnGit changed nothing") || !strings.Contains(err.Error(), "--https-port PORT") ||
		!strings.Contains(err.Error(), "https://"+tailscaletest.Name+":443/ to http://127.0.0.1:3000") ||
		!strings.Contains(err.Error(), "https://"+tailscaletest.Name+":10000/ to http://127.0.0.1:3000") {
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
	// as by every other command, and "status" reports the defaults; none
	// creates the directory.
	for _, command := range []string{"on", "status", "off"} {
		missing := filepath.Join(t.TempDir(), "missing")
		output, err := runTailscale(t, command, "--state-dir", missing, "--tailscale", fake.Path)
		if command == "status" {
			if err != nil || !strings.Contains(output, "No OwnGit state exists in "+missing) || !strings.Contains(output, "Sharing on the tailnet over HTTPS: off.") {
				t.Errorf("tailscale status on a missing state: %q %v", output, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "Start OwnGit once") {
			t.Errorf("tailscale %s on a missing state directory: %v", command, err)
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
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), WriteDenied: true})
	output, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	if err == nil || !strings.Contains(output, notice) {
		t.Fatalf("a failed write: err=%v output=%q", err, output)
	}
	// Printed while turning on, the notice reads as a statement, not as
	// advice for before turning on.
	if strings.Contains(notice, "before you turn") {
		t.Errorf("notice %q", notice)
	}
	fake.Update(func(s *tailscaletest.State) { s.WriteDenied = false })
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
	busy := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: everyPortTaken()})
	if output, err := runTailscale(t, "on", "--state-dir", taken, "--tailscale", busy.Path); err == nil || strings.Contains(output, "certificate log") {
		t.Fatalf("a refusal: err=%v output=%q", err, output)
	}
}

// "status" gives the verdict the Settings page gives: with every HTTPS port
// OwnGit would choose taken, it says that sharing cannot be turned on and
// does not suggest it.
func TestTailscaleStatusSharesTheVerdictOfSettings(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	output, err := runTailscale(t, "status", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if !strings.Contains(output, "Ready to share as https://"+tailscaletest.Name+"/") || !strings.Contains(output, "Turn it on with") {
		t.Fatalf("free port: %q", output)
	}
	if report := tailscaleJSON(t, stateDir, fake.Path); !report.CanTurnOn {
		t.Fatalf("free port: %+v", report)
	}
	fake.Update(func(s *tailscaletest.State) {
		s.Serve = everyPortTaken()
		s.Serve.Web[tailscaletest.Name+":443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/other": {Proxy: "http://127.0.0.1:9999"}}}
	})
	output, err = runTailscale(t, "status", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if strings.Contains(output, "Ready to share") || strings.Contains(output, "Turn it on") ||
		!strings.Contains(output, "so sharing cannot be turned on") || !strings.Contains(output, "http://127.0.0.1:9999") ||
		!strings.Contains(output, "--https-port PORT") {
		t.Fatalf("taken port: %q", output)
	}
	if report := tailscaleJSON(t, stateDir, fake.Path); report.CanTurnOn {
		t.Fatalf("taken port: %+v", report)
	}
}

// After the endpoint was changed by hand, "status" and a refused "off" give
// the commands that make turning off work.
func TestTailscaleCommandGivesStepsForAChangedEndpoint(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	fake.Update(func(s *tailscaletest.State) {
		s.Serve.Web[tailscaletest.Name+":443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:7701"}}}
	})
	for _, command := range []string{"status", "off"} {
		output, err := runTailscale(t, command, "--state-dir", stateDir, "--tailscale", fake.Path)
		if err != nil {
			output += err.Error()
		}
		if !strings.Contains(output, `"tailscale serve --https=443 off"`) || !strings.Contains(output, `"tailscale serve --bg --https=443 http://127.0.0.1:7654"`) ||
			strings.Contains(output, "Turn it on") {
			t.Errorf("%s printed %q", command, output)
		}
	}
}

// An address to OwnGit that OwnGit has no record of making is shown as
// taking the port, with the command that removes it, and "on" refuses it.
func TestTailscaleStatusShowsAnUnrecordedEndpointAsTaken(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:7654"}}}},
	}})
	output, err := runTailscale(t, "status", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if strings.Contains(output, "Ready to share") || strings.Contains(output, "Turn it on") ||
		!strings.Contains(output, "no record of making it") || !strings.Contains(output, `"tailscale serve --https=443 off"`) {
		t.Fatalf("status printed %q", output)
	}
	if _, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path); err == nil || !strings.Contains(err.Error(), "no record of making it") {
		t.Fatalf("on: %v", err)
	}
	if writes := fake.Writes(); len(writes) != 0 {
		t.Fatalf("writes=%q", writes)
	}
}

// "off" while Tailscale is stopped says how to fix it, and what Tailscale
// printed.
func TestTailscaleOffWhileTailscaleIsStopped(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	fake.Update(func(s *tailscaletest.State) {
		s.Status.BackendState = "Stopped"
		s.WriteError = "Tailscale is stopped."
	})
	_, err = runTailscale(t, "off", "--state-dir", stateDir, "--tailscale", fake.Path)
	want := webui.Text(webui.LangEN, webui.TailscaleProblemCode("stopped")) + " Tailscale said: updating config: Tailscale is stopped."
	if err == nil || err.Error() != want {
		t.Fatalf("off: %v", err)
	}
}

// With TCP forwarding on 443, "status" says that sharing will use 8443 and
// leave 443 alone, and "on" does so, saying why and that the first visit
// waits for the certificate. Port 443 is not touched.
func TestTailscaleCommandUsesAnotherPortWhen443IsTaken(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {TCPForward: "127.0.0.1:22"}},
	}})
	output, err := runTailscale(t, "status", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	note := fmt.Sprintf(webui.Text(webui.LangEN, webui.MsgTSPortNote), "443", "8443")
	if !strings.Contains(output, "Ready to share as https://"+tailscaletest.Name+":8443/.") || !strings.Contains(output, note) ||
		strings.Contains(output, "tailscale serve --tcp=443 off") {
		t.Fatalf("status printed %q", output)
	}
	output, err = runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	for _, want := range []string{
		"Tailscale now answers HTTPS for " + tailscaletest.Name + " on port 8443", note, webui.Text(webui.LangEN, webui.MsgTSFirstVisit),
		"Saved: base URL https://" + tailscaletest.Name + ":8443,", "Clone URL: https://" + tailscaletest.Name + ":8443/git/REPOSITORY.git",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("on printed %q, lacking %q", output, want)
		}
	}
	if strings.Count(output, webui.Text(webui.LangEN, webui.MsgTSFirstVisit)) != 1 {
		t.Errorf("on says more than once that the first visit waits: %q", output)
	}
	if len(fake.Writes()) != 1 || !fake.Endpoint(8443, "http://127.0.0.1:7654").Exact {
		t.Fatalf("writes=%q", fake.Writes())
	}
	output, err = runTailscale(t, "off", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if !strings.Contains(output, "Tailscale no longer answers HTTPS for "+tailscaletest.Name) {
		t.Fatalf("off printed %q", output)
	}
	if got := fake.State().Serve.TCP["443"]; got != (tailscale.TCPHandler{TCPForward: "127.0.0.1:22"}) {
		t.Fatalf("port 443 changed: %+v", got)
	}
}

// "on --https-port" uses the port named, and refuses a port that is not
// one, or that something else uses, without writing.
func TestTailscaleOnWithANamedPort(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	for _, bad := range []string{"0", "65536", "-1"} {
		if _, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path, "--https-port", bad); err == nil || !strings.Contains(err.Error(), "--https-port") {
			t.Errorf("--https-port %s: %v", bad, err)
		}
	}
	output, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path, "--https-port", "9443")
	noErr(t, err)
	if !strings.Contains(output, "https://"+tailscaletest.Name+":9443/") || strings.Contains(output, "leaves that as it is") {
		t.Fatalf("on printed %q", output)
	}
	report := tailscaleJSON(t, stateDir, fake.Path)
	if !report.On || report.Sharing == nil || report.Sharing.HTTPSPort != 9443 || report.URL != "https://"+tailscaletest.Name+":9443/" {
		t.Fatalf("report=%+v", report)
	}
	// Moving sharing that is on to another port needs turning it off first.
	_, err = runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path, "--https-port", "443")
	if err == nil || !strings.Contains(err.Error(), "owngit tailscale off") {
		t.Fatalf("moving: %v", err)
	}
	if len(fake.Writes()) != 1 || !fake.Endpoint(9443, "http://127.0.0.1:7654").Exact {
		t.Fatalf("writes=%q", fake.Writes())
	}
}

// Under --json every failure is a JSON error object with a code, and
// "status" on a state directory that does not exist yet reports the
// defaults as JSON.
func TestTailscaleCommandJSONErrors(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: everyPortTaken()})
	missing := filepath.Join(t.TempDir(), "missing")
	for _, test := range []struct {
		arguments []string
		code      string
	}{
		{[]string{"on", "--state-dir", stateDir}, server.TailscaleProblemTaken},
		{[]string{"off", "--state-dir", stateDir}, server.TailscaleProblemNotOn},
		{[]string{"on", "--state-dir", missing}, "state_missing"},
		{[]string{"on", "--state-dir", stateDir, "--https-port", "70000"}, "invalid_arguments"},
		{[]string{"status", "--state-dir", stateDir, "--json", "extra"}, "invalid_arguments"},
	} {
		arguments := append([]string{test.arguments[0], "--tailscale", fake.Path, "--json"}, test.arguments[1:]...)
		var printed strings.Builder
		_, err := runTailscale(t, arguments...)
		if err == nil || !writeStructuredCommandError(&printed, err) {
			t.Errorf("%q: err=%v is not a JSON error", test.arguments, err)
			continue
		}
		var envelope struct {
			OK    bool `json:"ok"`
			Error struct{ Code, Message string }
		}
		if json.Unmarshal([]byte(printed.String()), &envelope) != nil || envelope.OK || envelope.Error.Code != test.code || envelope.Error.Message == "" {
			t.Errorf("%q printed %q, want code %s", test.arguments, printed.String(), test.code)
		}
	}
	// An option that cannot be read, before --json or after it.
	for _, arguments := range [][]string{
		{"tailscale", "on", "--https-port", "abc", "--json"},
		{"tailscale", "on", "-json", "--home-network=maybe"},
		{"tailscale", "status", "--unknown", "--json=true"},
		{"tailscale", "off", "--state-dir"},
		{"network", "show", "--bogus", "--json"},
	} {
		run := runTailscale
		if arguments[0] == "network" {
			run = runNetwork
		}
		_, err := run(t, arguments[1:]...)
		var printed strings.Builder
		asJSON := slices.Contains(arguments, "--json") || slices.Contains(arguments, "-json") || slices.Contains(arguments, "--json=true")
		if err == nil || writeStructuredCommandError(&printed, err) != asJSON || asJSON && !strings.Contains(printed.String(), `"code":"invalid_arguments"`) {
			t.Errorf("%q: err=%v printed %q, want a JSON error %v", arguments, err, printed.String(), asJSON)
		}
	}
	if _, err := runTailscale(t, "on", "--https-port", "abc", "--json=false"); err == nil || writeStructuredCommandError(io.Discard, err) {
		t.Errorf("--json=false: err=%v is a JSON error", err)
	}

	output, err := runTailscale(t, "status", "--state-dir", missing, "--tailscale", fake.Path, "--json")
	noErr(t, err)
	var report struct {
		server.TailscaleReport
		StateMissing bool `json:"state_missing"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil || !report.StateMissing || report.On || report.Server != state.ServerNotRunning ||
		report.Listen != server.DefaultListenAddress || !report.PortsTaken {
		t.Fatalf("status on a missing state: %q %v", output, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("status created the state directory")
	}
}

// While the running server was started with --base-url, "status" and "on"
// name the option instead of claiming the HTTPS clone address.
func TestTailscaleCommandNamesABaseURLOption(t *testing.T) {
	stateDir := initializedState(t, false)
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "127.0.0.1:7822")
	noErr(t, err)
	const option = "http://gitbox.lan:7822"
	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--tailscale", fake.Path, "--base-url", option})
	defer instance.stop()
	note := fmt.Sprintf(webui.Text(webui.LangEN, webui.MsgTSBaseURLOption), option)
	output, err := runTailscale(t, "on", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if !strings.Contains(output, note) || strings.Contains(output, "Clone URL: https://") {
		t.Fatalf("on printed %q", output)
	}
	output, err = runTailscale(t, "status", "--state-dir", stateDir, "--tailscale", fake.Path)
	noErr(t, err)
	if !strings.Contains(output, note) || strings.Contains(output, "Clone URL: https://") {
		t.Fatalf("status printed %q", output)
	}
	if report := tailscaleJSON(t, stateDir, fake.Path); report.BaseURLOption != option {
		t.Fatalf("report=%+v", report)
	}
}

// A command that failed while Tailscale may already have the change says
// that OwnGit's settings were not saved, also when a Tailscale problem is
// explained in its place.
func TestTailscaleCommandSaysTheSettingsWereNotSaved(t *testing.T) {
	options := newTailscaleFlags("on")
	for _, cause := range []error{
		&server.TailscaleError{Problem: string(tailscale.KindFailed), Detail: "synthetic serve status failure"},
		fmt.Errorf("synthetic settings save failure"),
	} {
		err := options.failure("failed", fmt.Errorf("%w: %w", server.ErrTailscaleAhead, cause))
		if err == nil || !strings.HasPrefix(err.Error(), server.ErrTailscaleAhead.Error()+": ") {
			t.Errorf("failure for %v: %v", cause, err)
		}
	}
}
