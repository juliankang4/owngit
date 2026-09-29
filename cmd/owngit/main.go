package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"owngit/internal/auth"
	"owngit/internal/bootstrap"
	"owngit/internal/checkrun"
	"owngit/internal/firstrun"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/importsync"
	"owngit/internal/markdown"
	"owngit/internal/pullrequest"
	"owngit/internal/recovery"
	"owngit/internal/releasecheck"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/service"
	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/version"
	"owngit/internal/webui"
)

func main() {
	// A rendering child process does only that, before anything else.
	if markdown.IsChild(os.Args) {
		os.Exit(markdown.RunChild(os.Stdin, os.Stdout))
	}
	if executable, err := renderHelperPath(); err == nil {
		markdown.SetHelper(executable)
	}
	if err := run(os.Args[1:]); err != nil {
		os.Exit(reportError(os.Stdout, err))
	}
}

// reportError writes err where the command reports it and returns the exit
// code. An error is written once, to the log of the run that met it: an
// error that serve's log file confirmed is not written again, neither to
// standard error, which a service keeps in a file nothing bounds, nor as
// JSON to stdout. Any other error with a code is written as JSON to stdout,
// and the rest to standard error.
func reportError(stdout io.Writer, err error) int {
	// A completed check run already wrote its JSON result and only needs
	// its conventional exit code.
	var exit *checkExit
	if errors.As(err, &exit) {
		return exit.code
	}
	// A restore or verification that an interrupt stopped exits as a
	// process stopped by it does.
	var interrupted *recovery.Interrupted
	if errors.As(err, &interrupted) {
		log.Printf("error: %v", err)
		return 130
	}
	var logged loggedError
	if !errors.As(err, &logged) && !writeStructuredCommandError(stdout, err) {
		log.Printf("error: %v", err)
	}
	return 1
}

// loggedError is an error that the run's log confirmed it wrote.
type loggedError struct{ error }

func (err loggedError) Unwrap() error { return err.error }

func run(arguments []string) error {
	// Global help must be handled before command dispatch: the flag package
	// treats -h/--help as a request for help, and without this check those
	// flags fall into "serve" and make it exit with a flag error.
	if len(arguments) > 0 {
		switch arguments[0] {
		case "help", "-h", "--help":
			printUsage(os.Stdout)
			return nil
		case "version", "--version", "-version":
			// Version output must not open storage: it is useful before setup
			// and on a host whose state directory is unavailable.
			fmt.Printf("owngit %s\n", version.Version)
			return nil
		}
	}
	command := "serve"
	if len(arguments) != 0 && !strings.HasPrefix(arguments[0], "-") {
		command, arguments = arguments[0], arguments[1:]
	}
	// backup verify reads only the backup it is given, so it runs as the
	// account that starts it, like any command without a state.
	readsNoState := command == "backup" && len(arguments) != 0 && arguments[0] == "verify"
	if serviceStateCommands[command] && !helpRequested(arguments) && !readsNoState {
		if handled, err := stateCommandWithoutAdminRights(command, arguments); handled {
			return err
		}
		stateDir := stateDirArgument(arguments)
		if stateDir == "" {
			stateDir = defaultStateDir()
		}
		dropped, err := actAsStateOwner(stateDir)
		if err != nil {
			return err
		}
		if dropped {
			return accountPathHint(runCommand(command, arguments))
		}
	}
	err := runCommand(command, arguments)
	if errors.Is(err, errUsageShown) {
		return nil
	}
	return runAsOwnerHint(err, command, arguments)
}

// runAsOwnerHint completes a refusal of another account's state directory
// with the command that runs this one as that account. An account without a
// name gets no command: runuser and sudo take the name.
func runAsOwnerHint(err error, command string, arguments []string) error {
	var other *state.OtherAccountError
	if !errors.As(err, &other) || other.Account == "" {
		return err
	}
	line := "sudo -u " + service.ShellQuote(other.Account) + " owngit " + command
	if runtime.GOOS == "linux" {
		// runuser comes with Linux itself; sudo may not be installed.
		line = "runuser -u " + service.ShellQuote(other.Account) + " -- owngit " + command
	}
	for _, argument := range arguments {
		line += " " + service.ShellQuote(argument)
	}
	return fmt.Errorf("%w: %s", err, line)
}

// accountPathHint explains a permission error of a command that root runs
// as the owngit account, which cannot open root's own folders.
func accountPathHint(err error) error {
	switch {
	case errors.Is(err, errUsageShown):
		return nil
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w; this command runs as the %s account, which cannot open that path: give an absolute path in a folder it can use, such as %s", err, service.AccountName, service.AccountHome+"/backup")
	}
	return err
}

func runCommand(command string, arguments []string) error {
	switch command {
	case "serve":
		return serve(arguments)
	case "setup-link":
		return setupLink(arguments)
	case "service":
		return serviceCommand(arguments)
	case "health":
		return healthCommand(arguments)
	case "reset-admin":
		return resetAdmin(arguments)
	case "approve-host":
		return approveHost(arguments)
	case "network":
		return networkCommand(arguments)
	case "tailscale":
		return tailscaleCommand(arguments)
	case "forget-check-container":
		return forgetCheckContainer(arguments)
	case "backup":
		return backupState(arguments)
	case "restore":
		return restoreState(arguments)
	case "upgrade-backup":
		return upgradeBackupCommand(arguments)
	case "pr":
		return prCommand(arguments)
	case "repo":
		return repoCommand(arguments)
	case "skill":
		return skillCommand(arguments)
	case "mcp":
		return mcpCommand(arguments)
	case "check":
		return checkCommand(arguments)
	case "helper-credential":
		return helperCredentialCommand(arguments)
	case "check-policy":
		return checkPolicyCommand(arguments)
	case "check-job":
		return checkJobCommand(arguments)
	case "runner-credential":
		return runnerCredentialCommand(arguments)
	case "runner":
		return runnerCommand(arguments)
	case "settings":
		return settingsCommand(arguments)
	case "import":
		return importCommand(arguments)
	case "update":
		return updateCommand(arguments)
	case "uninstall":
		return uninstallCommand(arguments)
	case "doctor":
		return doctorCommand(arguments)
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

// renderHelperPath is the binary that renders Markdown documents in a child
// process: this one. On Linux the running image is used even after an
// upgrade replaced the file on disk.
func renderHelperPath() (string, error) {
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/proc/self/exe"); err == nil {
			return "/proc/self/exe", nil
		}
	}
	return os.Executable()
}

