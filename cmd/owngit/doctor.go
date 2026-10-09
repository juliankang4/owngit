package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

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
		return jsonFailure(jsonRequested(arguments), "invalid_arguments", errors.New("doctor takes no positional arguments"))
	}
	dir, err := filepath.Abs(*stateDir)
	if err != nil {
		return jsonFailure(jsonRequested(arguments), "invalid_arguments", err)
	}
	subject, err := commandSubject(dir)
	if err != nil {
		return jsonFailure(jsonRequested(arguments), "state_unavailable", err)
	}
	findings := diagnose(context.Background(), subject)
	running := subject.server == doctor.ServerRunning
	if *asJSON {
		type finding struct {
			webui.Finding
			Message string `json:"message"`
		}
		report := struct {
			Version         string                  `json:"version"`
			Program         string                  `json:"program"`
			StateDir        string                  `json:"state_dir"`
			Repositories    string                  `json:"repositories,omitempty"`
			Listen          string                  `json:"listen"`
			Running         bool                    `json:"running"`
			Service         bool                    `json:"service"`
			Log             string                  `json:"log,omitempty"`
			Findings        []finding               `json:"findings"`
			StateProtection []stateProtectionChange `json:"state_protection,omitempty"`
		}{version.Version, subject.program, dir, subject.repositories, subject.listen, running, subject.service.found, subject.service.log, []finding{}, stateProtectionPlan(dir, subject.stateProtection)}
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
	reportStateProtection(out, dir, subject.stateProtection)
	if len(findings) == 0 {
		if len(subject.stateProtection) == 0 {
			fmt.Fprintln(out, "No problem found.")
		}
		return nil
	}
	for _, item := range findings {
		heading := "Problem:"
		if item.Unchecked {
			heading = "Could not check:"
		}
		fmt.Fprintf(out, "\n%s %s\n", heading, printable(item.Sentence(webui.LangEN)))
		if item.Repair != "" {
			fmt.Fprintf(out, "Repair:  %s\n", printable(item.Repair))
		}
	}
	return nil
}

type stateProtectionChange struct {
	state.ProtectionChange
	Repair string `json:"repair"`
}

func stateProtectionPlan(stateDir string, changes []state.ProtectionChange) []stateProtectionChange {
	plan := make([]stateProtectionChange, 0, len(changes))
	for _, change := range changes {
		plan = append(plan, stateProtectionChange{change, "owngit serve --state-dir " + quoteForShell(stateDir)})
	}
	return plan
}

func reportStateProtection(out io.Writer, stateDir string, changes []state.ProtectionChange) {
	for _, change := range stateProtectionPlan(stateDir, changes) {
		fmt.Fprintf(out, "Starting OwnGit will make %q private (%s). To apply this, run: %s\n", printable(change.Path), printable(change.Before), printable(change.Repair))
	}
}

