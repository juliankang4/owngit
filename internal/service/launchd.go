package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The macOS service is a LaunchAgent of the installing user. launchd starts
// it when that user logs in at the desktop and keeps it running. It needs
// no administrator password and no entitlement, and it starts the owngit
// binary by path, so a signed build that replaces the file later keeps the
// same agent.

// LaunchAgentLabel names the agent that "owngit service install" writes,
// in reverse-DNS form under the project's domain owngit.app.
const LaunchAgentLabel = "app.owngit.server"

// HomebrewLabels are the labels of the agent that "brew services" writes
// for the formula: sh.brew.owngit in current Homebrew, homebrew.mxcl.owngit
// in earlier releases.
var HomebrewLabels = []string{"sh.brew.owngit", "homebrew.mxcl.owngit"}

// launchctl is launchd's command line tool.
const launchctl = "/bin/launchctl"

// plistMarker is in every agent that "owngit service install" writes. A
// file at LaunchAgentPath without it belongs to someone else.
const plistMarker = `<!-- Written by "owngit service install". Run it again to update this file, or run "owngit service uninstall" to remove it. Both keep the state directory, the repositories and the log. -->`

// LaunchAgentPath is the agent file in the user's LaunchAgents folder.
func LaunchAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}

// LaunchAgentLogPath is the agent's server log, which "owngit serve
// --log-file" keeps below 10 MiB with one older file beside it.
func LaunchAgentLogPath(home string) string {
	return filepath.Join(home, "Library", "Logs", "owngit", "owngit.log")
}

// LaunchAgentOutputPath is the file that receives the agent's standard
// output and error. The log does not go there (see --service), so it holds
// only what the log cannot, such as a crash report, and stays small.
// launchd does not bound it.
func LaunchAgentOutputPath(home string) string {
	return filepath.Join(home, "Library", "Logs", "owngit", "owngit.stderr.log")
}