// releaseCheckEndpoint and releaseCheckDelay are variables only so tests can
// point the check at a local server; tests never contact GitHub.
var (
	releaseCheckEndpoint = releasecheck.LatestURL
	releaseCheckDelay    = releasecheck.DefaultInitialDelay
)

// preparationGrace bounds how long startup waits for repository preparation
// before it starts serving the repositories that are ready.
const preparationGrace = 10 * time.Second

func serve(arguments []string) error {
	// On Windows the task of an administrator runs with administrator
	// rights; the server itself then runs as a copy without them.
	if flagGiven(arguments, "service") {
		if handled, err := serveWithoutAdminRights(arguments); handled {
			return err
		}
	}
	return serveWithOpener(arguments, bootstrap.Open, log.Printf)
}

// flagGiven reports whether a boolean flag is among the arguments.
func flagGiven(arguments []string, name string) bool {
	for _, argument := range arguments {
		if argument == "--" {
			break
		}
		flagName, value, hasValue := strings.Cut(strings.TrimLeft(argument, "-"), "=")
		if strings.HasPrefix(argument, "-") && flagName == name && (!hasValue || value == "true") {
			return true
		}
	}
	return false
}

func serveWithOpener(arguments []string, opener func(string) error, logf func(string, ...any)) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := serveWithContext(ctx, arguments, opener, logf)
	if err != nil {
		recordServeError(cmp.Or(stateDirArgument(arguments), defaultStateDir()), err)
	}
	return err
}

// interactiveSetup reports whether first-run setup can ask its questions in
// this terminal. Tests replace it, so a test run from a terminal never
// waits for answers.
var interactiveSetup = defaultInteractiveSetup

func defaultInteractiveSetup() bool { return firstrun.Interactive(os.Stdin, os.Stdout) }

