package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// unitMarker is the first line of every unit that "owngit service install"
// writes. A unit without it belongs to someone else and is never replaced.
const unitMarker = `# Written by "owngit service install".`

// heredocEnd ends the unit text in the root script.
const heredocEnd = "OWNGIT_UNIT_END"

// Plan is one service installation.
type Plan struct {
	Mode Mode
	// Executable is the absolute path of the owngit binary the unit starts.
	Executable string
	// StateDir is the absolute state directory the unit passes.
	StateDir string
	// User, Group and Home name the account of a system unit.
	User, Group, Home string
	// Headless is passed as --headless=true or --headless=false: with
	// true, the first start before setup listens on every address when no
	// listen address is saved.
	Headless bool
	// Path is the PATH of the service. Empty keeps the systemd default.
	Path string
}

// UnitPath is where the plan's unit lives. userConfigDir is the installing
// user's configuration directory, used by ModeUser.
func (plan Plan) UnitPath(userConfigDir string) string {
	if plan.Mode == ModeUser {
		return UserUnitPath(userConfigDir)
	}
	return SystemUnitPath
}

// UserUnitPath is the unit of ModeUser in the configuration directory.
func UserUnitPath(userConfigDir string) string {
	return filepath.Join(userConfigDir, "systemd", "user", UnitName)
}

// JournalCommand is the command that follows the service log.
func (mode Mode) JournalCommand() string {
	if mode == ModeUser {
		return "journalctl --user -u " + UnitName + " -f"
	}
	return "sudo journalctl -u " + UnitName + " -f"
}

// RenderUnit writes the systemd unit of a user or system plan.
//
// Every unit starts "owngit serve" with an absolute --state-dir and
// --no-open, so the service and the command line use the same state and no
// browser is opened by a background process. It never passes --listen or
// --base-url, which would override the saved network settings.
func RenderUnit(plan Plan) (string, error) {
	if plan.Mode != ModeUser && plan.Mode != ModeSystem && plan.Mode != ModeAccount {
		return "", fmt.Errorf("no systemd unit for mode %q", plan.Mode)
	}
	if err := checkAbsolute("executable", plan.Executable); err != nil {
		return "", err
	}
	if err := checkAbsolute("state directory", plan.StateDir); err != nil {
		return "", err
	}
	system := plan.Mode != ModeUser
	var unit strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&unit, format+"\n", args...) }
	line("%s", unitMarker)
	line(`# Run "owngit service install" again to update it, or "owngit service uninstall"`)
	line("# to remove it. Both keep the state directory and the repositories.")
	line("[Unit]")
	line("Description=OwnGit private Git server")
	line("Documentation=https://github.com/juliankang4/owngit/blob/main/docs/OPERATIONS.md#run-as-a-service")
	if system {
		// A saved listen address on a LAN or tailnet interface needs that
		// address to exist; Restart= retries if it comes later.
		line("Wants=network-online.target")
		line("After=network-online.target")
	}
	line("")
	line("[Service]")
	line("Type=exec")
	// --headless is always explicit, because the service can start before
	// a desktop session and must not guess again.
	command := []string{plan.Executable, "serve", "--state-dir", plan.StateDir, "--no-open", "--headless=" + strconv.FormatBool(plan.Headless)}
	execStart, err := execWords(command)
	if err != nil {
		return "", err
	}
	line("ExecStart=%s", execStart)
	if system {
		if plan.User == "" || plan.Group == "" {
			return "", errors.New("a system unit needs a user and a group")
		}
		for _, name := range []string{plan.User, plan.Group} {
			if !validAccountName(name) {
				return "", fmt.Errorf("unsupported account name %q", name)
			}
		}
		line("User=%s", plan.User)
		line("Group=%s", plan.Group)
	}
	if plan.Path != "" {
		value, err := quoteUnitValue("PATH="+plan.Path, false)
		if err != nil {
			return "", err
		}
		line("Environment=%s", value)
	}
	// State files and repositories stay private to the service account.
	line("UMask=0077")
	line("Restart=on-failure")
	line("RestartSec=5")
	// SIGTERM goes to OwnGit alone, which ends its Git processes, imports
	// and checks in order; whatever is left is killed after the timeout.
	line("KillMode=mixed")
	line("TimeoutStopSec=150")
	// Hardening. None of these limit Git, hooks, checks run on this
	// computer, or checks in Docker, which OwnGit reaches through its socket.
	line("NoNewPrivileges=yes")
	line("RestrictSUIDSGID=yes")
	line("LockPersonality=yes")
	line("RestrictRealtime=yes")
	if system {
		// These need file system namespaces, which only system units get
		// without unprivileged user namespaces.
		// System folders are read-only. Everything else follows the file
		// permissions of the account, so a repository folder that the
		// account may write, such as /srv/git, works as it does outside
		// the service. The home folder of a system unit stays writable,
		// because checks run there with that account's caches and tools.
		line("ProtectSystem=full")
		line("PrivateTmp=yes")
		if plan.Mode == ModeAccount {
			// The account keeps everything in its own home, outside /home.
			line("ProtectHome=yes")
		}
		line("ProtectKernelTunables=yes")
		line("ProtectKernelModules=yes")
		line("ProtectKernelLogs=yes")
		line("ProtectControlGroups=yes")
		line("ProtectClock=yes")
		line("ProtectHostname=yes")
		line("RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK")
		// The service never runs as root, so it needs no capabilities, and
		// it sees only its own processes.
		line("CapabilityBoundingSet=")
		line("SystemCallArchitectures=native")
		line("ProtectProc=invisible")
	}
	line("")
	line("[Install]")
	if system {
		line("WantedBy=multi-user.target")
	} else {
		line("WantedBy=default.target")
	}
	text := unit.String()
	if strings.Contains(text, "\n"+heredocEnd+"\n") {
		return "", errors.New("unit text contains the script delimiter")
	}
	return text, nil
}