// RenderLaunchAgent writes the LaunchAgent property list of a plan with
// ModeLaunchAgent. plan.Home is the user's home folder.
//
// The agent runs "owngit serve" with an absolute --state-dir, --no-open,
// --log-file LaunchAgentLogPath, --service and --headless=true or
// --headless=false at login (RunAtLoad) and again
// whenever it ends (KeepAlive). It loads in the desktop session and, for an
// install over SSH while the user is not logged in at the desktop, in the
// user's background session; launchd never runs both at once. Like the
// systemd units it passes the installing shell's PATH, a private umask and a
// stop timeout long enough for an orderly shutdown, and never --listen or
// --base-url.
func RenderLaunchAgent(plan Plan) (string, error) {
	if plan.Mode != ModeLaunchAgent {
		return "", fmt.Errorf("no LaunchAgent for mode %q", plan.Mode)
	}
	for label, path := range map[string]string{"executable": plan.Executable, "state directory": plan.StateDir, "home folder": plan.Home} {
		if err := checkAbsolute(label, path); err != nil {
			return "", err
		}
	}
	// --headless is always explicit: the agent can start in the background
	// domain while nobody is logged in on the screen, where serve would
	// otherwise decide again that the Mac has no screen.
	arguments := []string{plan.Executable, "serve", "--state-dir", plan.StateDir, "--no-open", "--log-file", LaunchAgentLogPath(plan.Home), "--service", "--headless=" + strconv.FormatBool(plan.Headless)}
	var text strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&text, format+"\n", args...) }
	var failed error
	str := func(value string) string {
		escaped, err := plistString(value)
		if err != nil && failed == nil {
			failed = err
		}
		return "<string>" + escaped + "</string>"
	}
	line(`<?xml version="1.0" encoding="UTF-8"?>`)
	line(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`)
	line("%s", plistMarker)
	line(`<plist version="1.0">`)
	line("<dict>")
	line("\t<key>Label</key>")
	line("\t%s", str(LaunchAgentLabel))
	line("\t<key>ProgramArguments</key>")
	line("\t<array>")
	for _, argument := range arguments {
		line("\t\t%s", str(argument))
	}
	line("\t</array>")
	if plan.Path != "" {
		line("\t<key>EnvironmentVariables</key>")
		line("\t<dict>")
		line("\t\t<key>PATH</key>")
		line("\t\t%s", str(plan.Path))
		line("\t</dict>")
	}
	line("\t<key>RunAtLoad</key>")
	line("\t<true/>")
	line("\t<key>KeepAlive</key>")
	line("\t<true/>")
	// Aqua is the desktop session. Background lets "owngit service install"
	// start the agent over SSH while the user is not logged in at the
	// desktop; it runs until the Mac restarts, and a later desktop login
	// finds it loaded and does not start a second one.
	line("\t<key>LimitLoadToSessionType</key>")
	line("\t<array>")
	line("\t\t%s", str("Aqua"))
	line("\t\t%s", str("Background"))
	line("\t</array>")
	// 63 is umask 077: state files and repositories stay private.
	line("\t<key>Umask</key>")
	line("\t<integer>63</integer>")
	// SIGTERM lets OwnGit end Git processes, imports and checks in order.
	line("\t<key>ExitTimeOut</key>")
	line("\t<integer>150</integer>")
	output := LaunchAgentOutputPath(plan.Home)
	line("\t<key>StandardOutPath</key>")
	line("\t%s", str(output))
	line("\t<key>StandardErrorPath</key>")
	line("\t%s", str(output))
	line("</dict>")
	line("</plist>")
	if failed != nil {
		return "", failed
	}
	return text.String(), nil
}

// plistString escapes a value for a property list string. Control
// characters and invalid UTF-8 cannot be written into one and are refused.
func plistString(value string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", fmt.Errorf("%q contains a control character, which a LaunchAgent cannot hold", value)
	}
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
		return "", err
	}
	return escaped.String(), nil
}

// ReadLaunchAgent reads an agent written by RenderLaunchAgent. A file
// without the marker returns ErrForeignUnit.
func ReadLaunchAgent(path string) (Installed, error) {
	installed := Installed{Mode: ModeLaunchAgent, UnitPath: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return installed, err
	}
	if !bytes.Contains(data, []byte(plistMarker)) {
		return installed, fmt.Errorf("%w: %s", ErrForeignUnit, path)
	}
	plist, err := parsePlist(data)
	if err != nil {
		return installed, fmt.Errorf("read %s: %w", path, err)
	}
	words := stringList(plist["ProgramArguments"])
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
	return installed, nil
}

// FoundAgent is a launchd job on this computer that runs "owngit serve"
// and was not written by "owngit service install".
type FoundAgent struct {
	Label string
	Path  string
}

// FindServeAgents looks in the launchd folders dirs for jobs that run
// "owngit serve", other than the labels in skip. It reads property lists
// only; binary ones are converted with plutil through run. Unreadable
// files, folders and jobs marked Disabled are passed over.
func FindServeAgents(ctx context.Context, run Runner, dirs []string, skip ...string) []FoundAgent {
	var found []FoundAgent
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".plist") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			plist, err := readAnyPlist(ctx, run, path)
			if err != nil {
				continue
			}
			label, _ := plist["Label"].(string)
			if disabled, _ := plist["Disabled"].(bool); disabled || slices.Contains(skip, label) {
				continue
			}
			words := stringList(plist["ProgramArguments"])
			if program, ok := plist["Program"].(string); ok {
				words = append([]string{program}, words...)
			}
			if RunsOwnGitServe(words) {
				found = append(found, FoundAgent{Label: label, Path: path})
			}
		}
	}
	return found
}

// JobLoaded reports whether launchd has loaded the job: a daemon in the
// system domain, an agent in the GUI or background domain of uid.
func JobLoaded(ctx context.Context, run Runner, uid int, job FoundAgent) bool {
	if job.Label == "" {
		return false
	}
	domains := []string{guiDomain(uid), userDomain(uid)}
	if filepath.Base(filepath.Dir(job.Path)) == "LaunchDaemons" {
		domains = []string{"system"}
	}
	for _, domain := range domains {
		if _, err := run(ctx, launchctl, "print", domain+"/"+job.Label); err == nil {
			return true
		}
	}
	return false
}

// RunsOwnGitServe reports whether a command line starts "owngit serve":
// an owngit executable (or the npm launcher owngit.js) directly followed by
// serve. Words are also split at spaces, so that a shell command such as
// sh -c "exec /usr/local/bin/owngit serve" counts too.
func RunsOwnGitServe(words []string) bool {
	program := false
	for _, word := range words {
		for _, token := range strings.Fields(word) {
			token = strings.Trim(token, `"';`)
			if program && token == "serve" {
				return true
			}
			base := filepath.Base(token)
			program = base == "owngit" || base == "owngit.js"
		}
	}
	return false
}

