package main

import (
	"context"
	"flag"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/state"
)

func TestEffectiveTrustedProxiesPrecedence(t *testing.T) {
	parse := func(arguments ...string) (*flag.FlagSet, stringList) {
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var values stringList
		flags.Var(&values, "trusted-proxy", "")
		noErr(t, flags.Parse(arguments))
		return flags, values
	}
	saved := []string{"192.0.2.10", "10.1.0.0/16"}
	for _, test := range []struct {
		name      string
		saved     []string
		arguments []string
		list      []string
		source    string
	}{
		{"nothing is trusted by default", nil, nil, []string{}, "default"},
		{"saved", saved, nil, []string{"10.1.0.0/16", "192.0.2.10"}, "saved"},
		{"a flag replaces the saved list", saved, []string{"--trusted-proxy", "::ffff:127.0.0.1", "--trusted-proxy", "127.0.0.1"}, []string{"127.0.0.1"}, "flag"},
		{"an empty flag trusts none for this run", saved, []string{"--trusted-proxy", ""}, []string{}, "flag"},
	} {
		flags, values := parse(test.arguments...)
		got, err := effectiveTrustedProxies(test.saved, flags, values)
		if err != nil || !reflect.DeepEqual(got.List, test.list) || got.Source != test.source || len(got.Prefixes) != len(test.list) {
			t.Errorf("%s: got %+v err=%v, want %v from %s", test.name, got, err, test.list, test.source)
		}
	}
	flags, values := parse()
	if _, err := effectiveTrustedProxies([]string{"0.0.0.0/0"}, flags, values); err == nil ||
		!strings.Contains(err.Error(), "--remove-trusted-proxy '0.0.0.0/0'") || !strings.Contains(err.Error(), "network reset --clear-trusted-proxies") {
		t.Errorf("invalid saved proxy: err=%v, want the command that removes it", err)
	}
	flags, values = parse("--trusted-proxy", "::/0")
	if _, err := effectiveTrustedProxies(nil, flags, values); err == nil || !strings.Contains(err.Error(), "--trusted-proxy") {
		t.Errorf("invalid flag: err=%v", err)
	}
	// A flag replaces an invalid saved list, so the owner can still start.
	flags, values = parse("--trusted-proxy", "")
	if _, err := effectiveTrustedProxies([]string{"0.0.0.0/0"}, flags, values); err != nil {
		t.Errorf("the flag did not override an invalid saved list: %v", err)
	}
}

func TestNetworkSetAndShowTrustedProxies(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	output, err := runNetwork(t, "set", "--state-dir", stateDir, "--base-url", "https://gitbox.test")
	noErr(t, err)
	if !strings.Contains(output, "no reverse proxy is trusted") {
		t.Fatalf("https base URL without a trusted proxy: %q", output)
	}
	output, err = runNetwork(t, "set", "--state-dir", stateDir, "--trusted-proxy", "192.0.2.10", "--trusted-proxy", "::ffff:192.0.2.10",
		"--trusted-proxy", "10.1.0.0/16", "--remove-trusted-proxy", "198.51.100.1")
	noErr(t, err)
	if strings.Contains(output, "no reverse proxy is trusted") || !strings.Contains(output, "198.51.100.1 was not a trusted proxy") {
		t.Fatalf("set output: %q", output)
	}
	report := networkJSON(t, stateDir)
	if want := []string{"10.1.0.0/16", "192.0.2.10"}; !reflect.DeepEqual(report.Saved.TrustedProxies, want) ||
		!reflect.DeepEqual(report.NextStart.TrustedProxies, want) || report.NextStart.TrustedProxiesSource != "saved" {
		t.Fatalf("saved %v, next start %v from %s", report.Saved.TrustedProxies, report.NextStart.TrustedProxies, report.NextStart.TrustedProxiesSource)
	}
	text, err := runNetwork(t, "show", "--state-dir", stateDir)
	noErr(t, err)
	if !strings.Contains(text, "Trusted proxies: 10.1.0.0/16, 192.0.2.10") {
		t.Fatalf("show text: %q", text)
	}

	for _, arguments := range [][]string{
		{"--trusted-proxy", "0.0.0.0/0"}, {"--trusted-proxy", "::/0"}, {"--trusted-proxy", "0.0.0.0/1"}, {"--trusted-proxy", "::/1"},
		{"--trusted-proxy", "0.0.0.0/32"}, {"--trusted-proxy", "::/128"}, {"--trusted-proxy", "proxy.internal"},
		{"--trusted-proxy", "192.168.1.5/24"}, {"--trusted-proxy", "0.0.0.0"},
		{"--trusted-proxy", "192.0.2.20", "--remove-trusted-proxy", "192.0.2.20"},
		{"--listen", "127.0.0.1:7721", "--trusted-proxy", "*"},
	} {
		if _, err := runNetwork(t, append([]string{"set", "--state-dir", stateDir}, arguments...)...); err == nil {
			t.Errorf("set %v succeeded", arguments)
		}
	}
	if after := networkJSON(t, stateDir); !reflect.DeepEqual(after.Saved, report.Saved) {
		t.Fatalf("a refused set changed %+v to %+v", report.Saved, after.Saved)
	}

	output, err = runNetwork(t, "reset", "--state-dir", stateDir)
	noErr(t, err)
	if !strings.Contains(output, "Trusted proxies kept: 10.1.0.0/16, 192.0.2.10") {
		t.Fatalf("reset output: %q", output)
	}
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--remove-trusted-proxy", "10.1.0.0/16", "--remove-trusted-proxy", "::ffff:192.0.2.10")
	noErr(t, err)
	if after := networkJSON(t, stateDir); len(after.Saved.TrustedProxies) != 0 || after.NextStart.TrustedProxiesSource != "default" {
		t.Fatalf("after removing every proxy: %+v", after)
	}
}

