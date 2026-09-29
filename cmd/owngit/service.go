package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"owngit/internal/service"
	"owngit/internal/state"
)

// "owngit service" runs OwnGit in the background and starts it at boot, or
// at login on macOS. It chooses who runs the service from the environment
// and asks nothing; see the service package. Only the root steps of a
// system service leave this process, through one sudo that the owner
// answers.

// probeEnvironment reads what mode selection and headless detection use.
// Tests replace it so that the machine running them never matters.
var probeEnvironment = service.Probe

// serviceRunner runs a service manager command and returns its output.
// Tests replace it.
var serviceRunner service.Runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// serviceStartTimeout bounds the wait for a started service to answer its
// health check. Startup prepares repositories for up to ten seconds first.
const serviceStartTimeout = 90 * time.Second

func serviceCommand(arguments []string) error {
	if len(arguments) == 0 {
		printServiceUsage(os.Stderr)
		return errors.New("service requires install, uninstall, status, start, stop, or restart")
	}
	if isHelpArgument(arguments[0]) {
		printServiceUsage(os.Stdout)
		return nil
	}
	action, rest := arguments[0], arguments[1:]
	if runtime.GOOS == "windows" {
		switch action {
		case "install", "uninstall", "status", "start", "stop", "restart",
			// Internal steps: the report and restricted state read used by an
			// elevated install, and the steps that run after the UAC prompt.
			"report", "repository-root", "elevated-install", "elevated-uninstall", "elevated-firewall":
			return taskServiceCommand(action, rest)
		}
	}
	switch action {
	case "install":
		return serviceInstall(rest)
	case "uninstall", "status", "start", "stop", "restart":
		flags := flag.NewFlagSet("service "+action, flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		if err := parseFlags(flags, rest); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("service %s takes no arguments", action)
		}
		if err := requireServicePlatform(); err != nil {
			return err
		}
		host, err := newServiceBackend()
		if err != nil {
			return err
		}
		switch action {
		case "uninstall":
			return host.uninstall()
		case "status":
			return host.status()
		default:
			return host.control(action)
		}
	default:
		printServiceUsage(os.Stderr)
		return fmt.Errorf("unknown service command %q", action)
	}
}

func printServiceUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit service <install|uninstall|status|start|stop|restart> [options]")
	fmt.Fprintln(writer, "  service install     run OwnGit in the background from now on and at every boot (every login on macOS); run again to update")
	fmt.Fprintln(writer, "  service uninstall   stop the service and remove it; the data stays")
	fmt.Fprintln(writer, "  service status      whether it runs, who runs it, its unit or task, log, state directory and address")
	fmt.Fprintln(writer, "  service start | stop | restart")
}

func requireServicePlatform() error {
	switch runtime.GOOS {
	case "linux", "darwin":
		return nil
	default:
		return fmt.Errorf("owngit service is %w (%s)", service.ErrUnsupported, runtime.GOOS)
	}
}

// serviceBackend is the service manager of this platform: systemd on
// Linux (serviceHost) and launchd on macOS (launchAgentHost).
type serviceBackend interface {
	install(stateDirFlag string, headlessFlag *bool) error
	uninstall() error
	status() error
	control(action string) error
}

func newServiceBackend() (serviceBackend, error) {
	host, err := newServiceHost()
	if err != nil || runtime.GOOS != "darwin" {
		return host, err
	}
	return newLaunchAgentHost(host)
}

// serviceHost is this computer and the account running the command.
type serviceHost struct {
	env           service.Environment
	account       *user.User
	group         string
	executable    string
	homebrew      string
	userConfigDir string
	out           io.Writer
}

func newServiceHost() (*serviceHost, error) {
	account, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("find the current account: %w", err)
	}
	group, err := user.LookupGroupId(account.Gid)
	if err != nil {
		return nil, fmt.Errorf("find the group of %s: %w", account.Username, err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = filepath.Join(account.HomeDir, ".config")
	}
	return &serviceHost{
		env: probeEnvironment(), account: account, group: group.Name, executable: executable,
		homebrew: service.HomebrewPrefix(executable), userConfigDir: configDir, out: os.Stdout,
	}, nil
}

