package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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
	want := []netip.Addr{address("192.168.1.20"), address("10.0.0.5"), address("100.64.0.7"), address("fd00::5")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orderAddresses = %v, want %v", got, want)
	}
}

// A setup link never names a public address, even the one of the default
// route, because it would carry the capability and the first passwords
// over the Internet unencrypted.
func TestOrderAddressesLeavesOutPublicAddresses(t *testing.T) {
	address := netip.MustParseAddr
	candidates := []interfaceAddress{
		{"eth0", address("203.0.113.10")},
		{"eth0", address("2001:db8::10")},
		{"eth0", address("100.128.0.1")}, // just outside the tailnet range
	}
	if got := orderAddresses(address("203.0.113.10"), candidates); len(got) != 0 {
		t.Fatalf("orderAddresses kept public addresses: %v", got)
	}
}

// Without a private address, a computer without a screen shows the link on
// 127.0.0.1 with the SSH tunnel that reaches it, named after the current SSH
// session and the account that ran sudo.
func TestSSHTunnelCommand(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "198.51.100.7 51000 203.0.113.10 22")
	t.Setenv("SUDO_USER", "alice")
	if got, want := sshTunnelCommand("7654"), "ssh -L 7654:127.0.0.1:7654 alice@203.0.113.10"; got != want {
		t.Fatalf("sshTunnelCommand = %q, want %q", got, want)
	}
	t.Setenv("SSH_CONNECTION", "2001:db8::7 51000 2001:db8::10 22")
	if got, want := sshTunnelCommand("7654"), "ssh -L 7654:127.0.0.1:7654 alice@[2001:db8::10]"; got != want {
		t.Fatalf("sshTunnelCommand = %q, want %q", got, want)
	}
}