// readAnyPlist parses an XML property list, converting a binary one to XML
// with plutil first.
func readAnyPlist(ctx context.Context, run Runner, path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		if run == nil {
			return nil, errors.New("binary property list")
		}
		if data, err = run(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", path); err != nil {
			return nil, err
		}
	}
	return parsePlist(data)
}

// parsePlist parses an XML property list whose top level is a dictionary.
// Strings, numbers and dates are returned as strings, booleans as bool,
// arrays as []any and dictionaries as map[string]any.
func parsePlist(data []byte) (map[string]any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("not a property list: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local == "plist" {
			continue
		}
		value, err := plistValue(decoder, start)
		if err != nil {
			return nil, err
		}
		dict, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("the property list is not a dictionary")
		}
		return dict, nil
	}
}

func plistValue(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict", "array":
		dict, array := map[string]any{}, []any{}
		key, haveKey := "", false
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			switch token := token.(type) {
			case xml.EndElement:
				if start.Name.Local == "dict" {
					return dict, nil
				}
				return array, nil
			case xml.StartElement:
				if start.Name.Local == "dict" && token.Name.Local == "key" {
					if err := decoder.DecodeElement(&key, &token); err != nil {
						return nil, err
					}
					haveKey = true
					continue
				}
				value, err := plistValue(decoder, token)
				if err != nil {
					return nil, err
				}
				if start.Name.Local == "array" {
					array = append(array, value)
				} else if haveKey {
					dict[key], haveKey = value, false
				}
			}
		}
	case "true", "false":
		return start.Name.Local == "true", decoder.Skip()
	default:
		var text string
		err := decoder.DecodeElement(&text, &start)
		return text, err
	}
}

// stringList returns the strings of a property list array.
func stringList(value any) []string {
	items, _ := value.([]any)
	var words []string
	for _, item := range items {
		if word, ok := item.(string); ok {
			words = append(words, word)
		}
	}
	return words
}

// AgentExecutable is the path the agent starts: Homebrew's opt link for a
// binary in the Cellar, whose versioned folder an upgrade removes, and
// otherwise the path the command was started by, so that an agent started
// through a link keeps working when the link moves to a new file.
func AgentExecutable(invoked, resolved string) string {
	if prefix := HomebrewPrefix(resolved); prefix != "" {
		return filepath.Join(prefix, "opt", "owngit", "bin", "owngit")
	}
	if filepath.IsAbs(invoked) {
		return filepath.Clean(invoked)
	}
	return resolved
}

// NPMExecutable reports whether path is the executable of an OwnGit npm
// platform package, such as .../node_modules/owngit-darwin-arm64/bin/owngit.
// npm replaces it in place when the package is updated.
func NPMExecutable(path string) bool {
	bin := filepath.Dir(path)
	platform := filepath.Dir(bin)
	return strings.TrimSuffix(filepath.Base(path), ".exe") == "owngit" && filepath.Base(bin) == "bin" &&
		strings.HasPrefix(filepath.Base(platform), "owngit-") && filepath.Base(filepath.Dir(platform)) == "node_modules"
}

