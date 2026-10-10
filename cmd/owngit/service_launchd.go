package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"owngit/internal/service"
	"owngit/internal/state"
)

// On macOS "owngit service" writes a LaunchAgent for the user who runs it;
// see service.RenderLaunchAgent. A binary that Homebrew installed is left
// to "brew services" in a desktop login, as on Linux; without one the OwnGit
// agent runs Homebrew's binary until brew services takes over. A launchd
// job of someone else that runs "owngit serve" is left alone.

// launchdFolders are the folders besides the user's own LaunchAgents
// folder that may hold a job running "owngit serve". Tests replace them.
var launchdFolders = []string{"/Library/LaunchAgents", "/Library/LaunchDaemons"}

var applicationsFolder = "/Applications"

// launchAgentHome replaces the home folder that holds the LaunchAgents and
// Logs folders when it is not empty. Tests set it, so that no test touches
// the real ones.
var launchAgentHome = ""

var requireProtectedPath = state.RequireProtectedPath

// launchAgentHost is the macOS service backend.
type launchAgentHost struct {
	*serviceHost
	uid int
	// agentPath is the agent file; agentExecutable is the binary it starts.
	agentPath, agentExecutable string
}

func newLaunchAgentHost(host *serviceHost) (*launchAgentHost, error) {
	if launchAgentHome != "" {
		account := *host.account
		account.HomeDir = launchAgentHome
		host.account = &account
	}
	uid, err := strconv.Atoi(host.account.Uid)
	if err != nil {
		return nil, fmt.Errorf("user ID %q: %w", host.account.Uid, err)
	}
	invoked, _ := os.Executable()
	return &launchAgentHost{
		serviceHost: host, uid: uid,
		agentPath:       service.LaunchAgentPath(host.account.HomeDir),
		agentExecutable: service.AgentExecutable(invoked, host.executable),
	}, nil
}

// installedAgent reads the agent of an earlier install.
func (host *launchAgentHost) installedAgent() (service.Installed, bool, error) {
	installed, err := service.ReadLaunchAgent(host.agentPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return installed, false, nil
	case errors.Is(err, service.ErrForeignUnit):
		return installed, false, fmt.Errorf("%s was not written by \"owngit service install\", so OwnGit leaves it alone. Remove or rename it first", host.agentPath)
	case err != nil:
		return installed, false, err
	}
	return installed, true, nil
}

// launchdJob is a launchd job of someone else that runs "owngit serve".
type launchdJob struct {
	service.FoundAgent
	loaded, homebrew bool
}

func (job launchdJob) String() string {
	if job.homebrew {
		return fmt.Sprintf("the Homebrew service (%s)", job.Path)
	}
	return fmt.Sprintf("the launchd job %s (%s)", cmp.Or(job.Label, "without a label"), job.Path)
}

// otherJobs lists the launchd jobs that run "owngit serve" other than the
// OwnGit agent, and whether launchd has loaded each.
func (host *launchAgentHost) otherJobs() []launchdJob {
	ctx := context.Background()
	folders := append([]string{filepath.Dir(host.agentPath)}, launchdFolders...)
	var jobs []launchdJob
	for _, found := range service.FindServeAgents(ctx, serviceRunner, folders, service.LaunchAgentLabel) {
		jobs = append(jobs, launchdJob{
			FoundAgent: found, loaded: service.JobLoaded(ctx, serviceRunner, host.uid, found),
			homebrew: slices.Contains(service.HomebrewLabels, found.Label),
		})
	}
	return jobs
}

// describeOtherJob says in one line what another job is doing, or "" when
// there is none.
func describeOtherJob(jobs []launchdJob) string {
	if len(jobs) == 0 {
		return ""
	}
	if jobs[0].loaded {
		return fmt.Sprintf("OwnGit runs from %s, which \"owngit service\" did not set up and leaves alone.", jobs[0])
	}
	return fmt.Sprintf("%s is set up to run \"owngit serve\" but is not loaded now; \"owngit service\" did not set it up and leaves it alone.", upperFirst(jobs[0].String()))
}