func TestShowSetupLinkWithATunnel(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	defer store.Close()
	previousProbe, previousLAN := probeEnvironment, lanAddresses
	t.Cleanup(func() { probeEnvironment, lanAddresses = previousProbe, previousLAN })
	probeEnvironment = func() service.Environment {
		return service.Environment{Getenv: func(string) string { return "" }, Linux: true}
	}
	lanAddresses = func() []netip.Addr { return nil }
	noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{Settings: state.NetworkSettings{Listen: "0.0.0.0:7654"}}))
	t.Setenv("SSH_CONNECTION", "198.51.100.7 51000 203.0.113.10 22")
	t.Setenv("SUDO_USER", "alice")
	var out bytes.Buffer
	_, err = issueAndShowSetupLink(context.Background(), store, "", &out, true)
	noErr(t, err)
	for _, want := range []string{"ssh -L 7654:127.0.0.1:7654 alice@203.0.113.10", "  http://127.0.0.1:7654/setup#"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("setup link output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "203.0.113.10:7654") {
		t.Fatalf("setup link names the public address:\n%s", out.String())
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
		bases, _, err := setupBases(context.Background(), store)
		probeEnvironment = previous
		noErr(t, err)
		want := []string{"http://gitbox.test:7950"}
		if !headless {
			want = append(want, "http://127.0.0.1:7950")
		}
		for _, address := range lan {
			want = append(want, "http://"+net.JoinHostPort(address.String(), "7950"))
		}
		if headless && len(lan) == 0 {
			want = append(want, "http://127.0.0.1:7950")
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
		_, err := actAsStateOwner(stateDir)
		noErr(t, err)
		noErr(t, os.Chmod(stateDir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
		if _, err := actAsStateOwner(stateDir); err == nil || !strings.Contains(err.Error(), "sudo") {
			t.Fatalf("actAsStateOwner on a closed directory: %v", err)
		}
		_, err = actAsStateOwner(filepath.Join(dir, "other"))
		noErr(t, err)
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

// The root steps reach the shell on standard input, and when they cannot
// run, the owner gets the whole script to read and paste instead of the
// path of a file that another account could change before root runs it.
func TestRootStepsLeaveNoFileBehind(t *testing.T) {
	previousLook, previousRun := lookPath, runScript
	t.Cleanup(func() { lookPath, runScript = previousLook, previousRun })
	const script = "useradd --system owngit\nsystemctl enable --now owngit.service\n"
	for _, test := range []struct {
		name      string
		sudo      bool
		runErr    error
		wantErr   bool
		wantPaste bool
	}{
		{"sudo works", true, nil, false, false},
		{"sudo fails", true, errors.New("exit status 1"), true, true},
		{"no sudo", false, nil, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			var ran []string
			lookPath = func(name string) (string, error) {
				if test.sudo {
					return "/usr/bin/" + name, nil
				}
				return "", errors.New("not found")
			}
			runScript = func(input, name string, args ...string) error {
				ran = append(ran, name+" "+strings.Join(args, " ")+"\n"+input)
				return test.runErr
			}
			var out bytes.Buffer
			host := &serviceHost{env: service.Environment{EUID: 1000}, account: &user.User{Username: "alice"}, out: &out}
			err := host.runAsRoot(script, "OwnGit needs root.")
			if (err != nil) != test.wantErr {
				t.Fatalf("runAsRoot error = %v", err)
			}
			if test.sudo && (len(ran) != 1 || ran[0] != "sudo /bin/sh -s\n"+script) {
				t.Fatalf("ran %q", ran)
			}
			paste := "sh <<'" + rootStepsEnd + "'\n" + script + rootStepsEnd + "\n"
			if got := strings.Contains(out.String(), paste); got != test.wantPaste {
				t.Fatalf("pasteable script shown=%v:\n%s", got, out.String())
			}
			if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
				t.Fatalf("root steps left %d files in the temporary folder", len(entries))
			}
			if strings.Contains(out.String(), ".sh") {
				t.Fatalf("output names a script file:\n%s", out.String())
			}
		})
	}
}

// Text that comes from state reaches root's terminal without control or
// direction characters.
func TestServicePrintfNeutralizesControlCharacters(t *testing.T) {
	var out bytes.Buffer
	host := &serviceHost{out: &out}
	host.printf("  Address: %s\n", "0.0.0.0:7654\x1b]0;owned\x07\r\u202eevil")
	if got, want := out.String(), "  Address: 0.0.0.0:7654?]0;owned???evil\n"; got != want {
		t.Fatalf("printf wrote %q, want %q", got, want)
	}
}

// Headless setup on the saved every-address listener is reported to the
// server only before setup and only for the address the headless rule
// saves.
func TestHeadlessListenInUse(t *testing.T) {
	for _, test := range []struct {
		headless bool
		network  serveNetwork
		want     string
	}{
		{true, serveNetwork{Listen: headlessListen, ListenSource: sourceSaved}, headlessListen},
		{false, serveNetwork{Listen: headlessListen, ListenSource: sourceSaved}, ""},
		{true, serveNetwork{Listen: headlessListen, ListenSource: sourceFlag}, ""},
		{true, serveNetwork{Listen: "0.0.0.0:7720", ListenSource: sourceSaved}, ""},
	} {
		if got := headlessListenInUse(test.headless, test.network); got != test.want {
			t.Errorf("headlessListenInUse(%v, %+v) = %q, want %q", test.headless, test.network, got, test.want)
		}
	}
}

// A service passes its install-time decision, which wins over what the
// computer looks like when the service starts: a desktop service that
// starts before the desktop session stays on this computer. Without the
// flag, as when the owner runs serve by hand, the environment decides.
func TestHeadlessChoicePrefersTheFlag(t *testing.T) {
	previous := probeEnvironment
	t.Cleanup(func() { probeEnvironment = previous })
	for _, test := range []struct {
		arguments []string
		screen    bool
		want      bool
	}{
		{nil, false, true},
		{nil, true, false},
		{[]string{"--headless=false"}, false, false},
		{[]string{"--headless=true"}, true, true},
		{[]string{"--headless"}, true, true},
	} {
		probeEnvironment = func() service.Environment {
			return service.Environment{Getenv: func(string) string { return "" }, Linux: true, GraphicalSession: test.screen}
		}
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		value := flags.Bool("headless", false, "")
		noErr(t, flags.Parse(test.arguments))
		if got := headlessChoice(flags, *value); got != test.want {
			t.Errorf("headlessChoice(%q) with screen=%v = %v, want %v", test.arguments, test.screen, got, test.want)
		}
	}
}

// A service that ends with an error while install waits for it is reported
// at once with that error; an error from before the wait is not.
func TestWaitHealthyStopsAtAServeError(t *testing.T) {
	stateDir := t.TempDir()
	recordServeError(stateDir, errors.New("an older failure"))
	old := time.Now().Add(-time.Minute)
	noErr(t, os.Chtimes(filepath.Join(stateDir, serveErrorFile), old, old))
	if _, err := waitHealthy(stateDir, 0); err == nil || errors.As(err, new(errServeFailed)) {
		t.Fatalf("an error from before the wait decided it: %v", err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		recordServeError(stateDir, errors.New("another program already uses port 7654"))
	}()
	started := time.Now()
	_, err := waitHealthy(stateDir, time.Minute)
	var failed errServeFailed
	if !errors.As(err, &failed) || failed.message != "another program already uses port 7654" || time.Since(started) > 10*time.Second {
		t.Fatalf("waitHealthy = %v after %s", err, time.Since(started))
	}
	clearServeError(stateDir)
	if _, found := serveErrorSince(stateDir, time.Time{}); found {
		t.Fatal("clearServeError left the error")
	}
}

// A port in use names the port and the way to move OwnGit, not "network
// reset", which would also give up a headless address.
func TestListenErrorForAPortInUse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a port in use with its own error number")
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	noErr(t, err)
	defer busy.Close()
	address := busy.Addr().String()
	_, listenErr := net.Listen("tcp", address)
	if listenErr == nil {
		t.Skip("the system allowed a second listener")
	}
	_, port, _ := net.SplitHostPort(address)
	message := serveNetwork{Listen: address, ListenSource: sourceSaved}.listenError(listenErr).Error()
	if !strings.Contains(message, "another program already uses port "+port) || !strings.Contains(message, "owngit network set --listen 127.0.0.1:PORT") || strings.Contains(message, "network reset") {
		t.Fatalf("listen error: %s", message)
	}
}

// A permission error of a command that root runs as the owngit account
// says what to do instead.
func TestAccountPathHint(t *testing.T) {
	denied := &fs.PathError{Op: "lstat", Path: "/root/bk", Err: fs.ErrPermission}
	if err := accountPathHint(denied); !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "runs as the owngit account") || !strings.Contains(err.Error(), "/var/lib/owngit/backup") {
		t.Fatalf("accountPathHint = %v", err)
	}
	other := errors.New("state is locked")
	if err := accountPathHint(other); err != other {
		t.Fatalf("accountPathHint changed %v", err)
	}
	if err := accountPathHint(errUsageShown); err != nil {
		t.Fatalf("accountPathHint(errUsageShown) = %v", err)
	}
}

// installFixture runs serviceHost.install with every system effect
// replaced: the unit found, the service manager, sudo and root's shell,
// and the wait for the started service.
type installFixture struct {
	host     *serviceHost
	out      bytes.Buffer
	commands []string // service manager commands
	scripts  []string // root scripts
}

func newInstallFixture(t *testing.T, env service.Environment, existing *service.Installed, managerDown bool) *installFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		// "owngit service" runs on Linux only (requireServicePlatform); its
		// units hold Unix paths, which are not absolute on Windows.
		t.Skip("systemd service installs are Linux-only")
	}
	fixture := &installFixture{}
	home := t.TempDir()
	fixture.host = &serviceHost{
		env: env, account: &user.User{Username: "alice", HomeDir: home}, group: "alice",
		executable: "/usr/local/bin/owngit", userConfigDir: filepath.Join(home, ".config"), out: &fixture.out,
	}
	previousFind, previousWait, previousRunner, previousLook, previousRun := findInstalled, waitForService, serviceRunner, lookPath, runScript
	t.Cleanup(func() {
		findInstalled, waitForService, serviceRunner, lookPath, runScript = previousFind, previousWait, previousRunner, previousLook, previousRun
	})
	findInstalled = func(string) (service.Installed, bool, error) {
		if existing == nil {
			return service.Installed{}, false, nil
		}
		return *existing, true, nil
	}
	waitForService = func(string, time.Duration) (string, error) { return "", errServeFailed{"stopped by the test"} }
	serviceRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		command := strings.Join(append([]string{name}, args...), " ")
		fixture.commands = append(fixture.commands, command)
		if managerDown && strings.HasPrefix(command, "systemctl --user") {
			return nil, errors.New("no user manager")
		}
		return nil, nil
	}
	lookPath = func(name string) (string, error) { return "", errors.New("no " + name + " in the test") }
	runScript = func(script, _ string, _ ...string) error {
		fixture.scripts = append(fixture.scripts, script)
		return nil
	}
	return fixture
}