// AgentState is what launchd reports about the agent.
type AgentState struct {
	// Domain is the launchd domain the agent is loaded in, gui/UID or
	// user/UID, or "" when it is not loaded.
	Domain string
	// Running is true while the process runs; PID is its process ID.
	Running bool
	PID     int
	// LastExit is launchd's description of the last exit, if any.
	LastExit string
}

// DesktopUID is the user whose desktop login decides whether a Mac has a
// screen for this process. That is the process's own user, except for root:
// root never has a desktop login of its own, so "sudo owngit serve" on a
// Mac where someone is logged in on the screen looks at that user, the
// owner of /dev/console. consoleOwner is -1 when unknown.
func DesktopUID(uid, consoleOwner int) int {
	if uid == 0 && consoleOwner > 0 {
		return consoleOwner
	}
	return uid
}

func guiDomain(uid int) string  { return "gui/" + strconv.Itoa(uid) }
func userDomain(uid int) string { return "user/" + strconv.Itoa(uid) }

// LaunchAgentStatus asks launchd where the agent is loaded and whether it
// runs.
func LaunchAgentStatus(ctx context.Context, run Runner, uid int) AgentState {
	for _, domain := range []string{guiDomain(uid), userDomain(uid)} {
		output, err := run(ctx, launchctl, "print", domain+"/"+LaunchAgentLabel)
		if err != nil {
			continue
		}
		state := AgentState{Domain: domain}
		for _, line := range strings.Split(string(output), "\n") {
			// Top-level properties of the service are indented once.
			if !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t") {
				continue
			}
			key, value, _ := strings.Cut(strings.TrimSpace(line), " = ")
			switch key {
			case "state":
				state.Running = value == "running"
			case "pid":
				state.PID, _ = strconv.Atoi(value)
			case "last exit code":
				state.LastExit = value
			}
		}
		return state
	}
	return AgentState{}
}

// preferredDomain is where a new agent loads: the desktop session when the
// user is logged in there, otherwise the user's background session.
func preferredDomain(uid int, gui bool) string {
	if gui {
		return guiDomain(uid)
	}
	return userDomain(uid)
}

// launchAgentUnloadTimeout bounds the wait for launchd to finish unloading
// the agent after bootout; launchd itself waits up to ExitTimeOut.
const launchAgentUnloadTimeout = 170 * time.Second

// ErrNotLoaded means launchd did not load the agent. The message says why
// and what to do.
var ErrNotLoaded = errors.New("launchd did not load the OwnGit service")

// InstallLaunchAgent writes the agent of plan and (re)loads it, which also
// starts it. An agent that is already loaded is unloaded first, so launchd
// reads the new file and runs the new binary. gui says whether the user is
// logged in at the desktop. It returns the domain the agent loaded in.
//
// When launchd does not load the new agent, the earlier agent file comes
// back and is loaded again, or, on a first install, the agent file and the
// log folder it created are removed, so a failed install leaves nothing.
func InstallLaunchAgent(ctx context.Context, run Runner, plan Plan, agent string, uid int, gui bool) (string, error) {
	path := LaunchAgentPath(plan.Home)
	logs := []string{LaunchAgentLogPath(plan.Home), LaunchAgentOutputPath(plan.Home)}
	previous, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	_, logMissing := os.Stat(filepath.Dir(logs[0]))
	loadedIn := LaunchAgentStatus(ctx, run, uid).Domain
	undo := func() {
		if readErr == nil {
			_ = writeAgentFile(path, string(previous))
			if loadedIn != "" && LaunchAgentStatus(ctx, run, uid).Domain == "" {
				_ = loadLaunchAgent(ctx, run, loadedIn, path)
			}
			return
		}
		_ = os.Remove(path)
		if errors.Is(logMissing, os.ErrNotExist) {
			for _, log := range logs {
				_ = os.Remove(log)
			}
			_ = os.Remove(filepath.Dir(logs[0]))
		}
	}
	// launchd does not create the log folder, and the job does not start
	// without it. It would create the output file readable by everyone, so
	// both files are created first, or made, for the owner only.
	if err := os.MkdirAll(filepath.Dir(logs[0]), 0o700); err != nil {
		return "", err
	}
	for _, log := range logs {
		logFile, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err == nil {
			err = logFile.Chmod(0o600)
			logFile.Close()
		}
		if err != nil {
			undo()
			return "", err
		}
	}
	if err := writeAgentFile(path, agent); err != nil {
		undo()
		return "", err
	}
	if err := unloadFrom(ctx, run, uid, loadedIn); err != nil {
		undo()
		return "", err
	}
	domain := preferredDomain(uid, gui)
	if err := loadLaunchAgent(ctx, run, domain, path); err != nil {
		undo()
		return "", err
	}
	return domain, nil
}

