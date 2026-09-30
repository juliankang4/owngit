package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
)

// backupInput is the backup folder that a restore or a verification reads,
// held open from the start: its manifest and the bundles of its
// repositories folder are read through the held folders, never through a
// link at any level and never by looking the path up again, so renaming or
// replacing the folder meanwhile changes nothing that is read.
type backupInput struct {
	// path names the folder, for overlap checks and messages.
	path         string
	root         *os.Root
	repositories *os.Root
	// borrowed is true when root belongs to a BackupCopy, which closes it.
	borrowed bool
}

// openBackupInput holds the backup folder input, which must be a real
// folder.
func openBackupInput(input string) (*backupInput, error) {
	dir, err := checkedInputRoot(input)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open backup source: %w", err)
	}
	return &backupInput{path: dir, root: root}, nil
}

// Close releases the folders the input opened.
func (in *backupInput) Close() {
	if in.repositories != nil {
		in.repositories.Close()
	}
	if !in.borrowed {
		in.root.Close()
	}
}

// readManifest reads the manifest and returns the SHA-256 of the whole
// file with it.
func (in *backupInput) readManifest() (Manifest, string, error) {
	file, info, err := openRegular(in.root, manifestName)
	if err != nil {
		return Manifest{}, "", err
	}
	defer file.Close()
	return decodeManifestDigest(file, info.Size())
}

// openBundle opens the bundle of item, a regular file in the repositories
// folder, and returns it with its size when it was opened.
func (in *backupInput) openBundle(item RepositoryManifest) (*os.File, os.FileInfo, error) {
	folder, name := path.Split(item.Bundle)
	if folder != "repositories/" || name == "" {
		return nil, nil, fmt.Errorf("bundle %q is not in the repositories folder", item.Bundle)
	}
	if in.repositories == nil {
		repositories, err := openFolder(in.root, in.path, "repositories")
		if err != nil {
			return nil, nil, err
		}
		in.repositories = repositories
	}
	return openRegular(in.repositories, name)
}

// openRegular opens name in dir, which must be a regular file there and
// not a link, and returns it with what it was when opened.
func openRegular(dir *os.Root, name string) (*os.File, os.FileInfo, error) {
	named, err := dir.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !named.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s: %w", name, errNotRegular)
	}
	file, err := dir.Open(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err == nil && !os.SameFile(named, opened) {
		err = fmt.Errorf("%s: %w", name, errNotRegular)
	}
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, opened, nil
}

// errNotRegular refuses a link, a folder or another kind of entry where a
// file of a backup belongs.
var errNotRegular = errors.New("not a regular file")

// decodeManifestDigest reads a manifest of size bytes and returns the
// SHA-256 of all of it.
func decodeManifestDigest(file io.Reader, size int64) (Manifest, string, error) {
	digest := sha256.New()
	content := io.TeeReader(file, digest)
	manifest, err := decodeManifest(content, size, manifestLimit)
	if err == nil {
		_, err = io.Copy(io.Discard, content)
	}
	if err != nil {
		return Manifest{}, "", err
	}
	return manifest, hex.EncodeToString(digest.Sum(nil)), nil
}
