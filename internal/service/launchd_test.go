package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func launchAgentPlan(home string) Plan {
	return Plan{
		Mode: ModeLaunchAgent, Home: home,
		Executable: "/Users/example/bin/own git&co/owngit",
		StateDir:   "/Users/example/Library/Application Support/owngit <main> & co",
		Path:       "/opt/homebrew/bin:/usr/bin:/bin",
	}
}

func TestRenderLaunchAgent(t *testing.T) {
	home := "/Users/example"
	plan := launchAgentPlan(home)
	agent, err := RenderLaunchAgent(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agent, plistMarker) {
		t.Fatalf("the agent lacks the marker:\n%s", agent)
	}
	plist, err := parsePlist([]byte(agent))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, agent)
	}
	log := "/Users/example/Library/Logs/owngit/owngit.log"
	want := map[string]any{
		"Label":                  LaunchAgentLabel,
		"ProgramArguments":       []any{plan.Executable, "serve", "--state-dir", plan.StateDir, "--no-open", "--headless=false"},
		"EnvironmentVariables":   map[string]any{"PATH": plan.Path},
		"RunAtLoad":              true,
		"KeepAlive":              true,
		"LimitLoadToSessionType": []any{"Aqua", "Background"},
		"Umask":                  "63",
		"ExitTimeOut":            "150",
		"StandardOutPath":        log,
		"StandardErrorPath":      log,
	}
	if !reflect.DeepEqual(plist, want) {
		t.Fatalf("agent =\n%#v\nwant\n%#v", plist, want)
	}
	if LaunchAgentPath(home) != "/Users/example/Library/LaunchAgents/app.owngit.server.plist" || LaunchAgentLogPath(home) != log {
		t.Fatalf("paths: %s, %s", LaunchAgentPath(home), LaunchAgentLogPath(home))
	}

	plan.Headless, plan.Path = true, ""
	agent, err = RenderLaunchAgent(plan)
	if err != nil {
		t.Fatal(err)
	}
	plist, err = parsePlist([]byte(agent))
	if err != nil {
		t.Fatal(err)
	}
	if got := stringList(plist["ProgramArguments"]); !slices.Equal(got, []string{plan.Executable, "serve", "--state-dir", plan.StateDir, "--no-open", "--headless=true"}) {
		t.Fatalf("headless arguments: %q", got)
	}
	if _, found := plist["EnvironmentVariables"]; found {
		t.Fatal("an empty PATH was written")
	}
	for _, word := range []string{"--listen", "--base-url", "--open\n"} {
		if strings.Contains(agent, word) {
			t.Fatalf("the agent passes %s:\n%s", word, agent)
		}
	}
	lintPlist(t, agent)
}

// lintPlist checks the agent with macOS's own parser where it exists.
func lintPlist(t *testing.T, agent string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	path := filepath.Join(t.TempDir(), "agent.plist")
	if err := os.WriteFile(path, []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/usr/bin/plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint: %v: %s", err, output)
	}
}

func TestRenderLaunchAgentRefuses(t *testing.T) {
	for name, change := range map[string]func(*Plan){
		"relative executable":      func(plan *Plan) { plan.Executable = "owngit" },
		"unclean state directory":  func(plan *Plan) { plan.StateDir = "/Users/example/../owngit" },
		"relative home":            func(plan *Plan) { plan.Home = "Users/owner" },
		"newline in the directory": func(plan *Plan) { plan.StateDir = "/Users/example/a\nb" },
		"control character in PATH": func(plan *Plan) {
			plan.Path = "/usr/bin:\x01"
		},
		"invalid UTF-8":  func(plan *Plan) { plan.StateDir = "/Users/example/\xff" },
		"a systemd mode": func(plan *Plan) { plan.Mode = ModeUser },
	} {
		plan := launchAgentPlan("/Users/example")
		change(&plan)
		if agent, err := RenderLaunchAgent(plan); err == nil {
			t.Errorf("%s: rendered\n%s", name, agent)
		}
	}
}