var (
	desktopEnv = service.Environment{Getenv: func(name string) string { return map[string]string{"DISPLAY": ":0"}[name] }, EUID: 1000, Linux: true, GraphicalSession: true}
	sshEnv     = service.Environment{Getenv: func(name string) string { return map[string]string{"SSH_CONNECTION": "x"}[name] }, EUID: 1000, Linux: true, GraphicalSession: true}
)

// A desktop gets a user unit that says it is not headless, so a start
// before the desktop session stays on this computer.
func TestInstallOnADesktopWritesAnExplicitNotHeadlessUserUnit(t *testing.T) {
	fixture := newInstallFixture(t, desktopEnv, nil, false)
	if err := fixture.host.install(filepath.Join(t.TempDir(), "state"), nil); err == nil || !strings.Contains(fixture.out.String(), "stopped by the test") {
		t.Fatalf("install = %v\n%s", err, fixture.out.String())
	}
	installed, err := service.ReadUnit(service.UserUnitPath(fixture.host.userConfigDir))
	noErr(t, err)
	unit, _ := os.ReadFile(installed.UnitPath)
	if installed.Mode != service.ModeUser || installed.Headless || !strings.Contains(string(unit), `"--headless=false"`) {
		t.Fatalf("desktop unit:\n%s", unit)
	}
	if !slices.Contains(fixture.commands, "systemctl --user restart owngit.service") || len(fixture.scripts) != 0 {
		t.Fatalf("commands %q, root scripts %d", fixture.commands, len(fixture.scripts))
	}
}