// printf prints to the owner. Text arguments can come from a state
// directory that the service account controls, so their control and
// direction characters are replaced before they reach root's terminal.
func (host *serviceHost) printf(format string, args ...any) {
	for index, arg := range args {
		if text, ok := arg.(string); ok {
			args[index] = printable(text)
		}
	}
	fmt.Fprintf(host.out, format, args...)
}

// installed finds the unit of an earlier install, so that a new install
// keeps its mode and state directory.
func (host *serviceHost) installed() (service.Installed, bool, error) {
	installed, found, err := findInstalled(host.userConfigDir)
	if errors.Is(err, service.ErrForeignUnit) {
		return installed, found, fmt.Errorf("%w; OwnGit leaves it alone. Remove or rename it first", err)
	}
	return installed, found, err
}

func serviceInstall(arguments []string) error {
	flags := flag.NewFlagSet("service install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDirFlag := flags.String("state-dir", "", "state directory the service uses (default: the one owngit uses for this account, or "+service.AccountStateDir+" when root installs)")
	headlessFlag := flags.Bool("headless", false, "whether this computer has no screen for setup (default: kept from the installed service, or detected for a new one)")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	var headless *bool
	flags.Visit(func(entry *flag.Flag) {
		if entry.Name == "headless" {
			headless = headlessFlag
		}
	})
	if flags.NArg() != 0 {
		return errors.New("service install takes no positional arguments")
	}
	if err := requireServicePlatform(); err != nil {
		return err
	}
	host, err := newServiceBackend()
	if err != nil {
		return err
	}
	return host.install(*stateDirFlag, headless)
}

// install installs or updates the service. headless is the --headless
// option, or nil without it.
func (host *serviceHost) install(stateDirFlag string, headlessFlag *bool) error {
	existing, found, err := host.installed()
	if err != nil {
		return err
	}
	root := host.env.EUID == 0
	mode := host.env.ChooseMode(host.homebrew != "")
	if found {
		mode = existing.Mode
		switch {
		case mode == service.ModeAccount && !root:
			arguments := []string{"service", "install"}
			if headlessFlag != nil {
				arguments = append(arguments, "--headless="+strconv.FormatBool(*headlessFlag))
			}
			return host.rerunWithSudo(arguments...)
		case mode == service.ModeSystem && existing.User != host.account.Username:
			return fmt.Errorf("the OwnGit service at %s runs as %s; run \"owngit service install\" as %s, or \"owngit service uninstall\" first", existing.UnitPath, existing.User, existing.User)
		}
	}
	stateDir, err := host.installStateDir(mode, stateDirFlag, existing, found)
	if err != nil {
		return err
	}
	// The headless choice is made once, when the service is first
	// installed, and only --headless changes it; a later run from another
	// place, such as SSH on a desktop, keeps it.
	headless := host.env.Headless()
	switch {
	case headlessFlag != nil:
		headless = *headlessFlag
	case found:
		headless = existing.Headless
	}
	if mode == service.ModeHomebrew {
		return host.installHomebrew(stateDir, headless)
	}
	if mode == service.ModeUser && !found {
		if reason := host.userServiceProblem(); reason != "" {
			host.printf("A user service cannot start at boot here (%s), so OwnGit installs a system service instead.\n", reason)
			mode = service.ModeSystem
		}
	}
	plan := service.Plan{Mode: mode, Executable: host.executable, StateDir: stateDir, Headless: headless}
	switch mode {
	case service.ModeUser:
		plan.Path = servicePath()
	case service.ModeSystem:
		plan.User, plan.Group, plan.Home, plan.Path = host.account.Username, host.group, host.account.HomeDir, servicePath()
	case service.ModeAccount:
		plan.User, plan.Group, plan.Home = service.AccountName, service.AccountName, service.AccountHome
		if err := rootControlledExecutable(host.executable); err != nil {
			return fmt.Errorf("a service installed by root must run a binary that only root can change (%v); install it, for example with \"install -m 0755 owngit /usr/local/bin/\", and run it from there", err)
		}
	}
	unit, err := service.RenderUnit(plan)
	if err != nil {
		return err
	}
	unitPath := plan.UnitPath(host.userConfigDir)
	ctx := context.Background()
	earlier := ""
	switch mode {
	case service.ModeUser:
		host.printf("Installing OwnGit as a %s.\n", mode.Describe())
		if err := service.InstallUserUnit(ctx, serviceRunner, unitPath, unit); err != nil {
			return err
		}
	default:
		script, err := service.RootInstallScript(plan, unit)
		if err != nil {
			return err
		}
		steps := "write the unit " + unitPath + " and start it"
		if mode == service.ModeAccount {
			steps = "make sure the " + service.AccountName + " account and " + service.AccountHome + " exist, write " + service.PointerPath + " and the unit " + unitPath + ", and start it"
		}
		explanation := "Installing OwnGit as a " + mode.Describe() + ": root will " + steps + "."
		if !root {
			explanation = "Installing OwnGit as a " + mode.Describe() + " needs root once to " + steps + "."
		}
		if mode == service.ModeAccount {
			// Root's own state from before the service stays where it is.
			if read, complete := setupStatus(ownStateDir()); read && complete {
				earlier = ownStateDir()
			}
		}
		if err := host.runAsRoot(script, explanation); err != nil {
			return err
		}
		if mode == service.ModeAccount {
			// Nothing below needs root, and the state belongs to the
			// account, so the rest runs as the account.
			if err := dropToAccount(service.AccountName); err != nil {
				return err
			}
		}
	}
	if err := host.reportStarted(mode, unitPath, stateDir); err != nil || earlier == "" {
		return err
	}
	if read, complete := setupStatus(stateDir); read && !complete {
		fmt.Fprint(host.out, earlierStateNotice(host.executable, earlier, os.Getenv("SUDO_USER") != ""))
	}
	return nil
}

// installStateDir is the state directory of a new or updated unit.
func (host *serviceHost) installStateDir(mode service.Mode, flagValue string, existing service.Installed, found bool) (string, error) {
	switch {
	case flagValue != "":
		return filepath.Abs(flagValue)
	case found && existing.StateDir != "":
		return existing.StateDir, nil
	case mode == service.ModeAccount:
		// The pointer outlives an uninstall and names the state to use
		// again, such as one restored from root's earlier installation.
		if pointed := pointerStateDir(); strings.HasPrefix(pointed, service.AccountHome+"/") {
			return pointed, nil
		}
		return service.AccountStateDir, nil
	default:
		return filepath.Abs(defaultStateDir())
	}
}

// userServiceProblem says why a user service of this account would not
// start at boot, or "" when it will. It turns on lingering, which systemd
// allows for one's own account without a password in an active local
// session; where it asks for one, the answer is to use a system service.
func (host *serviceHost) userServiceProblem() string {
	ctx := context.Background()
	if _, err := serviceRunner(ctx, "systemctl", "--user", "show-environment"); err != nil {
		return "no systemd user manager answers for this account"
	}
	if _, err := os.Stat(filepath.Join("/var/lib/systemd/linger", host.account.Username)); err == nil {
		return ""
	}
	if output, err := serviceRunner(ctx, "loginctl", "--no-ask-password", "enable-linger"); err != nil {
		if detail := strings.TrimSpace(string(output)); detail != "" {
			return "lingering needs a password: " + detail
		}
		return "lingering needs a password"
	}
	return ""
}

func (host *serviceHost) installHomebrew(stateDir string, headless bool) error {
	if filepath.Clean(stateDir) != filepath.Clean(mustAbs(defaultStateDir())) {
		return errors.New("the Homebrew service uses the default state directory; leave out --state-dir")
	}
	if headless {
		if err := saveHeadlessListen(stateDir); err != nil {
			return err
		}
	}
	if runtime.GOOS == "linux" {
		if reason := host.userServiceProblem(); reason != "" {
			host.printf("Note: the Homebrew service starts when you log in, not at boot (%s).\n", reason)
		}
	}
	host.printf("OwnGit was installed with Homebrew, so Homebrew runs the service: %s services restart owngit\n", filepath.Join(host.homebrew, "bin", "brew"))
	if err := host.brewServices("restart"); err != nil {
		return err
	}
	return host.reportStarted(service.ModeHomebrew, "", stateDir)
}

// brewServices runs "brew services ACTION owngit" of the Homebrew that
// installed this binary.
func (host *serviceHost) brewServices(action string) error {
	return runAttached(filepath.Join(host.homebrew, "bin", "brew"), "services", action, "owngit")
}

// reportStarted waits for the service to answer, then prints where it
// runs and, while setup is not done, the setup link.
func (host *serviceHost) reportStarted(mode service.Mode, unitPath, stateDir string) error {
	address, err := waitForService(stateDir, serviceStartTimeout)
	var failed errServeFailed
	switch {
	case errors.As(err, &failed):
		host.printf("OwnGit could not start: %s\nIt tries again every few seconds; after fixing this, \"owngit service status\" shows whether it runs.\n", failed.message)
		return errors.New("the service did not start")
	case err != nil:
		host.printf("OwnGit did not answer within %s: %v\nSee the log: %s\n", serviceStartTimeout, err, host.logHint(mode))
		return errors.New("the service did not start")
	}
	host.printf("OwnGit is running as a %s.\n", mode.Describe())
	if runtime.GOOS == "darwin" {
		host.printf("It starts again whenever %s logs in on this Mac.\n", host.account.Username)
	}
	host.printServiceFacts(mode, unitPath, stateDir, ownerAddresses(stateDir, address))
	return printSetupLinkIfNeeded(stateDir, host.out)
}

// ownerAddresses lists the addresses a browser can open OwnGit at, the
// most likely first, or the checked address when they cannot be read.
func ownerAddresses(stateDir, checked string) string {
	ctx := context.Background()
	if store, err := openLiveState(ctx, stateDir); err == nil {
		defer store.Close()
		if bases, _, err := setupBases(ctx, store); err == nil {
			return strings.Join(bases, ", ")
		}
	}
	return "http://" + checked
}

// logHint is where the log of the service is: a file, or the command that
// shows it.
func (host *serviceHost) logHint(mode service.Mode) string {
	switch mode {
	case service.ModeHomebrew:
		return filepath.Join(host.homebrew, "var", "log", "owngit.log")
	case service.ModeLaunchAgent:
		return service.LaunchAgentLogPath(host.account.HomeDir)
	}
	return mode.JournalCommand()
}

func (host *serviceHost) printServiceFacts(mode service.Mode, unitPath, stateDir, address string) {
	switch {
	case mode == service.ModeLaunchAgent:
		host.printf("  Agent:   %s\n", unitPath)
	case unitPath != "":
		host.printf("  Unit:    %s\n", unitPath)
	}
	host.printf("  Log:     %s\n", host.logHint(mode))
	host.printf("  State:   %s\n", stateDir)
	if address != "" {
		host.printf("  Address: %s\n", address)
	}
}

// printSetupLinkIfNeeded prints the setup link, or where the setup file is
// when the output is not a terminal, until setup is complete.
func printSetupLinkIfNeeded(stateDir string, out io.Writer) error {
	ctx := context.Background()
	store, err := openLiveState(ctx, stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	settings, err := store.Settings(ctx)
	if err != nil || settings.Initialized {
		return err
	}
	fmt.Fprintln(out)
	_, err = issueAndShowSetupLink(ctx, store, "", out, stdoutIsTerminal())
	return err
}

func (host *serviceHost) uninstall() error {
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	switch {
	case !found && host.homebrew != "":
		if err := host.brewServices("stop"); err != nil {
			return err
		}
		host.printf("The Homebrew service is stopped and no longer starts. The data stays in %s.\n", mustAbs(defaultStateDir()))
		return nil
	case !found:
		host.printf("OwnGit is not installed as a service.\n")
		host.printDataStays()
		return nil
	case installed.Mode == service.ModeUser:
		if err := service.UninstallUserUnit(context.Background(), serviceRunner, installed.UnitPath); err != nil {
			return err
		}
	default:
		if err := host.runAsRoot(service.RootUninstallScript(), "Removing the system service: root stops it and removes "+installed.UnitPath+"."); err != nil {
			return err
		}
	}
	readable, err := host.actForService(installed)
	if err != nil {
		return err
	}
	host.printf("The OwnGit service is stopped and removed. The state stays in %s", installed.StateDir)
	if repositories := savedRepositoryRoot(installed.StateDir); readable && repositories != "" {
		host.printf(" and the repositories in %s", repositories)
	}
	if installed.Mode == service.ModeAccount {
		host.printf(", and the %s account stays", service.AccountName)
	}
	host.printf(".\nRun \"owngit service install\" to use it again.\n")
	if installed.Mode == service.ModeUser {
		host.printf("Lingering stays on for this account, so its other user services still start at boot. \"loginctl disable-linger\" turns it off.\n")
	}
	return nil
}

// printDataStays says where the default state directory keeps its data,
// for an uninstall that found no service.
func (host *serviceHost) printDataStays() {
	stateDir := uninstallStateDir()
	if line := dataStaysLine(stateDir, savedRepositoryRoot(stateDir)); line != "" {
		host.printf("%s\n", line)
	}
}

func (host *serviceHost) status() error {
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	if !found {
		if host.homebrew != "" {
			host.printf("OwnGit was installed with Homebrew; its service is managed with brew services:\n")
			return host.brewServices("info")
		}
		host.printf("OwnGit is not installed as a service. Run \"owngit service install\".\n")
		return nil
	}
	arguments := []string{"is-active", service.UnitName}
	if installed.Mode == service.ModeUser {
		arguments = append([]string{"--user"}, arguments...)
	}
	output, _ := serviceRunner(context.Background(), "systemctl", arguments...)
	active := strings.TrimSpace(string(output))
	readable, err := host.actForService(installed)
	if err != nil {
		return err
	}
	address, answered := "", false
	if readable {
		if target, _, err := healthAddress(installed.StateDir); err == nil {
			address = "http://" + target
			answered = checkHealth(target) == nil
		}
	}
	switch {
	case answered:
		host.printf("OwnGit is running (systemd: %s) and answers its health check.\n", active)
		address = ownerAddresses(installed.StateDir, strings.TrimPrefix(address, "http://"))
	case active == "active" && !readable:
		host.printf("OwnGit is running (systemd: active).\n")
	case active == "active":
		host.printf("OwnGit is starting or not answering yet (systemd: active).\n")
	default:
		host.printf("OwnGit is not running (systemd: %s).\n", active)
	}
	host.printf("  Mode:    %s\n", installed.Mode.Describe())
	host.printServiceFacts(installed.Mode, installed.UnitPath, installed.StateDir, "")
	if answered {
		host.printf("  Address: %s\n", address)
	} else if !readable {
		host.printf("  Address: run \"sudo owngit service status\" to read it from the state directory of %s\n", installed.User)
	}
	if read, complete := setupStatus(installed.StateDir); readable && read && !complete {
		host.printf("Setup is not complete. Run \"owngit setup-link\" in a terminal for the one-time setup link.\n")
	}
	return nil
}

// setupStatus reports whether the state directory could be read and whether
// its setup is complete. Only a readable state is said to need setup, so
// nothing suggests setting up an installation that may already be in use.
func setupStatus(stateDir string) (readable, complete bool) {
	if err := state.RequireExisting(stateDir); err != nil {
		return false, false
	}
	store, err := openLiveState(context.Background(), stateDir)
	if err != nil {
		return false, false
	}
	defer store.Close()
	settings, err := store.Settings(context.Background())
	return err == nil, err == nil && settings.Initialized
}

// earlierStateNotice tells root how to serve its own earlier installation in
// earlier through the account service, whose state starts empty: back it up
// into a folder only root controls, outside the account's home, give that
// backup to the account, restore it as the account and point the service at
// the restored state. The steps are joined with && so that a failed step
// stops the rest.
func earlierStateNotice(executable, earlier string, sudo bool) string {
	run, owngit, backup, restored := "", service.ShellQuote(executable), service.AccountHome+"-root-backup", service.AccountHome+"/state-from-root"
	if sudo {
		run = "sudo "
	}
	return fmt.Sprintf("Root's earlier OwnGit in %s was not moved, so this service starts empty. To serve it instead, stop that OwnGit first and run:\n"+
		"  %[2]s%[3]s backup --state-dir %[4]s --output %[5]s && \\\n  %[2]schown -R %[6]s: %[5]s && \\\n"+
		"  %[2]srunuser -u %[6]s -- %[3]s restore --input %[5]s --state-dir %[7]s --repository-root %[8]s/repositories && \\\n  %[2]s%[3]s service install --state-dir %[7]s\n",
		earlier, run, owngit, service.ShellQuote(earlier), backup, service.AccountName, restored, service.AccountHome)
}

func (host *serviceHost) control(action string) error {
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	if !found {
		if host.homebrew != "" {
			return host.brewServices(action)
		}
		return errors.New("OwnGit is not installed as a service; run \"owngit service install\"")
	}
	switch {
	case installed.Mode == service.ModeUser:
		output, err := serviceRunner(context.Background(), "systemctl", "--user", action, service.UnitName)
		if err != nil {
			return fmt.Errorf("systemctl --user %s: %w: %s", action, err, strings.TrimSpace(string(output)))
		}
	case host.env.EUID == 0:
		if err := runAttached("systemctl", action, service.UnitName); err != nil {
			return err
		}
	default:
		if err := host.runRootCommand("systemctl", action, service.UnitName); err != nil {
			return err
		}
	}
	if action == "stop" {
		host.printf("OwnGit is stopped. It starts again at the next boot, or with \"owngit service start\".\n")
		return nil
	}
	readable, err := host.actForService(installed)
	if err != nil {
		return err
	}
	if !readable {
		host.printf("OwnGit is %s. Run \"sudo owngit service status\" to see its address.\n", map[string]string{"start": "started", "restart": "restarted"}[action])
		return nil
	}
	address, err := waitHealthy(installed.StateDir, serviceStartTimeout)
	if err != nil {
		return fmt.Errorf("OwnGit did not answer within %s: %w; see the log: %s", serviceStartTimeout, err, installed.Mode.JournalCommand())
	}
	host.printf("OwnGit is running at http://%s.\n", address)
	return nil
}

// actForService prepares to read the state directory of an installed
// service and reports whether this process may. Root continues as the
// account the unit runs as, so it never leaves files in that directory
// that the service cannot open. Another account cannot read it.
func (host *serviceHost) actForService(installed service.Installed) (bool, error) {
	switch {
	case installed.User == "" || installed.User == host.account.Username:
		return true, nil
	case host.env.EUID == 0:
		return true, dropToAccount(installed.User)
	default:
		return false, nil
	}
}

// runAsRoot runs a root script: directly for root, otherwise through sudo,
// which asks for the password itself; OwnGit never sees it. The script goes
// to the shell on standard input, so no file holds it. Without sudo, or
// when sudo does not finish, it prints the whole script for root to paste,
// so root runs exactly what it reads and nothing that another account can
// change afterwards.
func (host *serviceHost) runAsRoot(script, explanation string) error {
	host.printf("%s\n", explanation)
	if host.env.EUID == 0 {
		return runScript(script, "/bin/sh", "-s")
	}
	if _, err := lookPath("sudo"); err != nil {
		host.printf("sudo is not available.\n")
		host.printRootFallback(script)
		return errors.New("the root steps have not run yet")
	}
	host.printf("Running: sudo /bin/sh -s (the root steps)\n")
	if err := runScript(script, "sudo", "/bin/sh", "-s"); err != nil {
		host.printf("The root steps did not finish. Fix the problem shown above and try again.\n")
		host.printRootFallback(script)
		return fmt.Errorf("the root steps did not finish: %w", err)
	}
	return nil
}

// rootStepsEnd ends the pasted root script.
const rootStepsEnd = "OWNGIT_ROOT_STEPS_END"

// printRootFallback prints the root script as one command to paste into a
// root shell.
func (host *serviceHost) printRootFallback(script string) {
	host.printf("Or ask an administrator to read these lines and paste them into a root shell. They install the OwnGit service to run as %s:\n", host.account.Username)
	// The script is built here, not read from state, and its line breaks
	// are its structure.
	fmt.Fprintf(host.out, "sh <<'%s'\n%s%s\n", rootStepsEnd, script, rootStepsEnd)
	host.printf("Then run \"owngit service status\" here.\n")
}

// findInstalled and waitForService are service.FindInstalled and
// waitHealthy; tests replace them.
var (
	findInstalled  = service.FindInstalled
	waitForService = waitHealthy
)

// lookPath and runScript are exec.LookPath and a command that reads a
// script on standard input with this terminal for everything else. Tests
// replace them.
var (
	lookPath  = exec.LookPath
	runScript = func(script, name string, args ...string) error {
		command := exec.Command(name, args...)
		command.Stdin, command.Stdout, command.Stderr = strings.NewReader(script), os.Stdout, os.Stderr
		return command.Run()
	}
)

// runRootCommand runs one command through sudo, saying so first.
func (host *serviceHost) runRootCommand(name string, args ...string) error {
	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf("this needs root and sudo is not available; run as root: %s %s", name, strings.Join(args, " "))
	}
	host.printf("Running: sudo %s %s\n", name, strings.Join(args, " "))
	return runAttached("sudo", append([]string{name}, args...)...)
}

// rerunWithSudo runs this owngit command again as root through sudo.
func (host *serviceHost) rerunWithSudo(args ...string) error {
	host.printf("The OwnGit service runs as the %s account, so this needs root.\n", service.AccountName)
	if _, err := lookPath("sudo"); err != nil {
		return fmt.Errorf("sudo is not available; run as root: %s %s", host.executable, strings.Join(args, " "))
	}
	return runAttached("sudo", append([]string{host.executable}, args...)...)
}

// runAttached runs a command on this terminal, so that sudo and systemctl
// can ask and report there.
func runAttached(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}

// servicePath is the PATH a user or system service gets: the absolute
// entries of this shell's PATH, so that Git, Docker and the tools that
// checks run are found as in the terminal.
func servicePath() string {
	var entries []string
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(entry) && !slices.Contains(entries, filepath.Clean(entry)) && !strings.ContainsFunc(entry, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			entries = append(entries, filepath.Clean(entry))
		}
	}
	return strings.Join(entries, string(os.PathListSeparator))
}

