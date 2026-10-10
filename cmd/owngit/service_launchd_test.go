//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/service"
	"owngit/internal/state"
	"owngit/internal/version"
)

// testLaunchAgentHost is a macOS service backend for a home folder in a
// temporary directory. It runs on any platform, since it only writes files
// and calls launchctl through serviceRunner.
func testLaunchAgentHost(t *testing.T, env service.Environment, homebrew string) (*launchAgentHost, *bytes.Buffer) {
	t.Helper()
	previousApplications := applicationsFolder
	applicationsFolder = t.TempDir()
	t.Cleanup(func() { applicationsFolder = previousApplications })
	home := t.TempDir()
	out := &bytes.Buffer{}
	executable := filepath.Join(home, "bin", "owngit")
	if homebrew != "" {
		executable = filepath.Join(homebrew, "Cellar", "owngit", "1.1.1", "bin", "owngit")
	}
	host := &serviceHost{
		env:     env,
		account: &user.User{Username: "owner", Uid: "501", HomeDir: home}, group: "staff",
		executable: executable, homebrew: homebrew, out: out,
	}
	return &launchAgentHost{
		serviceHost: host, uid: 501,
		agentPath: service.LaunchAgentPath(home), agentExecutable: service.AgentExecutable("", executable),
	}, out
}

// fakeLaunchctl records the calls of serviceRunner. print succeeds for the
// targets in loaded, and bootstrap, the step that would start a server,
// fails after saving the agent it was given in bootstrapped.
type fakeLaunchctl struct {
	calls  []string
	loaded []string
	// iconRunning is an icon process for pgrep; pkill ends it only when
	// iconQuits.
	iconRunning, iconQuits bool
	iconCommands           []string
	bootstrapped           string
}

func recordLaunchctl(t *testing.T, loaded ...string) *fakeLaunchctl {
	t.Helper()
	previous := serviceRunner
	t.Cleanup(func() { serviceRunner = previous })
	fake := &fakeLaunchctl{loaded: loaded}
	serviceRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		fake.calls = append(fake.calls, name+" "+strings.Join(args, " "))
		switch {
		case name == "/usr/bin/open", filepath.Base(name) == "OwnGitLauncher":
			return nil, nil
		case name == "/usr/bin/pkill":
			if fake.iconQuits {
				fake.iconRunning = false
				fake.iconCommands = slices.DeleteFunc(fake.iconCommands, regexp.MustCompile(args[len(args)-1]).MatchString)
			}
			return nil, nil
		case name == "/usr/bin/pgrep":
			if fake.iconRunning && !strings.Contains(args[len(args)-1], "AppTranslocation/") || slices.ContainsFunc(fake.iconCommands, regexp.MustCompile(args[len(args)-1]).MatchString) {
				return []byte("4242\n"), nil
			}
			return nil, nil
		case len(args) == 0:
		case args[0] == "enable":
			return nil, nil
		case args[0] == "print" && slices.Contains(fake.loaded, args[len(args)-1]):
			return []byte("\tstate = running\n"), nil
		case args[0] == "bootout" && slices.Contains(fake.loaded, args[len(args)-1]):
			fake.loaded = slices.DeleteFunc(fake.loaded, func(target string) bool { return target == args[len(args)-1] })
			return nil, nil
		case args[0] == "bootstrap":
			agent, _ := os.ReadFile(args[len(args)-1])
			fake.bootstrapped = string(agent)
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
		}
		return []byte("not loaded"), errors.New("exit status 113")
	}
	return fake
}

// fakeBrewPrefix is a Homebrew prefix whose brew records its arguments in
// bin/brew.calls and fails except for "services info", so that no test can
// run the real brew.
func fakeBrewPrefix(t *testing.T) string {
	t.Helper()
	prefix := t.TempDir()
	noErr(t, os.MkdirAll(filepath.Join(prefix, "bin"), 0o755))
	noErr(t, os.WriteFile(filepath.Join(prefix, "bin", "brew"), []byte("#!/bin/sh\necho \"brew $*\" >>\"$0.calls\"\n[ \"$2\" = info ]\n"), 0o755))
	return prefix
}

func macDesktop() service.Environment {
	return service.Environment{Getenv: func(string) string { return "" }, EUID: 501, Darwin: true, GraphicalSession: true}
}

func macWithoutDesktop() service.Environment {
	return service.Environment{Getenv: func(string) string { return "" }, EUID: 501, Darwin: true}
}

