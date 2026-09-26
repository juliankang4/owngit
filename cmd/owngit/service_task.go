package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"owngit/internal/service"
)

// The Windows backend of "owngit service": a Task Scheduler task that runs
// "owngit serve" and, for an administrator account, a Windows Firewall rule
// for owngit.exe on private networks.
//
// An administrator account gets a task that starts at boot without a
// sign-in (S4U, no stored password). Registering it, the firewall rule and
// installing a missing Git need administrator rights, so this command asks
// Windows once (UAC) to run "owngit service elevated-install", which does
// exactly those steps for its own executable and touches no state. A
// standard account may neither use S4U nor a boot trigger, so it gets a
// task that starts when it signs in, registered without any prompt.
//
// The server never runs with administrator rights: an S4U task of an
// administrator gets the full token, so "serve --service" starts the real
// server with a restricted copy of it (see serveWithoutAdminRights).
// Likewise, an elevated "owngit service" leaves everything that reads or
// writes the state directory to a copy of itself without those rights.

// Operating system operations of the Windows backend. The functions live
// in service_task_windows.go; tests replace them.
var (
	// runElevated runs this executable with the arguments after the owner
	// approves it in the UAC prompt, and returns its exit code.
	runElevated = platformRunElevated
	// runWithoutAdminRights runs this executable with the arguments and a
	// restricted copy of this process's token, on this console, and
	// returns its exit code.
	runWithoutAdminRights = platformRunWithoutAdminRights
	// signalServiceStop asks the server of stateDir that runs as the task
	// to stop, and reports whether one was listening.
	signalServiceStop = platformSignalServiceStop
	// currentAccountSID returns the security identifier of this account.
	currentAccountSID = platformCurrentAccountSID
	// windowsSystemDirectory returns the System32 directory.
	windowsSystemDirectory = platformSystemDirectory
)

// taskStopTimeout bounds the wait for a server to stop after it was asked
// to; its own shutdown steps add up to about two minutes.
const taskStopTimeout = 150 * time.Second

// taskHost is this Windows computer and the account running the command.
type taskHost struct {
	env        service.Environment
	executable string
	sid        string
	system     string
	out        io.Writer
}

func newTaskHost() (*taskHost, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	sid, err := currentAccountSID()
	if err != nil {
		return nil, fmt.Errorf("find the current account: %w", err)
	}
	system, err := windowsSystemDirectory()
	if err != nil {
		return nil, err
	}
	return &taskHost{env: probeEnvironment(), executable: executable, sid: sid, system: system, out: os.Stdout}, nil
}

// printf prints to the owner, with control and direction characters of
// text arguments replaced, since some come from the state directory.
func (host *taskHost) printf(format string, args ...any) {
	for index, arg := range args {
		if text, ok := arg.(string); ok {
			args[index] = printable(text)
		}
	}
	fmt.Fprintf(host.out, format, args...)
}

func (host *taskHost) schtasks() string { return host.system + `\schtasks.exe` }
func (host *taskHost) powershell() string {
	return host.system + `\WindowsPowerShell\v1.0\powershell.exe`
}

// runPowerShell runs one of the fixed scripts of the service package.
func (host *taskHost) runPowerShell(script string) ([]byte, error) {
	return serviceRunner(context.Background(), host.powershell(), service.PowerShellArguments(script)...)
}

// taskServiceCommand runs one "owngit service" action on Windows.
func taskServiceCommand(action string, arguments []string) error {
	flags := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var stateDir *string
	var headless, installGit, remove *bool
	var attach *int
	switch action {
	case "install", "report":
		stateDir = flags.String("state-dir", "", "state directory the service uses (default: the one owngit uses for this account)")
	case "elevated-install", "elevated-uninstall", "elevated-firewall":
		// Set by the command that asked for the UAC prompt.
		stateDir = flags.String("state-dir", "", "state directory the task passes")
		headless = flags.Bool("headless", false, "pass --headless to the server")
		installGit = flags.Bool("install-git", false, "install Git with winget when it is missing")
		remove = flags.Bool("remove", false, "remove the firewall rule instead of adding it")
		attach = flags.Int("attach", 0, "process whose console shows the output")
	}
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("service %s takes no arguments", action)
	}
	host, err := newTaskHost()
	if err != nil {
		return err
	}
	switch action {
	case "install":
		return host.install(*stateDir)
	case "report":
		return host.report(*stateDir)
	case "elevated-install", "elevated-uninstall", "elevated-firewall":
		if *attach != 0 {
			attachToConsole(*attach)
			host.out = os.Stdout
		}
		if !host.env.Elevated {
			return errors.New("this step runs only with administrator rights, started by \"owngit service install\"")
		}
		switch {
		case action == "elevated-install":
			return host.elevatedInstall(*stateDir, *headless, *installGit)
		case action == "elevated-uninstall":
			return host.elevatedUninstall()
		case *remove:
			return host.removeFirewallRule()
		default:
			return host.allowThroughFirewall()
		}
	}
	// Everything else reads the state directory, which an elevated process
	// must not touch: a copy of this command without administrator rights
	// does it.
	if host.env.Elevated && action != "uninstall" {
		return host.withoutAdminRights(append([]string{"service", action}, arguments...))
	}
	switch action {
	case "uninstall":
		return host.uninstall()
	case "status":
		return host.status()
	default:
		return host.control(action)
	}
}