// Without a user manager, a desktop gets a system unit through root, still
// not headless.
func TestInstallFallsBackToASystemUnitWithoutAUserManager(t *testing.T) {
	fixture := newInstallFixture(t, desktopEnv, nil, true)
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	_ = fixture.host.install(filepath.Join(t.TempDir(), "state"), nil)
	if !strings.Contains(fixture.out.String(), "installs a system service instead") || len(fixture.scripts) != 1 {
		t.Fatalf("no system fallback:\n%s", fixture.out.String())
	}
	script := fixture.scripts[0]
	if !strings.Contains(script, "User=alice") || !strings.Contains(script, `"--headless=false"`) {
		t.Fatalf("fallback script:\n%s", script)
	}
}

// A re-install keeps the mode, the state directory and the headless choice
// of the unit it finds, even from a desktop terminal.
func TestInstallKeepsTheFoundUnitsChoices(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "kept-state")
	existing := &service.Installed{Mode: service.ModeSystem, User: "alice", StateDir: stateDir, Headless: true, UnitPath: service.SystemUnitPath}
	fixture := newInstallFixture(t, desktopEnv, existing, false)
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	_ = fixture.host.install("", nil)
	if len(fixture.scripts) != 1 {
		t.Fatalf("root scripts %d:\n%s", len(fixture.scripts), fixture.out.String())
	}
	script := fixture.scripts[0]
	if !strings.Contains(script, `"--state-dir" "`+stateDir+`"`) || !strings.Contains(script, `"--headless=true"`) || !strings.Contains(script, "User=alice") {
		t.Fatalf("re-install script:\n%s", script)
	}
}

// Another account's system unit is refused, and an account unit found by a
// non-root user runs the install again through sudo.
func TestInstallRefusesOtherOwnersAndRerunsAccountUnitsWithSudo(t *testing.T) {
	bob := &service.Installed{Mode: service.ModeSystem, User: "bob", StateDir: "/home/example/.config/owngit", UnitPath: service.SystemUnitPath}
	fixture := newInstallFixture(t, sshEnv, bob, false)
	if err := fixture.host.install("", nil); err == nil || !strings.Contains(err.Error(), "runs as bob") || len(fixture.scripts) != 0 {
		t.Fatalf("install over bob's unit: %v", err)
	}
	account := &service.Installed{Mode: service.ModeAccount, User: service.AccountName, StateDir: service.AccountStateDir, UnitPath: service.SystemUnitPath}
	fixture = newInstallFixture(t, sshEnv, account, false)
	err := fixture.host.install("", nil)
	if err == nil || !strings.Contains(err.Error(), "run as root: /usr/local/bin/owngit service install") || len(fixture.scripts) != 0 {
		t.Fatalf("install over the account unit as a user: %v", err)
	}
}