func writeJob(t *testing.T, path, label string, arguments ...string) {
	t.Helper()
	var words strings.Builder
	for _, argument := range arguments {
		words.WriteString("<string>" + argument + "</string>")
	}
	noErr(t, os.MkdirAll(filepath.Dir(path), 0o755))
	noErr(t, os.WriteFile(path, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>Label</key><string>`+label+`</string>
<key>ProgramArguments</key><array>`+words.String()+`</array><key>KeepAlive</key><true/></dict></plist>
`), 0o644))
}

func TestLaunchAgentStatusRequiresTheRunsProof(t *testing.T) {
	host, out := testLaunchAgentHost(t, macDesktop(), "")
	recordLaunchctl(t, "gui/501/"+service.LaunchAgentLabel)
	health := useFakeHealth(t)
	health.answering = true
	dir := publishedHealthRun(t, health, state.RunningNetwork{Listen: "127.0.0.1:18966", Address: "127.0.0.1:18966"})
	agent, err := service.RenderLaunchAgent(host.agentPlan(dir, nil, service.Installed{}, false))
	noErr(t, err)
	noErr(t, os.MkdirAll(filepath.Dir(host.agentPath), 0o755))
	noErr(t, os.WriteFile(host.agentPath, []byte(agent), 0o644))
	noErr(t, host.status())
	if !strings.Contains(out.String(), "OwnGit is running") || !strings.Contains(out.String(), "http://127.0.0.1:18966") {
		t.Fatalf("proven status: %s", out.String())
	}
	health.key = ""
	out.Reset()
	noErr(t, host.status())
	if strings.Contains(out.String(), "OwnGit is running") || !strings.Contains(out.String(), "another program answers") {
		t.Fatalf("unproven status: %s", out.String())
	}
}

// A loaded hand-made job that runs "owngit serve" is left alone and no
// second server starts.
func TestLaunchAgentInstallLeavesAnotherServeJobAlone(t *testing.T) {
	fake := recordLaunchctl(t, "user/501/com.example.owngit")
	host, out := testLaunchAgentHost(t, macDesktop(), "")
	other := filepath.Join(filepath.Dir(host.agentPath), "com.example.owngit.plist")
	writeJob(t, other, "com.example.owngit", "/usr/local/bin/owngit", "serve", "--state-dir", "/Users/example/owngit")

	noErr(t, host.install(filepath.Join(t.TempDir(), "state"), nil))
	if got := out.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "com.example.owngit") || !strings.Contains(got, other) || !strings.Contains(got, "no second server") {
		t.Fatalf("install printed %q", got)
	}
	if _, err := os.Stat(host.agentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install wrote an agent next to the other job: %v", err)
	}
	for _, call := range fake.calls {
		if !strings.Contains(call, "plutil") && !strings.Contains(call, " print ") {
			t.Fatalf("install ran %q", call)
		}
	}
	if written, err := os.ReadFile(other); err != nil || !strings.Contains(string(written), "com.example.owngit") {
		t.Fatalf("the other job changed: %v", err)
	}

	out.Reset()
	noErr(t, host.status())
	if !strings.Contains(out.String(), "runs from the launchd job com.example.owngit") {
		t.Fatalf("status printed %q", out.String())
	}
	out.Reset()
	noErr(t, host.uninstall())
	if !strings.Contains(out.String(), "not installed") || !strings.Contains(out.String(), "com.example.owngit") {
		t.Fatalf("uninstall printed %q", out.String())
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("uninstall touched the other job: %v", err)
	}
}

// A job that is only a file is not called running: install stops with the
// file and the next step, and status says it is not loaded.
func TestLaunchAgentInstallNamesAnUnloadedJob(t *testing.T) {
	recordLaunchctl(t)
	host, out := testLaunchAgentHost(t, macDesktop(), "")
	other := filepath.Join(filepath.Dir(host.agentPath), "com.example.owngit.plist")
	writeJob(t, other, "com.example.owngit", "/usr/local/bin/owngit", "serve")

	err := host.install("", nil)
	if err == nil || !strings.Contains(err.Error(), "not loaded") || !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), "remove that file") {
		t.Fatalf("install next to an unloaded job: %v", err)
	}
	if strings.Contains(out.String(), "runs from") {
		t.Fatalf("install called an unloaded job running: %q", out.String())
	}
	if _, err := os.Stat(host.agentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install wrote an agent: %v", err)
	}
	noErr(t, host.status())
	if got := out.String(); !strings.Contains(got, "is not loaded now") || strings.Contains(got, "runs from") {
		t.Fatalf("status printed %q", got)
	}
}

// The Homebrew service counts as another server for any other binary. For
// the Homebrew binary it is its own service, and that binary uses the
// default state directory only.
func TestLaunchAgentInstallAndTheHomebrewJob(t *testing.T) {
	for _, label := range service.HomebrewLabels {
		recordLaunchctl(t, "gui/501/"+label)
		host, out := testLaunchAgentHost(t, macDesktop(), "")
		brewJob := filepath.Join(filepath.Dir(host.agentPath), label+".plist")
		writeJob(t, brewJob, label, "/opt/homebrew/opt/owngit/bin/owngit", "serve", "-no-open")
		noErr(t, host.install("", nil))
		if got := out.String(); !strings.Contains(got, "the Homebrew service") || !strings.Contains(got, brewJob) {
			t.Fatalf("install next to the Homebrew service %s printed %q", label, got)
		}

		// Loaded at the desktop, or left unloaded over SSH: either way it
		// is this binary's own service.
		for _, env := range []service.Environment{macDesktop(), macWithoutDesktop()} {
			if !env.GraphicalSession {
				recordLaunchctl(t)
			}
			brewed, out := testLaunchAgentHost(t, env, fakeBrewPrefix(t))
			writeJob(t, filepath.Join(filepath.Dir(brewed.agentPath), label+".plist"), label, "/opt/homebrew/opt/owngit/bin/owngit", "serve")
			err := brewed.install(filepath.Join(t.TempDir(), "state"), nil)
			if err == nil || !strings.Contains(err.Error(), "uses the default state directory") {
				t.Fatalf("Homebrew binary with --state-dir next to %s: %v (printed %q)", label, err, out.String())
			}
		}
	}
}

// Over SSH without a desktop login, Homebrew's service cannot start, so the
// OwnGit agent runs Homebrew's binary through its opt link. A failed load
// leaves no agent and saves nothing.
func TestLaunchAgentInstallOfAHomebrewBinaryWithoutADesktop(t *testing.T) {
	fake := recordLaunchctl(t)
	prefix := fakeBrewPrefix(t)
	host, out := testLaunchAgentHost(t, macWithoutDesktop(), prefix)
	formula := filepath.Join(prefix, "Cellar", "owngit", "1.1.1")
	writeIconApp(t, filepath.Join(formula, service.AppName), "app.owngit.OwnGit", version.Version, true)
	noErr(t, os.MkdirAll(filepath.Join(prefix, "opt"), 0o755))
	noErr(t, os.Symlink(formula, filepath.Join(prefix, "opt", "owngit")))
	err := host.install("", nil)
	if !errors.Is(err, service.ErrNotLoaded) {
		t.Fatalf("install: %v (calls %q)", err, fake.calls)
	}
	if calls, err := os.ReadFile(filepath.Join(prefix, "bin", "brew.calls")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install ran brew without a desktop login: %q", calls)
	}
	for _, want := range []string{"<string>" + filepath.Join(prefix, "opt", "owngit", "bin", "owngit") + "</string>", "<string>--headless=true</string>", "<string>" + mustAbs(defaultStateDir()) + "</string>"} {
		if !strings.Contains(fake.bootstrapped, want) {
			t.Fatalf("the agent lacks %s:\n%s", want, fake.bootstrapped)
		}
	}
	if strings.Contains(fake.bootstrapped, "AssociatedBundleIdentifiers") {
		t.Fatalf("the formula's Cellar app was registered: %s", fake.bootstrapped)
	}
	if !strings.Contains(out.String(), "Homebrew's service starts only in a desktop login") {
		t.Fatalf("install printed %q", out.String())
	}
	if _, err := os.Stat(host.agentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed install left the agent: %v", err)
	}
}

// In a desktop login a Homebrew binary goes to brew services, which takes
// over from an OwnGit agent of an earlier install over SSH. When brew
// services fails, the agent runs again and stays.
func TestLaunchAgentHandsAHomebrewInstallToBrewServices(t *testing.T) {
	fake := recordLaunchctl(t, "gui/501/"+service.LaunchAgentLabel)
	prefix := fakeBrewPrefix(t)
	host, out := testLaunchAgentHost(t, macDesktop(), prefix)
	noErr(t, host.status())
	if !strings.Contains(out.String(), "State:   "+mustAbs(defaultStateDir())) {
		t.Fatalf("Homebrew status printed %q", out.String())
	}
	if err := host.install("", new(true)); err == nil || !strings.Contains(err.Error(), "network set --listen") {
		t.Fatalf("install --headless for brew services: %v", err)
	}
	headless := host.agentPlan(mustAbs(defaultStateDir()), nil, service.Installed{}, false)
	headless.Headless = true
	agent, err := service.RenderLaunchAgent(headless)
	noErr(t, err)
	noErr(t, os.MkdirAll(filepath.Dir(host.agentPath), 0o755))
	noErr(t, os.WriteFile(host.agentPath, []byte(agent), 0o644))

	err = host.install("", nil)
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("install with a failing brew: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(prefix, "bin", "brew.calls"))
	if string(calls) != "brew services info owngit\nbrew services restart owngit\n" {
		t.Fatalf("brew calls: %q", calls)
	}
	if !slices.Contains(fake.calls, "/bin/launchctl bootout gui/501/"+service.LaunchAgentLabel) || !strings.Contains(strings.Join(fake.calls, "\n"), "bootstrap gui/501 "+host.agentPath) {
		t.Fatalf("launchctl calls: %q", fake.calls)
	}
	if written, err := os.ReadFile(host.agentPath); err != nil || string(written) != agent {
		t.Fatalf("the earlier agent changed: %v", err)
	}
}

func TestLaunchAgentInstallRefusals(t *testing.T) {
	recordLaunchctl(t)
	root := macDesktop()
	root.EUID = 0
	host, _ := testLaunchAgentHost(t, root, "")
	if err := host.install("", nil); err == nil || !strings.Contains(err.Error(), "without sudo") {
		t.Fatalf("install as root: %v", err)
	}

	host, _ = testLaunchAgentHost(t, macDesktop(), "")
	writeJob(t, host.agentPath, service.LaunchAgentLabel, "/usr/local/bin/owngit", "serve")
	for name, action := range map[string]func() error{
		"install": func() error { return host.install("", nil) }, "uninstall": host.uninstall,
		"status": host.status, "restart": func() error { return host.control("restart") },
	} {
		if err := action(); err == nil || !strings.Contains(err.Error(), "leaves it alone") {
			t.Errorf("%s with a foreign file at the agent path: %v", name, err)
		}
	}

	// A binary or a PATH folder that another account could change.
	pathCheck := requireProtectedPath
	t.Cleanup(func() { requireProtectedPath = pathCheck })
	requireProtectedPath = func(path string) error {
		if path == host.agentExecutable || path == "/shared" {
			return errors.New("another account can change it")
		}
		return nil
	}
	t.Setenv("PATH", "/usr/bin:/shared")
	if plan := host.agentPlan("", nil, service.Installed{}, false); plan.Path != "/usr/bin" {
		t.Errorf("the agent PATH is %q", plan.Path)
	}
	noErr(t, os.Remove(host.agentPath))
	if err := host.install("", nil); err == nil || !strings.Contains(err.Error(), "only you or root can change") {
		t.Fatalf("install of a binary that others can change: %v", err)
	}
	if runtime.GOOS != "windows" {
		requireProtectedPath = state.RequireProtectedPath
		sticky := filepath.Join(t.TempDir(), "sticky")
		noErr(t, os.Mkdir(sticky, 0o755))
		noErr(t, os.Chmod(sticky, 0o777|os.ModeSticky))
		t.Setenv("PATH", sticky+string(os.PathListSeparator)+"/usr/bin")
		if path := host.agentPlan("", nil, service.Installed{}, false).Path; path != "/usr/bin" {
			t.Errorf("agent PATH with a sticky directory is %q", path)
		}
	}
}

// Without a desktop login the agent loads in the background domain and
// passes --headless=true; the plist names the state directory, the binary
// and the log. A failed load leaves no agent behind.
func TestLaunchAgentInstallWritesTheAgent(t *testing.T) {
	fake := recordLaunchctl(t)
	host, out := testLaunchAgentHost(t, macWithoutDesktop(), "")
	stateDir := filepath.Join(t.TempDir(), "state")
	err := host.install(stateDir, nil)
	if !errors.Is(err, service.ErrNotLoaded) || !slices.Contains(fake.calls, "/bin/launchctl bootstrap user/501 "+host.agentPath) {
		t.Fatalf("install: %v (calls %q)", err, fake.calls)
	}
	for _, want := range []string{"<string>" + stateDir + "</string>", "<string>" + host.agentExecutable + "</string>", "<string>--headless=true</string>", "<string>" + service.LaunchAgentLogPath(host.account.HomeDir) + "</string>"} {
		if !strings.Contains(fake.bootstrapped, want) {
			t.Fatalf("the agent lacks %s:\n%s", want, fake.bootstrapped)
		}
	}
	if _, err := os.Stat(host.agentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed install left the agent: %v", err)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed install created the state directory: %v", err)
	}
	if !strings.Contains(out.String(), "LaunchAgent") {
		t.Fatalf("install printed %q", out.String())
	}

	// With nothing installed there is nothing to stop or start.
	fresh, out := testLaunchAgentHost(t, macDesktop(), "")
	if err := fresh.control("start"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("start without an agent: %v", err)
	}
	noErr(t, fresh.status())
	if !strings.Contains(out.String(), "not installed as a service") {
		t.Fatalf("status without an agent printed %q", out.String())
	}
}

// The service of the program inside OwnGit.app names the app, so that
// System Settings lists it as OwnGit; a damaged app installs nothing.
func TestLaunchAgentInstallFromTheAppNamesTheApp(t *testing.T) {
	fake := recordLaunchctl(t)
	host, _ := testLaunchAgentHost(t, macDesktop(), "")
	contents := filepath.Join(host.account.HomeDir, "Applications", "OwnGit.app", "Contents")
	noErr(t, os.MkdirAll(filepath.Join(contents, "Helpers"), 0o755))
	info := filepath.Join(contents, "Info.plist")
	noErr(t, os.WriteFile(info, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>app.owngit.OwnGit</string></dict></plist>
`), 0o644))
	host.agentExecutable = filepath.Join(contents, "Helpers", "owngit")
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := host.install(stateDir, nil); !errors.Is(err, service.ErrNotLoaded) {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(fake.bootstrapped, "<key>AssociatedBundleIdentifiers</key>\n\t<array>\n\t\t<string>app.owngit.OwnGit</string>") ||
		!strings.Contains(fake.bootstrapped, "<string>"+host.agentExecutable+"</string>") {
		t.Fatalf("the agent does not name the app:\n%s", fake.bootstrapped)
	}

	fake.bootstrapped = ""
	noErr(t, os.WriteFile(info, []byte("not a property list"), 0o644))
	if err := host.install(stateDir, nil); err == nil || !strings.Contains(err.Error(), "icon app") || fake.bootstrapped != "" {
		t.Fatalf("install from a damaged app: %v, bootstrapped %q", err, fake.bootstrapped)
	}
}

