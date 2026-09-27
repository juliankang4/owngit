package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"owngit/internal/service"
)

// testLaunchAgentHost is a macOS service backend for a home folder in a
// temporary directory. It runs on any platform, since it only writes files
// and calls launchctl through serviceRunner.
func testLaunchAgentHost(t *testing.T, env service.Environment, homebrew string) (*launchAgentHost, *bytes.Buffer) {
	t.Helper()
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
	calls        []string
	loaded       []string
	bootstrapped string
}

func recordLaunchctl(t *testing.T, loaded ...string) *fakeLaunchctl {
	t.Helper()
	previous := serviceRunner
	t.Cleanup(func() { serviceRunner = previous })
	fake := &fakeLaunchctl{loaded: loaded}
	serviceRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		fake.calls = append(fake.calls, name+" "+strings.Join(args, " "))
		switch {
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
// bin/brew.calls and fails, so that no test can run the real brew.
func fakeBrewPrefix(t *testing.T) string {
	t.Helper()
	prefix := t.TempDir()
	noErr(t, os.MkdirAll(filepath.Join(prefix, "bin"), 0o755))
	noErr(t, os.WriteFile(filepath.Join(prefix, "bin", "brew"), []byte("#!/bin/sh\necho \"brew $*\" >>\"$0.calls\"\nexit 1\n"), 0o755))
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
	host, _ := testLaunchAgentHost(t, macDesktop(), prefix)
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
	if !strings.Contains(string(calls), "brew services restart owngit") {
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

// An agent installed without a desktop login stays headless when it is
// installed again from the desktop, and a desktop agent says so explicitly.
func TestLaunchAgentReinstallKeepsHeadless(t *testing.T) {
	fake := recordLaunchctl(t)
	host, _ := testLaunchAgentHost(t, macDesktop(), "")
	stateDir := filepath.Join(t.TempDir(), "state")
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
}

// Homebrew's service removes the OwnGit agent of the same state directory
// when it starts; any other process leaves it alone.
func TestServeUnderHomebrewTakesOverFromTheAgent(t *testing.T) {
	recordLaunchctl(t)
	host, _ := testLaunchAgentHost(t, macDesktop(), "")
	previousHome := launchAgentHome
	t.Cleanup(func() { launchAgentHome = previousHome })
	launchAgentHome = host.account.HomeDir
	stateDir := filepath.Join(t.TempDir(), "state")
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
	yieldToHomebrew(stateDir, logf)
	_, err = os.Stat(host.agentPath)
	if runtime.GOOS == "darwin" {
		if !errors.Is(err, os.ErrNotExist) || len(logged) != 1 {
			t.Fatalf("Homebrew's service kept the agent: %v, logged %q", err, logged)
		}
	} else if err != nil {
		t.Fatalf("off macOS the agent was removed: %v", err)
	}
}
