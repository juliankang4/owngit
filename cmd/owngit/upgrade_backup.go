package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/recovery"
	"owngit/internal/repository"
	"owngit/internal/service"
	"owngit/internal/state"
	"owngit/internal/version"
)

// Before a newer OwnGit upgrades the schema of an existing state, it makes
// an offline backup of the state as it is, in the format that the earlier
// version restores, so the owner can go back. The backup is made from a
// private copy of the database (state.Upgrade.OpenCopy), so nothing in the
// state directory changes until it is complete. When it cannot be made, the
// state is not upgraded and the earlier version can still use it.

// upgradeNoteName marks a backup that the upgrade made, and says how to use
// it. Only backups with this file are ever removed, and only when a newer
// one is complete.
const upgradeNoteName = "owngit-upgrade-backup.txt"

// backupBeforeUpgrade is the state.BeforeUpgrade of every command that
// opens the state in held while holding its offline lock. gitPath is the
// Git that serve was given, or "" to find Git as usual.
func backupBeforeUpgrade(held *os.File, gitPath string, report func(string, ...any)) state.BeforeUpgrade {
	return func(ctx context.Context, upgrade *state.Upgrade) error {
		stateDir := held.Name()
		enabled, err := state.UpgradeBackupEnabled(held)
		if err != nil {
			return notUpgraded(upgrade, err, stateDir)
		}
		if !enabled {
			report("upgrading the state %s without a backup, because the backup before upgrades is off; \"owngit upgrade-backup on\" turns it on", upgrade.Describe())
			return nil
		}
		backup, err := createUpgradeBackup(ctx, stateDir, upgrade, gitPath, report)
		if err != nil {
			return notUpgraded(upgrade, err, stateDir)
		}
		if backup.path == "" {
			// Setup is not complete, so there is nothing to go back to.
			return nil
		}
		report("backed up the state to %s before upgrading it %s", backup.path, upgrade.Describe())
		report("to go back to the earlier OwnGit, stop OwnGit, move %s aside and run with the earlier version: %s", stateDir, backup.restoreCommand)
		return nil
	}
}

// notUpgraded explains a backup that failed before the upgrade.
func notUpgraded(upgrade *state.Upgrade, cause error, stateDir string) error {
	return fmt.Errorf("the state was not upgraded %s, so the earlier OwnGit can still use it, because backing it up first failed: %w; fix that and start OwnGit again (the backup goes into %s), or run \"owngit upgrade-backup off --state-dir %s\" to upgrade without a backup",
		upgrade.Describe(), cause, state.UpgradeBackupFolder(stateDir), quoteForShell(stateDir))
}

// errOlderSchema refuses to open an older state in a command that works
// beside a running server. Only a process that holds the offline lock from
// its start (serve and backup) backs a state up and upgrades it: a command
// beside a server that took the lock for a backup would keep a starting
// server out for as long as the backup takes.
var errOlderSchema = errors.New("this state has an older schema")

// refuseUpgrade is the state.BeforeUpgrade of the commands that work beside
// a running server.
func refuseUpgrade(_ context.Context, upgrade *state.Upgrade) error {
	return fmt.Errorf("%w, which a starting OwnGit backs up and upgrades %s: start OwnGit once (owngit serve, or owngit service start) or run owngit backup, then run this command again", errOlderSchema, upgrade.Describe())
}

type upgradeBackup struct {
	path           string
	restoreCommand string
}

// createUpgradeBackup writes the backup of the state in stateDir, as it is
// before upgrade, into a new folder in state.UpgradeBackupFolder. It
// returns no path when setup is not complete. Once the backup is complete,
// the older backups that upgrades made there are removed.
func createUpgradeBackup(ctx context.Context, stateDir string, upgrade *state.Upgrade, gitPath string, report func(string, ...any)) (result upgradeBackup, err error) {
	// The folder follows the rules of a state directory: on a local disk,
	// this account's, reached through no link that it did not check.
	folder, err := state.CreateDirectory(state.UpgradeBackupFolder(stateDir))
	if err != nil {
		return upgradeBackup{}, err
	}
	defer folder.Close()
	copyDir, err := os.MkdirTemp(folder.Name(), ".owngit-upgrade-copy-")
	if err != nil {
		return upgradeBackup{}, err
	}
	defer func() {
		if removeErr := os.RemoveAll(copyDir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove the temporary copy %s: %w", copyDir, removeErr))
		}
	}()
	if err := state.ProtectPrivatePath(copyDir, true); err != nil {
		return upgradeBackup{}, err
	}
	store, err := upgrade.OpenCopy(ctx, copyDir)
	if err != nil {
		return upgradeBackup{}, err
	}
	defer store.Close()
	settings, err := store.Settings(ctx)
	if err != nil {
		return upgradeBackup{}, err
	}
	if !settings.Initialized {
		return upgradeBackup{}, nil
	}
	runner, err := gitexec.New(gitPath, filepath.Join(copyDir, "runtime"))
	if err != nil {
		return upgradeBackup{}, err
	}
	name, err := newUpgradeBackupName(folder.Name(), time.Now())
	if err != nil {
		return upgradeBackup{}, err
	}
	output := filepath.Join(folder.Name(), name)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: settings.RepositoryRoot}
	if err := recovery.Create(ctx, store, manager, output); err != nil {
		return upgradeBackup{}, err
	}
	// The restored repositories go into the backup folder, where this
	// account has just created the backup, so the command works as printed
	// even when it cannot create a folder beside the repository folder.
	restoreCommand := "owngit restore --input " + quoteForShell(output) + " --state-dir " + quoteForShell(stateDir) +
		" --repository-root " + quoteForShell(output+"-repositories")
	if err := writeUpgradeNote(output, stateDir, upgrade, restoreCommand); err != nil {
		return upgradeBackup{}, fmt.Errorf("the backup %s is complete, but its note could not be written: %w", output, err)
	}
	removeOlderUpgradeBackups(folder.Name(), name, stateDir, report)
	return upgradeBackup{path: output, restoreCommand: restoreCommand}, nil
}