func writeIconApp(t *testing.T, app, identifier, release string, helper bool) {
	t.Helper()
	noErr(t, os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755))
	noErr(t, os.WriteFile(service.AppLauncher(app), nil, 0o755))
	plist := "<plist><dict><key>CFBundleIdentifier</key><string>" + identifier + "</string><key>CFBundleShortVersionString</key><string>" + release + "</string></dict></plist>"
	noErr(t, os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644))
	if helper {
		noErr(t, os.MkdirAll(filepath.Join(app, "Contents", "Helpers"), 0o755))
		noErr(t, os.WriteFile(filepath.Join(app, "Contents", "Helpers", "owngit"), nil, 0o755))
	}
}

// service install opens the icon app that came with the program, so it
// shows and registers itself for sign-in, only in the owner's desktop
// login, for a service that is not headless, when the icon is not hidden
// and when the icon did not run the command itself.
func TestLaunchAgentOpensTheIcon(t *testing.T) {
	if testing.Short() {
		t.Skip("polls for the icon to exit at 300 ms intervals")
	}
	fake := recordLaunchctl(t)
	host, out := testLaunchAgentHost(t, macDesktop(), "")
	app := filepath.Join(filepath.Dir(host.agentExecutable), "OwnGit.app")
	writeIconApp(t, app, "app.owngit.OwnGit", version.Version, false)
	app, err := filepath.EvalSymlinks(app)
	noErr(t, err)
	pattern := regexp.MustCompile(iconPattern(app, "/Applications/OwnGit.app", "/opt/homebrew/Cellar/owngit/1.0.0/OwnGit.app"))
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{service.AppLauncher(app), true},
		{"/Applications/OwnGit.app/Contents/MacOS/OwnGitLauncher --at-sign-in", true},
		{"/opt/homebrew/Cellar/owngit/1.0.0/OwnGit.app/Contents/MacOS/OwnGitLauncher", true},
		{"sh -c /Applications/OwnGit.app/Contents/MacOS/OwnGitLauncher", false},
		{"/Applications/Other.app/Contents/MacOS/OwnGitLauncher", false},
	} {
		if got := pattern.MatchString(tc.command); got != tc.want {
			t.Errorf("icon pattern matched %q = %t, want %t", tc.command, got, tc.want)
		}
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	noErr(t, os.Mkdir(stateDir, 0o700))
	opened := func() bool {
		defer func() { fake.calls = nil }()
		return slices.Contains(fake.calls, "/usr/bin/open "+app)
	}

	host.openIcon(stateDir, false)
	if !opened() || !strings.Contains(out.String(), "owngit tray off") {
		t.Fatalf("the icon was not opened: %v\n%s", fake.calls, out)
	}
	host.openIcon(stateDir, true)
	if opened() {
		t.Error("a headless service opened the icon")
	}
	host.env = macWithoutDesktop()
	host.openIcon(stateDir, false)
	if opened() {
		t.Error("the icon opened without a desktop login")
	}
	host.env = macDesktop()
	host.env.Getenv = func(name string) string { return map[string]string{"OWNGIT_FROM_ICON": "1"}[name] }
	host.openIcon(stateDir, false)
	if opened() {
		t.Error("the icon was opened again by its own command")
	}
	host.env = macDesktop()

	// An update replaced the app of a running icon: it is quit and opened
	// again as at sign-in, without the first install's panel.
	reopened := func() bool {
		defer func() { fake.calls = nil }()
		pattern := "-U " + strconv.Itoa(host.uid) + " -f " + iconPattern(app)
		return slices.Contains(fake.calls, "/usr/bin/pkill "+pattern) &&
			slices.Contains(fake.calls, "/usr/bin/open "+app+" --args "+service.AppAtSignIn) &&
			!slices.Contains(fake.calls, "/usr/bin/open "+app)
	}
	fake.iconRunning, fake.iconQuits = true, true
	host.openIcon(stateDir, false)
	if !reopened() || !strings.Contains(out.String(), "restarted with this version") {
		t.Fatalf("the running icon was not restarted: %v\n%s", fake.calls, out)
	}
	// One that does not quit is not opened again, which would open its panel.
	fake.iconRunning, fake.iconQuits = true, false
	host.openIcon(stateDir, false)
	if slices.ContainsFunc(fake.calls, func(call string) bool { return strings.HasPrefix(call, "/usr/bin/open") }) || !strings.Contains(out.String(), "did not quit") {
		t.Fatalf("an icon that did not quit was opened again: %v\n%s", fake.calls, out)
	}
	fake.calls, fake.iconRunning = nil, false

	held, err := state.OpenStateDirectory(stateDir)
	noErr(t, err)
	noErr(t, state.SetTrayHidden(held, true))
	noErr(t, held.Close())
	host.openIcon(stateDir, false)
	if opened() {
		t.Error("a hidden icon was opened")
	}
	// A hidden icon that runs is restarted as at sign-in, so it stays hidden.
	fake.iconRunning, fake.iconQuits = true, true
	host.openIcon(stateDir, false)
	if !reopened() {
		t.Error("a running hidden icon was not restarted as at sign-in")
	}

	// A program without an app beside it opens nothing.
	plain, _ := testLaunchAgentHost(t, macDesktop(), "")
	plain.openIcon(stateDir, false)
	if len(fake.calls) != 0 {
		t.Errorf("a program without an app ran %v", fake.calls)
	}
	for _, tc := range []struct {
		name, identifier, release string
		runningLocal, stop        bool
	}{
		{"new system app replaces the old sibling", "app.owngit.OwnGit", version.Version, true, true},
		{"outdated system app stops before fallback opens", "app.owngit.OwnGit", "0.0.0", false, true},
		{"another bundle is left running", "example.other", version.Version, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := recordLaunchctl(t)
			host, out := testLaunchAgentHost(t, macDesktop(), "")
			local := filepath.Join(filepath.Dir(host.agentExecutable), service.AppName)
			system := filepath.Join(applicationsFolder, service.AppName)
			writeIconApp(t, local, "app.owngit.OwnGit", "0.0.0", false)
			writeIconApp(t, system, tc.identifier, tc.release, true)
			running, selected := system, local
			if tc.runningLocal {
				running, selected = local, system
			}
			resolved, err := filepath.EvalSymlinks(running)
			noErr(t, err)
			selected, err = filepath.EvalSymlinks(selected)
			noErr(t, err)
			fake.iconCommands, fake.iconQuits = []string{service.AppLauncher(resolved)}, true
			host.openIcon(t.TempDir(), false)
			open := "/usr/bin/open " + selected
			if tc.stop {
				open += " --args " + service.AppAtSignIn
			}
			opened := slices.Index(fake.calls, open)
			killed := slices.IndexFunc(fake.calls, func(call string) bool {
				return strings.HasPrefix(call, "/usr/bin/pkill -U "+strconv.Itoa(host.uid)+" -f ")
			})
			if opened < 0 || (len(fake.iconCommands) == 0) != tc.stop || (tc.stop && (killed < 0 || killed >= opened)) || (!tc.stop && killed >= 0) {
				t.Fatalf("running=%v, calls=%v, output=%s", fake.iconCommands, fake.calls, out)
			}
		})
	}
}