func upperFirst(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// brewRoute reports whether "brew services" runs the service: for a
// Homebrew binary in a desktop login. Homebrew's service loads only there,
// so over SSH without a desktop login the OwnGit agent runs Homebrew's
// binary instead.
func (host *launchAgentHost) brewRoute() bool {
	return host.homebrew != "" && host.env.GraphicalSession
}

// install installs or updates the service; see serviceHost.install for the
// headless choice.
func (host *launchAgentHost) install(stateDirFlag string, headlessFlag *bool) error {
	if host.env.EUID == 0 {
		return errors.New("on macOS the service runs as the user who uses OwnGit; run \"owngit service install\" as that user, without sudo")
	}
	existing, found, err := host.installedAgent()
	if err != nil {
		return err
	}
	for _, job := range host.otherJobs() {
		// Homebrew's own service of this binary: brew services manages it,
		// and one that is not loaded takes over from the OwnGit agent when
		// it starts (see yieldToHomebrew).
		if job.homebrew && host.homebrew != "" && (host.brewRoute() || !job.loaded) {
			continue
		}
		if job.loaded {
			host.printf("OwnGit already runs from %s, which \"owngit service\" did not set up, so it leaves that alone and starts no second server.\n", job)
			return nil
		}
		return fmt.Errorf("%s is set up to run \"owngit serve\" but is not loaded now, and OwnGit leaves it alone; remove that file, then run \"owngit service install\" again", upperFirst(job.String()))
	}
	stateDir, err := host.agentStateDir(stateDirFlag, existing, found)
	if err != nil {
		return err
	}
	if host.brewRoute() {
		if headlessFlag != nil {
			return errors.New("--headless applies to the OwnGit LaunchAgent; Homebrew's service decides it at its first start, and \"owngit network set --listen\" changes the address")
		}
		return host.installWithBrew(stateDir, found)
	}
	if err := requireProtectedPath(host.agentExecutable); err != nil {
		return fmt.Errorf("the service would run %s, but %w; install OwnGit where only you or root can change it, such as with Homebrew or npm", host.agentExecutable, err)
	}
	switch {
	case host.homebrew != "":
		host.printf("OwnGit was installed with Homebrew. Homebrew's service starts only in a desktop login, so the OwnGit LaunchAgent runs %s instead.\n", host.agentExecutable)
	case service.NPMExecutable(host.agentExecutable):
		host.printf("OwnGit was installed with npm, so the service runs the executable of its platform package directly: %s\n", host.agentExecutable)
	}
	plan := host.agentPlan(stateDir, headlessFlag, existing, found)
	if host.homebrew == "" || host.iconAppPath() != "" {
		if plan.App, err = service.AppBundleID(context.Background(), serviceRunner, plan.Executable, applicationsFolder); err != nil {
			return fmt.Errorf("the OwnGit icon app of %s is damaged: %w; install OwnGit again", plan.Executable, err)
		}
	}
	agent, err := service.RenderLaunchAgent(plan)
	if err != nil {
		return err
	}
	host.printf("Installing OwnGit as a %s.\n", plan.Mode.Describe())
	gui := host.env.GraphicalSession
	plan.Notice = func(format string, args ...any) { host.printf("Note: "+format+"\n", args...) }
	if _, err := service.InstallLaunchAgent(context.Background(), serviceRunner, plan, agent, host.uid, gui); err != nil {
		return err
	}
	if !gui {
		host.printf("%s is not logged in on this Mac's screen now, so OwnGit runs in the background until the Mac restarts.\n", host.account.Username)
	}
	if err := host.reportStarted(plan.Mode, host.agentPath, stateDir); err != nil {
		return err
	}
	if host.homebrew != "" && (plan.Headless || !host.env.GraphicalSession) && host.iconAppPath() == "" {
		host.printCaskGuidance()
	}
	host.openIcon(stateDir, plan.Headless)
	return nil
}

// openIcon opens OwnGit.app, the menu bar icon that came with this program,
// so that it shows now; the app then opens itself at every sign-in. An
// icon of this account that already runs is restarted instead, so that it
// runs the app an update just replaced; it opens as at sign-in, so a
// hidden icon stays hidden and no panel opens. Without a running icon it
// does nothing when the owner hid the icon. It never acts without the app,
// for a headless service, without this user's desktop login, or when the
// icon itself runs this command. The service runs either way, so a
// failure is only reported.
func (host *launchAgentHost) openIcon(stateDir string, headless bool) {
	if headless {
		return
	}
	if host.homebrew != "" && host.iconAppPath() == "" {
		if host.env.GraphicalSession && host.env.Getenv("OWNGIT_FROM_ICON") == "" {
			host.printCaskGuidance()
		}
		return
	}
	if host.restartIcon() || trayHiddenIn(stateDir) {
		return
	}
	app, ok := host.iconApp()
	if !ok {
		return
	}
	if output, err := serviceRunner(context.Background(), "/usr/bin/open", app); err != nil {
		host.printf("The OwnGit menu bar icon did not open (%v: %s). Open %s to show it; OwnGit runs without it.\n", err, strings.TrimSpace(string(output)), app)
		return
	}
	host.printf("The OwnGit icon is in the menu bar and opens when you sign in. \"owngit tray off\" hides it; OwnGit keeps running.\n")
}

func (host *launchAgentHost) iconAppPath() string {
	app := service.AppPath(host.agentExecutable, applicationsFolder)
	if host.homebrew == "" {
		return app
	}
	if app != filepath.Join(applicationsFolder, service.AppName) {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(app)
	if err != nil {
		return ""
	}
	cellar, err := os.Stat(filepath.Join(host.homebrew, "Cellar"))
	if err != nil || !cellar.IsDir() {
		return ""
	}
	for dir := resolved; ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil || os.SameFile(info, cellar) {
			return ""
		}
		parent := filepath.Dir(dir)
		if strings.EqualFold(filepath.Base(parent), "Cellar") && strings.EqualFold(filepath.Base(dir), "owngit") {
			formula, err := os.Stat(filepath.Join(filepath.Dir(parent), "Cellar", "owngit"))
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return ""
				}
			} else if os.SameFile(info, formula) {
				return ""
			}
		}
		if parent == dir {
			return app
		}
	}
}

