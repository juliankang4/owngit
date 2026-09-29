package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"owngit/internal/releasecheck"
	"owngit/internal/service"
	"owngit/internal/version"
)

// "owngit update" asks GitHub for the latest release and prints the one
// command that updates this OwnGit the way it was installed. OwnGit never
// runs that command or replaces its own files.

func updateCommand(arguments []string) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("update takes no arguments")
	}
	install := detectInstall()
	checker := &releasecheck.Checker{Current: version.Version, URL: releaseCheckEndpoint}
	if err := checker.Check(context.Background()); err != nil {
		return fmt.Errorf("could not ask GitHub for the latest release: %w", err)
	}
	release, newer := checker.Newer()
	runs := serviceRunning(install)
	platform := updatePlatform(install, runs)
	command, start := "", ""
	if newer {
		command = install.UpdateCommand(release.Version, platform)
		start = install.StartAfterUpdate(release.Version, platform)
	}
	if *asJSON {
		// The command keeps its & and > readable.
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			Current  string `json:"current"`
			Latest   string `json:"latest,omitempty"`
			Newer    bool   `json:"newer"`
			Route    string `json:"route"`
			Program  string `json:"program"`
			Command  string `json:"command,omitempty"`
			Start    string `json:"start,omitempty"`
			NotesURL string `json:"notes_url,omitempty"`
		}{version.Version, release.Version, newer, string(install.Route), install.Executable, command, start, release.NotesURL})
	}
	out := os.Stdout
	if !newer {
		fmt.Fprintf(out, "OwnGit %s is the latest release.\n", version.Version)
		return nil
	}
	fmt.Fprintf(out, "OwnGit %s is available (this is %s): %s\n", release.Version, version.Version, release.NotesURL)
	fmt.Fprintf(out, "%s: %s\n", routeDescription(install.Route), printable(install.Executable))
	if runs.other != "" {
		fmt.Fprintf(out, "The OwnGit service runs %s, so this updates only the program above.\n", printable(runs.other))
	}
	if command == "" {
		fmt.Fprintln(out, noUpdateCommand(install, release.Version))
		return nil
	}
	fmt.Fprintf(out, "Update it with this command; OwnGit does not run it for you:\n  %s\n", printable(command))
	switch {
	case start != "":
		fmt.Fprintf(out, "Then start OwnGit from %s.\n", printable(start))
	case !runs.this:
		fmt.Fprintln(out, "Then restart OwnGit where it runs.")
	}
	return nil
}

// routeDescription says in a few words who installed the program.
func routeDescription(route service.Route) string {
	switch route {
	case service.RouteHomebrew:
		return "Installed with Homebrew"
	case service.RouteNPM:
		return "Installed with npm"
	case service.RoutePacman:
		return "Installed with pacman"
	case service.RouteApp:
		return "Part of OwnGit.app"
	case service.RouteUnknown:
		return "Service copy of an unrecorded program"
	}
	return "Unpacked from a release archive"
}

// noUpdateCommand says what to do when the route has no update command.
func noUpdateCommand(install service.Install, latest string) string {
	switch install.Route {
	case service.RouteApp:
		return "Replace OwnGit.app with the app of the new release."
	case service.RouteUnknown:
		return "Update the owngit.exe you installed the service from, then run its \"owngit service install\"."
	case service.RoutePacman:
		if install.Package != service.ReleasePackage {
			return "Installed by the pacman package " + printable(install.Package) + "; update it the way you installed it."
		}
		return "makepkg does not run as root. Run \"owngit update\" as your normal account for the command that updates the package."
	}
	return fmt.Sprintf("No release archive exists for %s/%s; build %s from source.", runtime.GOOS, runtime.GOARCH, latest)
}

// dashboardUpdateCommand prepares the update command that the dashboard's
// new-release notice shows, with what the owner starts afterwards: start
// is a program in a new place, and restart means restarting OwnGit where it
// runs. The route and the service are read once, when the first notice
// needs them. asService is true when a service manager started this server
// with --service; a systemd unit of "owngit service" does not pass it, so
// the registration is read as the command reads it.
func dashboardUpdateCommand(asService bool) func(string) (command, start string, restart bool) {
	var (
		once     sync.Once
		install  service.Install
		platform service.Platform
		runs     serviceState
	)
	return func(latest string) (string, string, bool) {
		once.Do(func() {
			install = detectInstall()
			runs = serviceState{this: asService}
			if !asService && runtime.GOOS != "windows" {
				runs = serviceRunning(install)
			}
			if asService && runtime.GOOS == "windows" {
				// A sign-in task starts the program itself; a boot task
				// starts the service copy.
				executable, _ := runningExecutable()
				runs.file = sameFile(executable, install.Executable)
			}
			platform = updatePlatform(install, runs)
		})
		command := install.UpdateCommand(latest, platform)
		start := install.StartAfterUpdate(latest, platform)
		return command, start, command != "" && !runs.this && start == ""
	}
}