func TestReadLaunchAgent(t *testing.T) {
	dir := t.TempDir()
	plan := launchAgentPlan(dir)
	plan.Headless = true
	agent, err := RenderLaunchAgent(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ours.plist")
	writeFile(t, path, agent)
	installed, err := ReadLaunchAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Installed{Mode: ModeLaunchAgent, UnitPath: path, Executable: plan.Executable, StateDir: plan.StateDir, Headless: true}
	if installed != want {
		t.Fatalf("ReadLaunchAgent = %+v, want %+v", installed, want)
	}
	// An agent that passes the bare flag, as a pre-release build wrote it,
	// is headless too; --headless=false is not.
	bare := filepath.Join(dir, "bare.plist")
	writeFile(t, bare, strings.Replace(agent, "--headless=true", "--headless", 1))
	if installed, err := ReadLaunchAgent(bare); err != nil || !installed.Headless {
		t.Fatalf("bare --headless: %+v, %v", installed, err)
	}
	plan.Headless = false
	desktop, err := RenderLaunchAgent(plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, bare, desktop)
	if installed, err := ReadLaunchAgent(bare); err != nil || installed.Headless {
		t.Fatalf("--headless=false: %+v, %v", installed, err)
	}
	// The same job without the marker belongs to someone else.
	foreign := filepath.Join(dir, "foreign.plist")
	writeFile(t, foreign, strings.Replace(agent, plistMarker, "", 1))
	if _, err := ReadLaunchAgent(foreign); !errors.Is(err, ErrForeignUnit) {
		t.Fatalf("ReadLaunchAgent without the marker: %v", err)
	}
	if _, err := ReadLaunchAgent(filepath.Join(dir, "missing.plist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadLaunchAgent of a missing file: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// job is a hand-written launchd property list.
func job(label string, extra string, arguments ...string) string {
	var words strings.Builder
	for _, argument := range arguments {
		fmt.Fprintf(&words, "<string>%s</string>", argument)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + label + `</string>
  <key>EnvironmentVariables</key><dict><key>HOME</key><string>/Users/example</string></dict>
  <key>ProgramArguments</key><array>` + words.String() + `</array>
  ` + extra + `
  <key>RunAtLoad</key><true/>
</dict></plist>
`
}

func TestFindServeAgents(t *testing.T) {
	agents, system := t.TempDir(), t.TempDir()
	ours, err := RenderLaunchAgent(launchAgentPlan("/Users/example"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agents, LaunchAgentLabel+".plist"), ours)
	writeFile(t, filepath.Join(agents, "com.example.owngit.plist"), job("com.example.owngit", "", "/usr/local/bin/owngit", "serve", "--state-dir", "/Users/example/owngit"))
	writeFile(t, filepath.Join(agents, "com.example.shell.plist"), job("com.example.shell", "", "/bin/sh", "-c", "cd /Users/example &amp;&amp; exec /Users/example/bin/owngit serve --no-open &gt;&gt; log"))
	for _, label := range HomebrewLabels {
		writeFile(t, filepath.Join(agents, label+".plist"), job(label, "", "/opt/homebrew/opt/owngit/bin/owngit", "serve", "-no-open"))
	}
	writeFile(t, filepath.Join(agents, "com.example.backup.plist"), job("com.example.backup", "", "/usr/local/bin/owngit", "backup", "--output", "/Users/example/serve"))
	writeFile(t, filepath.Join(agents, "com.example.other.plist"), job("com.example.other", "", "/usr/local/bin/other", "serve"))
	writeFile(t, filepath.Join(agents, "com.example.off.plist"), job("com.example.off", "<key>Disabled</key><true/>", "/usr/local/bin/owngit", "serve"))
	writeFile(t, filepath.Join(agents, "broken.plist"), "<plist><dict><key>Label</key>")
	writeFile(t, filepath.Join(agents, "notes.txt"), job("com.example.text", "", "/usr/local/bin/owngit", "serve"))
	writeFile(t, filepath.Join(system, "com.example.program.plist"), job("com.example.program", "<key>Program</key><string>/usr/bin/env</string>", "env", "node", "/usr/local/lib/node_modules/owngit/bin/owngit.js", "serve"))

	labels := func(found []FoundAgent) []string {
		var names []string
		for _, agent := range found {
			names = append(names, agent.Label)
		}
		slices.Sort(names)
		return names
	}
	found := FindServeAgents(context.Background(), nil, []string{agents, system, filepath.Join(agents, "missing")}, LaunchAgentLabel)
	if want := []string{"com.example.owngit", "com.example.program", "com.example.shell", "homebrew.mxcl.owngit", "sh.brew.owngit"}; !slices.Equal(labels(found), want) {
		t.Fatalf("found %q, want %q", labels(found), want)
	}
	for _, agent := range found {
		if agent.Label == "com.example.program" && agent.Path != filepath.Join(system, "com.example.program.plist") {
			t.Fatalf("path of %s: %s", agent.Label, agent.Path)
		}
	}
	found = FindServeAgents(context.Background(), nil, []string{agents}, append([]string{LaunchAgentLabel}, HomebrewLabels...)...)
	if want := []string{"com.example.owngit", "com.example.shell"}; !slices.Equal(labels(found), want) {
		t.Fatalf("found %q without Homebrew, want %q", labels(found), want)
	}
}

// A binary property list, as "defaults write" leaves one, is read through
// plutil.
func TestFindServeAgentsReadsBinaryPropertyLists(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil exists on macOS only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "com.example.binary.plist")
	writeFile(t, path, job("com.example.binary", "", "/usr/local/bin/owngit", "serve"))
	if output, err := exec.Command("/usr/bin/plutil", "-convert", "binary1", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v: %s", err, output)
	}
	if found := FindServeAgents(context.Background(), nil, []string{dir}); len(found) != 0 {
		t.Fatalf("read a binary list without a converter: %v", found)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
	if found := FindServeAgents(context.Background(), run, []string{dir}); len(found) != 1 || found[0].Label != "com.example.binary" {
		t.Fatalf("binary list: %v", found)
	}
}

func TestRunsOwnGitServe(t *testing.T) {
	for _, test := range []struct {
		words []string
		want  bool
	}{
		{[]string{"/usr/local/bin/owngit", "serve"}, true},
		{[]string{"/opt/homebrew/opt/owngit/bin/owngit", "serve", "-no-open"}, true},
		{[]string{"/bin/zsh", "-lc", "exec owngit serve --state-dir '/Users/example/x'"}, true},
		{[]string{"/usr/bin/env", "node", "/x/node_modules/owngit/bin/owngit.js", "serve"}, true},
		{[]string{"/usr/local/bin/owngit", "backup", "--output", "serve"}, false},
		{[]string{"serve", "/usr/local/bin/owngit"}, false},
		{[]string{"/usr/local/bin/owngit-helper", "serve"}, false},
		{[]string{"/usr/local/bin/owngit", "health"}, false},
		{nil, false},
	} {
		if got := RunsOwnGitServe(test.words); got != test.want {
			t.Errorf("RunsOwnGitServe(%q) = %v, want %v", test.words, got, test.want)
		}
	}
}

func TestAgentExecutableAndInstallRoutes(t *testing.T) {
	for _, test := range []struct{ invoked, resolved, want string }{
		{"/opt/homebrew/bin/owngit", "/opt/homebrew/Cellar/owngit/1.1.1/bin/owngit", "/opt/homebrew/opt/owngit/bin/owngit"},
		{"/usr/local/bin/owngit", "/usr/local/Cellar/owngit/1.1.1/bin/owngit", "/usr/local/opt/owngit/bin/owngit"},
		{"/usr/local/bin/owngit", "/Users/example/Downloads/owngit-1.1.1/owngit", "/usr/local/bin/owngit"},
		{"/Users/example/bin/./owngit", "/Users/example/bin/owngit", "/Users/example/bin/owngit"},
		{"", "/Users/example/bin/owngit", "/Users/example/bin/owngit"},
	} {
		if got := AgentExecutable(test.invoked, test.resolved); got != test.want {
			t.Errorf("AgentExecutable(%q, %q) = %q, want %q", test.invoked, test.resolved, got, test.want)
		}
	}
	for path, want := range map[string]bool{
		"/opt/homebrew/lib/node_modules/owngit/node_modules/owngit-darwin-arm64/bin/owngit":         true,
		"/Users/example/.nvm/versions/node/v22.1.0/lib/node_modules/owngit-darwin-arm64/bin/owngit": true,
		"/usr/local/lib/node_modules/owngit/bin/owngit.js":                                          false,
		"/opt/homebrew/Cellar/owngit/1.1.1/bin/owngit":                                              false,
		"/Users/example/node_modules-backup/owngit-darwin-arm64/bin/owngit":                         false,
	} {
		if got := NPMExecutable(path); got != want {
			t.Errorf("NPMExecutable(%q) = %v, want %v", path, got, want)
		}
	}
}

// fakeLaunchd answers launchctl like launchd for the one agent.
type fakeLaunchd struct {
	loaded string // domain, or ""
	gui    bool   // whether the GUI domain exists
	calls  []string
	// refuse makes the next bootstrap fail, as when the owner turned the
	// item off under Login Items.
	refuse bool
	// jobs are other loaded labels, as "domain/label".
	jobs []string
}

func (fake *fakeLaunchd) run(_ context.Context, name string, args ...string) ([]byte, error) {
	fake.calls = append(fake.calls, strings.Join(args, " "))
	if name != launchctl {
		return nil, fmt.Errorf("unexpected command %s", name)
	}
	target := args[len(args)-1]
	domain := strings.TrimSuffix(target, "/"+LaunchAgentLabel)
	switch args[0] {
	case "print":
		if slices.Contains(fake.jobs, target) {
			return []byte("state = running"), nil
		}
		if fake.loaded == "" || fake.loaded != domain {
			return []byte("Could not find service"), errors.New("exit status 113")
		}
		return []byte("gui/501/app.owngit.server = {\n\tactive count = 1\n\tstate = running\n\tpid = 4242\n\tlast exit code = (never exited)\n\tendpoints = {\n\t\tstate = active\n\t}\n}\n"), nil
	case "bootout":
		if fake.loaded != domain {
			return []byte("Boot-out failed: 3: No such process"), errors.New("exit status 3")
		}
		fake.loaded = ""
	case "bootstrap":
		switch {
		case fake.refuse:
			fake.refuse = false
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
		case fake.loaded != "":
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
		case strings.HasPrefix(args[1], "gui/") && !fake.gui:
			return []byte("Bootstrap failed: 125: Domain does not support specified action"), errors.New("exit status 125")
		}
		fake.loaded = args[1]
	case "enable", "kickstart":
		if args[0] == "kickstart" && fake.loaded != domain {
			return nil, errors.New("exit status 113")
		}
	default:
		return nil, fmt.Errorf("unexpected launchctl %s", args[0])
	}
	return nil, nil
}

func TestLaunchAgentInstallIsIdempotentAndUninstallKeepsData(t *testing.T) {
	home := t.TempDir()
	plan := launchAgentPlan(home)
	plan.StateDir = filepath.Join(home, "Library", "Application Support", "owngit")
	writeFile(t, filepath.Join(plan.StateDir, "owngit.db"), "state")
	fake := &fakeLaunchd{gui: true}
	ctx := context.Background()
	path := LaunchAgentPath(home)
	var steps [][]string
	for round := 0; round < 3; round++ {
		plan.Executable = fmt.Sprintf("/usr/local/bin/owngit-%d", round)
		agent, err := RenderLaunchAgent(plan)
		if err != nil {
			t.Fatal(err)
		}
		fake.calls = nil
		domain, err := InstallLaunchAgent(ctx, fake.run, plan, agent, 501, true)
		if err != nil || domain != "gui/501" || fake.loaded != "gui/501" {
			t.Fatalf("round %d: domain %q, loaded %q, err %v", round, domain, fake.loaded, err)
		}
		if written, err := os.ReadFile(path); err != nil || string(written) != agent {
			t.Fatalf("round %d: the agent file was not replaced: %v", round, err)
		}
		steps = append(steps, fake.calls)
	}
	if want := []string{"print gui/501/app.owngit.server", "print user/501/app.owngit.server", "enable gui/501/app.owngit.server", "bootstrap gui/501 " + path}; !slices.Equal(steps[0], want) {
		t.Fatalf("first install: %q, want %q", steps[0], want)
	}
	// A loaded agent is booted out first, so launchd reads the new file.
	if want := []string{"print gui/501/app.owngit.server", "bootout gui/501/app.owngit.server", "print gui/501/app.owngit.server", "print user/501/app.owngit.server", "enable gui/501/app.owngit.server", "bootstrap gui/501 " + path}; !slices.Equal(steps[1], want) || !slices.Equal(steps[2], want) {
		t.Fatalf("reinstall: %q and %q, want %q", steps[1], steps[2], want)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("LaunchAgents holds %d entries, want only the agent", len(entries))
	}
	logs, err := os.Stat(filepath.Dir(LaunchAgentLogPath(home)))
	if err != nil || logs.Mode().Perm() != 0o700 {
		t.Fatalf("log folder: %v, %v", logs, err)
	}
	if log, err := os.Stat(LaunchAgentLogPath(home)); err != nil || log.Mode().Perm() != 0o600 {
		t.Fatalf("log file: %v, %v", log, err)
	}

	if err := UninstallLaunchAgent(ctx, fake.run, 501, path); err != nil || fake.loaded != "" {
		t.Fatalf("uninstall: loaded %q, err %v", fake.loaded, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the agent file stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plan.StateDir, "owngit.db")); err != nil {
		t.Fatalf("uninstall removed the state: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(LaunchAgentLogPath(home))); err != nil {
		t.Fatalf("uninstall removed the logs: %v", err)
	}
	// Uninstalling again finds nothing to do.
	if err := UninstallLaunchAgent(ctx, fake.run, 501, path); err != nil {
		t.Fatal(err)
	}
}

// Over SSH without a desktop login the agent loads in the user's background
// domain; a later install from the desktop moves it to the GUI domain.
func TestLaunchAgentWithoutADesktopLogin(t *testing.T) {
	home := t.TempDir()
	plan := launchAgentPlan(home)
	agent, err := RenderLaunchAgent(plan)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLaunchd{}
	ctx := context.Background()
	domain, err := InstallLaunchAgent(ctx, fake.run, plan, agent, 501, false)
	if err != nil || domain != "user/501" || fake.loaded != "user/501" {
		t.Fatalf("install without a desktop: domain %q, loaded %q, err %v", domain, fake.loaded, err)
	}
	if state := LaunchAgentStatus(ctx, fake.run, 501); state.Domain != "user/501" || !state.Running || state.PID != 4242 || state.LastExit != "(never exited)" {
		t.Fatalf("status: %+v", state)
	}
	fake.gui = true
	if domain, err := InstallLaunchAgent(ctx, fake.run, plan, agent, 501, true); err != nil || domain != "gui/501" || fake.loaded != "gui/501" {
		t.Fatalf("install from the desktop: domain %q, loaded %q, err %v", domain, fake.loaded, err)
	}
}

func TestLaunchAgentControl(t *testing.T) {
	path := "/Users/example/Library/LaunchAgents/app.owngit.server.plist"
	ctx := context.Background()
	fake := &fakeLaunchd{gui: true}
	if state := LaunchAgentStatus(ctx, fake.run, 501); state != (AgentState{}) {
		t.Fatalf("status of an unloaded agent: %+v", state)
	}
	// Start loads an unloaded agent; restart of a loaded one kills and
	// starts it again; stop unloads it.
	for _, step := range []struct {
		name   string
		action func() error
		want   string
		loaded string
	}{
		{"start", func() error { return StartLaunchAgent(ctx, fake.run, 501, true, path, false) }, "bootstrap gui/501 " + path, "gui/501"},
		{"start again", func() error { return StartLaunchAgent(ctx, fake.run, 501, true, path, false) }, "kickstart gui/501/app.owngit.server", "gui/501"},
		{"restart", func() error { return StartLaunchAgent(ctx, fake.run, 501, true, path, true) }, "kickstart -k gui/501/app.owngit.server", "gui/501"},
		{"stop", func() error { return StopLaunchAgent(ctx, fake.run, 501) }, "bootout gui/501/app.owngit.server", ""},
		{"stop again", func() error { return StopLaunchAgent(ctx, fake.run, 501) }, "print user/501/app.owngit.server", ""},
		{"restart when stopped", func() error { return StartLaunchAgent(ctx, fake.run, 501, true, path, true) }, "bootstrap gui/501 " + path, "gui/501"},
	} {
		fake.calls = nil
		if err := step.action(); err != nil {
			t.Fatalf("%s: %v (%q)", step.name, err, fake.calls)
		}
		if !slices.Contains(fake.calls, step.want) || fake.loaded != step.loaded {
			t.Fatalf("%s: calls %q, loaded %q; want %q and %q", step.name, fake.calls, fake.loaded, step.want, step.loaded)
		}
	}
}

// A failed load leaves nothing: a first install removes its agent and log
// folder, and a reinstall puts the earlier agent back and loads it again.
func TestLaunchAgentInstallFailureLeavesNothing(t *testing.T) {
	home := t.TempDir()
	plan := launchAgentPlan(home)
	agent, err := RenderLaunchAgent(plan)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fake := &fakeLaunchd{gui: true, refuse: true}
	_, err = InstallLaunchAgent(ctx, fake.run, plan, agent, 501, true)
	if !errors.Is(err, ErrNotLoaded) || !strings.Contains(err.Error(), "Login Items") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("refused first install: %v", err)
	}
	for _, path := range []string{LaunchAgentPath(home), filepath.Dir(LaunchAgentLogPath(home))} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a failed first install left %s: %v", path, err)
		}
	}

	if _, err := InstallLaunchAgent(ctx, fake.run, plan, agent, 501, true); err != nil {
		t.Fatal(err)
	}
	updated := plan
	updated.Executable = "/usr/local/bin/owngit-new"
	newAgent, err := RenderLaunchAgent(updated)
	if err != nil {
		t.Fatal(err)
	}
	fake.refuse = true
	if _, err := InstallLaunchAgent(ctx, fake.run, updated, newAgent, 501, true); !errors.Is(err, ErrNotLoaded) {
		t.Fatalf("refused reinstall: %v", err)
	}
	if written, err := os.ReadFile(LaunchAgentPath(home)); err != nil || string(written) != agent {
		t.Fatalf("the earlier agent did not come back: %v", err)
	}
	if fake.loaded != "gui/501" {
		t.Fatalf("the earlier agent is not loaded again: %q", fake.loaded)
	}
	if _, err := os.Stat(LaunchAgentLogPath(home)); err != nil {
		t.Fatalf("a failed reinstall removed the log: %v", err)
	}
}

func TestJobLoaded(t *testing.T) {
	fake := &fakeLaunchd{jobs: []string{"user/501/com.example.agent", "system/com.example.daemon"}}
	ctx := context.Background()
	for _, test := range []struct {
		job  FoundAgent
		want bool
	}{
		{FoundAgent{Label: "com.example.agent", Path: "/Users/example/Library/LaunchAgents/com.example.agent.plist"}, true},
		{FoundAgent{Label: "com.example.daemon", Path: "/Library/LaunchDaemons/com.example.daemon.plist"}, true},
		{FoundAgent{Label: "com.example.agent", Path: "/Library/LaunchDaemons/com.example.agent.plist"}, false},
		{FoundAgent{Label: "com.example.file", Path: "/Users/example/Library/LaunchAgents/com.example.file.plist"}, false},
		{FoundAgent{Path: "/Users/example/Library/LaunchAgents/nolabel.plist"}, false},
	} {
		if got := JobLoaded(ctx, fake.run, 501, test.job); got != test.want {
			t.Errorf("JobLoaded(%+v) = %v, want %v", test.job, got, test.want)
		}
	}
}

// Homebrew's service takes over from the OwnGit agent of the same state
// directory and leaves any other agent alone.
func TestYieldLaunchAgent(t *testing.T) {
	home := t.TempDir()
	plan := launchAgentPlan(home)
	agent, err := RenderLaunchAgent(plan)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fake := &fakeLaunchd{gui: true}
	if _, err := InstallLaunchAgent(ctx, fake.run, plan, agent, 501, true); err != nil {
		t.Fatal(err)
	}
	if removed, err := YieldLaunchAgent(ctx, fake.run, 501, home, "/Users/example/other-state"); removed || err != nil {
		t.Fatalf("yield for another state directory: %v, %v", removed, err)
	}
	if removed, err := YieldLaunchAgent(ctx, fake.run, 501, home, plan.StateDir); !removed || err != nil || fake.loaded != "" {
		t.Fatalf("yield: removed %v, err %v, loaded %q", removed, err, fake.loaded)
	}
	if _, err := os.Stat(LaunchAgentPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the agent stayed: %v", err)
	}
	if removed, err := YieldLaunchAgent(ctx, fake.run, 501, home, plan.StateDir); removed || err != nil {
		t.Fatalf("yield without an agent: %v, %v", removed, err)
	}
}

func TestDesktopUID(t *testing.T) {
	for _, test := range []struct{ uid, console, want int }{
		{501, 501, 501},
		{501, 502, 501},
		{0, 501, 501}, // sudo on a Mac where someone is logged in on the screen
		{0, 0, 0},     // root while the login window shows
		{0, -1, 0},
	} {
		if got := DesktopUID(test.uid, test.console); got != test.want {
			t.Errorf("DesktopUID(%d, %d) = %d, want %d", test.uid, test.console, got, test.want)
		}
	}
}
