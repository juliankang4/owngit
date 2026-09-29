package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"owngit/internal/doctor"
	"owngit/internal/server"
	"owngit/internal/service"
	"owngit/internal/state"
	"owngit/internal/version"
	"owngit/internal/webui"
)

// "owngit doctor" runs the checkup of this computer and prints one repair
// for each problem. The Settings page runs the same checkup inside the
// server (serverDiagnosis). Neither runs a repair.

func doctorCommand(arguments []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := parseFlagsJSON(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return jsonFailure(*asJSON, "invalid_arguments", errors.New("doctor takes no positional arguments"))
	}
	dir, err := filepath.Abs(*stateDir)
	if err != nil {
		return jsonFailure(*asJSON, "invalid_arguments", err)
	}
	subject, err := commandSubject(dir)
	if err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	findings := diagnose(subject)
	if *asJSON {
		type finding struct {
			webui.Finding
			Message string `json:"message"`
		}
		report := struct {
			Version      string    `json:"version"`
			Program      string    `json:"program"`
			StateDir     string    `json:"state_dir"`
			Repositories string    `json:"repositories,omitempty"`
			Listen       string    `json:"listen"`
			Running      bool      `json:"running"`
			Service      bool      `json:"service"`
			Log          string    `json:"log,omitempty"`
			Findings     []finding `json:"findings"`
		}{version.Version, subject.program, dir, subject.repositories, subject.listen, subject.running, subject.service.found, subject.service.log, []finding{}}
		for _, item := range findings {
			report.Findings = append(report.Findings, finding{item, item.Sentence(webui.LangEN)})
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	out := os.Stdout
	fmt.Fprintf(out, "OwnGit %s\n", version.Version)
	fmt.Fprintf(out, "  Program:      %s\n", printable(subject.program))
	fmt.Fprintf(out, "  State:        %s\n", printable(dir))
	if subject.repositories != "" {
		fmt.Fprintf(out, "  Repositories: %s\n", printable(subject.repositories))
	}
	fmt.Fprintf(out, "  Listen:       %s\n", printable(subject.listen))
	if subject.service.found {
		fmt.Fprintf(out, "  Log:          %s\n", printable(subject.service.log))
	} else {
		fmt.Fprintln(out, "  Service:      not installed")
	}
	if len(findings) == 0 {
		fmt.Fprintln(out, "No problem found.")
		return nil
	}
	for _, item := range findings {
		fmt.Fprintf(out, "\nProblem: %s\n", printable(item.Sentence(webui.LangEN)))
		if item.Repair != "" {
			fmt.Fprintf(out, "Repair:  %s\n", printable(item.Repair))
		}
	}
	return nil
}

// doctorSubject is the installation that the checkup looks at, as the
// command or the server sees it.
type doctorSubject struct {
	running, setupComplete bool
	service                servingService
	listen                 string
	// program is the owngit program that serves.
	program string
	// administrator is true on Windows when the account that runs OwnGit
	// is an administrator.
	administrator bool
	stateDir      string
	repositories  string
}

// servingService is the service of this account that runs OwnGit for a
// state directory: whether there is one, the program it runs, where its
// log is, and whether it is a Windows administrator's boot task.
type servingService struct {
	found         bool
	program, log  string
	administrator bool
}

// commandSubject reads the installation of stateDir from outside the
// server: its service, whether the server answers, and its saved setup.
func commandSubject(stateDir string) (doctorSubject, error) {
	subject := doctorSubject{stateDir: stateDir, service: findServingService(stateDir)}
	target, _, err := healthAddress(stateDir)
	if err != nil {
		return doctorSubject{}, err
	}
	subject.running = checkHealth(target) == nil
	if subject.listen, err = serverListen(stateDir); err != nil {
		return doctorSubject{}, err
	}
	_, subject.setupComplete = setupStatus(stateDir)
	subject.repositories = savedRepositoryRoot(stateDir)
	subject.program = subject.service.program
	if subject.program == "" {
		if subject.program, err = runningExecutable(); err != nil {
			return doctorSubject{}, err
		}
	}
	subject.administrator = subject.service.administrator || probeEnvironment().Administrator
	return subject, nil
}

// serverDiagnosis returns the checkup that the Settings page shows, from
// the facts of this server: it answers, setup is done, and it listens on
// listen. asService is true when a service manager started it.
func serverDiagnosis(stateDir, listen string, asService bool, repositoryRoot func(context.Context) (string, error)) func(context.Context) []webui.Finding {
	return func(ctx context.Context) []webui.Finding {
		program, _ := runningExecutable()
		subject := doctorSubject{
			running: true, setupComplete: true, service: servingService{found: asService},
			listen: listen, program: program, stateDir: stateDir,
			administrator: probeEnvironment().Administrator,
		}
		// The boot task of an administrator serves from the protected copy
		// with administrator rights removed.
		if runtime.GOOS == "windows" && asService {
			if paths, err := servicePaths(); err == nil && sameFile(program, paths.Executable) {
				subject.administrator = true
			}
		}
		root, err := repositoryRoot(ctx)
		if err != nil {
			return append(diagnose(subject), webui.Finding{Code: webui.MsgDoctorUncheckedOwner, Args: []string{err.Error()}})
		}
		subject.repositories = root
		return diagnose(subject)
	}
}

// diagnose reads what this computer says about subject and returns the
// findings.
func diagnose(subject doctorSubject) []webui.Finding {
	facts := doctor.Facts{
		GOOS: runtime.GOOS, Running: subject.running, Service: subject.service.found,
		SetupComplete: subject.setupComplete, Listen: subject.listen, Program: subject.program,
		Administrator: subject.administrator,
	}
	if host, _, err := net.SplitHostPort(subject.listen); err == nil {
		facts.OtherDevices = !server.IsLoopbackHost(host)
	}
	if !subject.running {
		return doctor.Diagnose(facts)
	}
	var firewallErr error
	switch runtime.GOOS {
	case "windows":
		host, err := newTaskHost()
		if err != nil {
			facts.Unchecked = append(facts.Unchecked, doctor.Unchecked{Code: webui.MsgDoctorUncheckedOwner, Reason: err.Error()})
			if facts.OtherDevices {
				firewallErr = err
			}
			break
		}
		firewallErr = host.readDoctorFacts(&facts, []string{subject.stateDir, subject.repositories})
	case "darwin":
		if facts.OtherDevices {
			facts.Firewall, firewallErr = macFirewall(subject.program)
		}
	case "linux":
		if facts.OtherDevices {
			facts.Firewall, firewallErr = linuxFirewall()
		}
	}
	if firewallErr != nil {
		facts.Unchecked = append(facts.Unchecked, doctor.Unchecked{Code: webui.MsgDoctorUncheckedFirewall, Reason: firewallErr.Error()})
	}
	return doctor.Diagnose(facts)
}

// readDoctorFacts reads the owners of folders and, when OwnGit listens for
// other devices, Windows Firewall. A folder it cannot read is recorded in
// facts; the firewall error is returned.
func (host *taskHost) readDoctorFacts(facts *doctor.Facts, folders []string) error {
	facts.AccountSID = host.sid
	owned, err := host.administratorsFolders(folders)
	facts.AdministratorsFolders = owned
	if err != nil {
		facts.Unchecked = append(facts.Unchecked, doctor.Unchecked{Code: webui.MsgDoctorUncheckedOwner, Reason: err.Error()})
	}
	if !facts.OtherDevices {
		return nil
	}
	rule, found, foreign, err := host.readFirewallRule()
	if err != nil {
		return err
	}
	switch {
	case foreign:
		facts.Firewall.Rule = doctor.RuleForeign
	case found && rule.Allows(facts.Program):
		facts.Firewall.Rule = doctor.RuleAllows
	case found:
		facts.Firewall.Rule = doctor.RuleOther
	}
	_, port, _ := net.SplitHostPort(facts.Listen)
	output, err := host.runPowerShell(service.FirewallAccessScript, service.FirewallProgramVariable+"="+facts.Program)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	facts.Firewall.Access, err = service.ParseFirewallAccess(string(output), port)
	return err
}

// socketFilter is the macOS application firewall's command line tool.
const socketFilter = "/usr/libexec/ApplicationFirewall/socketfilterfw"

// macFirewall reads whether the macOS application firewall is on, blocks
// every incoming connection, or blocks program.
func macFirewall(program string) (doctor.Firewall, error) {
	ask := func(args ...string) (string, error) {
		output, err := serviceRunner(context.Background(), socketFilter, args...)
		if err != nil {
			return "", fmt.Errorf("%s %s: %w: %s", socketFilter, args[0], err, strings.TrimSpace(string(output)))
		}
		return string(output), nil
	}
	global, err := ask("--getglobalstate")
	if err != nil {
		return doctor.Firewall{}, err
	}
	// "Firewall is enabled. (State = 1)"; 2 also blocks every incoming
	// connection that macOS does not need itself.
	_, stateText, found := strings.Cut(global, "(State = ")
	level, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(stateText), ")"))
	if !found || err != nil {
		return doctor.Firewall{}, fmt.Errorf("unexpected firewall state %q", strings.TrimSpace(global))
	}
	firewall := doctor.Firewall{AppFirewall: level > 0, BlockAll: level == 2}
	if !firewall.AppFirewall {
		return firewall, nil
	}
	blockAll, err := ask("--getblockall")
	if err != nil {
		return doctor.Firewall{}, err
	}
	firewall.BlockAll = firewall.BlockAll || strings.Contains(blockAll, "set to enabled")
	blocked, err := ask("--getappblocked", program)
	if err != nil {
		return doctor.Firewall{}, err
	}
	firewall.AppBlocked = strings.Contains(blocked, "is blocked")
	return firewall, nil
}