func TestLaunchAgentHomebrewIconRequiresCask(t *testing.T) {
	for _, tc := range []struct {
		name, identifier, release                                          string
		cask, manualLink, damaged, wantOpen                                bool
		cellarLink, cellarCaseLink, secondPrefixLink, secondPrefixCaseLink bool
	}{
		{name: "formula only"},
		{name: "matching app in Applications", cask: true, identifier: "app.owngit.OwnGit", release: version.Version, wantOpen: true},
		{name: "manual app linked in Applications", manualLink: true, wantOpen: true},
		{name: "outdated cask", cask: true, identifier: "app.owngit.OwnGit", release: "0.0.0"},
		{name: "damaged app", cask: true, damaged: true, identifier: "app.owngit.OwnGit", release: version.Version},
		{name: "unrelated app", cask: true, identifier: "example.other", release: version.Version},
		{name: "Cellar symlink in Applications", cellarLink: true},
		{name: "case-variant Cellar symlink in Applications", cellarCaseLink: true},
		{name: "second-prefix Cellar symlink in Applications", secondPrefixLink: true},
		{name: "second-prefix case-variant Cellar symlink in Applications", secondPrefixCaseLink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := recordLaunchctl(t)
			prefix := t.TempDir()
			host, out := testLaunchAgentHost(t, macDesktop(), prefix)
			formula := filepath.Join(prefix, "Cellar", "owngit", "1.1.1")
			cellarApp := filepath.Join(formula, service.AppName)
			writeIconApp(t, cellarApp, "app.owngit.OwnGit", version.Version, true)
			noErr(t, os.MkdirAll(filepath.Join(prefix, "opt"), 0o755))
			noErr(t, os.Symlink(formula, filepath.Join(prefix, "opt", "owngit")))
			cask := filepath.Join(applicationsFolder, service.AppName)
			if tc.cellarLink || tc.cellarCaseLink || tc.secondPrefixLink || tc.secondPrefixCaseLink {
				target := cellarApp
				cellarPrefix := prefix
				if tc.secondPrefixLink || tc.secondPrefixCaseLink {
					cellarPrefix = t.TempDir()
					target = filepath.Join(cellarPrefix, "Cellar", "owngit", "1.1.1", service.AppName)
					writeIconApp(t, target, "app.owngit.OwnGit", version.Version, true)
				}
				if tc.cellarCaseLink || tc.secondPrefixCaseLink {
					actual, err := os.Stat(target)
					noErr(t, err)
					target = filepath.Join(cellarPrefix, "CELLAR", "OWNGIT", "1.1.1", service.AppName)
					alias, err := os.Stat(target)
					if errors.Is(err, os.ErrNotExist) {
						t.Skip("the test volume is case-sensitive")
					}
					noErr(t, err)
					if !os.SameFile(actual, alias) {
						t.Fatal("case-variant path is not the formula app")
					}
					t.Log("case-variant target identifies the same formula app")
				}
				if tc.secondPrefixLink || tc.secondPrefixCaseLink {
					noErr(t, state.RequireProtectedPath(service.AppLauncher(target)))
				}
				noErr(t, os.Symlink(target, cask))
			} else if tc.manualLink {
				manual := filepath.Join(t.TempDir(), service.AppName)
				writeIconApp(t, manual, "app.owngit.OwnGit", version.Version, true)
				noErr(t, os.Symlink(manual, cask))
			} else if tc.cask {
				writeIconApp(t, cask, tc.identifier, tc.release, !tc.damaged)
			}
			resolvedCellar, err := filepath.EvalSymlinks(cellarApp)
			noErr(t, err)
			fake.iconCommands = []string{service.AppLauncher(resolvedCellar)}
			fake.iconQuits = tc.wantOpen
			if tc.cellarLink || tc.cellarCaseLink || tc.secondPrefixLink || tc.secondPrefixCaseLink {
				host.env.GraphicalSession = false
				if err := host.install("", nil); !errors.Is(err, service.ErrNotLoaded) {
					t.Fatalf("install: %v (calls %v)", err, fake.calls)
				}
				if strings.Contains(fake.bootstrapped, "AssociatedBundleIdentifiers") {
					t.Fatalf("formula alias was registered: %s", fake.bootstrapped)
				}
				host.env.GraphicalSession = true
			}
			host.openIcon(t.TempDir(), false)
			opened := slices.ContainsFunc(fake.calls, func(call string) bool { return strings.HasPrefix(call, "/usr/bin/open ") })
			if tc.wantOpen {
				resolvedCask, err := filepath.EvalSymlinks(cask)
				noErr(t, err)
				opened = slices.Contains(fake.calls, "/usr/bin/open "+resolvedCask+" --args "+service.AppAtSignIn)
			}
			if opened != tc.wantOpen || slices.ContainsFunc(fake.calls, func(call string) bool { return strings.HasPrefix(call, "/usr/bin/open "+resolvedCellar) }) {
				t.Fatalf("open calls: %v (output %s)", fake.calls, out)
			}
			if tc.wantOpen {
				if len(fake.iconCommands) != 0 || !strings.Contains(out.String(), "restarted with this version") {
					t.Fatalf("older Cellar icon was not replaced by the cask: %v (output %s)", fake.calls, out)
				}
			} else {
				if host.restartIcon() || len(fake.iconCommands) != 1 || slices.ContainsFunc(fake.calls, func(call string) bool {
					return strings.HasPrefix(call, "/usr/bin/pkill ")
				}) || !strings.Contains(out.String(), "brew install --cask owngit") || !strings.Contains(out.String(), "formula's command and server work") {
					t.Fatalf("Cellar icon changed or cask advice missing: %v (output %s)", fake.calls, out)
				}
			}
		})
	}
}