// rootControlledExecutable checks a program that root or an administrator
// relies on: the binary a root-installed service runs, and the pacman that
// install detection asks. Other accounts must be able to run it, and only
// root may change it or any folder on its path.
func rootControlledExecutable(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil {
			return err
		}
		if uid, _, ok := service.FileOwner(info); !ok || uid != 0 {
			return fmt.Errorf("%s does not belong to root", current)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("accounts other than root can change %s", current)
		}
		if info.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf("other accounts cannot run or enter %s", current)
		}
		if parent := filepath.Dir(current); parent == current {
			return nil
		}
	}
}

// savedRepositoryRoot returns the repository folder of an installation that
// is set up, or "" when there is none or the state cannot be read.
func savedRepositoryRoot(stateDir string) string {
	if err := state.RequireExisting(stateDir); err != nil {
		return ""
	}
	store, err := openLiveState(context.Background(), stateDir)
	if err != nil {
		return ""
	}
	defer store.Close()
	settings, err := store.Settings(context.Background())
	if err != nil || !settings.Initialized || !filepath.IsAbs(settings.RepositoryRoot) {
		return ""
	}
	return filepath.Clean(settings.RepositoryRoot)
}

// dropToAccount makes this root process act as the named account.
func dropToAccount(name string) error {
	account, err := user.Lookup(name)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	if err := service.DropTo(uid, gid); err != nil {
		return err
	}
	return os.Setenv("HOME", account.HomeDir)
}