func serveWithContext(ctx context.Context, arguments []string, opener func(string) error, logf func(string, ...any)) (serveErr error) {
	// Stopping setup in the terminal stops the server like a signal does.
	ctx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	listenAddress := flags.String("listen", server.DefaultListenAddress, "HTTP listen address; overrides the saved one for this run")
	baseURL := flags.String("base-url", "", "owner-facing HTTP origin; overrides the saved one for this run")
	gitPath := flags.String("git", "", "Git executable path")
	openOwner := flags.Bool("open", false, "open OwnGit for the owner after startup")
	noOpen := flags.Bool("no-open", false, "do not open the private setup file")
	headless := flags.Bool("headless", false, "whether this computer has no screen for setup (default: detected); before setup, with no saved listen address, a computer without a screen listens on every address and saves that")
	logFile := flags.String("log-file", "", "also write the server log to this `file`, or only there with --service (kept below 10 MB, with one older file beside it)")
	asService := flags.Bool("service", false, "run as the background service that \"owngit service install\" or Homebrew set up: with --log-file the log goes only to that file (on Windows also: without administrator rights, and \"owngit service stop\" stops it in order)")
	noUpdateCheck := flags.Bool("no-update-check", false, "never contact GitHub to check for a newer OwnGit release, whatever the Settings page says")
	var allowedHosts, trustedProxies stringList
	flags.Var(&allowedHosts, "allowed-host", "additional accepted `host` name (repeatable)")
	tailscalePath := flags.String("tailscale", "", "tailscale command `path` for sharing on the tailnet; found automatically when empty")
	flags.Var(&trustedProxies, "trusted-proxy", "trust forwarded headers from this reverse proxy `address` or CIDR range (repeatable); overrides the saved list for this run")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve does not accept positional arguments")
	}
	if *openOwner && *noOpen {
		return errors.New("serve --open and --no-open cannot be used together")
	}
	if *logFile != "" {
		closeLog, err := writeLogTo(*logFile, *asService)
		if err != nil {
			return err
		}
		// The error that ends serve reaches the log before it closes, and
		// only there (see reportError), when the log confirms the write.
		// Otherwise main writes it, with why the log could not. It goes
		// through the standard logger, which reports the write, rather than
		// logf, which does not.
		defer func() {
			if serveErr != nil {
				if err := log.Output(1, "error: "+serveErr.Error()); err != nil {
					serveErr = fmt.Errorf("%w (the log file did not record this: %v)", serveErr, err)
				} else {
					serveErr = loggedError{serveErr}
				}
			}
			closeLog()
		}()
		if status := os.Getenv(restartedVariable); *asService && status != "" {
			logf("started again after the server exited with status %s", status)
		}
	}
	// Without a screen that a person sees in this session (a service, a
	// scheduled task, SSH), a browser would run where nobody can see or
	// close it, or not at all; the log shows the setup file's path instead.
	environment := probeEnvironment()
	if !environment.ShowsBrowser() {
		*openOwner, *noOpen = false, true
	}
	if *asService {
		defer watchServiceStop(*stateDir, cancelServe, logf)()
	}
	// Under Homebrew's service on macOS, an OwnGit LaunchAgent of the same
	// state directory stops for good before this server takes the lock.
	yieldToHomebrew(*stateDir, logf)

	// The offline lock is taken before the state is opened, so a migration
	// cannot race another owner that is already serving the same directory.
	// The directory is created first because the lock file lives inside it.
	// The lock and the state are taken in the directory that was checked,
	// held open, whatever its path names later.
	stateDirectory, err := state.CreateDirectory(*stateDir)
	if err != nil {
		return err
	}
	defer stateDirectory.Close()
	unlock, err := state.AcquireLockBriefly(func() (func(), error) { return state.AcquireOfflineLockIn(stateDirectory) })
	if err != nil {
		return err
	}
	defer unlock()
	// Commands next to the server, such as "owngit service install" waiting
	// for this start, may open the state while it is inspected, so serve
	// retries as they do.
	store, err := retryUnstableOpen(serveStateAttempts, func() (*state.Store, error) {
		return openServeStateAttempt(ctx, stateDirectory, *gitPath, logf)
	})
	if err != nil {
		return err
	}
	defer store.Close()
	releaseRunning, runningLive := claimRunningRecord(ctx, store, logf)
	defer releaseRunning()
	runner, err := gitexec.New(*gitPath, filepath.Join(store.Dir(), "runtime"))
	if err != nil {
		return err
	}
	versionResult, err := runner.Run(ctx, "", nil, "--version")
	if err != nil {
		return err
	}
	logf("using %s at %s (%s)", strings.TrimSpace(string(versionResult.Stdout)), runner.GitPath, runner.GitSource)
	backendPath, err := githttp.DiscoverBackend(ctx, runner)
	if err != nil {
		return err
	}
	renderer, err := webui.New()
	if err != nil {
		return err
	}
	settings, err := store.Settings(ctx)
	if err != nil {
		return err
	}
	savedNetwork, err := store.NetworkSettings(ctx)
	if err != nil {
		return err
	}
	network, err := effectiveServeNetwork(savedNetwork, flags, *listenAddress, *baseURL)
	if err != nil {
		return err
	}
	// Without a screen, setup happens on another device, so the first start
	// listens on every address. Until setup is done every request except
	// the setup link is refused, as for any unknown Host.
	// A service passes --headless=true or --headless=false, decided at
	// install time, because the service may start before a desktop session.
	headlessSetup := !settings.Initialized && headlessChoice(flags, *headless)
	if network.ListenSource == sourceDefault && headlessSetup {
		saved, err := applyHeadlessListen(ctx, store)
		if err != nil {
			return err
		}
		if saved {
			network.Listen, network.ListenSource = headlessListen, sourceSaved
			logf("this computer has no screen for setup, so OwnGit listens on every address (%s) and saved that; run \"owngit network set --listen %s\" to keep it on this computer", headlessListen, server.DefaultListenAddress)
		}
	}
	savedProxies, err := store.TrustedProxies(ctx)
	if err != nil {
		return err
	}
	proxies, err := effectiveTrustedProxies(savedProxies, flags, trustedProxies)
	if err != nil {
		return err
	}
	repositories := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: settings.RepositoryRoot}
	// The repository folder is locked before anything writes to it, such as
	// the retention hooks that name this state directory, and it stays
	// locked until every user of the repositories has stopped. Another
	// server on the folder, for example one started from a copy of this
	// state directory, would have its hooks rewritten, so it stops the start.
	if err := repositories.ClaimStorage(); errors.Is(err, repository.ErrStorageInUse) {
		return fmt.Errorf("%w; stop the other server first (a copy of a state directory must not serve the same repository folder)", err)
	} else if err != nil {
		logf("could not check that no other OwnGit server uses the repository folder: %v", err)
	}
	defer repositories.ReleaseStorage()
	pullRequests := &pullrequest.Service{Store: store, Repositories: repositories}
	// A repository that becomes ready in the background wakes check
	// reconciliation, so the hook is set before preparation starts. Wake does
	// nothing until the coordinator starts.
	checkCoordinator := &checkrun.Coordinator{Store: store, Repositories: repositories, PullRequests: pullRequests, Logf: logf}
	// Every OwnGit write wakes check reconciliation and schedules repository
	// maintenance for when the repository is idle.
	noteChange := func(id string) {
		checkCoordinator.Wake(id)
		repositories.NoteRepositoryWrite(id)
	}
	repositories.OnChange = noteChange
	if settings.Initialized {
		// Deleted repositories have no rows, so an unfinished deletion never
		// blocks startup; it is reported and retried at the next start.
		if err := repositories.ReconcileDeletions(ctx); err != nil {
			logf("unfinished repository deletion was not completed: %v", err)
		}
	}
	// Each repository is prepared on its own: safety configuration,
	// retention hook, and pull request recovery. One that fails or hangs
	// stays locked and is retried in the background while the others are
	// served. Startup waits at most preparationGrace for the first attempts.
	// Preparation also runs before setup, with no repositories, so a
	// repository whose storage becomes unavailable later is prepared again
	// the same way. The stop is registered first, so a signal during the startup wait
	// also cancels and awaits the attempts before the store closes.
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := repositories.StopPreparation(stopContext); err != nil {
			logf("%v", err)
		}
	}()
	if err := repositories.StartPreparation(ctx, pullRequests.RecoverRepositoryLocked, preparationGrace, logf); err != nil {
		return err
	}
	if err := repositories.StartMaintenance(ctx, repository.MaintenanceSchedule{}, logf); err != nil {
		return err
	}
	// Registered after the store is opened and the offline lock is taken, so
	// a maintenance command is terminated and reaped before either closes.
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := repositories.StopMaintenance(stopContext); err != nil {
			logf("%v", err)
		}
	}()
	if _, err := store.PruneCheckLogs(ctx, time.Now()); err != nil {
		log.Printf("could not prune expired check logs: %v", err)
	}
	gitHandler, err := githttp.New(runner, repositories, backendPath, 4)
	if err != nil {
		return err
	}
	authentication := &auth.Manager{Store: store, AdminSessionLife: 15 * time.Minute}
	listener, err := net.Listen("tcp", network.Listen)
	if err != nil {
		return network.listenError(err)
	}
	defer listener.Close()
	clearServeError(stateDirectory.Name())

	policy := server.NewHostPolicy(allowedHosts...)
	trusted, err := store.TrustedHosts(ctx)
	if err != nil {
		return err
	}
	for _, host := range trusted {
		if err := policy.Add(host); err != nil {
			return fmt.Errorf("invalid stored trusted host: %w", err)
		}
	}
	if host, _, err := net.SplitHostPort(network.Listen); err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		if err := policy.Add(host); err != nil {
			return err
		}
	}
	originAddress := network.Listen
	if network.BaseURL == "" {
		originAddress = listener.Addr().String()
	}
	origin, err := ownerOrigin(network.BaseURL, originAddress)
	if err != nil {
		return err
	}
	parsedOrigin, _ := url.Parse(origin)
	if err := policy.Add(parsedOrigin.Host); err != nil {
		return fmt.Errorf("trust setup origin: %w", err)
	}

	home, _ := os.UserHomeDir()
	imports := &importsync.Service{Store: store, Repositories: repositories, Logf: logf}
	// Registered after the store is opened, so in-flight imports record their
	// outcome before the store closes.
	importRuntime := &importLifetime{ctx: ctx, service: imports, logf: logf}
	defer importRuntime.stop()
	if settings.Initialized {
		importRuntime.start()
	}
	// Apart from imports, which reach only the source hosts an owner
	// configures, the new-release check is OwnGit's only outbound
	// connection. It waits
	// for setup, reads the saved setting before every request, and
	// --no-update-check removes it entirely.
	var releases *releasecheck.Checker
	if !*noUpdateCheck {
		releases = &releasecheck.Checker{
			Current: version.Version, URL: releaseCheckEndpoint, InitialDelay: releaseCheckDelay, Logf: logf,
			Enabled: func(ctx context.Context) (bool, error) {
				current, err := store.Settings(ctx)
				return current.Initialized && current.UpdateCheck, err
			},
		}
	}
	live, clearNetwork, err := liveNetwork(store, runningLive, network, proxies, listener.Addr().String(), origin, trusted, allowedHosts, policy, logf)
	if err != nil {
		return err
	}
	application := &server.App{
		Store: store, Auth: authentication, Repositories: repositories, PullRequests: pullRequests, GitHTTP: gitHandler,
		Renderer: renderer, Hosts: policy, Network: live, SuggestedRepositoryRoot: filepath.Join(home, "OwnGit-Repositories"),
		Tailscale: &server.Tailscale{
			Store: store,
			Find:  func() (tailscale.Command, error) { return findTailscale(*tailscalePath) },
			Observe: func(ctx context.Context) (state.RunningObservation, error) {
				return store.OwnRunningNetwork(ctx, runningLive)
			},
			Live: live,
		},
		GitVersion: strings.TrimSpace(string(versionResult.Stdout)), HTTPBackendFound: true, Version: version.Version,
		WakeChecks: checkCoordinator.Wake, Imports: imports, RunningRecordLive: runningLive,
		ImportRunTimeout: importsync.DefaultLimits().RunTimeout,
		Releases:         releases,
		UpdateCommand:    dashboardUpdateCommand(*asService),
		Diagnose: serverDiagnosis(mustAbs(*stateDir), listener.Addr().String(), *asService, func(ctx context.Context) (string, error) {
			current, err := store.Settings(ctx)
			return current.RepositoryRoot, err
		}),
		HeadlessListen: headlessListenInUse(headlessSetup, network),
		// First-run setup inside this process starts the same import runtime
		// that an initialized startup starts above, and lets the release
		// check run without waiting a day.
		OnSetupComplete: func() {
			importRuntime.start()
			if releases != nil {
				releases.Wake()
			}
		},
	}
	// Background readings of Tailscale end with the server, within the same
	// grace as its requests, so no tailscale command outlives it.
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := application.Tailscale.Stop(stopContext); err != nil {
			logf("stopping: Tailscale readings still running after 10s: %v", err)
		}
	}()
	// First-run setup asks its questions in the terminal when OwnGit was
	// started from one. Otherwise, as under a service manager, it keeps the
	// private setup file. The terminal flow issues no setup file at all.
	// A service has no one at its console, even where Windows gives it a
	// hidden one; its setup always goes through the setup file.
	// A console that nobody reads, as a process started in the background
	// on Windows has, would wait for answers forever.
	terminalSetup := !settings.Initialized && !*asService && !environment.UnreadConsole() && interactiveSetup()
	if terminalSetup {
		application.Approvals = server.NewSetupApprovals()
	}
	gitHandler.Authorize = application.AuthorizeGit
	gitHandler.OnReceive = noteChange
	// Activity is counted in the background under the serving lifetime, so
	// startup does not wait for it and the dashboard finds it ready.
	application.StartBackground(ctx)
	defer application.StopBackground()
	if releases != nil {
		releaseContext, cancelReleases := context.WithCancel(ctx)
		releaseDone := make(chan struct{})
		go func() {
			defer close(releaseDone)
			releases.Run(releaseContext)
		}()
		defer func() {
			cancelReleases()
			<-releaseDone
		}()
	}
	pullRequests.OnChange = noteChange
	checkContext, cancelChecks := context.WithCancel(ctx)
	defer cancelChecks()
	if err := checkCoordinator.Start(checkContext); err != nil {
		var unavailable *checkrun.RuntimeUnavailableError
		if !errors.As(err, &unavailable) {
			return err
		}
		application.CheckRuntimeUnavailableCode = unavailable.Code
		application.CheckRuntimeUnavailableReason = checkRuntimeUnavailableReason(unavailable.Code)
		logf("configured check runtime unavailable; ordinary Git service remains available and OwnGit must be restarted after repair: %v", err)
	} else {
		defer func() {
			stopContext, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if err := checkCoordinator.Stop(stopContext); err != nil {
				logf("configured check shutdown: %v", err)
			}
		}()
	}

	if !settings.Initialized && !terminalSetup {
		path, err := (&bootstrap.Issuer{Store: store, BaseURL: origin}).Issue(ctx)
		if err != nil {
			return err
		}
		logf("owner setup file: %s", path)
		if target := serveOpenTarget(false, *openOwner, *noOpen, path, origin); target != "" {
			openServeTarget(target, "setup file", opener, logf)
		}
	}

	httpServer := &http.Server{
		Handler: application.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: server.ImportRunRequestTimeout(importsync.DefaultLimits().RunTimeout),
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	application.OnHostAccepted = live.Publish
	live.Publish()
	defer clearNetwork()
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(listener) }()
	logf("OwnGit listening on %s", listener.Addr())
	if len(proxies.List) > 0 {
		logf("trusting forwarded headers from reverse proxies at %s", strings.Join(proxies.List, ", "))
	}
	// An initialized installation opens only on explicit request, after the
	// listener exists and the HTTP server has started. First-run opening above
	// continues to use the private setup file exactly once.
	if target := serveOpenTarget(settings.Initialized, *openOwner, *noOpen, "", origin); target != "" {
		openServeTarget(target, "owner URL", opener, logf)
	}
	if terminalSetup {
		config := firstrun.Config{
			Input: os.Stdin, Output: os.Stdout, App: application, Origin: origin, Listen: listener.Addr().String(),
			ListenSaved: network.ListenSource == sourceSaved, SuggestedFolder: application.SuggestedRepositoryRoot,
			TailscalePath: *tailscalePath,
		}
		if !*noOpen {
			config.OpenBrowser = opener
		}
		if *stateDir != defaultStateDir() {
			config.StateDir, _ = filepath.Abs(*stateDir)
		}
		switch err := runTerminalSetup(ctx, config); {
		case err == nil:
			logf("setup completed")
			logf("OwnGit ready at %s/", origin)
		case errors.Is(err, firstrun.ErrStopped):
			logf("OwnGit stopped; setup is not complete")
			cancelServe()
		default:
			// The terminal could not be used, so setup falls back to the
			// private setup file, and browsers see its ordinary setup page.
			application.Approvals.Abandon()
			logf("terminal setup unavailable: %v", err)
			path, issueErr := (&bootstrap.Issuer{Store: store, BaseURL: origin}).Issue(ctx)
			if issueErr != nil {
				_ = httpServer.Close()
				return issueErr
			}
			logf("owner setup file: %s", path)
			if !*noOpen {
				openServeTarget(path, "setup file", opener, logf)
			}
		}
	}

	select {
	case serveErr := <-errCh:
		_ = httpServer.Close()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		gitErr := gitHandler.Wait(shutdownContext)
		cancel()
		if gitErr != nil {
			return fmt.Errorf("wait for Git process cleanup: %w", gitErr)
		}
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	case <-ctx.Done():
		// Stop import runs first, so their handlers return promptly with the
		// recorded outcome instead of outliving the HTTP shutdown window.
		importRuntime.stop()
		return stopServing(httpServer, gitHandler, 10*time.Second, logf)
	}
}

