package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/service"
	"owngit/internal/state"
)

func TestStateDirArgument(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		want      string
	}{
		{[]string{"show", "--state-dir", "/a"}, "/a"},
		{[]string{"--state-dir=/b", "--json"}, "/b"},
		{[]string{"-state-dir", "/c"}, "/c"},
		{[]string{"-state-dir=/d"}, "/d"},
		{[]string{"--no-open"}, ""},
		{[]string{"--", "--state-dir", "/e"}, ""},
		{[]string{"--state-dir"}, ""},
	} {
		if got := stateDirArgument(test.arguments); got != test.want {
			t.Errorf("stateDirArgument(%q) = %q, want %q", test.arguments, got, test.want)
		}
	}
}

func TestOrderAddressesPutsTheLikelyAddressFirst(t *testing.T) {
	address := netip.MustParseAddr
	candidates := []interfaceAddress{
		{"eth1", address("2001:db8::5")},
		{"docker0", address("172.17.0.1")},
		{"tailscale0", address("100.64.0.7")},
		{"eth1", address("fd00::5")},
		{"eth1", address("fe80::1")},
		{"eth0", address("10.0.0.5")},
		{"eth2", address("192.168.1.20")},
		{"veth12", address("172.18.0.1")},
		{"eth2", address("192.168.1.20")},
	}
	got := orderAddresses(address("192.168.1.20"), candidates)
	want := []netip.Addr{address("192.168.1.20"), address("10.0.0.5"), address("100.64.0.7"), address("fd00::5"), address("2001:db8::5")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orderAddresses = %v, want %v", got, want)
	}
}

func TestLocalTargetReachesEveryAddressListenersThroughLoopback(t *testing.T) {
	for address, want := range map[string]string{
		"0.0.0.0:7654":      "127.0.0.1:7654",
		":7654":             "127.0.0.1:7654",
		"[::]:7654":         "[::1]:7654",
		"127.0.0.1:7950":    "127.0.0.1:7950",
		"192.168.1.20:7654": "192.168.1.20:7654",
	} {
		if got, err := localTarget(address); got != want || err != nil {
			t.Errorf("localTarget(%q) = %q, %v; want %q", address, got, err, want)
		}
	}
}

var setupCapability = regexp.MustCompile(`const capability = "([^"]+)";`)

func setupFileCapability(t *testing.T, stateDir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(stateDir, "owner-setup.html"))
	noErr(t, err)
	match := setupCapability.FindSubmatch(content)
	if match == nil {
		t.Fatalf("no capability in the setup file")
	}
	return string(match[1])
}

// The link itself reaches only a terminal. Through a pipe, as with a log
// file, the journal or "docker logs", the output names the setup file.
func TestSetupLinkShowsTheLinkOnlyOnATerminal(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	address := freeLoopbackAddress(t)
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", address)
	noErr(t, err)

	// The real terminal check: captureStdout makes standard output a pipe.
	output, err := captureStdout(func() error { return run([]string{"setup-link", "--state-dir", stateDir, "--no-open"}) })
	noErr(t, err)
	capability := setupFileCapability(t, stateDir)
	if strings.Contains(output, capability) || strings.Contains(output, "#") || !strings.Contains(output, filepath.Join(stateDir, "owner-setup.html")) {
		t.Fatalf("setup-link through a pipe printed %q", output)
	}

	previous := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdoutIsTerminal = previous })
	output, err = captureStdout(func() error { return run([]string{"setup-link", "--state-dir", stateDir, "--no-open"}) })
	noErr(t, err)
	fresh := setupFileCapability(t, stateDir)
	if fresh == capability {
		t.Fatal("setup-link did not replace the earlier link")
	}
	// The saved listen address names this computer, so it is the one link.
	if want := "  http://" + address + "/setup#" + fresh + "\n"; !strings.Contains(output, want) || strings.Count(output, "/setup#") != 1 {
		t.Fatalf("setup-link on a terminal printed %q, want the line %q", output, want)
	}
	output, err = captureStdout(func() error {
		return run([]string{"setup-link", "--state-dir", stateDir, "--no-open", "--base-url", "http://gitbox.test:7654"})
	})
	noErr(t, err)
	if want := "  http://gitbox.test:7654/setup#" + setupFileCapability(t, stateDir) + "\n"; !strings.Contains(output, want) {
		t.Fatalf("setup-link --base-url printed %q, want %q", output, want)
	}
}

// Listening on every address shows the link for this computer's
// addresses; a headless computer leaves out 127.0.0.1, which only a
// browser on this computer could open.
func TestSetupBasesForEveryAddressListener(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "0.0.0.0:7950", "--base-url", "http://gitbox.test:7950")
	noErr(t, err)
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	defer store.Close()
	lan := lanAddresses()
	for _, headless := range []bool{false, true} {
		previous := probeEnvironment
		probeEnvironment = func() service.Environment {
			return service.Environment{Getenv: func(string) string { return "" }, Linux: true, GraphicalSession: !headless}
		}
		bases, err := setupBases(context.Background(), store)
		probeEnvironment = previous
		noErr(t, err)
		want := []string{"http://gitbox.test:7950"}
		if !headless {
			want = append(want, "http://127.0.0.1:7950")
		}
		for _, address := range lan {
			want = append(want, "http://"+net.JoinHostPort(address.String(), "7950"))
		}
		if !reflect.DeepEqual(bases, want) {
			t.Errorf("headless=%v: setupBases = %q, want %q", headless, bases, want)
		}
	}
}