// preauthCookieSecure reports whether serve marks its first cookie Secure for
// a request that claims to come through an HTTPS proxy.
func preauthCookieSecure(t *testing.T, base string) bool {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, base+"/setup", nil)
	noErr(t, err)
	request.Header.Set("X-Forwarded-Proto", "https")
	response, err := http.DefaultClient.Do(request)
	noErr(t, err)
	response.Body.Close()
	for _, cookie := range response.Cookies() {
		if cookie.Name == "owngit_preauth" {
			return cookie.Secure
		}
	}
	t.Fatalf("GET /setup set no preauth cookie (status %d)", response.StatusCode)
	return false
}

// serve trusts the saved proxies, a flag replaces them for one run, and
// "network show" reports what the running server trusts.
func TestServeAppliesTrustedProxies(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--listen", "127.0.0.1:0"})
	secure := preauthCookieSecure(t, instance.url)
	report := networkJSON(t, stateDir)
	instance.stop()
	if secure {
		t.Fatal("a forwarded scheme was trusted with no trusted proxy")
	}
	if report.Running == nil || len(report.Running.TrustedProxies) != 0 || report.Running.TrustedProxiesSource != "default" {
		t.Fatalf("default run: %+v", report.Running)
	}

	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--trusted-proxy", "127.0.0.1")
	noErr(t, err)
	instance = startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--listen", "127.0.0.1:0"})
	secure = preauthCookieSecure(t, instance.url)
	report = networkJSON(t, stateDir)
	logs := instance.log()
	if secure {
		_, err = runNetwork(t, "set", "--state-dir", stateDir, "--trusted-proxy", "192.0.2.10")
		noErr(t, err)
	}
	changed := networkJSON(t, stateDir)
	instance.stop()
	if !secure {
		t.Fatal("the saved trusted proxy was not trusted")
	}
	if report.Running == nil || !reflect.DeepEqual(report.Running.TrustedProxies, []string{"127.0.0.1"}) || report.Running.TrustedProxiesSource != "saved" || report.RestartNeeded {
		t.Fatalf("saved run: %+v", report.Running)
	}
	if !strings.Contains(logs, "trusting forwarded headers from reverse proxies at 127.0.0.1") {
		t.Fatalf("serve did not log the trusted proxies: %s", logs)
	}
	if !changed.RestartNeeded {
		t.Fatal("a newly saved proxy did not ask for a restart")
	}

	instance = startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--listen", "127.0.0.1:0", "--trusted-proxy", ""})
	secure = preauthCookieSecure(t, instance.url)
	report = networkJSON(t, stateDir)
	text, err := runNetwork(t, "show", "--state-dir", stateDir)
	instance.stop()
	noErr(t, err)
	if secure {
		t.Fatal("an empty --trusted-proxy flag still trusted the saved proxy")
	}
	if report.Running == nil || len(report.Running.TrustedProxies) != 0 || report.Running.TrustedProxiesSource != "flag" || report.RestartNeeded {
		t.Fatalf("flag run: %+v restart=%v", report.Running, report.RestartNeeded)
	}
	if !strings.Contains(text, "Trusted proxies: none (flag)") || !strings.Contains(text, "--trusted-proxy, which overrides") {
		t.Fatalf("show text: %q", text)
	}
}

// A saved proxy that no longer validates, for example one saved before the
// rules became stricter, stops serve with a message whose commands remove it.
func TestInvalidSavedTrustedProxiesCanBeRemoved(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{AddProxies: []string{"0.0.0.0/0", "0.0.0.0/1", "10.1.0.0/16"}}))
	noErr(t, store.Close())
	startErr := func() error {
		return serveWithContext(context.Background(), []string{"--state-dir", stateDir, "--no-open", "--listen", "127.0.0.1:0"}, func(string) error { return nil }, func(string, ...any) {})
	}
	err = startErr()
	if err == nil || !strings.Contains(err.Error(), "--remove-trusted-proxy '0.0.0.0/0'") {
		t.Fatalf("serve with an invalid saved proxy: err=%v", err)
	}
	// The named removal works although the value does not validate.
	output, err := runNetwork(t, "set", "--state-dir", stateDir, "--remove-trusted-proxy", "0.0.0.0/0")
	noErr(t, err)
	if strings.Contains(output, "was not a trusted proxy") {
		t.Fatalf("removal output: %q", output)
	}
	if report := networkJSON(t, stateDir); !reflect.DeepEqual(report.Saved.TrustedProxies, []string{"0.0.0.0/1", "10.1.0.0/16"}) {
		t.Fatalf("after removing one invalid value: %v", report.Saved.TrustedProxies)
	}
	if err := startErr(); err == nil || !strings.Contains(err.Error(), "network reset --clear-trusted-proxies") {
		t.Fatalf("serve with the remaining invalid proxy: err=%v", err)
	}
	// reset --clear-trusted-proxies removes the rest, valid or not.
	output, err = runNetwork(t, "reset", "--state-dir", stateDir, "--clear-trusted-proxies")
	noErr(t, err)
	if !strings.Contains(output, "Trusted proxies removed") {
		t.Fatalf("reset output: %q", output)
	}
	if report := networkJSON(t, stateDir); len(report.Saved.TrustedProxies) != 0 {
		t.Fatalf("after reset --clear-trusted-proxies: %v", report.Saved.TrustedProxies)
	}
	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--listen", "127.0.0.1:0"})
	instance.stop()
}
