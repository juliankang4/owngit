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

// "owngit service" runs OwnGit in the background and starts it at boot.
// It chooses who runs the service from the environment and asks nothing;
// see the service package. Only the root steps of a system service leave
// this process, through one sudo that the owner answers.

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
		host, err := newServiceHost()
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
	fmt.Fprintln(writer, "  service install     run OwnGit in the background from now on and at every boot; run again to update")
	fmt.Fprintln(writer, "  service uninstall   stop the service and remove it; the data stays")
	fmt.Fprintln(writer, "  service status      whether it runs, who runs it, its unit, log, state directory and address")
	fmt.Fprintln(writer, "  service start | stop | restart")
}

func requireServicePlatform() error {
	switch runtime.GOOS {
	case "linux":
		return nil
	case "darwin":
		return fmt.Errorf("owngit service is %w (macOS); with Homebrew, run \"brew services start owngit\"", service.ErrUnsupported)
	default:
		return fmt.Errorf("owngit service is %w (%s)", service.ErrUnsupported, runtime.GOOS)
	}
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

func (host *serviceHost) printf(format string, args ...any) { fmt.Fprintf(host.out, format, args...) }

// installed finds the unit of an earlier install, so that a new install
// keeps its mode and state directory.
func (host *serviceHost) installed() (service.Installed, bool, error) {
	installed, found, err := service.FindInstalled(host.userConfigDir)
	if errors.Is(err, service.ErrForeignUnit) {
		return installed, found, fmt.Errorf("%w; OwnGit leaves it alone. Remove or rename it first", err)
	}
	return installed, found, err
}

func serviceInstall(arguments []string) error {
	flags := flag.NewFlagSet("service install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDirFlag := flags.String("state-dir", "", "state directory the service uses (default: the one owngit uses for this account, or "+service.AccountStateDir+" when root installs)")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("service install takes no positional arguments")
	}
	if err := requireServicePlatform(); err != nil {
		return err
	}
	host, err := newServiceHost()
	if err != nil {
		return err
	}
	return host.install(*stateDirFlag)
}

func (host *serviceHost) install(stateDirFlag string) error {
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
			return host.rerunWithSudo("service", "install")
		case mode == service.ModeSystem && existing.User != host.account.Username:
			return fmt.Errorf("the OwnGit service at %s runs as %s; run \"owngit service install\" as %s, or \"owngit service uninstall\" first", existing.UnitPath, existing.User, existing.User)
		}
	}
	stateDir, err := host.installStateDir(mode, stateDirFlag, existing, found)
	if err != nil {
		return err
	}
	headless := host.env.Headless() || (found && existing.Headless)
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
		plan.Writable = []string{host.account.HomeDir, stateDir}
		// The state directory must exist before the unit starts, since
		// only the listed directories are writable for the service.
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return fmt.Errorf("create state directory: %w", err)
		}
	case service.ModeAccount:
		plan.User, plan.Group, plan.Home = service.AccountName, service.AccountName, service.AccountHome
		plan.Writable = []string{service.AccountHome, stateDir}
		if err := readableByOthers(host.executable); err != nil {
			return fmt.Errorf("the %s account cannot run %s (%v); put the binary in a folder such as /usr/local/bin and run it from there", service.AccountName, host.executable, err)
		}
	}
	if repositoryRoot := savedRepositoryRoot(stateDir); repositoryRoot != "" {
		plan.Writable = append(plan.Writable, repositoryRoot)
	}
	unit, err := service.RenderUnit(plan)
	if err != nil {
		return err
	}
	unitPath := plan.UnitPath(host.userConfigDir)
	ctx := context.Background()
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
		what := "the unit " + unitPath
		if mode == service.ModeAccount {
			what = "the " + service.AccountName + " account, " + service.AccountHome + ", " + service.PointerPath + " and " + what
		}
		if err := host.runAsRoot(script, "Installing OwnGit as a "+mode.Describe()+" needs root once to write "+what+" and start it."); err != nil {
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
	return host.reportStarted(mode, unitPath, stateDir)
}

// installStateDir is the state directory of a new or updated unit.
func (host *serviceHost) installStateDir(mode service.Mode, flagValue string, existing service.Installed, found bool) (string, error) {
	switch {
	case flagValue != "":
		return filepath.Abs(flagValue)
	case found && existing.StateDir != "":
		return existing.StateDir, nil
	case mode == service.ModeAccount:
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
	if reason := host.userServiceProblem(); reason != "" {
		host.printf("Note: the Homebrew service starts when you log in, not at boot (%s).\n", reason)
	}
	brew := filepath.Join(host.homebrew, "bin", "brew")
	host.printf("OwnGit was installed with Homebrew, so Homebrew runs the service: %s services restart owngit\n", brew)
	if err := runAttached(brew, "services", "restart", "owngit"); err != nil {
		return err
	}
	return host.reportStarted(service.ModeHomebrew, "", stateDir)
}

// reportStarted waits for the service to answer, then prints where it
// runs and, while setup is not done, the setup link.
func (host *serviceHost) reportStarted(mode service.Mode, unitPath, stateDir string) error {
	address, err := waitHealthy(stateDir, serviceStartTimeout)
	if err != nil {
		host.printf("OwnGit did not answer within %s: %v\nSee the log: %s\n", serviceStartTimeout, err, mode.JournalCommand())
		return errors.New("the service did not start")
	}
	host.printf("OwnGit is running as a %s.\n", mode.Describe())
	host.printServiceFacts(mode, unitPath, stateDir, "http://"+address)
	return printSetupLinkIfNeeded(stateDir, host.out)
}

func (host *serviceHost) printServiceFacts(mode service.Mode, unitPath, stateDir, address string) {
	if unitPath != "" {
		host.printf("  Unit:    %s\n", unitPath)
	}
	if mode == service.ModeHomebrew {
		host.printf("  Log:     %s\n", filepath.Join(host.homebrew, "var", "log", "owngit.log"))
	} else {
		host.printf("  Log:     %s\n", mode.JournalCommand())
	}
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
		if err := runAttached(filepath.Join(host.homebrew, "bin", "brew"), "services", "stop", "owngit"); err != nil {
			return err
		}
		host.printf("The Homebrew service is stopped and no longer starts. The data stays in %s.\n", mustAbs(defaultStateDir()))
		return nil
	case !found:
		host.printf("OwnGit is not installed as a service.\n")
		return nil
	case installed.Mode == service.ModeUser:
		if err := service.UninstallUserUnit(context.Background(), serviceRunner, installed.UnitPath); err != nil {
			return err
		}
	default:
		if err := host.runAsRoot(service.RootUninstallScript(), "Removing the system service needs root to stop it and remove "+installed.UnitPath+"."); err != nil {
			return err
		}
	}
	host.printf("The OwnGit service is stopped and removed. The data stays in %s", installed.StateDir)
	if installed.Mode == service.ModeAccount {
		host.printf(" (repositories set up with the defaults are in %s), and the %s account stays", service.AccountHome, service.AccountName)
	}
	host.printf(".\nRun \"owngit service install\" to use it again.\n")
	return nil
}