// runTerminalSetup runs first-run setup in the terminal. Server log lines
// written meanwhile are held and shown between the setup cards.
func runTerminalSetup(ctx context.Context, config firstrun.Config) error {
	previous := log.Writer()
	config.Logs = firstrun.NewLogGate(previous)
	log.SetOutput(config.Logs)
	defer log.SetOutput(previous)
	return firstrun.Run(ctx, config)
}

// stopServing stops the HTTP server and waits up to grace for running
// requests. Requests still running then, such as a slow clone, are ended by
// closing their connections, which is an ordinary stop and is logged. It
// fails only when the server cannot stop or when the Git processes are not
// cleaned up within another grace.
func stopServing(httpServer *http.Server, gitHandler *githttp.Handler, grace time.Duration, logf func(string, ...any)) error {
	shutdownContext, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	shutdownErr := httpServer.Shutdown(shutdownContext)
	if shutdownErr != nil {
		if errors.Is(shutdownErr, context.DeadlineExceeded) {
			logf("stopping: ended %d Git transfer(s) and any other requests still running after %s", gitHandler.Active(), grace)
			shutdownErr = nil
		}
		_ = httpServer.Close()
	}
	// The ended requests stop their Git processes; wait for that cleanup with
	// its own deadline, since the shutdown wait may have used all of grace.
	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), grace)
	defer cancelCleanup()
	if err := gitHandler.Wait(cleanupContext); err != nil {
		return fmt.Errorf("wait for Git process cleanup: %w", err)
	}
	if shutdownErr != nil {
		return fmt.Errorf("graceful shutdown: %w", shutdownErr)
	}
	return nil
}

