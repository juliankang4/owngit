package state

import (
	"os"
	"path/filepath"
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
	d.stage = stage
	return path, nil
}

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
