package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"owngit/internal/server"
	"owngit/internal/service"
	"owngit/internal/state"
	"owngit/internal/version"
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

type serviceInstallPaths struct {
	Directory  string
	Executable string
	Temp       string
}

// Operating system operations of the Windows backend. The functions live
// in service_task_windows.go and service_secure_windows.go; tests replace
// them.
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
	// ownerOf returns the owner of a file or folder as a SID string.
	ownerOf = platformOwnerOf
	// giveOwnership makes an account the owner of a folder and of what the
	// Administrators group owns below it; see platformGiveOwnership.
	giveOwnership = platformGiveOwnership
	// repositoryRootWithoutAdminRights returns the repository folder saved
	// in a state directory, read without administrator rights.
	repositoryRootWithoutAdminRights = platformRepositoryRootWithoutAdminRights
	// servicePaths locates the administrator-protected service copy.
	servicePaths = platformServiceInstallPaths
	// prepareServiceStorage creates and verifies its protected folders.
	prepareServiceStorage = platformPrepareServiceStorage
	// replaceServiceCopy refreshes the protected executable from this one.
	replaceServiceCopy = platformReplaceServiceCopy
	// trustedWinget finds winget only in a verified App Installer package.
	trustedWinget = platformTrustedWinget
	// serviceEnvironment is the explicit environment of elevated children.
	serviceEnvironment         = platformServiceEnvironment
	applyServiceEnvironment    = platformApplyServiceEnvironment
	runWithEnvironment         = platformRunWithEnvironment
	runAttachedWithEnvironment = platformRunAttachedWithEnvironment
	// gitOnServicePath reports whether the task will find Git on its PATH.
	gitOnServicePath = platformGitOnServicePath
	// readExecutableVersion reads the version reported by a protected copy.
	readExecutableVersion = executableVersion
	// taskPollInterval is how often waiting for the server also checks
	// whether Windows keeps the task queued.
	taskPollInterval = 5 * time.Second
)

// administratorsSID is the BUILTIN\Administrators group.
const administratorsSID = "S-1-5-32-544"

// restartedVariable tells a server that its supervisor started it again
// after it exited with the status in the value; the server logs that.
const restartedVariable = "OWNGIT_SERVICE_RESTARTED"

// queuedTaskMessage explains a task that Windows keeps queued. It was
// observed on newly installed Windows 11; see docs/OPERATIONS.md.
const queuedTaskMessage = "OwnGit has not started: Windows keeps the task queued. That happens until someone has signed in on this computer at the screen for the first time since Windows was installed (a sign-in over SSH does not count). Sign in once with any account, and OwnGit starts then and at every boot from then on.\n"

// wingetNoApplicableUpgrade is winget's exit code when the package is
// already installed and has no upgrade.
const wingetNoApplicableUpgrade = 0x8A15002B

// gitNotOnPathExit is the exit code of an elevated step that found Git
// installed but not on PATH, and said so in one line.
const gitNotOnPathExit = 3

// taskStopTimeout bounds the wait for a server to stop after it was asked
// to; its own shutdown steps add up to about two minutes.
const taskStopTimeout = 150 * time.Second