// importLifetime owns import reconciliation, the scheduler, and import
// shutdown for one serving process. start runs at most once, either when an
// initialized installation starts serving or when first-run setup completes
// while serving; both share the serve context and the same shutdown.
type importLifetime struct {
	ctx       context.Context
	service   *importsync.Service
	logf      func(string, ...any)
	mu        sync.Mutex
	started   bool
	stopped   bool
	scheduler *importsync.Scheduler
}

func (lifetime *importLifetime) start() {
	lifetime.mu.Lock()
	defer lifetime.mu.Unlock()
	if lifetime.started || lifetime.stopped {
		return
	}
	lifetime.started = true
	if err := lifetime.service.Reconcile(lifetime.ctx); err != nil {
		lifetime.logf("import reconciliation failed; ordinary Git service remains available: %v", err)
		lifetime.service.NoteStartupFailure(err)
	}
	scheduler := &importsync.Scheduler{Service: lifetime.service, Logf: lifetime.logf}
	if err := scheduler.Start(lifetime.ctx); err != nil {
		lifetime.logf("import scheduler did not start; ordinary Git service remains available: %v", err)
		return
	}
	lifetime.scheduler = scheduler
}

// importShutdownTimeout bounds how long serve waits for scheduled and manual
// import runs to record their outcome after cancellation.
const importShutdownTimeout = 45 * time.Second