// A new install over SSH is headless, as a system unit of the user.
func TestInstallOverSSHWritesAHeadlessSystemUnit(t *testing.T) {
	fixture := newInstallFixture(t, sshEnv, nil, false)
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	_ = fixture.host.install(filepath.Join(t.TempDir(), "state"), nil)
	if len(fixture.scripts) != 1 || !strings.Contains(fixture.scripts[0], `"--headless=true"`) || !strings.Contains(fixture.scripts[0], "User=alice") {
		t.Fatalf("SSH install:\n%s\n%q", fixture.out.String(), fixture.scripts)
	}
}

// Removing a user service keeps the data and says that lingering, which
// install turned on, stays on for the account's other user services.
func TestUninstallOfAUserServiceMentionsLingering(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	noErr(t, os.MkdirAll(stateDir, 0o700))
	unitPath := filepath.Join(t.TempDir(), "owngit.service")
	noErr(t, os.WriteFile(unitPath, []byte("unit"), 0o644))
	existing := &service.Installed{Mode: service.ModeUser, StateDir: stateDir, UnitPath: unitPath}
	fixture := newInstallFixture(t, desktopEnv, existing, false)
	noErr(t, fixture.host.uninstall())
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still there: %v", err)
	}
	if out := fixture.out.String(); !strings.Contains(out, "loginctl disable-linger") || !strings.Contains(out, stateDir) {
		t.Fatalf("uninstall output:\n%s", out)
	}
}

// The serve error file belongs to the service account, and root may read
// it after switching accounts: only a regular file counts, at most 4 KiB is
// read, and control characters never reach the terminal.
func TestServeErrorReadsOnlyABoundedRegularFile(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, serveErrorFile)
	noErr(t, os.WriteFile(path, []byte("port in use\x1b]0;owned\x07"+strings.Repeat("x", 10000)), 0o600))
	message, found := serveErrorSince(stateDir, time.Time{})
	if !found || len(message) > maxServeError || strings.ContainsAny(message, "\x1b\x07") || !strings.HasPrefix(message, "port in use?]0;owned?") {
		t.Fatalf("serve error %d bytes, found=%v: %.40q", len(message), found, message)
	}
	noErr(t, os.Rename(path, filepath.Join(stateDir, "elsewhere")))
	noErr(t, os.Symlink(filepath.Join(stateDir, "elsewhere"), path))
	if _, found := serveErrorSince(stateDir, time.Time{}); found {
		t.Fatal("followed a link")
	}
}

// A retryable state error of a starting serve does not end the wait: systemd
// starts it again (R2-1).
func TestWaitHealthyWaitsOutARetryableServeError(t *testing.T) {
	stateDir := t.TempDir()
	go func() {
		time.Sleep(100 * time.Millisecond)
		recordServeError(stateDir, fmt.Errorf("%w: owngit.sqlite-wal appeared during inspection", state.ErrInspectionUnstable))
	}()
	_, err := waitHealthy(stateDir, 1500*time.Millisecond)
	if err == nil || errors.As(err, new(errServeFailed)) {
		t.Fatalf("waitHealthy = %v, want the ordinary timeout", err)
	}
}

// A desktop unit run again over SSH stays not headless (R2-2): the choice
// made at the first install is kept, and only --headless changes it.
func TestInstallKeepsTheHeadlessChoiceUnlessTheFlagChangesIt(t *testing.T) {
	for _, test := range []struct {
		name string
		flag *bool
		want string
	}{
		{"over SSH, no flag", nil, `"--headless=false"`},
		{"--headless=true", ptr(true), `"--headless=true"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallFixture(t, sshEnv, nil, false)
			unitPath := service.UserUnitPath(fixture.host.userConfigDir)
			noErr(t, os.MkdirAll(filepath.Dir(unitPath), 0o755))
			stateDir := filepath.Join(t.TempDir(), "state")
			unit, err := service.RenderUnit(service.Plan{Mode: service.ModeUser, Executable: fixture.host.executable, StateDir: stateDir})
			noErr(t, err)
			noErr(t, os.WriteFile(unitPath, []byte(unit), 0o644))
			installed, err := service.ReadUnit(unitPath)
			noErr(t, err)
			findInstalled = func(string) (service.Installed, bool, error) { return installed, true, nil }
			_ = fixture.host.install("", test.flag)
			written, err := os.ReadFile(unitPath)
			noErr(t, err)
			if !strings.Contains(string(written), test.want) || len(fixture.scripts) != 0 {
				t.Fatalf("unit after the re-install:\n%s", written)
			}
		})
	}
}

func ptr[T any](value T) *T { return &value }