// service uninstall quits the icon of the removed service and turns off its
// opening at sign-in, as the Windows uninstall removes the icon's task.
func TestLaunchAgentUninstallClosesTheIcon(t *testing.T) {
	if testing.Short() {
		t.Skip("polls for the icon to exit at 300 ms intervals")
	}
	for _, tc := range []struct {
		name, systemRelease string
		quits, translocated bool
	}{
		{"resolved Cellar path", "", true, false},
		{"icon stays running", "", false, false},
		{"translocated icon remains", "", true, true},
		{"preferred system app and old sibling", version.Version, true, false},
		{"outdated system app and old sibling", "0.0.0", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := recordLaunchctl(t)
			host, out := testLaunchAgentHost(t, macDesktop(), "")
			real := filepath.Join(filepath.Dir(filepath.Dir(host.agentExecutable)), "Cellar")
			app := filepath.Join(real, service.AppName)
			writeIconApp(t, app, "app.owngit.OwnGit", "0.0.0", false)
			noErr(t, os.Symlink(real, filepath.Dir(host.agentExecutable)))
			apps := []string{app}
			if tc.systemRelease != "" {
				system := filepath.Join(applicationsFolder, service.AppName)
				writeIconApp(t, system, "app.owngit.OwnGit", tc.systemRelease, true)
				apps = append(apps, system)
			}
			resolved, err := filepath.EvalSymlinks(service.AppLauncher(app))
			noErr(t, err)
			fake.iconCommands, fake.iconQuits = []string{resolved}, tc.quits
			if tc.translocated {
				fake.iconCommands = []string{"/private/var/folders/abc/AppTranslocation/1234/d/OwnGit.app/Contents/MacOS/OwnGitLauncher"}
			}
			agent, err := service.RenderLaunchAgent(host.agentPlan(filepath.Join(t.TempDir(), "state"), nil, service.Installed{}, false))
			noErr(t, err)
			noErr(t, os.MkdirAll(filepath.Dir(host.agentPath), 0o755))
			noErr(t, os.WriteFile(host.agentPath, []byte(agent), 0o644))
			err = host.uninstall()
			if (err != nil) != (!tc.quits || tc.translocated) {
				t.Fatalf("uninstall result: %v", err)
			}
			calls := map[string]int{}
			for _, call := range fake.calls {
				calls[call]++
			}
			for _, app := range apps {
				resolved, err := filepath.EvalSymlinks(service.AppLauncher(app))
				noErr(t, err)
				want := "-U " + strconv.Itoa(host.uid) + " -f " + iconPattern(app)
				if calls["/usr/bin/pkill "+want] != 1 || calls["/usr/bin/pgrep "+want] == 0 || calls[resolved+" "+service.AppSignInOff] != 1 {
					t.Fatalf("uninstall did not check each resolved icon once: %v", fake.calls)
				}
				if _, err := os.Stat(app); err != nil {
					t.Fatalf("uninstall removed the app: %v", err)
				}
			}
			if got := out.String(); !strings.Contains(got, "no longer opens at sign-in") || !strings.Contains(got, "stopped and removed") {
				t.Fatalf("uninstall printed:\n%s", got)
			}
			if tc.translocated && (err == nil || !strings.Contains(err.Error(), "AppTranslocation")) {
				t.Fatalf("translocated process not reported: %v", err)
			}
			if !tc.quits && (err == nil || !strings.Contains(err.Error(), "still running")) {
				t.Fatalf("running icon not reported: %v", err)
			}
			if _, err := os.Stat(host.agentPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the agent is still there: %v", err)
			}
		})
	}
}

