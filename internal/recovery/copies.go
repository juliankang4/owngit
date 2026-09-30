package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
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
	// ManifestSHA256 is the SHA-256 of its manifest (CaptureReport).
	ManifestSHA256 string
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
	digest := sha256.New()
	content := io.TeeReader(file, digest)
	manifest, err := decodeManifest(content, info.Size(), manifestLimit)
	if err == nil {
		err = validateManifest(manifest)
	}
	if err == nil {
		_, err = io.Copy(io.Discard, content)
	}
	if err != nil {
		return nil, fmt.Errorf("read the manifest of %s: %w", name, err)
	}
	backup := &BackupCopy{folder: folder, name: name, dir: dir, CreatedAt: manifest.CreatedAt, ManifestSHA256: hex.EncodeToString(digest.Sum(nil)), bundles: map[string]string{}}
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
// name, which removes only an empty folder. Companions (isCompanion) go
// with their files. Anything else in it, such as a file added by hand,
// even one named like a bundle, refuses the removal before anything is
// removed. If another folder took the backup's name
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
	// Every entry is checked before anything is removed.
	var bundles, companions []string
	for _, entry := range entries {
		companion, err := isCompanion(c.dir, entry.Name(), entryExists(entries))
		if err != nil {
			return err
		}
		switch {
		case companion:
			companions = append(companions, entry.Name())
		case entry.Name() == manifestName && entry.Type().IsRegular():
		case entry.Name() == "repositories" && entry.IsDir():
			inner, err := readDirectory(c.dir, "repositories")
			if err != nil {
				return err
			}
			for _, bundle := range inner {
				name := path.Join("repositories", bundle.Name())
				companion, err := isCompanion(c.dir, name, entryExists(inner))
				if err != nil {
					return err
				}
				switch {
				case companion:
					companions = append(companions, name)
				case !bundle.Type().IsRegular() || !names[bundle.Name()]:
					return fmt.Errorf("%s holds %s, which its manifest does not name", c.name, name)
				default:
					bundles = append(bundles, name)
				}
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
	// A companion usually goes with its file; those left go before the
	// folders that hold them, the deepest first.
	slices.SortFunc(companions, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	for _, name := range append(companions, "repositories") {
		if err := c.dir.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := c.dir.Remove(manifestName); err != nil {
		return err
	}
	c.dir.Close()
	return c.folder.root.Remove(c.name)
}

// entryExists reports whether entries hold an entry of that name.
func entryExists(entries []fs.DirEntry) func(string) bool {
	return func(name string) bool {
		return slices.ContainsFunc(entries, func(entry fs.DirEntry) bool { return entry.Name() == name })
	}
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

// RenameLimitError refuses a folder that a restore would write into: its
// file system cannot rename a folder without replacing what is there.
type RenameLimitError struct {
	Dir        string
	FileSystem string
}

func (e *RenameLimitError) Error() string {
	return fmt.Sprintf("%s is on a file system (%s) that cannot rename a folder without replacing, which a restore needs; restore to a folder on another disk", e.Dir, e.FileSystem)
}

// RestoreLimit names the file system of dir when a restore could not write
// into a folder there (RenameLimitError), and is "" when it could. It
// renames a new empty folder in dir once to find out.
func RestoreLimit(dir string) (string, error) {
	err := requireExclusiveRename(dir)
	var limit *RenameLimitError
	if errors.As(err, &limit) {
		return limit.FileSystem, nil
	}
	return "", err
}

// ErrReplaced says that a backup's folder held another backup by the time
// it was read than the one that was opened.
var ErrReplaced = errors.New("the folder holds another backup than the one that was opened")

// Verify verifies the backup as Verify does, reading it only through the
// folder Open held, and only as the backup that was opened: its manifest
// must still have ManifestSHA256, and the manifest's digests bind every
// bundle to it. A manifest that changed or can no longer be read fails
// with ErrReplaced. Whether the folder's name still leads to this folder
// is for the caller to ask (BackupCopy.StillThere).
func (c *BackupCopy) Verify(ctx context.Context, temporary, gitPath string) (Verification, error) {
	input := &backupInput{path: filepath.Join(c.folder.area.Dir(), c.name), root: c.dir, borrowed: true}
	defer input.Close()
	operations := defaultRestoreOperations()
	operations.input, operations.manifestSHA256 = input, c.ManifestSHA256
	return verify(ctx, input.path, temporary, gitPath, operations)
}

// StillThere reports whether the backup's name in its folder still leads
// to the very folder Open held, not a link, another folder or nothing.
func (c *BackupCopy) StillThere() (bool, error) {
	named, err := c.folder.root.Lstat(c.name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	held, err := c.dir.Stat(".")
	if err != nil {
		return false, err
	}
	return named.IsDir() && os.SameFile(named, held), nil
}