// writeAgentFile replaces the agent file in one step.
func writeAgentFile(path, agent string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, []byte(agent), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// StartLaunchAgent starts the agent, loading it when it is not loaded.
// restart stops a running agent first.
func StartLaunchAgent(ctx context.Context, run Runner, uid int, gui bool, path string, restart bool) error {
	state := LaunchAgentStatus(ctx, run, uid)
	if state.Domain == "" {
		return loadLaunchAgent(ctx, run, preferredDomain(uid, gui), path)
	}
	arguments := []string{"kickstart"}
	if restart {
		arguments = append(arguments, "-k")
	}
	return runStep(ctx, run, append([]string{launchctl}, append(arguments, state.Domain+"/"+LaunchAgentLabel)...))
}

// StopLaunchAgent unloads the agent, which stops it until the next login
// or StartLaunchAgent. The file stays.
func StopLaunchAgent(ctx context.Context, run Runner, uid int) error {
	return unloadLaunchAgent(ctx, run, uid)
}

// UninstallLaunchAgent stops the agent and removes its file. The state
// directory, the repositories and the log stay.
func UninstallLaunchAgent(ctx context.Context, run Runner, uid int, path string) error {
	if err := unloadLaunchAgent(ctx, run, uid); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func loadLaunchAgent(ctx context.Context, run Runner, domain, path string) error {
	// "launchctl disable" would keep it from loading; this clears it.
	_, _ = run(ctx, launchctl, "enable", domain+"/"+LaunchAgentLabel)
	output, err := run(ctx, launchctl, "bootstrap", domain, path)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("%w (%s). If owngit is turned off in System Settings under General, Login Items & Extensions, turn it on, then run \"owngit service install\" again", ErrNotLoaded, strings.ReplaceAll(detail, "\n", " "))
}

// YieldLaunchAgent removes the OwnGit agent when Homebrew's service of the
// same state directory starts, so that the two never run side by side:
// brew services takes over once the owner turns it on at the desktop. It
// reports whether it removed the agent.
func YieldLaunchAgent(ctx context.Context, run Runner, uid int, home, stateDir string) (bool, error) {
	path := LaunchAgentPath(home)
	installed, err := ReadLaunchAgent(path)
	if err != nil || filepath.Clean(installed.StateDir) != filepath.Clean(stateDir) {
		return false, nil
	}
	if err := UninstallLaunchAgent(ctx, run, uid, path); err != nil {
		return false, err
	}
	return true, nil
}

// unloadLaunchAgent boots the agent out of the domain it is loaded in, if
// any, and waits until launchd no longer lists it.
func unloadLaunchAgent(ctx context.Context, run Runner, uid int) error {
	return unloadFrom(ctx, run, uid, LaunchAgentStatus(ctx, run, uid).Domain)
}

// unloadFrom boots the agent out of domain, which "" means none.
func unloadFrom(ctx context.Context, run Runner, uid int, domain string) error {
	if domain == "" {
		return nil
	}
	if err := runStep(ctx, run, []string{launchctl, "bootout", domain + "/" + LaunchAgentLabel}); err != nil {
		return err
	}
	deadline := time.Now().Add(launchAgentUnloadTimeout)
	for LaunchAgentStatus(ctx, run, uid).Domain != "" {
		if time.Now().After(deadline) {
			return fmt.Errorf("launchd still lists %s after %s", LaunchAgentLabel, launchAgentUnloadTimeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}