// ufwConfig and firewallCmd are where ufw keeps whether it is enabled and
// where firewalld's command line tool is.
const (
	ufwConfig   = "/etc/ufw/ufw.conf"
	firewallCmd = "/usr/bin/firewall-cmd"
)

// linuxFirewall reads whether ufw is enabled and whether firewalld runs.
// Their rules need root to read, so it does not say whether they allow
// OwnGit's port.
func linuxFirewall() (doctor.Firewall, error) {
	var firewall doctor.Firewall
	content, err := os.ReadFile(ufwConfig)
	switch {
	case err == nil:
		firewall.UFW = ufwEnabled(string(content))
	case !errors.Is(err, os.ErrNotExist):
		return doctor.Firewall{}, err
	}
	if _, err := os.Stat(firewallCmd); err == nil {
		if err := rootControlledExecutable(firewallCmd); err != nil {
			return doctor.Firewall{}, err
		}
		// firewall-cmd --state prints "running" and exits 0, or prints
		// "not running" and exits 252.
		output, err := serviceRunner(context.Background(), firewallCmd, "--state")
		switch answer := strings.TrimSpace(string(output)); {
		case err == nil && answer == "running":
			firewall.Firewalld = true
		case answer != "not running":
			return doctor.Firewall{}, fmt.Errorf("%s --state: %v: %s", firewallCmd, err, answer)
		}
	}
	return firewall, nil
}