func TestLaunchAgentHomebrewUninstallReportsIconFailure(t *testing.T) {
	fake := recordLaunchctl(t)
	prefix := t.TempDir()
	brew := filepath.Join(prefix, "bin", "brew")
	noErr(t, os.MkdirAll(filepath.Dir(brew), 0o755))
	noErr(t, os.WriteFile(brew, []byte("#!/bin/sh\necho \"$*\" >\"$0.calls\"\n"), 0o755))
	host, out := testLaunchAgentHost(t, macDesktop(), prefix)
	app := filepath.Join(filepath.Dir(host.agentExecutable), service.AppName)
	writeIconApp(t, app, "app.owngit.OwnGit", version.Version, true)
	fake.iconCommands = []string{"/private/var/folders/abc/T/AppTranslocation/1234/d/OwnGit.app/Contents/MacOS/OwnGitLauncher"}
	fake.iconQuits = true
	if err := host.uninstall(); err == nil || !strings.Contains(err.Error(), "AppTranslocation") {
		t.Fatalf("Homebrew cleanup hid the remaining icon: %v, output=%s", err, out)
	}
	calls, err := os.ReadFile(brew + ".calls")
	noErr(t, err)
	if string(calls) != "services stop owngit\n" || !strings.Contains(out.String(), "Homebrew service is stopped") {
		t.Fatalf("Homebrew service was not stopped: calls=%q output=%s", calls, out)
	}
}