func updatePlatform(install service.Install, runs serviceState) service.Platform {
	return service.Platform{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Service: runs.this, ServiceRunsFile: runs.file,
		Sudo: needsSudo(install),
		Root: runtime.GOOS != "windows" && os.Geteuid() == 0,
	}
}

// needsSudo reports whether the update and remove commands need root,
// because this account cannot write the folder they change.
func needsSudo(install service.Install) bool {
	folder := install.ProgramFolder()
	return folder != "" && !canWrite(folder)
}

// runningExecutable is this program with links resolved.
func runningExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return executable, nil
}

// detectInstall finds how the running program was installed, from facts on
// this computer: where the file is, which package pacman lists it in, and
// for the Windows service copy the record of the file it was copied from.
func detectInstall() service.Install {
	executable, err := runningExecutable()
	if err != nil {
		return service.Install{Route: service.RouteUnknown}
	}
	if runtime.GOOS == "windows" {
		if paths, err := servicePaths(); err == nil && sameFile(executable, paths.Executable) {
			source, err := os.ReadFile(filepath.Join(paths.Directory, service.ServiceCopyRecord))
			if err != nil || !filepath.IsAbs(string(source)) {
				return service.Install{Route: service.RouteUnknown, Executable: executable}
			}
			executable = string(source)
		}
	}
	install := service.ClassifyExecutable(executable)
	if runtime.GOOS == "linux" {
		install = install.OwnedBy(pacmanOwner(executable))
	}
	return install
}

// pacmanPath is the pacman that install detection asks, at the fixed path
// Arch Linux installs it; PATH is not searched.
const pacmanPath = "/usr/bin/pacman"

// pacmanOwner returns pacman's answer for the package that holds path, or
// "" when pacman is missing or no package holds it. The answer is checked
// as a package name by service.Install.OwnedBy. Tests replace it.
var pacmanOwner = func(path string) string { return askPacman(pacmanPath, path) }

// askPacman runs pacman only when it is a program that only root can change,
// by the rule for programs root runs (rootControlledExecutable).
func askPacman(pacman, path string) string {
	if err := rootControlledExecutable(pacman); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, pacman, "-Qqo", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(string(output), "\n")
}

// serviceState is whether an OwnGit service of this account starts the
// installed program (this), whether it starts that very file rather than
// the Windows service copy (file), and which other program it starts
// instead (other).
type serviceState struct {
	this, file bool
	other      string
}

// serviceRunning reads the service registration of this account. Only a
// registration that starts this program gets "owngit service install" in
// the update command, so an update never moves a service that runs another
// installation. Tests replace it.
var serviceRunning = func(install service.Install) serviceState {
	compare := func(program string, file bool) serviceState {
		if sameFile(program, install.Executable) {
			return serviceState{this: true, file: file}
		}
		return serviceState{other: program}
	}
	switch runtime.GOOS {
	case "linux":
		configDir, _ := os.UserConfigDir()
		if installed, found, err := findInstalled(configDir); err == nil && found {
			return compare(installed.Executable, true)
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		if installed, err := service.ReadLaunchAgent(service.LaunchAgentPath(home)); err == nil {
			return compare(installed.Executable, true)
		}
	case "windows":
		host, err := newTaskHost()
		if err != nil {
			return serviceState{}
		}
		installed, found, err := host.installed()
		if err != nil || !found {
			return serviceState{}
		}
		if installed.Mode != service.ModeBootTask {
			return compare(installed.Executable, true)
		}
		source, err := os.ReadFile(filepath.Join(host.serviceInstall.Directory, service.ServiceCopyRecord))
		if err != nil {
			return serviceState{other: installed.Executable}
		}
		return compare(string(source), false)
	}
	if install.Route == service.RouteHomebrew && homebrewServiceRegistered() {
		return serviceState{this: true}
	}
	return serviceState{}
}

// homebrewServiceRegistered reports whether "brew services" has OwnGit set
// up for this account, from the files it writes.
func homebrewServiceRegistered() bool {
	home, _ := os.UserHomeDir()
	var paths []string
	for _, label := range service.HomebrewLabels {
		if runtime.GOOS == "darwin" {
			paths = append(paths, filepath.Join(home, "Library", "LaunchAgents", label+".plist"), filepath.Join("/Library/LaunchDaemons", label+".plist"))
			continue
		}
		name := strings.Replace(label, "homebrew.mxcl.", "homebrew.", 1) + ".service"
		paths = append(paths, filepath.Join(home, ".config", "systemd", "user", name), filepath.Join("/usr/lib/systemd/system", name))
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// sameFile reports whether two paths name the same program file.
func sameFile(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(resolve(left), resolve(right))
	}
	return resolve(left) == resolve(right)
}