// newUpgradeBackupName names a new backup after the version that makes it
// and the time, as in pre-1.1.3-20260929T101500Z, with a number added when
// that name is taken.
func newUpgradeBackupName(folder string, now time.Time) (string, error) {
	base := "pre-" + version.Version + "-" + now.UTC().Format("20060102T150405Z")
	for number := 1; number <= 100; number++ {
		name := base
		if number > 1 {
			name += "-" + strconv.Itoa(number)
		}
		if _, err := os.Lstat(filepath.Join(folder, name)); errors.Is(err, os.ErrNotExist) {
			return name, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("%s holds 100 backups named %s", folder, base)
}

// writeUpgradeNote writes upgradeNoteName into the completed backup.
func writeUpgradeNote(backup, stateDir string, upgrade *state.Upgrade, restoreCommand string) error {
	file, err := state.CreatePrivateFile(filepath.Join(backup, upgradeNoteName))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "OwnGit %s made this backup before it upgraded the state in %s %s.\n\n"+
		"To go back to the earlier OwnGit version, stop OwnGit, move %s aside, and run this with the earlier version:\n\n  %s\n\n"+
		"Then start the earlier version. The restored repositories are in the folder after --repository-root; another new folder in a place this account can create works as well.\n"+
		"OwnGit removes this backup once it has made a newer one for this state directory before a later upgrade.\n\n"+
		upgradeNoteStatePrefix+"%s\n",
		version.Version, stateDir, upgrade.Describe(), stateDir, restoreCommand, stateDir)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

// upgradeNoteStatePrefix starts the last line of the note, which names the
// state directory whose backup it is.
const upgradeNoteStatePrefix = "State directory: "

// removeOlderUpgradeBackups removes the backups in folder that an upgrade of
// the state in stateDir made, other than keep: real folders whose note names
// stateDir. Any other entry stays, including the backups of another state
// directory whose backup folder is a link to the same place. A removal
// failure is reported and does not stop the upgrade, because the new backup
// is complete.
func removeOlderUpgradeBackups(folder, keep, stateDir string, report func(string, ...any)) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		report("could not list %s to remove older upgrade backups: %v", folder, err)
		return
	}
	for _, entry := range entries {
		if entry.Name() == keep || !entry.IsDir() {
			continue
		}
		path := filepath.Join(folder, entry.Name())
		if !upgradeBackupOf(path, stateDir) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			report("could not remove the older upgrade backup %s: %v", path, err)
		}
	}
}

// upgradeBackupOf reports whether the folder backup holds a regular note
// whose last line names stateDir.
func upgradeBackupOf(backup, stateDir string) bool {
	path := filepath.Join(backup, upgradeNoteName)
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	return lines[len(lines)-1] == upgradeNoteStatePrefix+stateDir
}

// quoteForShell quotes a path for the shell that the owner is likely to
// use: a POSIX shell, or PowerShell on Windows.
func quoteForShell(word string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(word, "'", "''") + "'"
	}
	return service.ShellQuote(word)
}

// upgradeBackupCommand shows or changes whether OwnGit backs up the state
// before it upgrades its schema.
func upgradeBackupCommand(arguments []string) error {
	flags := flag.NewFlagSet("upgrade-backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	asJSON := flags.Bool("json", false, "print JSON")
	operands, err := parseFlagsAndOperands(flags, arguments)
	if err != nil {
		if errors.Is(err, errUsageShown) {
			return err
		}
		return jsonFailure(jsonRequested(arguments), "invalid_arguments", err)
	}
	if len(operands) > 1 || (len(operands) == 1 && operands[0] != "on" && operands[0] != "off") {
		return jsonFailure(*asJSON, "invalid_arguments", errors.New("upgrade-backup takes on, off or nothing"))
	}
	if err := state.RequireExisting(*stateDir); err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	held, err := state.OpenStateDirectory(*stateDir)
	if err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	defer held.Close()
	if len(operands) == 1 {
		if err := state.SetUpgradeBackup(held, operands[0] == "on"); err != nil {
			return jsonFailure(*asJSON, "state_unavailable", err)
		}
	}
	enabled, err := state.UpgradeBackupEnabled(held)
	if err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	folder := state.UpgradeBackupFolder(held.Name())
	if *asJSON {
		return writeJSONValue(struct {
			Enabled bool   `json:"enabled"`
			Folder  string `json:"folder"`
		}{enabled, folder})
	}
	if enabled {
		fmt.Printf("Before OwnGit upgrades this state to a newer schema, it backs it up into a new folder in %s and keeps the newest of these backups.\n", folder)
	} else {
		fmt.Println("Warning: OwnGit upgrades this state to a newer schema without backing it up first, so the earlier version cannot use it afterwards. Back it up with \"owngit backup\" before you install a newer version, or run \"owngit upgrade-backup on\".")
	}
	return nil
}
