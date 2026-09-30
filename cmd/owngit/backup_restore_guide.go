package main

import (
	"errors"
	"io/fs"
	"os"
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
			Shell: commandShell(runtime.GOOS),
		}
		// An uploaded backup is in the state folder, which moves first. A
		// command is given only when where input will be is known.
		relative, inside, err := pathInside(stateDir, input)
		if err != nil {
			guide.Unchecked, guide.Problem = input, err.Error()
			return guide
		}
		if inside {
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

// pathInside reports whether input is inside the folder dir, and its path
// relative to dir when it is. The folders are compared as files, not as
// names, so that a way through a link or another spelling of either still
// matches. A folder on the way that cannot be inspected is an error, not
// an answer; one that does not exist means that input does not either, so
// nothing of it moves with dir.
func pathInside(dir, input string) (string, bool, error) {
	folder, err := os.Stat(dir)
	if err != nil {
		return "", false, err
	}
	for parent := filepath.Dir(input); parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
		info, err := os.Stat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if os.SameFile(info, folder) {
			relative, err := filepath.Rel(parent, input)
			return relative, err == nil, err
		}
	}
	return "", false, nil
}

// commandWord quotes a path for this computer's shell (shellWord).
func commandWord(word string) string { return shellWord(runtime.GOOS, word) }

// commandShell names the shell the commands are written for, on a system
// where that is not the usual POSIX shell.
func commandShell(goos string) string {
	if goos == "windows" {
		return "PowerShell"
	}
	return ""
}

// shellWord quotes word as one literal argument for the shell of goos: a
// PowerShell literal string on Windows, and a POSIX shell word elsewhere.
// Neither expands anything inside the quotes. PowerShell ends a literal
// string at an apostrophe or any of the single quotation marks U+2018,
// U+2019, U+201A and U+201B, and reads each doubled as the character
// itself.
func shellWord(goos, word string) string {
	if goos == "windows" {
		var quoted strings.Builder
		quoted.WriteByte('\'')
		for _, r := range word {
			if strings.ContainsRune("'\u2018\u2019\u201a\u201b", r) {
				quoted.WriteRune(r)
			}
			quoted.WriteRune(r)
		}
		quoted.WriteByte('\'')
		return quoted.String()
	}
	return service.ShellQuote(word)
}
