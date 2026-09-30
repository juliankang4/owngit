package main

import (
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"owngit/internal/service"
	"owngit/internal/webui"
)

// restoreGuide returns how the dashboard tells an owner to restore a
// backup on this computer: stop OwnGit, rename its current state folder
// stateDir and repository folder aside (a restore never replaces
// anything), run owngit restore --verify into the same folders, and start
// OwnGit again. asService is true when OwnGit runs as its service, which
// the owngit service commands stop and start; a Linux system service runs
// as its own account, which the restore then runs as too.
func restoreGuide(stateDir string, asService bool) func(input, repositoryRoot string) *webui.BackupRestore {
	runAs := ""
	if asService && runtime.GOOS == "linux" {
		if account, err := user.Current(); err == nil && account.Username == service.AccountName {
			runAs = "sudo -u " + service.AccountName + " "
		}
	}
	return func(input, repositoryRoot string) *webui.BackupRestore {
		guide := &webui.BackupRestore{
			StateDir: stateDir, RepositoryRoot: repositoryRoot,
			MovedState: stateDir + ".before-restore", MovedRepositories: repositoryRoot + ".before-restore",
		}
		// An uploaded backup is in the state folder, which moves first.
		if relative, err := filepath.Rel(stateDir, input); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			input = filepath.Join(guide.MovedState, relative)
		}
		guide.Command = runAs + "owngit restore --input " + commandWord(input) + " --state-dir " + commandWord(stateDir) +
			" --repository-root " + commandWord(repositoryRoot) + " --verify"
		if asService {
			guide.Stop, guide.Start = "owngit service stop", "owngit service start"
		}
		return guide
	}
}

// commandWord quotes a path for this computer's shell: single quotes for a
// POSIX shell, and double quotes on Windows, where a path cannot hold one.
func commandWord(word string) string {
	if runtime.GOOS == "windows" {
		return `"` + word + `"`
	}
	return service.ShellQuote(word)
}