func TestLaunchAgentIconCheckError(t *testing.T) {
	recordLaunchctl(t)
	host, _ := testLaunchAgentHost(t, macDesktop(), "")
	app := filepath.Join(filepath.Dir(host.agentExecutable), service.AppName)
	writeIconApp(t, app, "app.owngit.OwnGit", version.Version, true)
	runner := serviceRunner
	serviceRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/bin/pgrep" {
			return nil, errors.New("process inspection unavailable")
		}
		return runner(ctx, name, args...)
	}
	if err := host.closeIcon(host.agentExecutable); err == nil || !strings.Contains(err.Error(), "process inspection unavailable") {
		t.Fatalf("failed process check was reported as closed: %v", err)
	}
}

func TestLaunchAgentSignInOffDeadline(t *testing.T) {
	fake := recordLaunchctl(t)
	host, out := testLaunchAgentHost(t, macDesktop(), "")
	app := filepath.Join(filepath.Dir(host.agentExecutable), service.AppName)
	writeIconApp(t, app, "app.owngit.OwnGit", version.Version, true)
	launcher, err := filepath.EvalSymlinks(service.AppLauncher(app))
	noErr(t, err)
	runner := serviceRunner
	serviceRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == launcher {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 15*time.Second || time.Until(deadline) <= 0 {
				t.Errorf("sign-in helper has no usable deadline: %v, %t", deadline, ok)
			}
			return nil, context.DeadlineExceeded
		}
		return runner(ctx, name, args...)
	}
	if err := host.closeIcon(host.agentExecutable); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "Open at Login") {
		t.Fatalf("helper timeout was hidden: %v, output=%s, calls=%v", err, out, fake.calls)
	}
	if strings.Contains(out.String(), "no longer opens at sign-in") {
		t.Fatalf("timeout was reported as successful: %s", out)
	}
}