// doctorSubject is the installation that the checkup looks at, as the
// command or the server sees it.
type doctorSubject struct {
	server doctor.Server
	// serverReason is why server is doctor.ServerUnknown.
	serverReason  string
	serverFinding *webui.Finding
	setupComplete bool
	service       servingService
	listen        string
	// program is the owngit program that serves.
	program string
	// administrator is true on Windows when the account that runs OwnGit
	// is an administrator.
	administrator   bool
	stateDir        string
	repositories    string
	repositoryIDs   []string
	stateProtection []state.ProtectionChange
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
// server: its service, running record, listen address and saved setup.
// Only the per-start health proof confirms that this server answers.
// An answer without a running record belongs to another program.
func commandSubject(stateDir string) (doctorSubject, error) {
	subject := doctorSubject{stateDir: stateDir, service: findServingService(stateDir)}
	if held, err := state.OpenStateDirectory(stateDir); err == nil {
		held.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return stateRefusalSubject(subject, err)
	}
	observed := state.RunningObservation{Server: state.ServerNotRunning}
	savedListen := ""
	switch err := state.RequireExisting(stateDir); {
	case errors.Is(err, state.ErrNotExist):
	case err != nil:
		return doctorSubject{}, err
	default:
		ctx := context.Background()
		store, err := openObservedState(ctx, stateDir)
		if err != nil {
			return stateRefusalSubject(subject, err)
		}
		defer store.Close()
		subject.stateProtection = store.StateProtectionChanges()
		if observed, err = store.ObserveRunningNetwork(ctx); err != nil {
			return doctorSubject{}, err
		}
		saved, err := store.NetworkSettings(ctx)
		if err != nil {
			return doctorSubject{}, err
		}
		savedListen = saved.Listen
		settings, err := store.Settings(ctx)
		if err != nil {
			return doctorSubject{}, err
		}
		subject.setupComplete = settings.Initialized
		if settings.Initialized && filepath.IsAbs(settings.RepositoryRoot) {
			subject.repositories = filepath.Clean(settings.RepositoryRoot)
			repositories, err := store.Repositories(ctx)
			if err != nil {
				return doctorSubject{}, err
			}
			for _, repository := range repositories {
				subject.repositoryIDs = append(subject.repositoryIDs, repository.ID)
			}
		}
	}
	address, _ := healthTarget(observed, savedListen)
	subject.listen = listenOf(observed, savedListen)
	target, err := localTarget(address)
	if err != nil {
		return doctorSubject{}, err
	}
	switch observed.Server {
	case state.ServerRunning, state.ServerStarting:
		if _, err := confirmedHealth(stateDir); err == nil {
			subject.server = doctor.ServerRunning
		} else if checkHealth(target) != nil {
			subject.server = doctor.ServerSilent
		} else {
			subject.server = doctor.ServerUnknown
			finding := webui.Finding{Code: webui.MsgTrayUnproven, Unchecked: true}
			if errors.Is(err, errHealthKeyMissing) {
				finding.Code, finding.Args = webui.MsgTrayHealthKey, []string{stateDir}
				if subject.service.found {
					finding.Repair = "owngit service restart"
				}
			}
			subject.serverFinding = &finding
		}
	case state.ServerNotRunning:
		subject.server = doctor.ServerStopped
		if checkHealth(target) == nil {
			subject.server = doctor.ServerElsewhere
		}
	default:
		subject.server, subject.serverReason = doctor.ServerUnknown, "another program holds the state directory, or its running record cannot be vouched for"
	}
	subject.program = subject.service.program
	if subject.program == "" {
		if subject.program, err = runningExecutable(); err != nil {
			return doctorSubject{}, err
		}
	}
	subject.administrator = subject.service.administrator || probeEnvironment().Administrator
	return subject, nil
}

func stateRefusalSubject(subject doctorSubject, err error) (doctorSubject, error) {
	var private *state.NotPrivateError
	if !errors.As(err, &private) {
		return doctorSubject{}, err
	}
	subject.server = doctor.ServerUnknown
	subject.serverFinding = &webui.Finding{Code: webui.MsgTrayStateUnsafe, Args: []string{subject.stateDir}, Repair: private.Fix, Unchecked: true}
	return subject, nil
}

// serverDiagnosis returns the checkup that the Settings page shows, from
// the facts of this server: it runs, setup is done, and it listens on
// listen. asService is true when a service manager started it.
func serverDiagnosis(stateDir, listen string, asService bool, repositoryStorage func(context.Context) (string, []string, error)) func(context.Context) []webui.Finding {
	return func(ctx context.Context) []webui.Finding {
		program, _ := runningExecutable()
		subject := doctorSubject{
			server: doctor.ServerRunning, setupComplete: true, service: servingService{found: asService},
			listen: listen, program: program, stateDir: stateDir,
			administrator: probeEnvironment().Administrator,
		}
		root, repositoryIDs, err := repositoryStorage(ctx)
		if err != nil {
			return append(diagnose(ctx, subject), webui.Finding{Code: webui.MsgDoctorUncheckedOwner, Args: []string{err.Error()}, Unchecked: true})
		}
		subject.repositories = root
		subject.repositoryIDs = repositoryIDs
		return diagnose(ctx, subject)
	}
}

// diagnose reads what this computer says about subject and returns the
// findings. Every tool it runs ends with ctx.
func diagnose(ctx context.Context, subject doctorSubject) []webui.Finding {
	if subject.serverFinding != nil {
		return []webui.Finding{*subject.serverFinding}
	}
	facts := doctor.Facts{
		GOOS: runtime.GOOS, Server: subject.server, ServerReason: subject.serverReason, Service: subject.service.found,
		SetupComplete: subject.setupComplete, Listen: subject.listen, Program: subject.program,
		Administrator: subject.administrator,
	}
	host, _, err := net.SplitHostPort(subject.listen)
	if err == nil {
		facts.OtherDevices = !server.IsLoopbackHost(host)
	}
	if subject.server != doctor.ServerRunning {
		return append(doctor.Diagnose(facts), repositoryPrivacyFindings(subject)...)
	}
	var firewallErr error
	switch runtime.GOOS {
	case "windows":
		taskHost, err := newTaskHost()
		if err != nil {
			facts.Unchecked = append(facts.Unchecked, doctor.Unchecked{Code: webui.MsgDoctorUncheckedOwner, Reason: err.Error()})
			if facts.OtherDevices {
				firewallErr = err
			}
			break
		}
		firewallErr = taskHost.readDoctorFacts(ctx, &facts, []string{subject.stateDir, subject.repositories})
	case "darwin":
		if facts.OtherDevices {
			facts.Firewall, firewallErr = macFirewall(ctx, subject.program)
		}
	case "linux":
		if facts.OtherDevices {
			facts.Firewall, firewallErr = linuxFirewall(ctx, host, ufwConfig, firewallCmd)
		}
	}
	if firewallErr != nil {
		facts.Unchecked = append(facts.Unchecked, doctor.Unchecked{Code: webui.MsgDoctorUncheckedFirewall, Reason: firewallErr.Error()})
	}
	return append(doctor.Diagnose(facts), repositoryPrivacyFindings(subject)...)
}

func repositoryPrivacyFindings(subject doctorSubject) []webui.Finding {
	if subject.repositories == "" {
		return nil
	}
	var findings []webui.Finding
	rootInfo, err := os.Lstat(subject.repositories)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		if err == nil {
			err = fmt.Errorf("%s is not a plain folder", subject.repositories)
		}
		return append(findings, repositoryRootUnchecked(subject.repositories, err))
	}
	root, err := os.Open(subject.repositories)
	if err != nil {
		return append(findings, repositoryRootUnchecked(subject.repositories, err))
	}
	defer root.Close()
	heldRootInfo, err := root.Stat()
	if err != nil || !os.SameFile(rootInfo, heldRootInfo) {
		if err == nil {
			err = errors.New("repository root changed while it was checked")
		}
		return append(findings, repositoryRootUnchecked(subject.repositories, err))
	}
	changeable, inspectErr := state.ExposedToOtherAccounts(subject.repositories, root, heldRootInfo)
	if inspectErr != nil {
		findings = append(findings, repositoryRootUnchecked(subject.repositories, inspectErr))
	} else if changeable {
		fix, fixErr := state.PrivateDirectoryFix(subject.repositories, false)
		finding := webui.Finding{Code: webui.MsgDoctorRepositoryRootShared, Args: []string{subject.repositories}}
		if fixErr != nil {
			findings = append(findings, finding, repositoryRootUnchecked(subject.repositories, fixErr))
		} else {
			finding.Repair = fix
			findings = append(findings, finding)
		}
	}

	var exposedRepairs []string
	exposedCount := 0
	for _, id := range subject.repositoryIDs {
		name := id + ".git"
		path := filepath.Join(subject.repositories, name)
		repository, openErr := state.OpenOwnFolderIn(root, name)
		if errors.Is(openErr, fs.ErrNotExist) {
			continue
		}
		if openErr != nil {
			findings = append(findings, uncheckedRepositoryEntry(id, openErr))
			continue
		}
		info, statErr := repository.Stat()
		if statErr != nil {
			repository.Close()
			findings = append(findings, uncheckedRepositoryEntry(id, statErr))
			continue
		}
		changeable, inspectErr := state.ExposedToOtherAccounts(path, repository, info)
		if inspectErr != nil {
			findings = append(findings, uncheckedRepositoryEntry(id, inspectErr))
		} else if changeable {
			exposedCount++
			if fix, fixErr := state.PrivateDirectoryFix(path, true); fixErr != nil {
				findings = append(findings, uncheckedRepositoryEntry(id, fixErr))
			} else {
				exposedRepairs = append(exposedRepairs, fix)
			}
		}
		findings = append(findings, repositoryHookFindings(repository, id)...)
		repository.Close()
	}
	if exposedCount != 0 {
		findings = append(findings, webui.Finding{
			Code: webui.MsgDoctorRepositoriesShared, Args: []string{strconv.Itoa(exposedCount), subject.repositories},
			Repair: repositoryPrivacyRepair(exposedRepairs),
		})
	}
	return findings
}