// ufwEnabled reads ENABLED from ufw.conf.
func ufwEnabled(config string) bool {
	for _, line := range strings.Split(config, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "ENABLED="); found {
			return strings.EqualFold(strings.Trim(strings.TrimSpace(value), `"'`), "yes")
		}
	}
	return false
}

// findServingService finds the service of this account that serves
// stateDir.
func findServingService(stateDir string) servingService {
	same := func(installed service.Installed) bool {
		return sameFile(installed.StateDir, stateDir)
	}
	switch runtime.GOOS {
	case "windows":
		host, err := newTaskHost()
		if err != nil {
			return servingService{}
		}
		if installed, found, err := host.installed(); err == nil && found && same(installed) {
			return servingService{found: true, program: installed.Executable, log: service.TaskLogFile(installed.StateDir), administrator: installed.Mode == service.ModeBootTask}
		}
		return servingService{}
	case "darwin":
		home, _ := os.UserHomeDir()
		if installed, err := service.ReadLaunchAgent(service.LaunchAgentPath(home)); err == nil && same(installed) {
			return servingService{found: true, program: installed.Executable, log: service.LaunchAgentLogPath(home)}
		}
	case "linux":
		configDir, _ := os.UserConfigDir()
		if installed, found, err := findInstalled(configDir); err == nil && found && same(installed) {
			return servingService{found: true, program: installed.Executable, log: installed.Mode.JournalCommand()}
		}
	}
	// A Homebrew service always uses the default state directory.
	executable, _ := runningExecutable()
	if prefix := service.HomebrewPrefix(executable); prefix != "" && homebrewServiceRegistered() && sameFile(stateDir, mustAbs(ownStateDir())) {
		return servingService{found: true, log: filepath.Join(prefix, "var", "log", "owngit.log")}
	}
	return servingService{}
}

// serverListen returns the address the server of stateDir listens on, or
// will listen on by its saved setting.
func serverListen(stateDir string) (string, error) {
	listen := server.DefaultListenAddress
	if err := state.RequireExisting(stateDir); errors.Is(err, state.ErrNotExist) {
		return listen, nil
	} else if err != nil {
		return "", err
	}
	ctx := context.Background()
	store, err := openLiveState(ctx, stateDir)
	if err != nil {
		return "", err
	}
	defer store.Close()
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return "", err
	}
	if observed.Server == state.ServerRunning && observed.Record != nil {
		return cmp.Or(observed.Record.Listen, observed.Record.Address, listen), nil
	}
	saved, err := store.NetworkSettings(ctx)
	if err != nil {
		return "", err
	}
	return cmp.Or(saved.Listen, listen), nil
}