// An agent installed without a desktop login stays headless when it is
// installed again from the desktop, and a desktop agent says so explicitly.
func TestLaunchAgentReinstallKeepsHeadless(t *testing.T) {
	fake := recordLaunchctl(t)
	// status checks the default address, since the state cannot be read.
	useFakeHealth(t)
	host, out := testLaunchAgentHost(t, macDesktop(), "")
	stateParent := filepath.Join(t.TempDir(), "not-a-directory")
	noErr(t, os.WriteFile(stateParent, nil, 0o600))
	stateDir := filepath.Join(stateParent, "state")
	earlier := host.agentPlan(stateDir, nil, service.Installed{}, false)
	if earlier.Headless {
		t.Fatal("a desktop install is headless")
	}
	earlier.Headless = true
	agent, err := service.RenderLaunchAgent(earlier)
	noErr(t, err)
	noErr(t, os.MkdirAll(filepath.Dir(host.agentPath), 0o755))
	noErr(t, os.WriteFile(host.agentPath, []byte(agent), 0o644))

	if err := host.install("", nil); !errors.Is(err, service.ErrNotLoaded) {
		t.Fatalf("reinstall: %v", err)
	}
	if !strings.Contains(fake.bootstrapped, "<string>--headless=true</string>") || !strings.Contains(fake.bootstrapped, "<string>"+stateDir+"</string>") {
		t.Fatalf("the reinstalled agent lost --headless=true or its state directory:\n%s", fake.bootstrapped)
	}
	if written, err := os.ReadFile(host.agentPath); err != nil || string(written) != agent {
		t.Fatalf("a failed reinstall did not put the earlier agent back: %v", err)
	}

	// A desktop agent passes --headless=false and keeps it when installed
	// again over SSH without a desktop login; only --headless changes it.
	desktop, _ := testLaunchAgentHost(t, macDesktop(), "")
	for _, step := range []struct {
		env  service.Environment
		flag *bool
		want string
	}{
		{macDesktop(), nil, "--headless=false"},
		{macWithoutDesktop(), nil, "--headless=false"},
		{macWithoutDesktop(), new(true), "--headless=true"},
		{macWithoutDesktop(), new(false), "--headless=false"},
	} {
		desktop.env = step.env
		if err := desktop.install(stateDir, step.flag); !errors.Is(err, service.ErrNotLoaded) {
			t.Fatalf("install: %v", err)
		}
		if !strings.Contains(fake.bootstrapped, "<string>"+step.want+"</string>") {
			t.Fatalf("install (flag %v) does not pass %s:\n%s", step.flag, step.want, fake.bootstrapped)
		}
		// The failed load put the earlier agent back; keep the new one as
		// the installed agent for the next step.
		noErr(t, os.WriteFile(desktop.agentPath, []byte(fake.bootstrapped), 0o644))
	}
	fake.loaded = []string{"gui/501/" + service.LaunchAgentLabel}
	out.Reset()
	noErr(t, host.status())
	if strings.Contains(out.String(), "Setup is not complete") {
		t.Fatalf("unreadable state was called incomplete: %q", out.String())
	}
}

// Homebrew's service removes the OwnGit agent of the same state directory
// when it starts; any other process leaves it alone.
func TestServeUnderHomebrewTakesOverFromTheAgent(t *testing.T) {
	recordLaunchctl(t)
	host, _ := testLaunchAgentHost(t, macDesktop(), "")
	previousHome := launchAgentHome
	t.Cleanup(func() { launchAgentHome = previousHome })
	launchAgentHome = host.account.HomeDir
	// A state directory that serve cannot create ends it right after the
	// handover.
	blocker := filepath.Join(t.TempDir(), "file")
	noErr(t, os.WriteFile(blocker, nil, 0o600))
	stateDir := filepath.Join(blocker, "state")
	agent, err := service.RenderLaunchAgent(host.agentPlan(stateDir, nil, service.Installed{}, false))
	noErr(t, err)
	noErr(t, os.MkdirAll(filepath.Dir(host.agentPath), 0o755))
	noErr(t, os.WriteFile(host.agentPath, []byte(agent), 0o644))

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, format) }
	t.Setenv("XPC_SERVICE_NAME", "com.example.other")
	yieldToHomebrew(stateDir, logf)
	if _, err := os.Stat(host.agentPath); err != nil {
		t.Fatalf("another job removed the agent: %v", err)
	}
	t.Setenv("XPC_SERVICE_NAME", service.HomebrewLabels[0])
	if err := serveWithContext(context.Background(), []string{"--state-dir", stateDir}, nil, logf); err == nil {
		t.Fatal("serve created a state directory inside a file")
	}
	_, err = os.Stat(host.agentPath)
	if runtime.GOOS == "darwin" {
		if !errors.Is(err, os.ErrNotExist) || len(logged) != 1 {
			t.Fatalf("Homebrew's service kept the agent: %v, logged %q", err, logged)
		}
	} else if err != nil {
		t.Fatalf("off macOS the agent was removed: %v", err)
	}
}