func repositoryRootUnchecked(path string, err error) webui.Finding {
	return webui.Finding{Code: webui.MsgDoctorRepositoryRootUnchecked, Args: []string{path, err.Error()}, Unchecked: true}
}

func uncheckedRepositoryEntry(id string, err error) webui.Finding {
	return webui.Finding{Code: webui.MsgDoctorUncheckedOwner, Args: []string{fmt.Sprintf("repository %q: %v", id, err)}, Unchecked: true}
}

func repositoryPrivacyRepair(commands []string) string {
	separator := " && "
	if runtime.GOOS == "windows" {
		separator = "; "
	}
	return strings.Join(commands, separator)
}

func repositoryHookFindings(repository *os.File, id string) []webui.Finding {
	hooks, err := state.OpenOwnFolderIn(repository, "hooks")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		path := filepath.Join(repository.Name(), "hooks")
		if unsafeManagedHookEntry(path, true, err) {
			return []webui.Finding{managedHookFinding(id, "hooks", path, repository.Name()+".hooks-moved")}
		}
		return []webui.Finding{uncheckedRepositoryEntry(id, err)}
	}
	defer hooks.Close()
	update, err := state.OpenOwnFile(hooks, "update", os.O_RDONLY)
	if err == nil {
		update.Close()
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	path := filepath.Join(hooks.Name(), "update")
	if unsafeManagedHookEntry(path, false, err) {
		return []webui.Finding{managedHookFinding(id, "hooks/update", path, repository.Name()+".update-hook-moved")}
	}
	return []webui.Finding{uncheckedRepositoryEntry(id, err)}
}

