package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Upgrade is the schema upgrade that OpenIn is about to apply to an existing
// database. OpenIn passes it to the caller's beforeUpgrade function after
// inspecting the state and before changing anything in the state directory,
// so the caller can keep a copy of the state as it is. A new or empty
// database, and one at the current schema, have no upgrade.
type Upgrade struct {
	// From is the schema version of the database, or 0 for the committed
	// baseline, which has no version. To is the schema this build writes.
	From, To int
	in       *inspection
}

// BeforeUpgrade runs before OpenIn upgrades the schema of an existing
// database. An error stops OpenIn before it changes anything, so the
// database stays at its schema, usable by the build that wrote it.
type BeforeUpgrade func(ctx context.Context, upgrade *Upgrade) error

// Describe names the upgrade for messages, for example "from schema 14 to
// 15".
func (upgrade *Upgrade) Describe() string {
	if upgrade.From == 0 {
		return fmt.Sprintf("from the committed baseline (no schema version) to schema %d", upgrade.To)
	}
	return fmt.Sprintf("from schema %d to %d", upgrade.From, upgrade.To)
}

// OpenCopy writes the database, exactly as OpenIn inspected it, with its
// write-ahead log, into dir, an empty directory that OpenStateDirectory
// accepts, and opens the copy. Opening upgrades the copy; the state itself
// is not changed. A state that changed since the inspection is reported as
// ErrInspectionUnstable. It may be called only while beforeUpgrade runs.
func (upgrade *Upgrade) OpenCopy(ctx context.Context, dir string) (*Store, error) {
	held, err := OpenStateDirectory(dir)
	if err != nil {
		return nil, err
	}
	defer held.Close()
	in := upgrade.in
	target := filepath.Join(held.Name(), databaseName)
	for _, item := range []struct {
		object *sourceObject
		hash   []byte
		target string
	}{{in.main, in.mainHash, target}, {in.wal, in.walHash, target + walSuffix}} {
		if item.object == nil {
			continue
		}
		hash, err := copyPrivate(ctx, item.object, item.target)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(hash, item.hash) {
			return nil, unstable("%s content changed", filepath.Base(item.object.path))
		}
	}
	if err := in.validateSource(true, true); err != nil {
		return nil, err
	}
	return OpenIn(ctx, held, nil)
}

// upgradeBackupOffName is the file in the state directory that turns off
// the backup before a schema upgrade (see UpgradeBackupEnabled).
const upgradeBackupOffName = "no-upgrade-backup"

// UpgradeBackupEnabled reports whether OwnGit backs up the state in the held
// state directory before it upgrades its schema. The setting belongs to this
// computer: it is a file in the state directory, not a database record, so
// it can be read and changed before the database is upgraded, and backups do
// not carry it.
func UpgradeBackupEnabled(held *os.File) (bool, error) {
	file, err := OpenOwnFile(held, upgradeBackupOffName, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the upgrade backup setting: %w", err)
	}
	return false, file.Close()
}

// SetUpgradeBackup turns the backup before a schema upgrade on or off for
// the state in the held state directory.
func SetUpgradeBackup(held *os.File, enabled bool) error {
	if enabled {
		err := removeOwnFile(held, upgradeBackupOffName)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("turn on the upgrade backup: %w", err)
		}
		return nil
	}
	file, err := OpenOwnFile(held, upgradeBackupOffName, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return fmt.Errorf("turn off the upgrade backup: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		file.Close()
		return fmt.Errorf("turn off the upgrade backup: %w", err)
	}
	_, err = file.WriteString("OwnGit upgrades this state without backing it up first. Run \"owngit upgrade-backup on\" to back it up before each upgrade again.\n")
	return errors.Join(err, file.Close())
}

// UpgradeBackupFolder is the folder that holds the backups made before
// schema upgrades of the state directory stateDir: a folder beside it, named
// after it, for example owngit-backups beside owngit.
func UpgradeBackupFolder(stateDir string) string {
	return filepath.Join(filepath.Dir(stateDir), filepath.Base(stateDir)+"-backups")
}

// OpenUpgradeBackupFolder opens the UpgradeBackupFolder of stateDir,
// creating it when it is missing, and holds it until release is called.
//
// The folder follows the rules of a state directory (CreateDirectory: on a
// local disk, this account's, reached through no link that was not
// checked), and it must be private as well: one in which no other account
// can create, rename or remove names. A folder that this call creates is
// private from the moment it exists (CreateDirectory), and is then made
// private as well, which on macOS drops access list entries inherited from
// its parent. An existing folder that is not private is refused, not
// repaired, because another account may already have put something in it.
// On Windows the way to the folder and the folder itself are held without
// delete sharing until release (holdWay), as OpenIn holds the way to the
// state; on Unix no other account can change that way (CreateDirectory).
// So until release, a path name inside the folder leads to what this
// account put there, and the callers work inside it by path.
func OpenUpgradeBackupFolder(stateDir string) (folder *os.File, release func(), err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w; the backups before upgrades need a folder that only this account can change: make it so, or move it away so that OwnGit creates one", err)
		}
	}()
	path := UpgradeBackupFolder(stateDir)
	_, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	held, err := CreateDirectory(path)
	if err != nil {
		return nil, nil, err
	}
	unhold, err := holdWay(held)
	if err != nil {
		held.Close()
		return nil, nil, err
	}
	release = func() {
		unhold()
		held.Close()
	}
	if created {
		// By path, which leads to the held folder (see above): on Linux
		// the walk holds it with O_PATH, which cannot change its mode.
		err = ProtectPrivatePath(held.Name(), true)
	}
	if err == nil && runtime.GOOS == "windows" {
		// Unix CreateDirectory already checks writers; Windows needs the ACL check here.
		err = requirePrivateFolder(held)
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return held, release, nil
}
