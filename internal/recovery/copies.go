package recovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"owngit/internal/state"
)

// BackupFolder is a folder of backups, held open, whose backups are opened
// and removed through handles, never through a path that could lead
// elsewhere by the time it is used.
type BackupFolder struct {
	area *state.Destination
	root *os.Root
}

// OpenBackupFolder holds the folder dir, checked like the parent of a
// backup (state.OpenStagingArea), and opens it.
func OpenBackupFolder(dir string) (*BackupFolder, error) {
	area, err := state.OpenStagingArea(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(area.Dir())
	if err != nil {
		area.Close()
		return nil, err
	}
	return &BackupFolder{area: area, root: root}, nil
}

// Close releases the folder.
func (f *BackupFolder) Close() {
	f.root.Close()
	f.area.Close()
}

// BackupCopy is one backup in a BackupFolder, held by the handle through
// which its manifest was read.
type BackupCopy struct {
	folder *BackupFolder
	name   string
	dir    *os.Root
	// CreatedAt is the instant the backup describes.
	CreatedAt time.Time
	// bundles are the files the manifest names in its repositories
	// folder, by repository ID.
	bundles map[string]string
}

// ErrNotABackup says that a folder is not a backup OwnGit wrote.
var ErrNotABackup = errors.New("not a backup that OwnGit wrote")

// Open opens the backup folder name, which must be a folder in f, not a
// link, and reads its manifest through the opened folder. A missing folder
// gives an error for which errors.Is(err, fs.ErrNotExist) holds.
func (f *BackupFolder) Open(name string) (*BackupCopy, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return nil, fmt.Errorf("%q is not a folder name", name)
	}
	named, err := f.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !named.IsDir() {
		return nil, fmt.Errorf("%s: %w: it is a link or a file", name, ErrNotABackup)
	}
	dir, err := f.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := dir.Stat(".")
	if err == nil && !os.SameFile(named, opened) {
		err = fmt.Errorf("%s changed while it was opened", name)
	}
	var backup *BackupCopy
	if err == nil {
		backup, err = readCopy(f, name, dir)
	}
	if err != nil {
		dir.Close()
		return nil, err
	}
	return backup, nil
}

func readCopy(folder *BackupFolder, name string, dir *os.Root) (*BackupCopy, error) {
	info, err := dir.Lstat(manifestName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w: it has no manifest", name, ErrNotABackup)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w: its manifest is not a regular file", name, ErrNotABackup)
	}
	file, err := dir.Open(manifestName)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	manifest, err := decodeManifest(file, info.Size(), manifestLimit)
	if err == nil {
		err = validateManifest(manifest)
	}
	if err != nil {
		return nil, fmt.Errorf("read the manifest of %s: %w", name, err)
	}
	backup := &BackupCopy{folder: folder, name: name, dir: dir, CreatedAt: manifest.CreatedAt, bundles: map[string]string{}}
	for _, item := range manifest.Repositories {
		if item.Bundle != "" {
			backup.bundles[item.ID] = path.Base(item.Bundle)
		}
	}
	return backup, nil
}

// Close releases the backup.
func (c *BackupCopy) Close() { c.dir.Close() }

// Size is the size of the bundles of the repositories that keep says still
// exist, as the backup holds them now.
func (c *BackupCopy) Size(keep func(id string) bool) (uint64, error) {
	var sizes []uint64
	for id, bundle := range c.bundles {
		if !keep(id) {
			continue
		}
		info, err := c.dir.Lstat(path.Join("repositories", bundle))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		sizes = append(sizes, uint64(info.Size()))
	}
	return sumSizes(sizes), nil
}

// Remove removes the backup: first, through the folder opened by Open, the
// bundles its manifest names and then its manifest, and last the folder by
// name, which removes only an empty folder. Anything else in it, such as a
// file added by hand, even one named like a bundle, refuses the removal
// before anything is removed. If another folder took the backup's name
// meanwhile, what it holds is not touched and the removal fails.
func (c *BackupCopy) Remove() error {
	names := map[string]bool{}
	for _, bundle := range c.bundles {
		names[bundle] = true
	}
	entries, err := readDirectory(c.dir, ".")
	if err != nil {
		return err
	}
	var bundles []string
	for _, entry := range entries {
		switch {
		case entry.Name() == manifestName && entry.Type().IsRegular():
		case entry.Name() == "repositories" && entry.IsDir():
			inner, err := readDirectory(c.dir, "repositories")
			if err != nil {
				return err
			}
			for _, bundle := range inner {
				if !bundle.Type().IsRegular() || !names[bundle.Name()] {
					return fmt.Errorf("%s holds %s, which its manifest does not name", c.name, path.Join("repositories", bundle.Name()))
				}
				bundles = append(bundles, path.Join("repositories", bundle.Name()))
			}
		default:
			return fmt.Errorf("%s holds %s, which a backup does not write", c.name, entry.Name())
		}
	}
	for _, bundle := range bundles {
		if err := c.dir.Remove(bundle); err != nil {
			return err
		}
	}
	if err := c.dir.Remove("repositories"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := c.dir.Remove(manifestName); err != nil {
		return err
	}
	c.dir.Close()
	return c.folder.root.Remove(c.name)
}

func readDirectory(root *os.Root, name string) ([]fs.DirEntry, error) {
	dir, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

// CheckRoom refuses a new backup in f when its file system has less room
// than needed, the size of an earlier backup there.
func (f *BackupFolder) CheckRoom(needed uint64) error {
	free, known, err := diskFreeSpace(f.area.Dir())
	if err != nil {
		return fmt.Errorf("read the free space in %s: %w", f.area.Dir(), err)
	}
	if known && free < needed {
		return &SpaceError{Dir: f.area.Dir(), Needed: needed, Free: free, FromLastBackup: true}
	}
	return nil
}