func unsafeManagedHookEntry(path string, directory bool, cause error) bool {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
			return true
		}
	}
	message := cause.Error()
	return strings.Contains(message, "belongs to another account") || strings.Contains(message, "has another name")
}

func managedHookFinding(id, label, source, destination string) webui.Finding {
	return webui.Finding{
		Code: webui.MsgDoctorRepositoryHooksUnsafe, Args: []string{id, label},
		Repair: moveAsideCommand(source, destination),
	}
}

func moveAsideCommand(source, destination string) string {
	from, to := shellWord(runtime.GOOS, source), shellWord(runtime.GOOS, destination)
	if runtime.GOOS == "windows" {
		return "if (Test-Path -LiteralPath " + to + ") { throw 'move-aside destination already exists' }; Move-Item -LiteralPath " + from + " -Destination " + to + " -ErrorAction Stop"
	}
	return "test ! -e " + to + " && test ! -L " + to + " && mv " + from + " " + to
}

// doctorToolTimeout and doctorOutputLimit bound each tool that the
// checkup runs: a firewall tool that hangs or floods its output becomes a
// check that could not run.
const (
	doctorToolTimeout = 10 * time.Second
	doctorOutputLimit = 256 << 10
)

// doctorTool runs a fixed tool of the checkup (runBounded). Tests replace
// it, as serviceRunner.
var doctorTool = runBounded