// validAccountName accepts the portable subset of Linux user names.
func validAccountName(name string) bool {
	if name == "" || len(name) > 32 || name[0] == '-' {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// execWords quotes a command line for ExecStart=.
func execWords(words []string) (string, error) {
	quoted := make([]string, len(words))
	for index, word := range words {
		value, err := quoteUnitValue(word, true)
		if err != nil {
			return "", err
		}
		quoted[index] = value
	}
	return strings.Join(quoted, " "), nil
}

// quoteUnitValue double-quotes a value for a unit file. Specifiers are
// escaped as %%, and in ExecStart= also variable references as $$. Control
// characters cannot be written into a unit and are refused.
func quoteUnitValue(value string, exec bool) (string, error) {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for _, r := range value {
		switch {
		case r < 0x20 || r == 0x7f:
			return "", fmt.Errorf("%q contains a control character, which a systemd unit cannot hold", value)
		case r == '\\' || r == '"':
			quoted.WriteByte('\\')
			quoted.WriteRune(r)
		case r == '%':
			quoted.WriteString("%%")
		case r == '$' && exec:
			quoted.WriteString("$$")
		default:
			quoted.WriteRune(r)
		}
	}
	quoted.WriteByte('"')
	return quoted.String(), nil
}

// unquoteUnitWords splits a value written by execWords back into words.
func unquoteUnitWords(value string) []string {
	var words []string
	var word strings.Builder
	inWord, quoted := false, false
	runes := []rune(value)
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		switch {
		case quoted && r == '\\' && index+1 < len(runes):
			index++
			word.WriteRune(runes[index])
		case r == '"':
			quoted, inWord = !quoted, true
		case !quoted && (r == ' ' || r == '\t'):
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		case (r == '%' || r == '$') && index+1 < len(runes) && runes[index+1] == r:
			index++
			word.WriteRune(r)
			inWord = true
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// Installed describes a unit found on this computer.
type Installed struct {
	Mode       Mode
	UnitPath   string
	User       string
	Executable string
	StateDir   string
	Headless   bool
}

// ErrForeignUnit means a unit named owngit.service exists that "owngit
// service install" did not write.
var ErrForeignUnit = errors.New("a unit that owngit service install did not write already exists")

// systemUnitPath is SystemUnitPath; tests use a temporary file.
var systemUnitPath = SystemUnitPath

// FindInstalled looks for the system unit, then for the user unit in
// userConfigDir. It returns false when neither exists.
func FindInstalled(userConfigDir string) (Installed, bool, error) {
	for _, path := range []string{systemUnitPath, UserUnitPath(userConfigDir)} {
		installed, err := ReadUnit(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return installed, err == nil || errors.Is(err, ErrForeignUnit), err
	}
	return Installed{}, false, nil
}

// ReadUnit reads a unit written by RenderUnit.
func ReadUnit(path string) (Installed, error) {
	file, err := os.Open(path)
	if err != nil {
		return Installed{}, err
	}
	defer file.Close()
	installed := Installed{UnitPath: path, Mode: ModeUser}
	if path == systemUnitPath {
		installed.Mode = ModeSystem
	}
	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		text := scanner.Text()
		if first {
			if text != unitMarker {
				return installed, fmt.Errorf("%w: %s", ErrForeignUnit, path)
			}
			first = false
			continue
		}
		key, value, _ := strings.Cut(text, "=")
		switch key {
		case "User":
			installed.User = value
		case "ExecStart":
			words := unquoteUnitWords(value)
			if len(words) > 0 {
				installed.Executable = words[0]
			}
			for index, word := range words {
				switch {
				case word == "--state-dir" && index+1 < len(words):
					installed.StateDir = words[index+1]
				case word == "--headless" || word == "--headless=true":
					installed.Headless = true
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return installed, err
	}
	if first {
		return installed, fmt.Errorf("%w: %s", ErrForeignUnit, path)
	}
	if installed.Mode == ModeSystem && installed.User == AccountName {
		installed.Mode = ModeAccount
	}
	return installed, nil
}

// RootInstallScript is the POSIX shell script that does every root step of
// a system plan at once: for ModeAccount the account, its directories and
// the pointer file, then the unit, daemon-reload, enable and restart. It can
// run again at any time; each step leaves an existing result in place or
// replaces it whole.
func RootInstallScript(plan Plan, unit string) (string, error) {
	if plan.Mode != ModeSystem && plan.Mode != ModeAccount {
		return "", fmt.Errorf("mode %q needs no root steps", plan.Mode)
	}
	var script strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&script, format+"\n", args...) }
	line("#!/bin/sh")
	line(`# Written by "owngit service install": the steps that need root.`)
	line("set -eu")
	line("umask 022")
	if plan.Mode == ModeAccount {
		if !strings.HasPrefix(plan.StateDir, strings.TrimSuffix(plan.Home, "/")+"/") {
			return "", fmt.Errorf("the state directory of the %s account must be inside %s", plan.User, plan.Home)
		}
		group, user, home, stateDir := shellQuote(plan.Group), shellQuote(plan.User), shellQuote(plan.Home), shellQuote(plan.StateDir)
		line("getent group %s >/dev/null || groupadd --system %s", group, group)
		line(`getent passwd %s >/dev/null || useradd --system --gid %s --home-dir %s --no-create-home --shell "$(command -v nologin || echo /bin/false)" --comment 'OwnGit service' %s`, user, group, home, user)
		// Root creates only the home, in a folder only root can change, and
		// only when it is missing. Everything inside belongs to the account,
		// which could have replaced any of it with a link, so the account
		// creates the state directory itself.
		line("[ -d %s ] || install -d -m 0700 -o %s -g %s %s", home, user, group, home)
		line("setpriv --reuid=%s --regid=%s --init-groups mkdir -p -m 0700 %s", user, group, stateDir)
		line("install -d -m 0755 %s", shellQuote(filepath.Dir(PointerPath)))
		line("printf '%%s\\n' %s >%s", stateDir, shellQuote(PointerPath+".new"))
		line("mv -f %s %s", shellQuote(PointerPath+".new"), shellQuote(PointerPath))
	}
	line("mkdir -p %s", shellQuote(filepath.Dir(SystemUnitPath)))
	line("cat >%s <<'%s'", shellQuote(SystemUnitPath+".new"), heredocEnd)
	script.WriteString(unit)
	line("%s", heredocEnd)
	line("mv -f %s %s", shellQuote(SystemUnitPath+".new"), shellQuote(SystemUnitPath))
	line("systemctl daemon-reload")
	line("systemctl enable --quiet %s", UnitName)
	line("systemctl restart %s", UnitName)
	return script.String(), nil
}

// RootUninstallScript stops and removes the system unit. It removes no
// state, repository, account or pointer file.
func RootUninstallScript() string {
	return strings.Join([]string{
		"#!/bin/sh",
		`# Written by "owngit service uninstall": the steps that need root.`,
		"set -eu",
		"systemctl disable --now --quiet " + UnitName + " || true",
		"rm -f " + shellQuote(SystemUnitPath),
		"systemctl daemon-reload",
		"",
	}, "\n")
}

// shellQuote quotes a word for a POSIX shell.
func shellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// ShellQuote quotes a word for a POSIX shell, for commands shown to the
// owner.
func ShellQuote(word string) string { return shellQuote(word) }

// UserSystemctl is the systemctl command line for ModeUser.
func UserSystemctl(args ...string) []string {
	return append([]string{"systemctl", "--user"}, args...)
}

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// InstallUserUnit writes the unit of a ModeUser plan and starts it: enable
// for boot, then restart, which also starts a stopped service and moves a
// running one to the new binary.
func InstallUserUnit(ctx context.Context, run Runner, path, unit string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	for _, step := range [][]string{{"daemon-reload"}, {"enable", "--quiet", UnitName}, {"restart", UnitName}} {
		if err := runStep(ctx, run, UserSystemctl(step...)); err != nil {
			return err
		}
	}
	return nil
}

// UninstallUserUnit stops the user unit and removes it. The state directory
// and the repositories stay.
func UninstallUserUnit(ctx context.Context, run Runner, path string) error {
	// A unit that is already stopped or disabled is not an error.
	_, _ = run(ctx, "systemctl", "--user", "disable", "--now", "--quiet", UnitName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return runStep(ctx, run, UserSystemctl("daemon-reload"))
}

func runStep(ctx context.Context, run Runner, command []string) error {
	output, err := run(ctx, command[0], command[1:]...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("%s: %w: %s", strings.Join(command, " "), err, detail)
		}
		return fmt.Errorf("%s: %w", strings.Join(command, " "), err)
	}
	return nil
}