func mustAbs(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

// serviceStateCommands are the commands that work on a state directory
// directly. Root running one of them on the directory of the account
// service acts as that account; see actAsStateOwner.
var serviceStateCommands = map[string]bool{
	"serve": true, "setup-link": true, "approve-host": true, "network": true, "tailscale": true,
	"forget-check-container": true, "backup": true, "restore": true, "health": true,
	"upgrade-backup": true,
}

// stateDirArgument returns the value of --state-dir among arguments, or ""
// when it is not given.
func stateDirArgument(arguments []string) string {
	for index, argument := range arguments {
		if argument == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(argument, "-"), "=")
		if !strings.HasPrefix(argument, "-") || name != "state-dir" {
			continue
		}
		if hasValue {
			return value
		}
		if index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}

// pointerFile is the pointer to the account service's state directory.
// Tests replace it.
var pointerFile = service.PointerPath

// pointerStateDir returns the state directory of the account service for
// root, for the owngit account itself and for members of its group, and ""
// for everyone else or when there is none.
func pointerStateDir() string {
	if runtime.GOOS != "linux" || !pointerApplies() {
		return ""
	}
	dir, err := service.ReadPointer(pointerFile)
	if err != nil {
		return ""
	}
	return dir
}

// pointerApplies reports whether this account follows the pointer. Tests
// replace it.
var pointerApplies = defaultPointerApplies

func defaultPointerApplies() bool {
	if os.Geteuid() == 0 {
		return true
	}
	group, err := user.LookupGroup(service.AccountName)
	if err != nil {
		return false
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return false
	}
	groups, _ := os.Getgroups()
	return os.Getgid() == gid || os.Getegid() == gid || slices.Contains(groups, gid)
}

// actAsStateOwner lets root use the state directory of the account service
// without leaving files there that the service cannot open: root becomes
// the owngit account before the command starts. The account controls what
// is inside its home, so root first checks that the directory is the
// account's own real directory, reached through no link the account could
// have made. For a member of the owngit group who cannot enter the
// directory, it explains that sudo is needed.
func actAsStateOwner(stateDir string) (dropped bool, err error) {
	pointed := pointerStateDir()
	if pointed == "" || filepath.Clean(mustAbs(stateDir)) != pointed {
		return false, nil
	}
	if os.Geteuid() != 0 {
		if _, err := os.Stat(pointed); err != nil && errors.Is(err, os.ErrPermission) || err == nil && !accessible(pointed) {
			return false, fmt.Errorf("the OwnGit service keeps its state in %s, which only the %s account can open; run this command with sudo", pointed, service.AccountName)
		}
		return false, nil
	}
	account, err := user.Lookup(service.AccountName)
	if err != nil {
		return false, fmt.Errorf("the state directory %s belongs to the OwnGit service, but the %s account is missing: %w", pointed, service.AccountName, err)
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)
	if err := accountStateDirectory(pointed, uid); err != nil {
		return false, fmt.Errorf("refusing to use %s as root: %w", pointed, err)
	}
	return true, dropToAccountID(uid, gid)
}

// accountStateDirectory checks that dir is a real directory owned by the
// account uid and that no folder on its path is a symbolic link that
// anyone but root made.
func accountStateDirectory(dir string, uid int) error {
	for path := dir; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if owner, _, ok := service.FileOwner(info); !ok || owner != 0 {
				return fmt.Errorf("%s is a symbolic link that root did not make", path)
			}
		}
		if path == dir {
			if !info.IsDir() {
				return errors.New("it is not a directory")
			}
			if owner, _, ok := service.FileOwner(info); !ok || owner != uid {
				return fmt.Errorf("it does not belong to the %s account", service.AccountName)
			}
		}
		if parent := filepath.Dir(path); parent == path {
			return nil
		}
	}
}

func dropToAccountID(uid, gid int) error {
	if err := service.DropTo(uid, gid); err != nil {
		return fmt.Errorf("act as the owner of the state directory: %w", err)
	}
	if account, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		_ = os.Setenv("HOME", account.HomeDir)
	}
	return nil
}

func accessible(dir string) bool {
	entries, err := os.Open(dir)
	if err != nil {
		return false
	}
	entries.Close()
	return true
}
