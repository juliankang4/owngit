package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

var ErrOlderSchema = errors.New("this state has an older schema")

// OpenObserved reads an existing current-schema state without changing its
// files or permissions. Queries use a private snapshot, including committed
// WAL records. Other writers, unknown schemas and incomplete restores refuse.
func OpenObserved(ctx context.Context, dir string) (result *Store, err error) {
	held, err := OpenStateDirectory(dir)
	if err != nil {
		return nil, err
	}
	defer held.Close()
	release, err := holdWay(held)
	if err != nil {
		return nil, err
	}
	var inspected *inspection
	defer func() {
		if result == nil {
			if inspected != nil {
				err = errors.Join(err, inspected.release())
			}
			release()
		}
	}()
	if err := requireCompleteRestore(held.Name()); err != nil {
		return nil, err
	}
	inspected, err = inspectStateFor(ctx, held, inspectForReader)
	if err != nil {
		return nil, err
	}
	if inspected.class == schemaEmpty {
		return nil, ErrNotExist
	}
	if inspected.class != schemaCurrent {
		upgrade := &Upgrade{From: inspected.class.version, To: currentSchemaVersion()}
		return nil, fmt.Errorf("%w (%s); start or restart OwnGit to back up and upgrade it, then run this command again", ErrOlderSchema, upgrade.Describe())
	}
	var changes []ProtectionChange
	inspect := func(file *os.File, directory bool) error {
		change, err := inspectObjectProtection(held.Name(), file, directory)
		if err == nil && change != nil {
			changes = append(changes, *change)
		}
		return err
	}
	if err := inspect(inspected.dir.handle, true); err != nil {
		return nil, err
	}
	for _, object := range []*sourceObject{inspected.main, inspected.wal, inspected.shm} {
		if object != nil {
			if err := inspect(object.handle, false); err != nil {
				return nil, err
			}
		}
	}
	if err := walkManagedState(inspected.dir.handle, "", inspect); err != nil {
		return nil, err
	}
	if err := inspected.validateSource(true, true); err != nil {
		return nil, err
	}
	closeObservation := sync.OnceValue(func() error {
		err := inspected.release()
		release()
		return err
	})
	return &Store{db: inspected.queryDB, dir: held.Name(), database: inspected.main.info,
		observationDir: inspected.dir.handle, releaseObservation: closeObservation, protectionChanges: changes}, nil
}

func requireCompleteRestore(dir string) error {
	if _, err := os.Lstat(filepath.Join(dir, IncompleteRestoreMarkerName)); err == nil {
		return errors.New("state directory belongs to an incomplete offline restore; follow the interrupted-restore procedure before use")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect incomplete restore marker: %w", err)
	}
	return nil
}

func (s *Store) StateProtectionChanges() []ProtectionChange {
	return slices.Clone(s.protectionChanges)
}

func inspectObjectProtection(root string, file *os.File, directory bool) (*ProtectionChange, error) {
	if err := requireManagedObject(file, directory); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	changeable, err := stateFileChangeable(file, info)
	if err != nil || changeable {
		if err == nil {
			err = errors.New("another account can change this managed state entry; starting OwnGit makes it private")
		}
		return nil, stateProtectionError(file.Name(), directory, err)
	}
	needed, err := stateProtectionNeeded(file, directory)
	if err != nil || !needed {
		return nil, err
	}
	before, err := heldProtectionFingerprint(file)
	if err != nil {
		return nil, err
	}
	path, err := filepath.Rel(root, file.Name())
	if err != nil {
		return nil, err
	}
	return &ProtectionChange{Path: path, Before: before}, nil
}

func (s *Store) runningObservationLock(name string) (func(), error) {
	if s.observationDir == nil {
		return AcquireExclusiveFileLock(filepath.Join(s.dir, name))
	}
	file, err := OpenOwnFile(s.observationDir, name, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	_, release, err := acquireExclusiveFileLock(file)
	return release, err
}