// stop stops the scheduler, then cancels and drains manual runs, bounded,
// and releases the import runtime lease. It is idempotent.
func (lifetime *importLifetime) stop() {
	lifetime.mu.Lock()
	defer lifetime.mu.Unlock()
	if lifetime.stopped {
		return
	}
	lifetime.stopped = true
	stopContext, cancel := context.WithTimeout(context.Background(), importShutdownTimeout)
	defer cancel()
	if lifetime.scheduler != nil {
		if err := lifetime.scheduler.Stop(stopContext); err != nil {
			lifetime.logf("import scheduler shutdown: %v", err)
		}
	}
	if err := lifetime.service.Shutdown(stopContext); err != nil {
		lifetime.logf("import shutdown: %v; the next start reconciles unfinished runs", err)
	}
}

func resetAdmin(arguments []string) error {
	flags := flag.NewFlagSet("reset-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	passwordFile := flags.String("password-file", "", "owner-readable file containing the new password")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("reset-admin takes no positional arguments")
	}
	if *passwordFile == "" {
		return errors.New("--password-file is required; passwords are never accepted as command arguments")
	}
	password, err := readPrivatePassword(*passwordFile)
	if err != nil {
		return err
	}
	// Root reads its own password file first, then acts as the account that
	// owns the state directory; see actAsStateOwner.
	if _, err := actAsStateOwner(*stateDir); err != nil {
		return err
	}
	encoded, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	store, err := openLiveState(context.Background(), *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	settings, err := store.Settings(context.Background())
	if err != nil {
		return err
	}
	if !settings.Initialized {
		return errors.New("setup is not complete")
	}
	accessHash, err := store.PasswordHash(context.Background(), "access")
	if err != nil {
		return err
	}
	if accessHash != "" && auth.CheckPassword(accessHash, password) {
		return errors.New("administrator password must differ from the shared access password")
	}
	if err := store.SetAdminPassword(context.Background(), encoded); err != nil {
		return err
	}
	fmt.Println("Administrator password reset. Repository data was not changed; existing administrator sessions were revoked.")
	return nil
}

func approveHost(arguments []string) error {
	flags := flag.NewFlagSet("approve-host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	operands, err := parseFlagsAndOperands(flags, arguments)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		return errors.New("approve-host requires exactly one host name")
	}
	host := operands[0]
	policy := server.NewHostPolicy()
	if err := policy.Add(host); err != nil {
		return err
	}
	store, err := openLiveState(context.Background(), *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.AddTrustedHost(context.Background(), host); err != nil {
		return err
	}
	fmt.Println("Host approved. Restart the server to load the change.")
	return nil
}

// forgetCheckContainer releases the container cleanup record of one check job
// whose Docker daemon OwnGit can no longer reach, such as after Docker was
// reset or reinstalled. Like reset-admin it edits host-local state directly and
// works whether or not the server is running. It removes no container, so the
// owner must first remove any leftover container or confirm that its daemon is
// gone.
func forgetCheckContainer(arguments []string) error {
	flags := flag.NewFlagSet("forget-check-container", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	jobID := flags.String("job", "", "configured-check job identifier")
	confirmed := flags.Bool("confirm-container-removed", false, "confirm that the job's container was removed or its Docker daemon no longer exists")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("forget-check-container takes no positional arguments")
	}
	if !validHexID(*jobID) {
		return errors.New("--job must be a valid job identifier")
	}
	if !*confirmed {
		return errors.New("--confirm-container-removed is required: first remove any container labeled com.owngit.check-job=" + *jobID + " on the Docker daemon that ran it, or make sure that daemon no longer exists")
	}
	store, err := openLiveState(context.Background(), *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	forgotten, err := (&checkrun.Coordinator{Store: store}).ForgetForeignContainer(context.Background(), *jobID)
	if err != nil {
		return err
	}
	record := forgotten.Record
	containerID := record.ContainerID
	if containerID == "" {
		containerID = "(not yet assigned)"
	}
	fmt.Printf("Forgot the container cleanup record of job %s.\n", record.JobID)
	fmt.Printf("Container name: %s\nContainer ID: %s\nDocker daemon: %s\nLabel: com.owngit.check-job=%s\n",
		record.ContainerName, containerID, record.DaemonID, record.JobID)
	if forgotten.DockerUnavailable != nil {
		fmt.Printf("Docker could not be checked: %v\n", forgotten.DockerUnavailable)
	}
	fmt.Println("OwnGit removed no container. If that Docker daemon comes back, remove any container with this label yourself. Restart OwnGit to clean up check workspaces.")
	return nil
}

func backupState(arguments []string) error {
	if len(arguments) != 0 && arguments[0] == "verify" {
		return verifyBackup(arguments[1:])
	}
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	output := flags.String("output", "", "new backup directory")
	gitPath := flags.String("git", "", "Git executable path")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" {
		return errors.New("backup requires --output and accepts no positional arguments")
	}
	if err := state.RequireExisting(*stateDir); err != nil {
		return err
	}
	// The lock file is created only in a state directory that OwnGit may
	// use, as serve's is; see state.CreateDirectory.
	stateDirectory, err := state.OpenStateDirectory(*stateDir)
	if err != nil {
		return err
	}
	defer stateDirectory.Close()
	unlock, err := state.AcquireOfflineLockIn(stateDirectory)
	if err != nil {
		return fmt.Errorf("backup requires OwnGit to be offline: %w", err)
	}
	defer unlock()
	store, err := openStateIn(context.Background(), stateDirectory, *gitPath, stderrf)
	if err != nil {
		return err
	}
	defer store.Close()
	settings, err := store.Settings(context.Background())
	if err != nil {
		return err
	}
	if !settings.Initialized {
		return errors.New("setup is not complete")
	}
	runner, err := gitexec.New(*gitPath, filepath.Join(store.Dir(), "runtime"))
	if err != nil {
		return err
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: settings.RepositoryRoot}
	if err := recovery.Create(context.Background(), store, manager, *output); err != nil {
		return err
	}
	fmt.Printf("Offline backup written to %s. SHA-256 hashes detect corruption but do not authenticate a replaced backup.\n", *output)
	return nil
}

func restoreState(arguments []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "new host-local state directory")
	input := flags.String("input", "", "offline backup directory")
	repositoryRoot := flags.String("repository-root", "", "new repository storage directory")
	gitPath := flags.String("git", "", "Git executable path")
	verifyFirst := flags.Bool("verify", false, "rehearse the restore first, as backup verify does, and restore only a verified backup")
	temporary := flags.String("temp-dir", "", "folder for the rehearsal of --verify (default: the system's temporary folder)")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *repositoryRoot == "" {
		return errors.New("restore requires --input and --repository-root and accepts no positional arguments")
	}
	// An interrupt stops the work, and the rehearsal and the restore remove
	// what they created.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *verifyFirst {
		result, err := recovery.Verify(ctx, *input, *temporary, *gitPath)
		if !result.Verified || err != nil {
			printVerification(os.Stdout, result)
			printSpaceHint(os.Stdout, err)
			var interrupted *recovery.Interrupted
			if errors.As(err, &interrupted) {
				return &recovery.Interrupted{What: "verification", Detail: "nothing was restored", Cause: err}
			}
			return errors.New("the backup was not verified, so nothing was restored")
		}
		fmt.Printf("Backup verified: %d repositories and the database passed the rehearsal.\n", len(result.Repositories))
	}
	if err := recovery.Restore(ctx, *input, *stateDir, *repositoryRoot, *gitPath); err != nil {
		return err
	}
	fmt.Printf("Offline backup restored to %s with repositories at %s. Previous sessions, trusted hosts, and network settings were not restored.\n", *stateDir, *repositoryRoot)
	return nil
}