// runBounded runs name with args and returns its combined output. It ends
// the tool when ctx ends, after doctorToolTimeout, or when the output
// passes doctorOutputLimit, and waits for it; each of those is an error.
// A nil environment is this process's.
func runBounded(ctx context.Context, environment []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, doctorToolTimeout)
	defer cancel()
	output := &limitedOutput{limit: doctorOutputLimit, over: cancel}
	command := exec.CommandContext(ctx, name, args...)
	command.Env = environment
	command.Stdout, command.Stderr = output, output
	// Wait returns even when a child of the tool keeps the output open.
	command.WaitDelay = time.Second
	err := command.Run()
	switch {
	case output.exceeded:
		return nil, fmt.Errorf("%s printed more than %d bytes", name, doctorOutputLimit)
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s did not finish: %w", name, context.Cause(ctx))
	}
	return output.data, err
}

// limitedOutput keeps what a tool prints up to limit, and calls over once
// when it passes the limit.
type limitedOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	exceeded bool
	over     func()
}

func (output *limitedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.exceeded || len(output.data)+len(data) > output.limit {
		if !output.exceeded {
			output.exceeded = true
			output.over()
		}
		return 0, errors.New("too much output")
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

// readDoctorFacts reads the owners of folders and, when OwnGit listens for
// other devices, Windows Firewall. A folder it cannot read is recorded in
// facts; the firewall error is returned.
func (host *taskHost) readDoctorFacts(ctx context.Context, facts *doctor.Facts, folders []string) error {
	facts.System = host.system
	owned, err := host.administratorsFolders(folders)
	// A standard account's install gives back only folders in its profile;
	// without the profile location that is a check that could not run.
	profile := ""
	if !facts.Administrator && len(owned) > 0 {
		var profileErr error
		if profile, profileErr = accountProfile(host.sid); profileErr != nil {
			facts.Unchecked = append(facts.Unchecked, doctor.Unchecked{Code: webui.MsgDoctorUncheckedOwner, Reason: "read the location of your user folder: " + profileErr.Error()})
			owned = nil
		}
	}
	for _, folder := range owned {
		if facts.Administrator || insideFolder(folder, profile) {
			facts.AdministratorsFolders = append(facts.AdministratorsFolders, folder)
		} else {
			facts.FoldersElsewhere = append(facts.FoldersElsewhere, folder)
		}
	}
	if err != nil {
		facts.Unchecked = append(facts.Unchecked, doctor.Unchecked{Code: webui.MsgDoctorUncheckedOwner, Reason: err.Error()})
	}
	if !facts.OtherDevices {
		return nil
	}
	// The checkup runs without administrator rights: the server never has
	// them, and "owngit doctor" drops them first.
	powershell := func(script string, extra ...string) ([]byte, error) {
		output, err := doctorTool(ctx, append(os.Environ(), extra...), host.powershell(), service.PowerShellArguments(script)...)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return output, nil
	}
	output, err := powershell(service.FirewallShowScript)
	if err != nil {
		return fmt.Errorf("read the Windows Firewall rule %q: %w", service.FirewallRuleName, err)
	}
	rule, found, foreign := firewallRuleOf(output)
	switch {
	case foreign:
		facts.Firewall.Rule = doctor.RuleForeign
	case found && rule.Allows(facts.Program):
		facts.Firewall.Rule = doctor.RuleAllows
	case found:
		facts.Firewall.Rule = doctor.RuleOther
	}
	_, port, _ := net.SplitHostPort(facts.Listen)
	if output, err = powershell(service.FirewallAccessScript, service.FirewallProgramVariable+"="+facts.Program); err != nil {
		return err
	}
	facts.Firewall.Access, err = service.ParseFirewallAccess(string(output), port)
	return err
}

// socketFilter is the macOS application firewall's command line tool.
const socketFilter = "/usr/libexec/ApplicationFirewall/socketfilterfw"

// macFirewall reads whether the macOS application firewall is on, blocks
// every incoming connection, or blocks program.
func macFirewall(ctx context.Context, program string) (doctor.Firewall, error) {
	ask := func(args ...string) (string, error) {
		output, err := doctorTool(ctx, nil, socketFilter, args...)
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
	// ufwConfigLimit bounds how much of ufwConfig is read.
	ufwConfigLimit = 64 << 10
)

// linuxFirewall reads whether ufw is enabled (in the file config) and
// whether firewalld runs (from the tool firewalld), and, when one of them
// is on, the private networks that a server listening on host reaches,
// with each interface's firewalld zone. Their rules need root to read, so
// it does not say whether they allow OwnGit's port.
func linuxFirewall(ctx context.Context, host, config, firewalld string) (doctor.Firewall, error) {
	var firewall doctor.Firewall
	content, err := readBounded(config, ufwConfigLimit)
	switch {
	case err == nil:
		firewall.UFW = ufwEnabled(string(content))
	case !errors.Is(err, os.ErrNotExist):
		return doctor.Firewall{}, err
	}
	if _, err := os.Stat(firewalld); err == nil {
		if err := rootControlledExecutable(firewalld); err != nil {
			return doctor.Firewall{}, err
		}
		// firewall-cmd --state prints "running" and exits 0, or prints
		// "not running" and exits 252.
		output, err := doctorTool(ctx, nil, firewalld, "--state")
		switch answer := strings.TrimSpace(string(output)); {
		case err == nil && answer == "running":
			firewall.Firewalld = true
		case err == nil || answer != "not running":
			return doctor.Firewall{}, fmt.Errorf("%s --state: %v: %s", firewalld, err, answer)
		}
	}
	if !firewall.UFW && !firewall.Firewalld {
		return firewall, nil
	}
	interfaces, err := localInterfaces()
	if err != nil {
		return doctor.Firewall{}, err
	}
	if firewall.Firewalld {
		// The zone decides only for an interface with a network OwnGit
		// gives a command for; one it cannot read in time leaves the
		// command out.
		zones, cancel := context.WithTimeout(ctx, doctorToolTimeout)
		defer cancel()
		for index := range interfaces {
			if len(doctor.PrivateNetworks(host, interfaces[index:index+1])) == 0 {
				continue
			}
			if output, err := doctorTool(zones, nil, firewalld, "--get-zone-of-interface="+interfaces[index].Name); err == nil {
				interfaces[index].Zone = zoneName(string(output))
			}
		}
	}
	firewall.Private = doctor.PrivateNetworks(host, interfaces)
	return firewall, nil
}

// zoneName is the zone that firewall-cmd printed, or "" when it printed
// something else.
func zoneName(output string) string {
	zone := strings.TrimSpace(output)
	if zone == "" || strings.Trim(zone, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return ""
	}
	return zone
}

// localInterfaces returns the interfaces of this computer that are up,
// with their addresses.
func localInterfaces() ([]doctor.Interface, error) {
	links, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var interfaces []doctor.Interface
	for _, link := range links {
		if link.Flags&net.FlagUp == 0 || link.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := link.Addrs()
		if err != nil {
			return nil, err
		}
		found := doctor.Interface{Name: link.Name}
		for _, address := range addresses {
			if network, ok := address.(*net.IPNet); ok {
				if prefix, err := netip.ParsePrefix(network.String()); err == nil {
					found.Prefixes = append(found.Prefixes, prefix)
				}
			}
		}
		interfaces = append(interfaces, found)
	}
	return interfaces, nil
}

// readBounded reads the file at path, refusing one larger than limit.
func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return content, nil
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
