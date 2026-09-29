package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Destination is a folder that does not exist yet and that OwnGit makes
// in two steps, as a backup makes its output and a restore makes the state
// directory and the repository folder: it fills a stage, a new folder
// beside the destination, and then renames the stage to the destination.
//
// The parent is held until Close, and no other account can rename or
// replace the parent, a folder on the way to it or a stage in it. On Unix
// the parent is checked like every folder on the way to a state directory
// (OpenDirectory): no other account can change it, although a sticky
// folder such as /tmp is accepted, because others cannot rename or remove
// what this account puts in it. On Windows the way to the parent is held
// without delete sharing (holdWay), and the stage is held the same way
// until ReleaseStage. A stage is created under a new name, private to this
// account from the moment it exists; a name that another account took
// first makes the creation fail. So a path name inside the stage leads to
// what this account put there, and callers, and Git, which takes path
// names, work inside it by path.
type Destination struct {
	// Path is the destination, named in its parent as the walk to the
	// parent names that.
	Path   string
	parent *os.File
	unhold func()
	stage  *os.File
	// stageName names the stage in the parent until RemoveStage.
	stageName string
}

// OpenDestination checks the parent of the folder path and holds it,
// creating it and its missing parents private to this account, as
// CreateDirectory creates a state directory's. The parent may be on a
// network share, except when OwnGit runs as root or as an elevated
// administrator on Windows, as for OpenDirectory.
func OpenDestination(path string) (*Destination, error) {
	return openDestination(path, false)
}

// OpenStateDestination is OpenDestination for a state directory: the
// parent and the way to it must be on local disks, as for
// OpenStateDirectory.
func OpenStateDestination(path string) (*Destination, error) {
	return openDestination(path, true)
}

// OpenStagingArea holds the folder dir, checked and created like the
// parent of a Destination, for stages that are removed and never renamed,
// such as the rehearsal of a restore. Path is empty.
func OpenStagingArea(dir string) (*Destination, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// openDestinationParent takes a path in the folder it opens.
	parent, unhold, err := openDestinationParent(filepath.Join(absolute, "stage"), false)
	if err != nil {
		return nil, err
	}
	return &Destination{parent: parent, unhold: unhold}, nil
}

func openDestination(path string, local bool) (*Destination, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, unhold, err := openDestinationParent(absolute, local)
	if err != nil {
		return nil, err
	}
	return &Destination{Path: filepath.Join(parent.Name(), filepath.Base(absolute)), parent: parent, unhold: unhold}, nil
}

// CreateStage creates the folder name in the held parent, which must not
// exist, private to this account, holds it until ReleaseStage and returns
// its path. A Destination has one stage at a time.
func (d *Destination) CreateStage(name string) (string, error) {
	stage, err := createStage(d.parent, name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(d.parent.Name(), name)
	// By path, which leads to the held stage (see Destination): on Linux
	// the stage is held with O_PATH, which cannot change its mode. On macOS
	// this drops access list entries inherited from the parent.
	if err := ProtectPrivatePath(path, true); err != nil {
		stage.Close()
		_ = os.Remove(path)
		return "", err
	}
	d.stage, d.stageName = stage, name
	return path, nil
}

// Stages lists the entries of the held parent whose names start with
// prefix and that are folders of this account, not links: stages that an
// earlier run left when it stopped, or that another run still uses.
func (d *Destination) Stages(prefix string) ([]string, error) {
	entries, err := os.ReadDir(d.parent.Name())
	if err != nil {
		return nil, err
	}
	var stages []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) || !entry.IsDir() || entry.Name() == d.stageName {
			continue
		}
		path := filepath.Join(d.parent.Name(), entry.Name())
		folder, err := OpenDirectory(path, false)
		if err != nil {
			continue
		}
		folder.Close()
		stages = append(stages, path)
	}
	return stages, nil
}

// RemoveStage removes the stage: first what it holds, while the stage is
// still held, so on Windows no account can rename it and put another
// folder in its place meanwhile, then, released, the empty stage.
func (d *Destination) RemoveStage() error {
	if d.stageName == "" {
		return nil
	}
	path := filepath.Join(d.parent.Name(), d.stageName)
	entries, err := os.ReadDir(path)
	for _, entry := range entries {
		err = errors.Join(err, os.RemoveAll(filepath.Join(path, entry.Name())))
	}
	d.ReleaseStage()
	d.stageName = ""
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Dir is the held parent.
func (d *Destination) Dir() string { return d.parent.Name() }

// ReleaseStage stops holding the stage, which renaming or removing it needs
// on Windows. The parent stays held.
func (d *Destination) ReleaseStage() {
	if d.stage != nil {
		d.stage.Close()
		d.stage = nil
	}
}

// Close releases the stage and the parent.
func (d *Destination) Close() {
	d.ReleaseStage()
	d.unhold()
	d.parent.Close()
}