// readPrivatePassword reads a password file for a command that sends it to
// no server. A server line, if present, is accepted and not used.
func readPrivatePassword(path string) (string, error) {
	file, err := readPasswordFile(path)
	var notPrivate *state.NotPrivateError
	var content *passwordContentError
	if errors.As(err, &notPrivate) || errors.As(err, &content) {
		return "", errors.New(secretFileMessage("The password file", err))
	}
	return file.secret, err
}

func checkRuntimeUnavailableReason(code string) string {
	switch code {
	case checkrun.RuntimeUnavailableWorkspace:
		return "Configured checks are unavailable because the private workspace could not be acquired. Repair the workspace and restart OwnGit."
	case checkrun.RuntimeUnavailableRecovery:
		return "Configured checks are unavailable because restart authority reconciliation failed. Repair check state and restart OwnGit."
	default:
		return "Configured checks are unavailable. Repair the check runtime and restart OwnGit."
	}
}

// openState opens the state directory and reports a schema upgrade that the
// open applied, so the operator can tell when older builds stopped accepting
// the database. openState is for the commands that work beside a running
// server, without the offline lock, so it refuses an older schema; see
// refuseUpgrade.
func openState(ctx context.Context, dir string, report func(string, ...any)) (*state.Store, error) {
	held, err := state.CreateDirectory(dir)
	if err != nil {
		return nil, err
	}
	defer held.Close()
	return openHeldState(ctx, held, refuseUpgrade, report)
}

// openStateIn is openState for the state directory held, which
// state.CreateDirectory or state.OpenStateDirectory returned, and whose
// offline lock the caller has held since before the open. An older schema
// is backed up first and then upgraded; see backupBeforeUpgrade. gitPath is
// the Git for that backup, or "" to find it as usual. serve passes its log;
// offline commands pass stderrf.
func openStateIn(ctx context.Context, held *os.File, gitPath string, report func(string, ...any)) (*state.Store, error) {
	return openHeldState(ctx, held, backupBeforeUpgrade(held, gitPath, report), report)
}

func openHeldState(ctx context.Context, held *os.File, beforeUpgrade state.BeforeUpgrade, report func(string, ...any)) (*state.Store, error) {
	store, err := state.OpenIn(ctx, held, beforeUpgrade)
	if err != nil {
		return nil, err
	}
	if upgrade := store.SchemaUpgrade(); upgrade != "" {
		report("%s", upgrade)
	}
	return store, nil
}

// openLiveState opens a state directory that a running server may be writing
// to. Opening refuses a directory that changed while it was inspected and
// says the operation can be retried, so the commands that work next to a
// running server retry a few times instead of failing because the server
// wrote at that moment.
func openLiveState(ctx context.Context, stateDir string) (*state.Store, error) {
	return retryUnstableOpen(liveStateAttempts, func() (*state.Store, error) { return openLiveStateAttempt(ctx, stateDir) })
}

// retryUnstableOpen runs open up to attempts times while it fails with
// state.ErrInspectionUnstable. The wait between attempts varies, so a retry
// does not keep meeting writes that repeat at a steady interval.
func retryUnstableOpen(attempts int, open func() (*state.Store, error)) (*state.Store, error) {
	for attempt := 1; ; attempt++ {
		store, err := open()
		if !errors.Is(err, state.ErrInspectionUnstable) || attempt == attempts {
			return store, err
		}
		time.Sleep(liveStateRetryDelay/2 + rand.N(liveStateRetryDelay))
	}
}

const (
	liveStateAttempts = 5
	// serveStateAttempts is larger, about ten seconds: a starting server
	// has no one to report a retryable error to, and commands may open the
	// state the whole time it starts.
	serveStateAttempts  = 100
	liveStateRetryDelay = 100 * time.Millisecond
)

