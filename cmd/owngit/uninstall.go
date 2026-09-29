package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"owngit/internal/service"
	"owngit/internal/state"
)

// "owngit uninstall" removes what OwnGit put on this computer: the service
// registration, and on Windows the protected copy in Program Files. The
// state and the repositories always stay, and the command says where. The
// program files belong to whoever put them here, so the command names the
// command that removes them: the package manager's, or the file to delete
// for a release archive.

func uninstallCommand(arguments []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("uninstall takes no arguments")
	}
	install := detectInstall()
	// Read before the service step, which can make root act as the owngit
	// account.
	sudo := install.Route == service.RouteArchive && !canWrite(filepath.Dir(install.Executable))
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		if err := serviceCommand([]string{"uninstall"}); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stdout, programStaysLine(install, runtime.GOOS, sudo))
	return nil
}

// programStaysLine says who removes the program files and with which
// command.
func programStaysLine(install service.Install, goos string, sudo bool) string {
	program := printable(install.Executable)
	command := printable(install.RemoveCommand(goos, sudo))
	switch install.Route {
	case service.RouteHomebrew:
		return "The program belongs to Homebrew, which removes it: " + command
	case service.RouteNPM:
		return "The program belongs to npm, which removes it: " + command
	case service.RoutePacman:
		return "The program belongs to the pacman package " + printable(install.Package) + ", which pacman removes: " + command
	case service.RouteApp:
		return "The program is part of OwnGit.app; move the app to the Trash to remove it."
	case service.RouteUnknown:
		return "The program " + program + " stays; remove the owngit.exe you installed the service from as you installed it."
	}
	return "The program " + program + " stays, because OwnGit did not put it there. To remove it, delete it (and the folder you unpacked it into, if you made one): " + command
}

// dataStaysLine says where the data of stateDir is, for an uninstall that
// found no service, or "" when there is no state there.
func dataStaysLine(stateDir, repositories string) string {
	err := state.RequireExisting(stateDir)
	switch {
	case errors.Is(err, state.ErrNotExist):
		return ""
	case err == nil && repositories != "":
		return fmt.Sprintf("The state stays in %s and the repositories in %s.", stateDir, repositories)
	}
	return fmt.Sprintf("The state stays in %s.", stateDir)
}
