package recovery

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

// A backup archive is a tar stream of one backup as its folder holds it:
// NAME/, NAME/manifest.json, NAME/repositories/ and the bundles the
// manifest names in it, and nothing else. Unpacking it gives the backup
// folder NAME that owngit backup verify and owngit restore read.

// WriteArchive writes the backup as a tar stream into writer, with its
// folder name as the top folder: the manifest first, then every bundle the
// manifest names. Every entry is read through the folders Open held, and
// no link is followed at any level: the repositories folder is opened as
// a folder that is not a link, and each file in it as a regular file. It
// stops when ctx ends. Anything else in the folder is left out; a bundle
// that is missing, not a regular file, or that changes size while it is
// written fails the archive, and so does a manifest that is not the one
// Open read (ManifestSHA256). A failed archive has no end, and the caller
// must not pass what it wrote as a whole one.
func (c *BackupCopy) WriteArchive(ctx context.Context, writer io.Writer) error {
	archive := tar.NewWriter(writer)
	modified := c.CreatedAt.UTC().Truncate(time.Second)
	directory := func(name string) error {
		return archive.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o700, ModTime: modified, Format: tar.FormatPAX})
	}
	if err := directory(c.name); err != nil {
		return err
	}
	digest := sha256.New()
	if err := c.archiveFile(ctx, archive, c.dir, manifestName, manifestName, modified, digest); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != c.ManifestSHA256 {
		return fmt.Errorf("the manifest of %s changed after it was opened", c.name)
	}
	if err := directory(path.Join(c.name, "repositories")); err != nil {
		return err
	}
	bundles := make([]string, 0, len(c.bundles))
	for _, bundle := range c.bundles {
		bundles = append(bundles, bundle)
	}
	slices.Sort(bundles)
	if len(bundles) > 0 {
		repositories, err := openFolder(c.dir, c.name, "repositories")
		if err != nil {
			return err
		}
		defer repositories.Close()
		for _, bundle := range bundles {
			if err := c.archiveFile(ctx, archive, repositories, bundle, path.Join("repositories", bundle), modified, io.Discard); err != nil {
				return err
			}
		}
	}
	return archive.Close()
}

// openFolder opens the folder name in dir, which must be a folder there
// and not a link, and holds it; backup names the backup for errors.
func openFolder(dir *os.Root, backup, name string) (*os.Root, error) {
	named, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !named.IsDir() {
		return nil, fmt.Errorf("%s in %s is a link or a file", name, backup)
	}
	folder, err := dir.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if opened, err := folder.Stat("."); err != nil || !os.SameFile(named, opened) {
		folder.Close()
		if err == nil {
			err = fmt.Errorf("%s in %s changed while it was opened", name, backup)
		}
		return nil, err
	}
	return folder, nil
}

// archiveFile writes the regular file name of dir into archive as the
// entry entry of the backup, and what it wrote into also.
func (c *BackupCopy) archiveFile(ctx context.Context, archive *tar.Writer, dir *os.Root, name, entry string, modified time.Time, also io.Writer) error {
	file, opened, err := openRegular(dir, name)
	if err != nil {
		return fmt.Errorf("%s in %s: %w", entry, c.name, err)
	}
	defer file.Close()
	size := opened.Size()
	if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: path.Join(c.name, entry), Size: size, Mode: 0o600, ModTime: modified, Format: tar.FormatPAX}); err != nil {
		return err
	}
	copied, err := io.CopyN(io.MultiWriter(archive, also), contextReader{ctx, file}, size)
	if err != nil {
		if copied < size && errors.Is(err, io.EOF) {
			return fmt.Errorf("%s in %s became shorter while it was written", entry, c.name)
		}
		return err
	}
	return nil
}

// ErrNotABackupArchive says that an archive is not one backup as
// WriteArchive writes it; the error that wraps it says why.
var ErrNotABackupArchive = errors.New("not a backup archive")

// UnpackArchive unpacks the backup archive read from reader into the empty
// folder dir and returns the name of the backup folder it made there. It
// accepts only the entries WriteArchive writes, under one top folder whose
// name holds only ASCII letters, digits, '.', '_' and '-': the folders,
// manifest.json and repositories/NAME.bundle files. An absolute
// path, a ".." or "." element, a link, any other kind of entry or file, and
// a second entry of the same name refuse the archive, as does one that ends
// early or holds no manifest. Every file is created new inside dir, never
// through a link. It stops when ctx ends. What it wrote is left for the
// caller to remove.
func UnpackArchive(ctx context.Context, reader io.Reader, dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	refuse := func(format string, arguments ...any) (string, error) {
		return "", fmt.Errorf("%w: %s", ErrNotABackupArchive, fmt.Sprintf(format, arguments...))
	}
	archive := tar.NewReader(contextReader{ctx, reader})
	top, seen, manifest := "", map[string]bool{}, false
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return refuse("it ends early")
			}
			return refuse("%v", err)
		}
		name := strings.TrimSuffix(header.Name, "/")
		elements := strings.Split(name, "/")
		if name == "" || strings.ContainsAny(name, `\:`) || path.IsAbs(header.Name) || path.Clean(name) != name || slices.Contains(elements, "..") || slices.Contains(elements, ".") {
			return refuse("it holds the path %q", header.Name)
		}
		if top == "" {
			top = elements[0]
			// The folder name reaches the restore command shown for this
			// backup, so it holds only what OwnGit's own names hold.
			if strings.Trim(top, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
				return refuse("its folder name %q holds characters other than letters, digits, '.', '_' and '-'", top)
			}
		}
		if elements[0] != top {
			return refuse("it holds more than one folder: %q and %q", top, elements[0])
		}
		if seen[name] {
			return refuse("it holds %q twice", header.Name)
		}
		seen[name] = true
		inner := path.Join(elements[1:]...)
		switch header.Typeflag {
		case tar.TypeDir:
			if inner != "" && inner != "repositories" {
				return refuse("it holds the folder %q, which a backup does not have", header.Name)
			}
			if err := root.Mkdir(name, 0o700); err != nil {
				return "", err
			}
			continue
		case tar.TypeReg:
		default:
			return refuse("%q is a link or another entry that is not a file or folder", header.Name)
		}
		bundle, inRepositories := strings.CutPrefix(inner, "repositories/")
		switch {
		case inner == manifestName:
			manifest = true
		case inRepositories && !strings.Contains(bundle, "/") && strings.HasSuffix(bundle, ".bundle") && len(bundle) > len(".bundle"):
		default:
			return refuse("it holds the file %q, which a backup does not have", header.Name)
		}
		if header.Size < 0 {
			return refuse("%q has a negative size", header.Name)
		}
		if err := unpackFile(root, name, archive, header.Size); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return refuse("it ends early")
			}
			return "", err
		}
	}
	if !manifest {
		return refuse("it holds no manifest.json")
	}
	return top, nil
}

// unpackFile creates the new file name in root with the next size bytes of
// archive. Its folders exist once their entries came first; a file whose
// folder did not come first is refused by the missing folder.
func unpackFile(root *os.Root, name string, archive io.Reader, size int64) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %q comes before its folder", ErrNotABackupArchive, name)
	}
	if err != nil {
		return err
	}
	_, err = io.CopyN(file, archive, size)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