func (host *launchAgentHost) printCaskGuidance() {
	host.printf("The Homebrew menu bar icon needs a matching app in /Applications. Install or update it with brew install --cask owngit (or brew upgrade --cask owngit), then run owngit service install. The formula's command and server work without the icon.\n")
}

// iconApp returns the OwnGit.app of the service's program, resolved to
// the place macOS runs it from, when this command may open it.
func (host *launchAgentHost) iconApp() (string, bool) {
	app := host.iconAppPath()
	if app == "" || !host.env.GraphicalSession || host.env.Getenv("OWNGIT_FROM_ICON") != "" {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(app); err == nil {
		app = resolved
	}
	if err := requireProtectedPath(service.AppLauncher(app)); err != nil {
		host.printf("OwnGit does not open its menu bar icon at %s, because %v. OwnGit runs without it.\n", app, err)
		if host.homebrew != "" {
			host.printCaskGuidance()
		}
		return "", false
	}
	return app, true
}

// restartIcon quits this account's running icon of the service's app and
// opens it again as at sign-in. It reports whether an icon was running.
func (host *launchAgentHost) restartIcon() bool {
	app, ok := host.iconApp()
	if !ok {
		return false
	}
	ctx := context.Background()
	apps := service.AppPaths(host.agentExecutable, applicationsFolder)
	if len(apps) == 0 {
		return false
	}
	account, running := strconv.Itoa(host.uid), iconPattern(apps...)
	if output, _ := serviceRunner(ctx, "/usr/bin/pgrep", "-U", account, "-f", running); strings.TrimSpace(string(output)) == "" {
		return false
	}
	_, _ = serviceRunner(ctx, "/usr/bin/pkill", "-U", account, "-f", running)
	if exited, err := iconExited(ctx, account, running); !exited {
		if err != nil {
			host.printf("Could not check whether the earlier OwnGit icon quit (%v). Quit it or sign in again.\n", err)
		} else {
			host.printf("An earlier OwnGit icon did not quit, so it still runs until you quit it or sign in again.\n")
		}
		return true
	}
	if output, err := serviceRunner(ctx, "/usr/bin/open", app, "--args", service.AppAtSignIn); err != nil {
		host.printf("The OwnGit menu bar icon did not open again (%v: %s). Open %s to show it; OwnGit runs without it.\n", err, strings.TrimSpace(string(output)), app)
		return true
	}
	host.printf("The OwnGit icon was restarted with this version.\n")
	return true
}

func iconPattern(apps ...string) string {
	launchers := []string{}
	for _, app := range apps {
		if resolved, err := filepath.EvalSymlinks(app); err == nil {
			app = resolved
		}
		launchers = append(launchers, regexp.QuoteMeta(service.AppLauncher(app)))
	}
	return "^(" + strings.Join(launchers, "|") + ")( |$)"
}

// closeIcon quits OwnGit.app, the menu bar icon, of the given programs and
// turns off its opening at sign-in, since the service it shows is gone.
// macOS runs an app at its resolved path, such as Homebrew's versioned
// Cellar folder behind the opt link, so the icon is found there.
func (host *launchAgentHost) closeIcon(programs ...string) error {
	ctx := context.Background()
	done := map[string]bool{}
	var failures []error
	for _, program := range programs {
		for _, app := range service.AppPaths(program, applicationsFolder) {
			if resolved, err := filepath.EvalSymlinks(app); err == nil {
				app = resolved
			}
			if done[app] {
				continue
			}
			done[app] = true
			launcher := service.AppLauncher(app)
			running := iconPattern(app)
			// Only this account's icon: another account may run the same app,
			// and root must not quit every account's icon. pkill exits 1 when
			// no icon runs; whether one is left is checked after it.
			account := strconv.Itoa(host.uid)
			_, _ = serviceRunner(ctx, "/usr/bin/pkill", "-U", account, "-f", running)
			exited, err := iconExited(ctx, account, running)
			switch {
			case err != nil:
				failures = append(failures, fmt.Errorf("check icon at %s: %w", app, err))
			case !exited:
				failures = append(failures, fmt.Errorf("icon at %s is still running; quit it in its panel", app))
			}
			// macOS can launch a quarantined app at a random read-only path.
			translocated := `^/private/var/folders/[^ ]+/AppTranslocation/[^ /]+/d/OwnGit\.app/Contents/MacOS/OwnGitLauncher( |$)`
			if output, checkErr := serviceRunner(ctx, "/usr/bin/pgrep", "-U", account, "-f", translocated); strings.TrimSpace(string(output)) != "" {
				failures = append(failures, fmt.Errorf("a launcher in AppTranslocation may still be running; quit the icon in its panel before removing %s", app))
			} else if checkErr != nil && !noProcessMatch(checkErr) {
				failures = append(failures, fmt.Errorf("check translocated launcher: %w", checkErr))
			}
			if err := requireProtectedPath(launcher); err != nil {
				failures = append(failures, fmt.Errorf("turn off opening the icon at %s at sign-in: %w; turn it off under Open at Login in System Settings, General, Login Items & Extensions", app, err))
				continue
			}
			// The launcher exits 0 only when macOS reports the icon's sign-in
			// item as not enabled any more.
			helperCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			output, helperErr := serviceRunner(helperCtx, launcher, service.AppSignInOff)
			cancel()
			if helperErr != nil {
				failures = append(failures, fmt.Errorf("turn off opening the icon at %s at sign-in: %w (%s); turn it off under Open at Login in System Settings, General, Login Items & Extensions", app, helperErr, strings.TrimSpace(string(output))))
				continue
			}
			host.printf("The OwnGit icon at %s no longer opens at sign-in.\n", app)
		}
	}
	return errors.Join(failures...)
}

// iconExited waits up to three seconds for no process of the account to
// match running.
func iconExited(ctx context.Context, account, running string) (bool, error) {
	for attempt := 0; ; attempt++ {
		// pgrep exits 1 when no process matches. Any other error is unknown.
		output, err := serviceRunner(ctx, "/usr/bin/pgrep", "-U", account, "-f", running)
		if err != nil && !noProcessMatch(err) {
			return false, err
		}
		if strings.TrimSpace(string(output)) == "" && (err == nil || noProcessMatch(err)) {
			return true, nil
		}
		if attempt == 10 {
			return false, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func noProcessMatch(err error) bool {
	var exited *exec.ExitError
	return errors.As(err, &exited) && exited.ExitCode() == 1
}

// trayHiddenIn reports whether the owner hid the icon for stateDir. A state
// directory that cannot be read yet, as before the first start, has not
// hidden it.
func trayHiddenIn(stateDir string) bool {
	held, err := state.OpenStateDirectory(stateDir)
	if err != nil {
		return false
	}
	defer held.Close()
	hidden, err := state.TrayHidden(held)
	return err == nil && hidden
}

// agentStateDir is the state directory of a new or updated service. A
// Homebrew binary always uses the default one, which Homebrew's service
// uses too, so that either can take over from the other.
func (host *launchAgentHost) agentStateDir(flagValue string, existing service.Installed, found bool) (string, error) {
	stateDir, err := host.installStateDir(service.ModeLaunchAgent, flagValue, existing, found)
	if err != nil || host.homebrew == "" {
		return stateDir, err
	}
	if defaultDir := mustAbs(defaultStateDir()); filepath.Clean(stateDir) != filepath.Clean(defaultDir) {
		return "", fmt.Errorf("a Homebrew install of OwnGit uses the default state directory %s, not %s; leave out --state-dir, or run \"owngit service uninstall\" first", defaultDir, stateDir)
	}
	return stateDir, nil
}

// agentPlan is the agent of a new or updated install. --headless decides
// whether it is headless, then the installed agent, then this session: a
// desktop install stays one when it is installed again over SSH. Its PATH
// leaves out folders that another account could change.
func (host *launchAgentHost) agentPlan(stateDir string, headlessFlag *bool, existing service.Installed, found bool) service.Plan {
	headless := host.env.Headless()
	switch {
	case headlessFlag != nil:
		headless = *headlessFlag
	case found:
		headless = existing.Headless
	}
	return service.Plan{
		Mode: service.ModeLaunchAgent, Executable: host.agentExecutable, StateDir: stateDir,
		Headless: headless, Home: host.account.HomeDir, Path: protectedServicePath(),
	}
}

func protectedServicePath() string {
	entries := filepath.SplitList(servicePath())
	entries = slices.DeleteFunc(entries, func(entry string) bool { return requireProtectedPath(entry) != nil })
	return strings.Join(entries, string(os.PathListSeparator))
}

// installWithBrew hands the service to "brew services". An OwnGit agent
// from an earlier install over SSH is stopped first and removed once
// Homebrew's service runs, so only one server runs; if Homebrew's service
// does not start, the agent runs again and nothing else changes.
func (host *launchAgentHost) installWithBrew(stateDir string, agentFound bool) error {
	ctx := context.Background()
	brewFileExisted := false
	for _, label := range service.HomebrewLabels {
		if _, err := os.Stat(filepath.Join(filepath.Dir(host.agentPath), label+".plist")); err == nil {
			brewFileExisted = true
		}
	}
	if agentFound {
		if err := service.StopLaunchAgent(ctx, serviceRunner, host.uid); err != nil {
			return err
		}
	}
	host.printf("OwnGit was installed with Homebrew, so Homebrew runs the service: %s services restart owngit\n", filepath.Join(host.homebrew, "bin", "brew"))
	if err := host.brewServices("restart"); err != nil {
		if !brewFileExisted {
			_, _ = serviceRunner(ctx, filepath.Join(host.homebrew, "bin", "brew"), "services", "stop", "owngit")
		}
		if agentFound {
			_ = service.StartLaunchAgent(ctx, serviceRunner, host.uid, true, host.agentPath, false)
		}
		return errors.New("brew services could not start OwnGit (its message is above); nothing was changed. Run \"brew services restart owngit\" to see why, then \"owngit service install\" again")
	}
	if agentFound {
		if err := service.UninstallLaunchAgent(ctx, serviceRunner, host.uid, host.agentPath); err != nil {
			return err
		}
		host.printf("The OwnGit LaunchAgent of an earlier install is removed, so only brew services runs OwnGit.\n")
	}
	if err := host.reportStarted(service.ModeHomebrew, "", stateDir); err != nil {
		return err
	}
	host.openIcon(stateDir, false)
	return nil
}

// yieldToHomebrew runs when "owngit serve" starts under Homebrew's service:
// an OwnGit agent of the same state directory, installed over SSH before
// brew services was turned on, is removed so that the two never run side
// by side.
func yieldToHomebrew(stateDir string, logf func(string, ...any)) {
	if runtime.GOOS != "darwin" || !slices.Contains(service.HomebrewLabels, os.Getenv("XPC_SERVICE_NAME")) {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	home = cmp.Or(launchAgentHome, home)
	removed, err := service.YieldLaunchAgent(context.Background(), serviceRunner, os.Getuid(), home, mustAbs(stateDir))
	switch {
	case err != nil:
		logf("could not remove the OwnGit LaunchAgent that serves the same state directory: %v", err)
	case removed:
		logf("brew services now runs OwnGit, so the OwnGit LaunchAgent %s is removed", service.LaunchAgentPath(home))
	}
}

func (host *launchAgentHost) uninstall() error {
	installed, found, err := host.installedAgent()
	if err != nil {
		return err
	}
	switch {
	case !found && host.homebrew != "":
		if err := host.brewServices("stop"); err != nil {
			return err
		}
		iconErr := host.closeIcon(host.agentExecutable)
		host.printf("The Homebrew service is stopped and no longer starts. The data stays in %s.\n", mustAbs(defaultStateDir()))
		return iconErr
	case !found:
		host.printf("OwnGit is not installed as a service.\n")
		if line := describeOtherJob(host.otherJobs()); line != "" {
			host.printf("%s\n", line)
		}
		host.printDataStays()
		return nil
	}
	if err := service.UninstallLaunchAgent(context.Background(), serviceRunner, host.uid, installed.UnitPath); err != nil {
		return err
	}
	iconErr := host.closeIcon(installed.Executable, host.agentExecutable)
	host.printf("The OwnGit service is stopped and removed. The state stays in %s", installed.StateDir)
	if repositories := savedRepositoryRoot(installed.StateDir); repositories != "" {
		host.printf(", the repositories in %s", repositories)
	}
	host.printf(" and the log in %s.\nRun \"owngit service install\" to use it again.\n", host.logHint(service.ModeLaunchAgent))
	return iconErr
}

func (host *launchAgentHost) status() error {
	installed, found, err := host.installedAgent()
	if err != nil {
		return err
	}
	if !found {
		if host.homebrew != "" {
			host.printf("OwnGit was installed with Homebrew; its service is managed with brew services:\n")
			if err := host.brewServices("info"); err != nil {
				return err
			}
			host.printf("  State:   %s (a Homebrew install always uses the default state directory)\n", mustAbs(defaultStateDir()))
			host.printf("  Log:     %s\n", host.logHint(service.ModeHomebrew))
			return nil
		}
		if line := describeOtherJob(host.otherJobs()); line != "" {
			host.printf("OwnGit is not installed with \"owngit service\". %s\n", line)
			return nil
		}
		host.printf("OwnGit is not installed as a service. Run \"owngit service install\".\n")
		return nil
	}
	if host.homebrew != "" {
		host.printf("OwnGit was installed with Homebrew and runs as the OwnGit LaunchAgent, because it was installed without a desktop login.\n")
		for _, job := range host.otherJobs() {
			if job.homebrew {
				host.printf("brew services also has OwnGit set up (%s); when it starts, it takes over and removes the OwnGit LaunchAgent.\n", job.Path)
				break
			}
		}
	}
	state := service.LaunchAgentStatus(context.Background(), serviceRunner, host.uid)
	launchd := "launchd: not loaded"
	switch {
	case state.Running:
		launchd = fmt.Sprintf("launchd: running as process %d", state.PID)
	case state.Domain != "":
		launchd = "launchd: loaded, not running"
		if state.LastExit != "" {
			launchd += ", last exit code " + state.LastExit
		}
	}
	target, healthErr := confirmedHealth(installed.StateDir)
	answered := healthErr == nil
	address := ""
	if answered {
		address = ownerAddresses(installed.StateDir, target)
	}
	switch {
	case answered:
		host.printf("OwnGit is running (%s) and answers its health check.\n", launchd)
	case state.Running:
		host.printf("OwnGit is starting or not answering yet (%s).\n", launchd)
	default:
		host.printf("OwnGit is not running (%s).\n", launchd)
	}
	if statusHealthProblem(installed.StateDir, healthErr, state.Running) {
		host.printf("  Health:  %v\n", healthErr)
	}
	host.printf("  Mode:    %s, started when %s logs in on this Mac\n", installed.Mode.Describe(), host.account.Username)
	host.printServiceFacts(installed.Mode, installed.UnitPath, installed.StateDir, address)
	read, complete := setupStatus(installed.StateDir)
	switch {
	case state.Domain == "":
		host.printf("Run \"owngit service start\" to start it.\n")
	case read && !complete:
		host.printf("Setup is not complete. Run \"owngit setup-link\" in a terminal for the one-time setup link.\n")
	}
	return nil
}

func (host *launchAgentHost) control(action string) error {
	installed, found, err := host.installedAgent()
	if err != nil {
		return err
	}
	if !found {
		if host.homebrew != "" {
			if err := host.brewServices(action); err != nil || action != "restart" {
				return err
			}
			host.restartIcon()
			return nil
		}
		return errors.New("OwnGit is not installed as a service; run \"owngit service install\"")
	}
	ctx := context.Background()
	if action == "stop" {
		if err := service.StopLaunchAgent(ctx, serviceRunner, host.uid); err != nil {
			return err
		}
		host.printf("OwnGit is stopped. It starts again at the next login, or with \"owngit service start\".\n")
		return nil
	}
	if err := service.StartLaunchAgent(ctx, serviceRunner, host.uid, host.env.GraphicalSession, installed.UnitPath, action == "restart"); err != nil {
		return err
	}
	address, err := waitHealthy(installed.StateDir, serviceStartTimeout)
	if err != nil {
		return fmt.Errorf("OwnGit did not answer within %s: %w; see the log: %s", serviceStartTimeout, err, host.logHint(service.ModeLaunchAgent))
	}
	host.printf("OwnGit is running at http://%s.\n", address)
	if action == "restart" && !installed.Headless {
		host.restartIcon()
	}
	return nil
}