// openLiveStateAttempt and openServeStateAttempt are one attempt of
// openLiveState and of serve's open; tests replace them to make the state
// directory change during inspection.
var (
	openLiveStateAttempt = func(ctx context.Context, stateDir string) (*state.Store, error) {
		return openState(ctx, stateDir, stderrf)
	}
	openServeStateAttempt = openStateIn
)

// stderrf writes one line to standard error.
func stderrf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func serveOpenTarget(initialized, explicitlyOpen, noOpen bool, setupPath, origin string) string {
	if !initialized {
		if noOpen {
			return ""
		}
		return setupPath
	}
	if explicitlyOpen {
		return origin
	}
	return ""
}

func openServeTarget(target, label string, opener func(string) error, logf func(string, ...any)) {
	if err := opener(target); err != nil {
		logf("could not open %s automatically: %v", label, err)
	}
}

func ownerOrigin(configured, listenAddress string) (string, error) {
	if configured == "" {
		host, port, err := net.SplitHostPort(listenAddress)
		if err != nil {
			return "", fmt.Errorf("invalid listen address: %w", err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		configured = "http://" + net.JoinHostPort(host, port)
	}
	canonical, err := server.ValidateBaseURL(configured)
	if err != nil {
		return "", fmt.Errorf("--base-url: %w", err)
	}
	return canonical, nil
}

func defaultStateDir() string {
	if pointed := pointerStateDir(); pointed != "" {
		return pointed
	}
	return ownStateDir()
}

// ownStateDir is the default state directory of this account, without the
// account service's pointer.
func ownStateDir() string {
	if configured, err := os.UserConfigDir(); err == nil {
		return defaultStatePath(configured, "")
	}
	home, _ := os.UserHomeDir()
	return defaultStatePath("", home)
}

func defaultStatePath(configured, home string) string {
	if configured != "" {
		return filepath.Join(configured, "owngit")
	}
	return filepath.Join(home, ".owngit")
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit [serve|service|health|setup-link|reset-admin|approve-host|network|tailscale|forget-check-container|backup|restore|upgrade-backup|repo|pr|check|helper-credential|check-policy|check-job|runner-credential|runner|import|settings|skill|mcp|update|uninstall|doctor|version] [options]")
	fmt.Fprintln(writer, "Run owngit <command> --help for the options of a command.")
}

// errUsageShown ends a command that printed its usage because -h or --help
// was given. run turns it into success.
var errUsageShown = errors.New("usage shown")

// helpRequested reports whether a command's arguments ask for its usage
// instead of running it.
func helpRequested(arguments []string) bool {
	for index, argument := range arguments {
		switch {
		case argument == "--":
			return false
		case argument == "-h" || argument == "--help" || argument == "-help" || index == 0 && argument == "help":
			return true
		}
	}
	return false
}

// isHelpArgument reports whether a command group's first argument asks for
// its usage.
func isHelpArgument(argument string) bool {
	return argument == "help" || argument == "-h" || argument == "--help"
}

// commandOperands names the positional operands of the commands that take
// them, keyed by flag set name, for the usage line.
var commandOperands = map[string]string{
	"approve-host":       "<host>",
	"backup verify":      "<backup>",
	"import add":         "<name> <url>",
	"import refresh":     "<name>",
	"import status":      "<name>",
	"import history":     "<name>",
	"import cancel":      "<name>",
	"import schedule":    "<name>",
	"import credentials": "<name>",
	"import resolve":     "<name>",
	"upgrade-backup":     "[on|off]",
}

// parseFlags parses a command's flags. On -h or --help it prints the
// command's usage and options to stdout and returns errUsageShown, so the
// command stops without doing anything and the process exits 0.
func parseFlags(flags *flag.FlagSet, arguments []string) error {
	err := flags.Parse(arguments)
	if errors.Is(err, flag.ErrHelp) {
		printFlagUsage(os.Stdout, flags)
		return errUsageShown
	}
	return err
}

// parseFlagsAndOperands parses a command's flags wherever they appear among
// its positional operands, as the usage line "owngit approve-host <host>
// [options]" shows, and returns the operands in order. After "--", every
// argument is an operand.
func parseFlagsAndOperands(flags *flag.FlagSet, arguments []string) ([]string, error) {
	var operands []string
	for {
		if err := parseFlags(flags, arguments); err != nil {
			return nil, err
		}
		rest := flags.Args()
		if consumed := len(arguments) - len(rest); consumed > 0 && arguments[consumed-1] == "--" {
			return append(operands, rest...), nil
		}
		if len(rest) == 0 {
			return operands, nil
		}
		operands = append(operands, rest[0])
		arguments = rest[1:]
	}
}

func printFlagUsage(writer io.Writer, flags *flag.FlagSet) {
	synopsis := "owngit " + flags.Name()
	if operands := commandOperands[flags.Name()]; operands != "" {
		synopsis += " " + operands
	}
	defined := false
	flags.VisitAll(func(*flag.Flag) { defined = true })
	if !defined {
		fmt.Fprintf(writer, "Usage: %s\n\nThis command takes no options.\n", synopsis)
		return
	}
	fmt.Fprintf(writer, "Usage: %s [options]\n\nOptions:\n", synopsis)
	flags.VisitAll(func(entry *flag.Flag) {
		kind, usage := flag.UnquoteUsage(entry)
		name := "--" + entry.Name
		if kind != "" {
			name += " " + kind
		}
		switch {
		case entry.DefValue == "" || entry.DefValue == "false" || entry.DefValue == "0" || entry.DefValue == "0s":
		case kind == "string":
			usage += fmt.Sprintf(" (default %q)", entry.DefValue)
		default:
			usage += fmt.Sprintf(" (default %s)", entry.DefValue)
		}
		fmt.Fprintf(writer, "  %s\n        %s\n", name, usage)
	})
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}