func (host *serviceHost) status() error {
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	if !found {
		if host.homebrew != "" {
			host.printf("OwnGit was installed with Homebrew; its service is managed with brew services:\n")
			return runAttached(filepath.Join(host.homebrew, "bin", "brew"), "services", "info", "owngit")
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
	readable := true
	if installed.Mode == service.ModeAccount {
		if host.env.EUID == 0 {
			if err := dropToAccount(service.AccountName); err != nil {
				return err
			}
		} else {
			readable = false
		}
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
		host.printf("OwnGit is running (systemd: %s) and answers at %s.\n", active, address)
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
		host.printf("  Address: run \"sudo owngit service status\" to read it from the state directory\n")
	}
	return nil
}

func (host *serviceHost) control(action string) error {
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	if !found {
		if host.homebrew != "" {
			return runAttached(filepath.Join(host.homebrew, "bin", "brew"), "services", action, "owngit")
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
	if installed.Mode == service.ModeAccount && host.env.EUID != 0 {
		host.printf("OwnGit is %s. Run \"sudo owngit service status\" to see its address.\n", map[string]string{"start": "started", "restart": "restarted"}[action])
		return nil
	}
	if installed.Mode == service.ModeAccount {
		if err := dropToAccount(service.AccountName); err != nil {
			return err
		}
	}
	address, err := waitHealthy(installed.StateDir, serviceStartTimeout)
	if err != nil {
		return fmt.Errorf("OwnGit did not answer within %s: %w; see the log: %s", serviceStartTimeout, err, installed.Mode.JournalCommand())
	}
	host.printf("OwnGit is running at http://%s.\n", address)
	return nil
}

// runAsRoot runs a root script: directly for root, otherwise through sudo,
// which asks for the password itself; OwnGit never sees it. Without sudo,
// or when sudo does not finish, it prints the one command to run as root.
func (host *serviceHost) runAsRoot(script, explanation string) error {
	file, err := os.CreateTemp("", "owngit-service-*.sh")
	if err != nil {
		return err
	}
	path := file.Name()
	_, writeErr := file.WriteString(script)
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		os.Remove(path)
		return errors.Join(writeErr, closeErr)
	}
	if host.env.EUID == 0 {
		defer os.Remove(path)
		return runAttached("/bin/sh", path)
	}
	host.printf("%s\n", explanation)
	if _, err := exec.LookPath("sudo"); err != nil {
		host.printf("sudo is not available. Run this command as root, then \"owngit service status\":\n  sh %s\n", service.ShellQuote(path))
		return errors.New("the root steps have not run yet")
	}
	host.printf("Running: sudo sh %s\n", service.ShellQuote(path))
	if err := runAttached("sudo", "/bin/sh", path); err != nil {
		host.printf("The root steps did not finish. Fix the problem shown above and run \"owngit service install\" again, or run this command as root:\n  sh %s\n", service.ShellQuote(path))
		return err
	}
	os.Remove(path)
	return nil
}

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
	if _, err := exec.LookPath("sudo"); err != nil {
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

// readableByOthers checks that an account other than the owner can reach
// and run the file: every folder on the way lets others through and the
// file lets others run it.
func readableByOthers(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o001 == 0 {
		return fmt.Errorf("%s is not executable by other accounts", path)
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf("the folder %s is closed to other accounts", dir)
		}
		if parent := filepath.Dir(dir); parent == dir {
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
// without leaving files there that the service cannot open: root switches
// to the account that owns the directory before the command starts. For a
// member of the owngit group who cannot enter the directory, it explains
// that sudo is needed.
func actAsStateOwner(stateDir string) error {
	pointed := pointerStateDir()
	if pointed == "" || filepath.Clean(mustAbs(stateDir)) != pointed {
		return nil
	}
	info, err := os.Stat(pointed)
	if os.Geteuid() != 0 {
		if err != nil && errors.Is(err, os.ErrPermission) || err == nil && !accessible(pointed) {
			return fmt.Errorf("the OwnGit service keeps its state in %s, which only the %s account can open; run this command with sudo", pointed, service.AccountName)
		}
		return nil
	}
	if err != nil {
		return nil
	}
	uid, gid, ok := service.FileOwner(info)
	if !ok || uid == 0 {
		return nil
	}
	return dropToAccountID(uid, gid)
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