// taskHost is this Windows computer and the account running the command.
type taskHost struct {
	env                   service.Environment
	executable            string
	serviceInstall        serviceInstallPaths
	sid                   string
	system                string
	out                   io.Writer
	administratorPrepared bool
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
	install, err := servicePaths()
	if err != nil {
		return nil, err
	}
	return &taskHost{env: probeEnvironment(), executable: executable, serviceInstall: install, sid: sid, system: system, out: os.Stdout}, nil
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

// administratorEnvironment is the complete explicit environment for a child
// that keeps this process's administrator token.
func (host *taskHost) administratorEnvironment(extra ...string) ([]string, error) {
	return serviceEnvironment(host.serviceInstall, extra)
}

func (host *taskHost) prepareAdministrator() error {
	if host.administratorPrepared {
		return nil
	}
	if err := prepareServiceStorage(host.serviceInstall); err != nil {
		return err
	}
	environment, err := host.administratorEnvironment()
	if err != nil {
		return err
	}
	if err := applyServiceEnvironment(environment); err != nil {
		return fmt.Errorf("set the administrator environment: %w", err)
	}
	host.administratorPrepared = true
	return nil
}

// runPowerShell runs one of the fixed scripts of the service package.
func (host *taskHost) runPowerShell(script string, extra ...string) ([]byte, error) {
	arguments := service.PowerShellArguments(script)
	if !host.env.Elevated {
		return serviceRunner(context.Background(), host.powershell(), arguments...)
	}
	environment, err := host.administratorEnvironment(extra...)
	if err != nil {
		return nil, err
	}
	return runWithEnvironment(context.Background(), environment, host.powershell(), arguments...)
}

// taskServiceCommand runs one "owngit service" action on Windows.
func taskServiceCommand(action string, arguments []string) error {
	if strings.HasPrefix(action, "elevated-") {
		host, err := newTaskHost()
		if err != nil {
			return err
		}
		return host.administratorStep(action, arguments)
	}
	flags := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var stateDir *string
	var headless *bool
	switch action {
	case "install":
		stateDir = flags.String("state-dir", "", "state directory the service uses (default: the one owngit uses for this account)")
		headless = flags.Bool("headless", false, "whether this computer has no screen for setup (default: kept from the installed service, or detected for a new one)")
	case "report", "repository-root":
		stateDir = flags.String("state-dir", "", "state directory of the service")
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
		var headlessFlag *bool
		flags.Visit(func(entry *flag.Flag) {
			if entry.Name == "headless" {
				headlessFlag = headless
			}
		})
		return host.install(*stateDir, headlessFlag)
	case "report":
		return host.report(*stateDir)
	case "repository-root":
		if host.env.Elevated {
			return errors.New("this step runs only without administrator rights")
		}
		if root := savedRepositoryRoot(*stateDir); root != "" {
			fmt.Fprintln(host.out, root)
		}
		return nil
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

// administratorStep runs one of the steps that need administrator rights,
// "elevated-install", "elevated-uninstall" or "elevated-firewall", with the
// arguments that the command asking for them passed.
func (host *taskHost) administratorStep(action string, arguments []string) error {
	flags := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", "", "state directory the task passes")
	headless := flags.Bool("headless", false, "the --headless value the task passes to the server")
	installGit := flags.Bool("install-git", false, "install Git with winget when it is missing")
	remove := flags.Bool("remove", false, "remove the firewall rule instead of adding it")
	attach := flags.Int("attach", 0, "process whose console shows the output")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("service %s takes no arguments", action)
	}
	if *attach != 0 {
		attachToConsole(*attach)
		host.out = os.Stdout
	}
	if !host.env.Elevated {
		return errors.New("this step runs only with administrator rights, started by \"owngit service install\"")
	}
	if err := host.prepareAdministrator(); err != nil {
		return err
	}
	switch {
	case action == "elevated-install":
		return host.elevatedInstall(*stateDir, *headless, *installGit)
	case action == "elevated-uninstall":
		return host.elevatedUninstall()
	case action != "elevated-firewall":
		return fmt.Errorf("unknown step %s", action)
	case *remove:
		return host.removeFirewallRule()
	default:
		return host.allowThroughFirewall()
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
	executable := host.executable
	if mode == service.ModeBootTask {
		executable = host.serviceInstall.Executable
	}
	return service.TaskPlan{
		Mode: mode, Executable: executable, StateDir: stateDir, UserSID: host.sid,
		Headless: headless, Conhost: host.system + `\conhost.exe`,
	}
}

// install installs or updates the service. headlessFlag is the --headless
// option, or nil without it.
func (host *taskHost) install(stateDirFlag string, headlessFlag *bool) error {
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
	// As on Linux, the headless choice is made when the service is first
	// installed, and only --headless changes it.
	headless := host.env.Headless()
	switch {
	case headlessFlag != nil:
		headless = *headlessFlag
	case found:
		headless = existing.Headless
	}
	mode := host.env.ChooseMode(false)
	plan := host.plan(mode, stateDir, headless)
	// Refuse a path the task cannot hold before anything changes.
	if _, err := service.RenderTask(plan); err != nil {
		return err
	}
	installGit := !gitOnServicePath()
	if installGit && mode == service.ModeLogonTask {
		host.printf("OwnGit needs Git for Windows. Install it from https://git-scm.com/download/win, then run \"owngit service install\" again.\n")
		return errors.New("git is not installed")
	}
	switch mode {
	case service.ModeBootTask:
		// The running server stops only after the approval, in the step
		// with administrator rights, so declining changes nothing.
		arguments := []string{"service", "elevated-install", "--state-dir", stateDir, "--headless=" + strconv.FormatBool(headless)}
		if installGit {
			arguments = append(arguments, "--install-git")
		}
		steps := []string{"copy OwnGit into Program Files", "register the OwnGit task that starts at boot", "allow OwnGit through Windows Firewall on private networks"}
		if installGit {
			steps = append(steps, "install Git with winget")
		}
		if !host.env.Elevated {
			for _, folder := range host.foldersOfAdministrators(stateDir) {
				steps = append(steps, "make your account the owner of "+folder)
			}
		}
		if err := host.asAdministrator(arguments, listSteps(steps)); err != nil {
			return err
		}
	default:
		// No administrator rights: a folder of the Administrators group
		// stays theirs, and the command says who can change that.
		for _, folder := range host.foldersOfAdministrators(stateDir) {
			host.printf("%s belongs to the Administrators group, so OwnGit cannot use it. An administrator can make your account its owner with: icacls \"%s\" /setowner \"*%s\" /T /C\n", folder, folder, host.sid)
		}
		if found {
			host.stopTask(existing.StateDir)
		}
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
		return host.administratorStep(arguments[1], arguments[2:])
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
	case code == gitNotOnPathExit:
		return &checkExit{code: code, err: errors.New("git is not on PATH")}
	case code != 0:
		// The elevated copy printed why on this console when it could.
		host.printf("The administrator steps did not finish (exit status %d). If no reason is shown above, run the same command in a terminal opened with \"Run as administrator\".\n", code)
		return &checkExit{code: 1, err: errors.New("the administrator steps did not finish")}
	}
	return nil
}

// listSteps joins steps into one sentence: "a and b", "a, b, and c".
func listSteps(steps []string) string {
	if len(steps) < 3 {
		return strings.Join(steps, " and ")
	}
	return strings.Join(steps[:len(steps)-1], ", ") + ", and " + steps[len(steps)-1]
}

// errElevationCancelled means the owner declined the UAC prompt.
var errElevationCancelled = errors.New("administrator approval was declined")

// elevatedInstall does the steps that need administrator rights: Git if it
// is missing, stopping the server that runs, refreshing the protected service
// copy, the account's ownership of its folders, the boot task for this account,
// the firewall rule, and the start. It creates nothing in the state directory,
// which the server creates itself without administrator rights.
func (host *taskHost) elevatedInstall(stateDir string, headless, installGit bool) error {
	if err := host.prepareAdministrator(); err != nil {
		return err
	}
	if installGit {
		winget, err := trustedWinget()
		if err != nil {
			return errors.New("OwnGit needs Git for Windows. Install it from https://git-scm.com/download/win, then run \"owngit service install\" again")
		}
		host.printf("Installing Git for Windows with the trusted App Installer copy of winget.\n")
		environment, err := host.administratorEnvironment()
		if err != nil {
			return err
		}
		code, err := runAttachedWithEnvironment(environment, winget, "install", "--id", "Git.Git", "-e", "--source", "winget", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
		if err == nil && code != 0 && uint32(code) != wingetNoApplicableUpgrade {
			err = fmt.Errorf("exit status 0x%X", uint32(code))
		}
		if err != nil {
			return fmt.Errorf("install Git with winget: %w", err)
		}
		if !gitOnServicePath() {
			host.printf("Git for Windows is installed but not on PATH. Open a new terminal and run \"owngit service install\" again.\n")
			return &checkExit{code: gitNotOnPathExit, err: errors.New("git is not on PATH")}
		}
	}
	if existing, found, err := host.installed(); err == nil && found {
		host.stopTask(existing.StateDir)
	}
	if err := replaceServiceCopy(host.executable, host.serviceInstall); err != nil {
		return err
	}
	// Folders an earlier OwnGit with administrator rights left to the
	// Administrators group are given back to the account, since the server
	// runs without those rights. The state directory comes first, so that
	// the repository folder saved in it can be read.
	host.giveFolderToAccount(stateDir)
	if repositories := repositoryRootWithoutAdminRights(stateDir); repositories != "" {
		host.giveFolderToAccount(repositories)
	}
	if err := host.registerTask(host.plan(service.ModeBootTask, stateDir, headless)); err != nil {
		return err
	}
	if err := host.allowThroughFirewallFor(host.serviceInstall.Executable); err != nil {
		return err
	}
	// A server that did not stop in time is ended; the task starts the new
	// one.
	_ = host.runStep(host.schtasks(), "/End", "/TN", `\`+service.TaskName)
	return host.runTask()
}

// foldersOfAdministrators returns the state directory and the saved
// repository folder when the Administrators group owns them, as far as
// this process can read them.
func (host *taskHost) foldersOfAdministrators(stateDir string) []string {
	var folders []string
	for _, folder := range []string{stateDir, savedRepositoryRoot(stateDir)} {
		if folder == "" || ownershipRefusal(folder, host.env.Getenv) != "" {
			continue
		}
		if owner, err := ownerOf(folder); err == nil && owner == administratorsSID {
			folders = append(folders, folder)
		}
	}
	return folders
}

// giveFolderToAccount makes the account the owner of folder and of what the
// Administrators group owns below it, when the Administrators group or the
// account owns folder. It leaves alone a folder of another account, a
// drive and the folders of Windows and installed programs, and says so in
// one line when the folder is not the account's. It needs administrator
// rights.
func (host *taskHost) giveFolderToAccount(folder string) {
	var refused string
	changed, failed, err := giveOwnership(folder, host.sid, func(finalPath, owner string) error {
		if owner != administratorsSID && owner != host.sid {
			refused = "it belongs to another account"
		} else {
			refused = ownershipRefusal(finalPath, host.env.Getenv)
		}
		if refused != "" && owner != host.sid {
			return errors.New(refused)
		}
		if refused != "" {
			return errOwnershipNotNeeded
		}
		return nil
	})
	switch {
	case errors.Is(err, errOwnershipNotNeeded), errors.Is(err, os.ErrNotExist):
	case refused != "":
		host.printf("OwnGit leaves the owner of %s as it is, because %s. If OwnGit cannot use it, choose a folder in your user folder.\n", folder, refused)
	case err != nil:
		host.printf("Your account could not be made the owner of %s: %v\n", folder, err)
	case failed > 0:
		host.printf("Your account is now the owner of %d files and folders in %s; %d could not be changed.\n", changed, folder, failed)
	case changed > 0:
		host.printf("Your account is now the owner of %d files and folders in %s that belonged to the Administrators group.\n", changed, folder)
	}
}

// errOwnershipNotNeeded stops giveOwnership for a folder that the account
// owns but that OwnGit does not walk, such as a drive.
var errOwnershipNotNeeded = errors.New("the account owns the folder")

// ownershipRefusal says why OwnGit never changes owners in folder, or "".
// folder is a Windows path; getenv reads the environment.
func ownershipRefusal(folder string, getenv func(string) string) string {
	normalize := func(path string) string {
		return strings.TrimRight(strings.ToLower(strings.ReplaceAll(path, "/", `\`)), `\`)
	}
	path := normalize(folder)
	switch {
	case len(path) == 2 && path[1] == ':':
		return "it is a drive"
	case strings.HasPrefix(path, `\\`) && strings.Count(path, `\`) <= 3:
		return "it is a network share"
	}
	within := func(parent string) bool {
		return parent != "" && (path == parent || strings.HasPrefix(path, parent+`\`))
	}
	for _, name := range []string{"SystemRoot", "windir", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if within(normalize(getenv(name))) {
			return "it is a folder of Windows or of installed programs"
		}
	}
	for _, name := range []string{"ProgramData", "USERPROFILE", "PUBLIC"} {
		if parent := normalize(getenv(name)); parent != "" && path == parent {
			return "it holds more than OwnGit's files"
		}
	}
	if drive := normalize(getenv("SystemDrive")); drive != "" && (path == drive+`\users` || path == drive+`\windows`) {
		return "it holds more than OwnGit's files"
	}
	return ""
}

// registerTask creates or replaces the task. An administrator's definition
// passes through the protected service folder, since schtasks reads it from a
// file. A standard account's task has no administrator token and may use its
// private temporary folder.
func (host *taskHost) registerTask(plan service.TaskPlan) error {
	definition, err := service.RenderTask(plan)
	if err != nil {
		return err
	}
	directory := ""
	if host.env.Elevated {
		directory = host.serviceInstall.Temp
	}
	file, err := os.CreateTemp(directory, "owngit-task-*.xml")
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
	var output []byte
	var err error
	if host.env.Elevated {
		environment, environmentErr := host.administratorEnvironment()
		if environmentErr != nil {
			return environmentErr
		}
		output, err = runWithEnvironment(context.Background(), environment, name, args...)
	} else {
		output, err = serviceRunner(context.Background(), name, args...)
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		return fmt.Errorf("%s %s: %w: %s", filepath.Base(strings.ReplaceAll(name, `\`, "/")), strings.Join(args, " "), err, detail)
	}
	return nil
}

// allowThroughFirewall replaces OwnGit's inbound rule with one for this
// owngit.exe on the Private profile. It needs administrator rights.
func (host *taskHost) allowThroughFirewall() error {
	return host.allowThroughFirewallFor(host.executable)
}

func (host *taskHost) allowThroughFirewallFor(program string) error {
	output, err := host.runPowerShell(service.FirewallAllowScript, service.FirewallProgramVariable+"="+program)
	if err != nil {
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
	_ = host.runStep(host.schtasks(), "/End", "/TN", `\`+service.TaskName)
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
	address, err := host.waitForServer(stateDir)
	if err != nil {
		return err
	}
	host.printf("OwnGit is running as a %s.\n", installed.Mode.Describe())
	host.printTaskFacts(stateDir, ownerAddresses(stateDir, address), installed.Executable)
	return printSetupLinkIfNeeded(stateDir, host.out)
}

// waitForServer waits until the server of the task answers and returns its
// address. It stops early, with one line that says why, when Windows keeps
// the task queued or the server could not start.
func (host *taskHost) waitForServer(stateDir string) (string, error) {
	deadline := time.Now().Add(serviceStartTimeout)
	for {
		address, err := waitForService(stateDir, taskPollInterval)
		if err == nil {
			return address, nil
		}
		var failed errServeFailed
		if errors.As(err, &failed) {
			host.printf("OwnGit could not start: %s\n", failed.message)
			host.printLogIfWritten(stateDir)
			return "", errors.New("the service did not start")
		}
		if state, _, stateErr := host.taskState(); stateErr == nil && state == service.TaskQueued {
			host.printf(queuedTaskMessage)
			return "", &checkExit{code: 1, err: errors.New("Windows keeps the task queued")}
		}
		if time.Now().After(deadline) {
			host.printf("OwnGit did not answer within %s: %v\n", serviceStartTimeout, err)
			host.printLogIfWritten(stateDir)
			return "", errors.New("the service did not start")
		}
	}
}

// printLogIfWritten points to the server log when the server has written
// something there.
func (host *taskHost) printLogIfWritten(stateDir string) {
	if info, err := os.Stat(service.TaskLogFile(stateDir)); err == nil && info.Size() > 0 {
		host.printf("See the log: %s\n", service.TaskLogFile(stateDir))
	}
}

func (host *taskHost) printTaskFacts(stateDir, address, executable string) {
	host.printf("  Task:     %s (Task Scheduler)\n", `\`+service.TaskName)
	host.printf("  Log:      %s\n", service.TaskLogFile(stateDir))
	host.printf("  State:    %s\n", stateDir)
	if address != "" {
		host.printf("  Address:  %s\n", address)
	}
	switch rule, found := host.firewallRule(); {
	case found && rule.Allows(executable):
		host.printf("  Firewall: devices on private networks may connect (rule %q)\n", service.FirewallRuleName)
	case listensBeyondThisComputer(stateDir):
		host.printf("  Firewall: no rule for this owngit.exe, so other devices may be blocked; run \"owngit service install\" to add it\n")
	}
}

// listensBeyondThisComputer reports whether the server of stateDir listens,
// or will listen by its saved setting, on an address other devices reach.
func listensBeyondThisComputer(stateDir string) bool {
	if state.RequireExisting(stateDir) != nil {
		return false
	}
	ctx := context.Background()
	store, err := openLiveState(ctx, stateDir)
	if err != nil {
		return false
	}
	defer store.Close()
	listen := server.DefaultListenAddress
	if observed, err := store.ObserveRunningNetwork(ctx); err == nil && observed.Server == state.ServerRunning && observed.Record != nil {
		listen = cmp.Or(observed.Record.Listen, observed.Record.Address)
	} else if saved, err := store.NetworkSettings(ctx); err == nil && saved.Listen != "" {
		listen = saved.Listen
	}
	host, _, err := net.SplitHostPort(listen)
	return err == nil && !server.IsLoopbackHost(host)
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
	_, ruleFound := host.firewallRule()
	removed := "The OwnGit service is stopped and removed"
	switch {
	case installed.Mode == service.ModeBootTask || ruleFound && host.env.Administrator:
		// The server stops in the step with administrator rights, so
		// declining the prompt changes nothing.
		if err := host.asAdministrator([]string{"service", "elevated-uninstall"}, "remove the OwnGit task and its Windows Firewall rule"); err != nil {
			return err
		}
		removed += ", with its Windows Firewall rule"
	default:
		host.stopTask(installed.StateDir)
		if err := host.runStep(host.schtasks(), "/Delete", "/TN", `\`+service.TaskName, "/F"); err != nil {
			return err
		}
		if ruleFound {
			host.printf("The Windows Firewall rule %q stays; an administrator can remove it with \"owngit service uninstall\".\n", service.FirewallRuleName)
		}
	}
	host.printf("%s. The state stays in %s", removed, installed.StateDir)
	repositories := ""
	if host.env.Elevated {
		repositories = repositoryRootWithoutAdminRights(installed.StateDir)
	} else {
		repositories = savedRepositoryRoot(installed.StateDir)
	}
	if repositories != "" {
		host.printf(" and the repositories in %s", repositories)
	}
	host.printf(".\nRun \"owngit service install\" to use it again.\n")
	return nil
}

// elevatedUninstall removes the task and OwnGit's firewall rule. It keeps
// the state directory and the repositories.
func (host *taskHost) elevatedUninstall() error {
	if err := host.prepareAdministrator(); err != nil {
		return err
	}
	installed, found, err := host.installed()
	if err != nil {
		return err
	}
	if found {
		host.stopTask(installed.StateDir)
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
	// A server that starts listening removes the error of the last start.
	lastError, failed := serveErrorSince(installed.StateDir, time.Time{})
	failed = failed && !answered
	switch {
	case answered:
		host.printf("OwnGit is running and answers its health check.\n")
	case state == service.TaskQueued:
		host.printf(queuedTaskMessage)
	case state == service.TaskRunning && failed:
		host.printf("OwnGit keeps failing to start (task: Running).\n")
	case state == service.TaskRunning:
		host.printf("OwnGit is starting or not answering yet (task: Running).\n")
	case stateErr != nil:
		host.printf("OwnGit is not running, and the task state cannot be read: %v\n", stateErr)
	default:
		host.printf("OwnGit is not running (task: %s, last result %s).\n", taskStateName(state), taskResult(result))
	}
	if failed {
		host.printf("  Last error: %s\n", lastError)
	}
	host.printf("  Mode:     %s\n", installed.Mode.Describe())
	host.printTaskFacts(installed.StateDir, address, installed.Executable)
	if installed.Mode == service.ModeBootTask {
		if !strings.EqualFold(installed.Executable, host.serviceInstall.Executable) {
			host.printf("  Update:   this task does not use the protected service copy; run \"owngit service install\" to refresh it\n")
		} else if installedVersion, err := readExecutableVersion(installed.Executable); err == nil && installedVersion != version.Version {
			host.printf("  Update:   the service copy is version %s; run \"owngit service install\" to refresh it to version %s\n", installedVersion, version.Version)
		}
	}
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

func executableVersion(path string) (string, error) {
	output, err := exec.Command(path, "version").Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	value, found := strings.CutPrefix(value, "owngit ")
	if !found || value == "" || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("unexpected version output %q", value)
	}
	return value, nil
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
	address, err := host.waitForServer(installed.StateDir)
	if err != nil {
		return err
	}
	host.printf("OwnGit is running at %s.\n", ownerAddresses(installed.StateDir, address))
	return nil
}