// Without a screen the first start listens on every address and saves
// that; a saved address, a flag, or finished setup are left alone.
func TestServeSavesTheHeadlessListenAddressOnlyOnTheFirstStart(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	// The test listens on a free loopback port instead of 0.0.0.0:7654.
	address := freeLoopbackAddress(t)
	previousListen, previousProbe := headlessListen, probeEnvironment
	headlessListen = address
	t.Cleanup(func() { headlessListen, probeEnvironment = previousListen, previousProbe })

	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--headless"})
	logs := instance.log()
	instance.stop()
	if instance.url != "http://"+address || !strings.Contains(logs, "no screen for setup") {
		t.Fatalf("headless first start listened on %s, logs:\n%s", instance.url, logs)
	}
	if report := networkJSON(t, stateDir); report.Saved.Listen != address {
		t.Fatalf("saved listen = %q, want %q", report.Saved.Listen, address)
	}

	// The same computer detected as headless, but with the address saved
	// by the owner: nothing changes.
	probeEnvironment = func() service.Environment {
		return service.Environment{Getenv: func(string) string { return "" }, Linux: true}
	}
	owned := freeLoopbackAddress(t)
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", owned)
	noErr(t, err)
	instance = startServedWith(t, []string{"--state-dir", stateDir, "--no-open"})
	logs = instance.log()
	instance.stop()
	if instance.url != "http://"+owned || strings.Contains(logs, "no screen for setup") {
		t.Fatalf("with a saved address the start listened on %s, logs:\n%s", instance.url, logs)
	}

	// A --listen flag wins and saves nothing.
	other := filepath.Join(t.TempDir(), "state")
	instance = startServedWith(t, []string{"--state-dir", other, "--no-open", "--listen", "127.0.0.1:0"})
	instance.stop()
	if report := networkJSON(t, other); report.Saved.Listen != "" {
		t.Fatalf("a --listen run saved %q", report.Saved.Listen)
	}

	// Detection alone (no flag) saves it too, but not once setup is done.
	store, err := state.Open(context.Background(), other)
	noErr(t, err)
	saved, err := applyHeadlessListen(context.Background(), store)
	noErr(t, err)
	if !saved {
		t.Fatal("applyHeadlessListen did not save for a new installation")
	}
	noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{}))
	root := t.TempDir()
	noErr(t, store.CompleteSetup(context.Background(), root, "open", "", "hash", true))
	saved, err = applyHeadlessListen(context.Background(), store)
	noErr(t, err)
	settings, _ := store.NetworkSettings(context.Background())
	noErr(t, store.Close())
	if saved || settings.Listen != "" {
		t.Fatalf("after setup applyHeadlessListen saved=%v listen=%q", saved, settings.Listen)
	}
}

func TestHealthCommand(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	address := freeLoopbackAddress(t)
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", address)
	noErr(t, err)
	if _, err := captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) }); err == nil {
		t.Fatal("health succeeded with no server running")
	}
	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open"})
	output, err := captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) })
	waited, waitErr := waitHealthy(stateDir, serviceStartTimeout)
	instance.stop()
	noErr(t, err)
	noErr(t, waitErr)
	if want := "OwnGit answers at http://" + address + "\n"; output != want || waited != address {
		t.Fatalf("health printed %q and waitHealthy returned %q, want %q", output, waited, want)
	}
	if _, err := captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) }); err == nil {
		t.Fatal("health succeeded after the server stopped")
	}
	// health never creates a state directory.
	missing := filepath.Join(t.TempDir(), "missing")
	_, _ = captureStdout(func() error { return run([]string{"health", "--state-dir", missing}) })
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("health created %s: %v", missing, err)
	}
}

func TestServiceCommandsAreRefusedOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux has the systemd backend")
	}
	for _, action := range []string{"install", "uninstall", "status", "start", "stop", "restart"} {
		_, err := captureStdout(func() error { return run([]string{"service", action}) })
		if !errors.Is(err, service.ErrUnsupported) {
			t.Errorf("service %s off Linux: %v, want ErrUnsupported", action, err)
		}
	}
}

// Root, the owngit account and members of its group find the account
// service's state through the pointer file; nobody else follows it.
func TestDefaultStateDirFollowsThePointer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the pointer file exists on Linux only")
	}
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	noErr(t, os.Mkdir(stateDir, 0o700))
	previousFile, previousApplies := pointerFile, pointerApplies
	t.Cleanup(func() { pointerFile, pointerApplies = previousFile, previousApplies })
	pointerFile = filepath.Join(dir, "state-dir")
	noErr(t, os.WriteFile(pointerFile, []byte(stateDir+"\n"), 0o644))

	pointerApplies = func() bool { return false }
	if got := defaultStateDir(); got == stateDir {
		t.Fatal("an unrelated account followed the pointer")
	}
	pointerApplies = func() bool { return true }
	if got := defaultStateDir(); got != stateDir {
		t.Fatalf("defaultStateDir = %q, want the pointer %q", got, stateDir)
	}
	// A group member who cannot open the directory is told to use sudo.
	if os.Geteuid() != 0 {
		noErr(t, actAsStateOwner(stateDir))
		noErr(t, os.Chmod(stateDir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
		if err := actAsStateOwner(stateDir); err == nil || !strings.Contains(err.Error(), "sudo") {
			t.Fatalf("actAsStateOwner on a closed directory: %v", err)
		}
		noErr(t, actAsStateOwner(filepath.Join(dir, "other")))
	}
}

func TestServiceUsageListsEveryAction(t *testing.T) {
	var usage bytes.Buffer
	printServiceUsage(&usage)
	for _, action := range []string{"install", "uninstall", "status", "start", "stop", "restart"} {
		if !strings.Contains(usage.String(), action) {
			t.Errorf("service usage lacks %s: %q", action, usage.String())
		}
	}
}
