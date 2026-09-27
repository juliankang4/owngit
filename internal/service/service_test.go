package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func environ(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestHeadlessDetection(t *testing.T) {
	ssh := map[string]string{"SSH_CONNECTION": "192.0.2.10 50000 192.0.2.20 22"}
	sshWithDisplay := map[string]string{"SSH_CONNECTION": "192.0.2.10 50000 192.0.2.20 22", "DISPLAY": "localhost:10.0"}
	desktop := map[string]string{"WAYLAND_DISPLAY": "wayland-1"}
	for _, test := range []struct {
		name string
		env  Environment
		want bool
	}{
		{"desktop terminal", Environment{Getenv: environ(desktop), EUID: 1000, Linux: true, GraphicalSession: true}, false},
		{"service on a desktop", Environment{Getenv: environ(nil), EUID: 1000, Linux: true, GraphicalSession: true}, false},
		{"SSH without a display", Environment{Getenv: environ(ssh), EUID: 1000, Linux: true, GraphicalSession: true}, true},
		{"SSH_TTY alone", Environment{Getenv: environ(map[string]string{"SSH_TTY": "/dev/pts/1"}), EUID: 1000, Linux: true, GraphicalSession: true}, true},
		{"SSH with X forwarding", Environment{Getenv: environ(sshWithDisplay), EUID: 1000, Linux: true, GraphicalSession: true}, false},
		{"no graphical session", Environment{Getenv: environ(nil), EUID: 1000, Linux: true}, true},
		{"a display without logind", Environment{Getenv: environ(desktop), EUID: 1000, Linux: true}, false},
		{"X forwarding without logind", Environment{Getenv: environ(sshWithDisplay), EUID: 1000, Linux: true}, true},
		{"root in a container", Environment{Getenv: environ(desktop), EUID: 0, Linux: true, InContainer: true, GraphicalSession: true}, true},
		{"user in a container with a desktop", Environment{Getenv: environ(desktop), EUID: 1000, Linux: true, InContainer: true, GraphicalSession: true}, false},
		{"root at a desktop", Environment{Getenv: environ(desktop), EUID: 0, Linux: true, GraphicalSession: true}, false},
		{"not Linux", Environment{Getenv: environ(ssh), EUID: 501}, false},
	} {
		if got := test.env.Headless(); got != test.want {
			t.Errorf("%s: Headless() = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestChooseMode(t *testing.T) {
	desktop := Environment{Getenv: environ(map[string]string{"DISPLAY": ":0"}), EUID: 1000, Linux: true, GraphicalSession: true}
	ssh := Environment{Getenv: environ(map[string]string{"SSH_CONNECTION": "x"}), EUID: 1000, Linux: true, GraphicalSession: true}
	root := Environment{Getenv: environ(nil), EUID: 0, Linux: true, InContainer: true}
	for _, test := range []struct {
		name     string
		env      Environment
		homebrew bool
		want     Mode
	}{
		{"desktop", desktop, false, ModeUser},
		{"SSH", ssh, false, ModeSystem},
		{"root", root, false, ModeAccount},
		{"Homebrew binary", ssh, true, ModeHomebrew},
		{"root with a Homebrew binary", root, true, ModeAccount},
	} {
		if got := test.env.ChooseMode(test.homebrew); got != test.want {
			t.Errorf("%s: ChooseMode = %s, want %s", test.name, got, test.want)
		}
	}
}

func TestHomebrewPrefix(t *testing.T) {
	unixPathsOnly(t)
	for path, want := range map[string]string{
		"/home/linuxbrew/.linuxbrew/Cellar/owngit/1.1.1/bin/owngit": "/home/linuxbrew/.linuxbrew",
		"/opt/homebrew/Cellar/owngit/1.1.1/bin/owngit":              "/opt/homebrew",
		"/usr/local/bin/owngit":                                     "",
		"/home/you/Cellar/other/1.0/bin/owngit":                     "",
	} {
		if got := HomebrewPrefix(path); got != want {
			t.Errorf("HomebrewPrefix(%q) = %q, want %q", path, got, want)
		}
	}
}

func unitLines(unit string) map[string][]string {
	lines := map[string][]string{}
	for _, line := range strings.Split(unit, "\n") {
		if key, value, found := strings.Cut(line, "="); found && !strings.HasPrefix(line, "#") {
			lines[key] = append(lines[key], value)
		}
	}
	return lines
}

func TestRenderUnitForEachMode(t *testing.T) {
	unixPathsOnly(t)
	user, err := RenderUnit(Plan{Mode: ModeUser, Executable: "/usr/bin/owngit", StateDir: "/home/you/.config/owngit", Path: "/usr/bin:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	lines := unitLines(user)
	if !strings.HasPrefix(user, unitMarker+"\n") {
		t.Errorf("user unit does not start with the marker:\n%s", user)
	}
	if got := lines["ExecStart"]; !reflect.DeepEqual(got, []string{`"/usr/bin/owngit" "serve" "--state-dir" "/home/you/.config/owngit" "--no-open" "--headless=false"`}) {
		t.Errorf("user ExecStart = %q", got)
	}
	if lines["WantedBy"][0] != "default.target" || lines["User"] != nil || lines["ProtectSystem"] != nil || lines["NoNewPrivileges"][0] != "yes" {
		t.Errorf("user unit:\n%s", user)
	}
	if lines["Environment"][0] != `"PATH=/usr/bin:/bin"` {
		t.Errorf("user Environment = %q", lines["Environment"])
	}

	system, err := RenderUnit(Plan{
		Mode: ModeSystem, Executable: "/usr/local/bin/owngit", StateDir: "/home/you/.config/owngit", Headless: true,
		User: "owner", Group: "owner", Home: "/home/you",
	})
	if err != nil {
		t.Fatal(err)
	}
	lines = unitLines(system)
	if got := lines["ExecStart"][0]; !strings.HasSuffix(got, `"--no-open" "--headless=true"`) {
		t.Errorf("headless ExecStart = %q", got)
	}
	if lines["User"][0] != "owner" || lines["Group"][0] != "owner" || lines["WantedBy"][0] != "multi-user.target" || lines["ProtectSystem"][0] != "full" || lines["PrivateTmp"][0] != "yes" {
		t.Errorf("system unit:\n%s", system)
	}
	// Outside the system folders the account's own file permissions
	// decide, so a repository folder such as /srv/git works.
	if lines["ReadWritePaths"] != nil || lines["ProtectHome"] != nil {
		t.Errorf("a system unit of the installing user must keep its home writable:\n%s", system)
	}

	account, err := RenderUnit(Plan{
		Mode: ModeAccount, Executable: "/usr/local/bin/owngit", StateDir: AccountStateDir,
		User: AccountName, Group: AccountName, Home: AccountHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines = unitLines(account)
	if lines["User"][0] != AccountName || lines["ProtectHome"][0] != "yes" || lines["ProtectSystem"][0] != "full" || lines["ReadWritePaths"] != nil {
		t.Errorf("account unit:\n%s", account)
	}
	// Units that systemd starts as another account also drop every
	// capability, native system calls only, and hide other processes.
	// These need namespaces, which a user unit may not have.
	for name, unit := range map[string]string{"system": system, "account": account, "user": user} {
		lines := unitLines(unit)
		hardened := lines["CapabilityBoundingSet"] != nil && lines["CapabilityBoundingSet"][0] == "" &&
			lines["SystemCallArchitectures"] != nil && lines["SystemCallArchitectures"][0] == "native" &&
			lines["ProtectProc"] != nil && lines["ProtectProc"][0] == "invisible"
		if hardened != (name != "user") {
			t.Errorf("%s unit hardened=%v:\n%s", name, hardened, unit)
		}
	}
	for _, unit := range []string{user, system, account} {
		for _, override := range []string{"--listen", "--base-url", "--allowed-host", "--trusted-proxy"} {
			if strings.Contains(unit, override) {
				t.Errorf("unit passes %s, which would override the saved settings:\n%s", override, unit)
			}
		}
	}
}

func TestRenderUnitQuotesAndRefuses(t *testing.T) {
	unixPathsOnly(t)
	unit, err := RenderUnit(Plan{Mode: ModeUser, Executable: `/opt/own git/100%/$HOME/"q"\b/owngit`, StateDir: "/home/you/state"})
	if err != nil {
		t.Fatal(err)
	}
	execStart := unitLines(unit)["ExecStart"][0]
	if want := `"/opt/own git/100%%/$$HOME/\"q\"\\b/owngit"`; !strings.HasPrefix(execStart, want+" ") {
		t.Errorf("ExecStart = %s, want it to start with %s", execStart, want)
	}
	path := filepath.Join(t.TempDir(), "owngit.service")
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err := ReadUnit(path)
	if err != nil || installed.Executable != `/opt/own git/100%/$HOME/"q"\b/owngit` || installed.StateDir != "/home/you/state" {
		t.Errorf("ReadUnit = %+v, %v", installed, err)
	}
	for _, plan := range []Plan{
		{Mode: ModeUser, Executable: "/usr/bin/owngit", StateDir: "/home/you/new\nline"},
		{Mode: ModeUser, Executable: "owngit", StateDir: "/home/you/state"},
		{Mode: ModeUser, Executable: "/usr/bin/owngit", StateDir: "relative/state"},
		{Mode: ModeSystem, Executable: "/usr/bin/owngit", StateDir: "/s", User: "bad name", Group: "g"},
		{Mode: ModeSystem, Executable: "/usr/bin/owngit", StateDir: "/s", User: "", Group: "g"},
		{Mode: ModeHomebrew, Executable: "/usr/bin/owngit", StateDir: "/s"},
	} {
		if _, err := RenderUnit(plan); err == nil {
			t.Errorf("RenderUnit(%+v) succeeded", plan)
		}
	}
}

func TestReadUnitRecognizesModesAndForeignUnits(t *testing.T) {
	unixPathsOnly(t)
	dir := t.TempDir()
	unit, err := RenderUnit(Plan{Mode: ModeUser, Executable: "/usr/bin/owngit", StateDir: "/home/you/.config/owngit"})
	if err != nil {
		t.Fatal(err)
	}
	userPath := UserUnitPath(dir)
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err := ReadUnit(userPath)
	if err != nil || installed.Mode != ModeUser || installed.StateDir != "/home/you/.config/owngit" || installed.Headless {
		t.Errorf("user unit read as %+v, %v", installed, err)
	}
	// The headless choice survives a re-install, including from a unit of
	// an earlier version that passed a bare --headless.
	for execStart, want := range map[string]bool{
		`"/usr/bin/owngit" "serve" "--state-dir" "/s" "--no-open" "--headless=true"`:  true,
		`"/usr/bin/owngit" "serve" "--state-dir" "/s" "--no-open" "--headless"`:       true,
		`"/usr/bin/owngit" "serve" "--state-dir" "/s" "--no-open" "--headless=false"`: false,
		`"/usr/bin/owngit" "serve" "--state-dir" "/s" "--no-open"`:                    false,
	} {
		noErr(t, os.WriteFile(userPath, []byte(unitMarker+"\n[Service]\nExecStart="+execStart+"\n"), 0o644))
		if installed, err := ReadUnit(userPath); err != nil || installed.Headless != want {
			t.Errorf("ReadUnit(%s): Headless=%v, %v; want %v", execStart, installed.Headless, err, want)
		}
	}
	foreign := filepath.Join(dir, "foreign.service")
	if err := os.WriteFile(foreign, []byte("[Service]\nExecStart=/usr/bin/owngit serve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUnit(foreign); !errors.Is(err, ErrForeignUnit) {
		t.Errorf("foreign unit: err = %v, want ErrForeignUnit", err)
	}
	if _, err := ReadUnit(filepath.Join(dir, "missing.service")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing unit: err = %v", err)
	}
}

func TestRootScripts(t *testing.T) {
	unixPathsOnly(t)
	plan := Plan{
		Mode: ModeAccount, Executable: "/usr/local/bin/owngit", StateDir: AccountStateDir,
		User: AccountName, Group: AccountName, Home: AccountHome,
	}
	unit, err := RenderUnit(plan)
	if err != nil {
		t.Fatal(err)
	}
	script, err := RootInstallScript(plan, unit)
	if err != nil {
		t.Fatal(err)
	}
	// The unit is written verbatim through a quoted here-document, then
	// moved into place whole, so a rerun replaces it.
	if !strings.Contains(script, "cat >'/etc/systemd/system/owngit.service.new' <<'"+heredocEnd+"'\n"+unit+heredocEnd+"\n") ||
		!strings.Contains(script, "mv -f '/etc/systemd/system/owngit.service.new' '/etc/systemd/system/owngit.service'") {
		t.Errorf("script does not write the unit verbatim:\n%s", script)
	}
	// Every account step leaves an earlier result in place.
	for _, want := range []string{
		"set -eu", "getent group 'owngit' >/dev/null || groupadd --system 'owngit'",
		"getent passwd 'owngit' >/dev/null || useradd --system --gid 'owngit' --home-dir '/var/lib/owngit' --no-create-home",
		"[ -d '/var/lib/owngit' ] || install -d -m 0700 -o 'owngit' -g 'owngit' '/var/lib/owngit'",
		"setpriv --reuid='owngit' --regid='owngit' --init-groups mkdir -p -m 0700 '/var/lib/owngit/state'",
		"printf '%s\\n' '/var/lib/owngit/state' >'/etc/owngit/state-dir.new'", "mv -f '/etc/owngit/state-dir.new' '/etc/owngit/state-dir'",
		"systemctl daemon-reload\nsystemctl enable --quiet owngit.service\nsystemctl restart owngit.service\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("account script lacks %q:\n%s", want, script)
		}
	}
	// Root creates nothing inside the account's home and changes no owner
	// there, because the account may have replaced what is inside with a
	// link to a file of root's.
	for _, line := range strings.Split(strings.Replace(script, unit, "", 1), "\n") {
		if strings.Contains(line, "chown") || strings.Contains(line, AccountStateDir) && !strings.HasPrefix(line, "setpriv ") && !strings.HasPrefix(line, "printf ") {
			t.Errorf("root touches the state directory: %q", line)
		}
	}
	outside := plan
	outside.StateDir = "/etc/owngit-state"
	if _, err := RootInstallScript(outside, unit); err == nil || !strings.Contains(err.Error(), AccountHome) {
		t.Errorf("an account state directory outside its home got a script: %v", err)
	}
	plan.Mode, plan.User, plan.Group = ModeSystem, "owner", "owner"
	systemUnit, err := RenderUnit(plan)
	if err != nil {
		t.Fatal(err)
	}
	system, err := RootInstallScript(plan, systemUnit)
	if err != nil || strings.Contains(system, "useradd") || strings.Contains(system, PointerPath) {
		t.Errorf("system script creates an account or a pointer: %v\n%s", err, system)
	}
	if _, err := RootInstallScript(Plan{Mode: ModeUser}, unit); err == nil {
		t.Error("a user plan got a root script")
	}
	// Uninstalling keeps every piece of data.
	uninstall := RootUninstallScript()
	for _, kept := range []string{AccountHome, "state", PointerPath, "userdel", "/home"} {
		if strings.Contains(uninstall, kept) {
			t.Errorf("uninstall script touches %q:\n%s", kept, uninstall)
		}
	}
	if !strings.Contains(uninstall, "rm -f '/etc/systemd/system/owngit.service'") || !strings.Contains(uninstall, "disable --now") {
		t.Errorf("uninstall script:\n%s", uninstall)
	}
}

type recordedRunner struct{ commands []string }

func (runner *recordedRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, strings.Join(append([]string{name}, args...), " "))
	return nil, nil
}

// Installing again replaces the unit and restarts the service, with the
// same steps every time; uninstalling removes only the unit.
func TestUserUnitInstallIsIdempotentAndUninstallKeepsData(t *testing.T) {
	unixPathsOnly(t)
	root := t.TempDir()
	systemUnitPath = filepath.Join(root, "system", UnitName)
	t.Cleanup(func() { systemUnitPath = SystemUnitPath })
	configDir := filepath.Join(root, "config")
	stateDir := filepath.Join(configDir, "owngit")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateDir, "owngit.db")
	if err := os.WriteFile(stateFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := UserUnitPath(configDir)
	steps := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --quiet owngit.service",
		"systemctl --user restart owngit.service",
	}
	for index, executable := range []string{"/usr/bin/owngit", "/usr/local/bin/owngit", "/usr/local/bin/owngit"} {
		unit, err := RenderUnit(Plan{Mode: ModeUser, Executable: executable, StateDir: stateDir})
		if err != nil {
			t.Fatal(err)
		}
		runner := &recordedRunner{}
		if err := InstallUserUnit(context.Background(), runner.run, path, unit); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(runner.commands, steps) {
			t.Errorf("install %d ran %q, want %q", index+1, runner.commands, steps)
		}
		written, err := os.ReadFile(path)
		if err != nil || string(written) != unit {
			t.Errorf("install %d wrote %q, %v", index+1, written, err)
		}
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) != 1 {
			t.Errorf("install %d left %d files next to the unit", index+1, len(entries))
		}
	}
	installed, found, err := FindInstalled(configDir)
	if err != nil || !found || installed.Mode != ModeUser || installed.Executable != "/usr/local/bin/owngit" {
		t.Errorf("FindInstalled = %+v, %v, %v", installed, found, err)
	}
	runner := &recordedRunner{}
	if err := UninstallUserUnit(context.Background(), runner.run, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unit still exists after uninstall: %v", err)
	}
	if content, err := os.ReadFile(stateFile); err != nil || string(content) != "data" {
		t.Errorf("state after uninstall: %q, %v", content, err)
	}
	if want := []string{"systemctl --user disable --now --quiet owngit.service", "systemctl --user daemon-reload"}; !reflect.DeepEqual(runner.commands, want) {
		t.Errorf("uninstall ran %q, want %q", runner.commands, want)
	}
	if _, found, err := FindInstalled(configDir); found || err != nil {
		t.Errorf("FindInstalled after uninstall = %v, %v", found, err)
	}
	// Uninstalling what is gone is not an error.
	if err := UninstallUserUnit(context.Background(), (&recordedRunner{}).run, path); err != nil {
		t.Errorf("second uninstall: %v", err)
	}
}

func TestReadPointer(t *testing.T) {
	unixPathsOnly(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "state-dir")
	if got, err := ReadPointer(path); got != "" || err != nil {
		t.Errorf("missing pointer = %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(AccountStateDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadPointer(path); got != AccountStateDir || err != nil {
		t.Errorf("pointer = %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("var/lib/owngit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPointer(path); err == nil {
		t.Error("a relative pointer was accepted")
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// unixPathsOnly skips a test of units, pointers and Homebrew prefixes, which
// hold Unix paths: "owngit service" runs on Linux, and those paths are not
// absolute on Windows.
func unixPathsOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("systemd units and Homebrew prefixes use Unix paths; owngit service runs on Linux only")
	}
}