// withoutAdminRights runs an owngit command without administrator rights on
// this console and passes its result on.
func (host *taskHost) withoutAdminRights(arguments []string) error {
	_, err := stateCommandWithoutAdminRights(arguments[0], arguments[1:])
	return err
}

// stateCommandWithoutAdminRights runs a command that works on a state
// directory as a copy of this process without administrator rights, when
// this process has them on Windows (a terminal opened with "Run as
// administrator", or SSH as an administrator). Files it creates then
// belong to the account, as those of the service do, and Git accepts the
// repositories as the account's own. "serve --service" does the same in
// serve, where it also passes on the stop request.
func stateCommandWithoutAdminRights(command string, arguments []string) (bool, error) {
	environment := probeEnvironment()
	if !environment.Windows || !environment.Elevated || command == "serve" && flagGiven(arguments, "service") {
		return false, nil
	}
	code, err := runWithoutAdminRights(append([]string{command}, arguments...))
	if err != nil {
		return true, fmt.Errorf("run owngit without administrator rights: %w", err)
	}
	if code != 0 {
		return true, &checkExit{code: code, err: fmt.Errorf("exit status %d", code)}
	}
	return true, nil
}

// installed finds the task of an earlier install.
func (host *taskHost) installed() (service.Installed, bool, error) {
	output, err := host.runPowerShell(service.TaskDefinitionScript)
	if err != nil {
		return service.Installed{}, false, fmt.Errorf("read the scheduled task %q: %w: %s", service.TaskName, err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(string(output)) == "" {
		return service.Installed{}, false, nil
	}
	installed, err := service.ParseTask(output)
	if errors.Is(err, service.ErrForeignTask) {
		return installed, true, fmt.Errorf("%w; OwnGit leaves it alone. Remove or rename it first", err)
	}
	return installed, err == nil, err
}

func (host *taskHost) plan(mode service.Mode, stateDir string, headless bool) service.TaskPlan {
	return service.TaskPlan{
		Mode: mode, Executable: host.executable, StateDir: stateDir, UserSID: host.sid,
		Headless: headless, Conhost: host.system + `\conhost.exe`,
	}
}

func (host *taskHost) install(stateDirFlag string) error {
	existing, found, err := host.installed()
	if err != nil {
		return err
	}
	if found && existing.User != host.sid {
		return fmt.Errorf("the OwnGit task runs as another account (%s); run \"owngit service uninstall\" from that account first", existing.User)
	}
	stateDir := existing.StateDir
	if stateDirFlag != "" || stateDir == "" {
		value := stateDirFlag
		if value == "" {
			value = defaultStateDir()
		}
		if stateDir, err = filepath.Abs(value); err != nil {
			return err
		}
	}
	headless := host.env.Headless() || (found && existing.Headless)
	mode := host.env.ChooseMode(false)
	plan := host.plan(mode, stateDir, headless)
	// Refuse a path the task cannot hold before anything changes.
	if _, err := service.RenderTask(plan); err != nil {
		return err
	}
	_, gitErr := lookPath("git")
	_, wingetErr := lookPath("winget")
	installGit := gitErr != nil
	if installGit && (wingetErr != nil || mode == service.ModeLogonTask) {
		host.printf("OwnGit needs Git for Windows. Install it from https://git-scm.com/download/win (or run \"winget install --id Git.Git -e\"), then run \"owngit service install\" again.\n")
		return errors.New("git is not installed")
	}
	if found {
		host.stopTask(existing.StateDir)
	}
	switch mode {
	case service.ModeBootTask:
		arguments := []string{"service", "elevated-install", "--state-dir", stateDir}
		if headless {
			arguments = append(arguments, "--headless")
		}
		if installGit {
			arguments = append(arguments, "--install-git")
		}
		steps := "register the OwnGit task that starts at boot and allow OwnGit through Windows Firewall on private networks"
		if installGit {
			steps += ", and install Git with winget"
		}
		if err := host.asAdministrator(arguments, steps); err != nil {
			return err
		}
	default:
		if err := host.registerTask(plan); err != nil {
			return err
		}
		if err := host.runTask(); err != nil {
			return err
		}
		host.printf("OwnGit starts when you sign in. To start it at boot, run \"owngit service install\" from an administrator account.\n")
	}
	if host.env.Elevated {
		return host.withoutAdminRights([]string{"service", "report", "--state-dir", stateDir})
	}
	return host.report(stateDir)
}

// asAdministrator runs "owngit" with the arguments with administrator
// rights: here when this process has them, otherwise after one UAC prompt
// that it announces in one line.
func (host *taskHost) asAdministrator(arguments []string, steps string) error {
	if host.env.Elevated {
		return taskServiceCommand(arguments[1], arguments[2:])
	}
	if host.env.NoDesktop {
		host.printf("Windows asks for administrator approval to %s, and this session has no desktop to show that prompt. Run the same command in a terminal opened with \"Run as administrator\", or from the desktop.\n", steps)
		return errors.New("administrator approval is needed")
	}
	host.printf("Windows asks once for administrator approval to %s.\n", steps)
	code, err := runElevated(append(arguments, "--attach", strconv.Itoa(os.Getpid())))
	switch {
	case errors.Is(err, errElevationCancelled):
		host.printf("Nothing changed, because the administrator approval was not given.\n")
		return &checkExit{code: 1, err: err}
	case err != nil:
		return err
	case code != 0:
		// The elevated copy printed why on this console when it could.
		host.printf("The administrator steps did not finish (exit status %d). If no reason is shown above, run the same command in a terminal opened with \"Run as administrator\".\n", code)
		return &checkExit{code: 1, err: errors.New("the administrator steps did not finish")}
	}
	return nil
}

// errElevationCancelled means the owner declined the UAC prompt.
var errElevationCancelled = errors.New("administrator approval was declined")

// elevatedInstall does the steps that need administrator rights: Git if it
// is missing, the boot task for this executable and this account, the
// firewall rule, and the start. It creates nothing in the state directory,
// which the server creates itself without administrator rights.
func (host *taskHost) elevatedInstall(stateDir string, headless, installGit bool) error {
	if _, err := lookPath("git"); err != nil && installGit {
		host.printf("Installing Git for Windows with winget.\n")
		winget, err := lookPath("winget")
		if err != nil {
			return errors.New("winget is not available; install Git for Windows from https://git-scm.com/download/win")
		}
		if err := runAttached(winget, "install", "--id", "Git.Git", "-e", "--source", "winget", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"); err != nil {
			return fmt.Errorf("install Git with winget: %w", err)
		}
	}
	if err := host.registerTask(host.plan(service.ModeBootTask, stateDir, headless)); err != nil {
		return err
	}
	if err := host.allowThroughFirewall(); err != nil {
		return err
	}
	// A server still running from before is stopped; the task starts the
	// new one.
	_, _ = serviceRunner(context.Background(), host.schtasks(), "/End", "/TN", `\`+service.TaskName)
	return host.runTask()
}

// registerTask creates or replaces the task. The definition passes through
// a private temporary file, since schtasks reads it from a file.
func (host *taskHost) registerTask(plan service.TaskPlan) error {
	definition, err := service.RenderTask(plan)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "owngit-task-*.xml")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	_, writeErr := file.Write(service.EncodeUTF16(definition))
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return host.runStep(host.schtasks(), "/Create", "/TN", `\`+service.TaskName, "/XML", path, "/F")
}

func (host *taskHost) runTask() error {
	return host.runStep(host.schtasks(), "/Run", "/TN", `\`+service.TaskName)
}

func (host *taskHost) runStep(name string, args ...string) error {
	output, err := serviceRunner(context.Background(), name, args...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		return fmt.Errorf("%s %s: %w: %s", filepath.Base(strings.ReplaceAll(name, `\`, "/")), strings.Join(args, " "), err, detail)
	}
	return nil
}

// allowThroughFirewall replaces OwnGit's inbound rule with one for this
// owngit.exe on the Private profile. It needs administrator rights.
func (host *taskHost) allowThroughFirewall() error {
	if err := os.Setenv(service.FirewallProgramVariable, host.executable); err != nil {
		return err
	}
	if output, err := host.runPowerShell(service.FirewallAllowScript); err != nil {
		return fmt.Errorf("add the Windows Firewall rule %q: %w: %s", service.FirewallRuleName, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (host *taskHost) removeFirewallRule() error {
	if output, err := host.runPowerShell(service.FirewallRemoveScript); err != nil {
		return fmt.Errorf("remove the Windows Firewall rule %q: %w: %s", service.FirewallRuleName, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// firewallRule reads OwnGit's rule; found is false when there is none or it
// cannot be read.
func (host *taskHost) firewallRule() (service.FirewallRule, bool) {
	output, err := host.runPowerShell(service.FirewallShowScript)
	if err != nil {
		return service.FirewallRule{}, false
	}
	return service.ParseFirewallRule(string(output))
}

// ensureWindowsFirewallRule lets devices on private networks reach this
// owngit.exe. It does nothing when the rule is already there; otherwise it
// adds it, asking Windows once for administrator approval when needed. It
// is for turning on access from other devices.
func ensureWindowsFirewallRule(out io.Writer) error {
	host, err := newTaskHost()
	if err != nil {
		return err
	}
	host.out = out
	if rule, found := host.firewallRule(); found && rule.Allows(host.executable) {
		return nil
	}
	return host.asAdministrator([]string{"service", "elevated-firewall"}, "allow OwnGit through Windows Firewall on private networks")
}

// stopTask asks a running server to stop, waits until the task no longer
// runs, and ends the task when it does not stop in time.
func (host *taskHost) stopTask(stateDir string) {
	if running, _ := host.taskRunning(); !running {
		return
	}
	if asked, _ := signalServiceStop(stateDir); asked {
		deadline := time.Now().Add(taskStopTimeout)
		for time.Now().Before(deadline) {
			if running, err := host.taskRunning(); err != nil || !running {
				return
			}
			time.Sleep(time.Second)
		}
	}
	_, _ = serviceRunner(context.Background(), host.schtasks(), "/End", "/TN", `\`+service.TaskName)
}

// taskState returns the task's state (service.TaskReady and the others)
// and its last result.
func (host *taskHost) taskState() (int, int64, error) {
	output, err := host.runPowerShell(service.TaskStateScript)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("unexpected task state %q", string(output))
	}
	state, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected task state %q", string(output))
	}
	result, _ := strconv.ParseInt(fields[1], 10, 64)
	return state, result, nil
}

func (host *taskHost) taskRunning() (bool, error) {
	state, _, err := host.taskState()
	return state == service.TaskRunning, err
}

// report waits for the started server and prints where it runs and, while
// setup is not done, the setup link.
func (host *taskHost) report(stateDir string) error {
	installed, found, err := host.installed()
	if err != nil || !found {
		return errors.Join(err, errors.New("the OwnGit task is not registered"))
	}
	logFile := service.TaskLogFile(stateDir)
	address, err := waitHealthy(stateDir, serviceStartTimeout)
	if err != nil {
		host.printf("OwnGit did not answer within %s: %v\nSee the log: %s\n", serviceStartTimeout, err, logFile)
		return errors.New("the service did not start")
	}
	host.printf("OwnGit is running as a %s.\n", installed.Mode.Describe())
	host.printTaskFacts(stateDir, ownerAddresses(stateDir, address))
	return printSetupLinkIfNeeded(stateDir, host.out)
}

func (host *taskHost) printTaskFacts(stateDir, address string) {
	host.printf("  Task:     %s (Task Scheduler)\n", `\`+service.TaskName)
	host.printf("  Log:      %s\n", service.TaskLogFile(stateDir))
	host.printf("  State:    %s\n", stateDir)
	if address != "" {
		host.printf("  Address:  %s\n", address)
	}
	if rule, found := host.firewallRule(); found && rule.Allows(host.executable) {
		host.printf("  Firewall: devices on private networks may connect (rule %q)\n", service.FirewallRuleName)
	} else {
		host.printf("  Firewall: no rule for this owngit.exe, so other devices may be blocked\n")
	}
}

func (host *taskHost) uninstall() error {
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	if !found {
		host.printf("OwnGit is not installed as a service.\n")
		return nil
	}
	host.stopTask(installed.StateDir)
	_, ruleFound := host.firewallRule()
	removed := "The OwnGit service is stopped and removed"
	switch {
	case installed.Mode == service.ModeBootTask || ruleFound && host.env.Administrator:
		if err := host.asAdministrator([]string{"service", "elevated-uninstall"}, "remove the OwnGit task and its Windows Firewall rule"); err != nil {
			return err
		}
		removed += ", with its Windows Firewall rule"
	default:
		if err := host.runStep(host.schtasks(), "/Delete", "/TN", `\`+service.TaskName, "/F"); err != nil {
			return err
		}
		if ruleFound {
			host.printf("The Windows Firewall rule %q stays; an administrator can remove it with \"owngit service uninstall\".\n", service.FirewallRuleName)
		}
	}
	host.printf("%s. The state stays in %s", removed, installed.StateDir)
	if !host.env.Elevated {
		if repositories := savedRepositoryRoot(installed.StateDir); repositories != "" {
			host.printf(" and the repositories in %s", repositories)
		}
	}
	host.printf(".\nRun \"owngit service install\" to use it again.\n")
	return nil
}

// elevatedUninstall removes the task and OwnGit's firewall rule. It keeps
// the state directory and the repositories.
func (host *taskHost) elevatedUninstall() error {
	_, _ = serviceRunner(context.Background(), host.schtasks(), "/End", "/TN", `\`+service.TaskName)
	if _, found, err := host.installed(); err != nil {
		return err
	} else if found {
		if err := host.runStep(host.schtasks(), "/Delete", "/TN", `\`+service.TaskName, "/F"); err != nil {
			return err
		}
	}
	return host.removeFirewallRule()
}

func (host *taskHost) status() error {
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	if !found {
		host.printf("OwnGit is not installed as a service. Run \"owngit service install\".\n")
		return nil
	}
	state, result, stateErr := host.taskState()
	address, answered := "", false
	if target, _, err := healthAddress(installed.StateDir); err == nil {
		answered = checkHealth(target) == nil
		if answered {
			address = ownerAddresses(installed.StateDir, target)
		}
	}
	switch {
	case answered:
		host.printf("OwnGit is running and answers its health check.\n")
	case state == service.TaskQueued:
		host.printf("OwnGit has not started: Windows keeps the task queued. That happens while this account has never signed in on this computer (at the screen or through Remote Desktop); sign in once, and it starts at every boot from then on.\n")
	case state == service.TaskRunning:
		host.printf("OwnGit is starting or not answering yet (task: Running).\n")
	case stateErr != nil:
		host.printf("OwnGit is not running, and the task state cannot be read: %v\n", stateErr)
	default:
		host.printf("OwnGit is not running (task: %s, last result %s).\n", taskStateName(state), taskResult(result))
	}
	host.printf("  Mode:     %s\n", installed.Mode.Describe())
	host.printTaskFacts(installed.StateDir, address)
	if read, complete := setupStatus(installed.StateDir); read && !complete {
		host.printf("Setup is not complete. Run \"owngit setup-link\" in a terminal for the one-time setup link.\n")
	}
	return nil
}

// taskStateName names a task state as Task Scheduler shows it.
func taskStateName(state int) string {
	switch state {
	case service.TaskDisabled:
		return "Disabled"
	case service.TaskQueued:
		return "Queued"
	case service.TaskReady:
		return "Ready"
	case service.TaskRunning:
		return "Running"
	}
	return "Unknown"
}

// taskResult shows a task's last result as Task Scheduler does.
func taskResult(result int64) string {
	return fmt.Sprintf("0x%X", uint32(result))
}

func (host *taskHost) control(action string) error {
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	if !found {
		return errors.New("OwnGit is not installed as a service; run \"owngit service install\"")
	}
	if action == "stop" || action == "restart" {
		host.stopTask(installed.StateDir)
	}
	if action == "stop" {
		when := "at the next boot"
		if installed.Mode == service.ModeLogonTask {
			when = "when you sign in next time"
		}
		host.printf("OwnGit is stopped. It starts again %s, or with \"owngit service start\".\n", when)
		return nil
	}
	if err := host.runTask(); err != nil {
		return err
	}
	address, err := waitHealthy(installed.StateDir, serviceStartTimeout)
	if err != nil {
		return fmt.Errorf("OwnGit did not answer within %s: %w; see the log: %s", serviceStartTimeout, err, service.TaskLogFile(installed.StateDir))
	}
	host.printf("OwnGit is running at http://%s.\n", address)
	return nil
}
