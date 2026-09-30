package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/state"
)

// BackupHeader is what the start of a backup's manifest says: that OwnGit
// wrote it, and when its instant was.
type BackupHeader struct {
	Version   int
	CreatedAt time.Time
}

// ReadBackupHeader reads the format, version and creation time that start
// the manifest of the backup folder dir, without reading the rest. It
// follows no link.
func ReadBackupHeader(dir string) (BackupHeader, error) {
	manifestPath := filepath.Join(dir, manifestName)
	if err := requireRegularFile(manifestPath); err != nil {
		return BackupHeader{}, err
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return BackupHeader{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var header BackupHeader
	var format string
	targets := []struct {
		name  string
		value any
	}{{"format", &format}, {"version", &header.Version}, {"created_at", &header.CreatedAt}}
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return BackupHeader{}, errManifestStart
	}
	for _, target := range targets {
		if token, err := decoder.Token(); err != nil || token != target.name {
			return BackupHeader{}, errManifestStart
		}
		if err := decoder.Decode(target.value); err != nil {
			return BackupHeader{}, fmt.Errorf("decode backup manifest: %w", err)
		}
	}
	if format != backupFormat {
		return BackupHeader{}, fmt.Errorf("unsupported backup format %q", format)
	}
	return header, nil
}

// bundleSuffix ends the name of every file in a backup's repositories
// folder.
const bundleSuffix = ".bundle"

// RemoveBackup removes the backup folder dir, which must hold only what a
// backup writes: its manifest and a repositories folder of bundles. The
// folder that holds dir is held meanwhile (state.OpenStagingArea), and dir
// must be a folder of this account, not a link. Anything else in dir
// refuses the removal before anything is removed. Bundles go first and the
// manifest last, so a removal that stops halfway leaves a folder that
// ReadBackupHeader still recognizes.
func RemoveBackup(dir string) error {
	area, err := state.OpenStagingArea(filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer area.Close()
	dir = filepath.Join(area.Dir(), filepath.Base(dir))
	folder, err := state.OpenDirectory(dir, false)
	if err != nil {
		return err
	}
	folder.Close()
	if _, err := ReadBackupHeader(dir); err != nil {
		return err
	}
	bundles := filepath.Join(dir, "repositories")
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch {
		case entry.Name() == manifestName && entry.Type().IsRegular():
		case entry.Name() == "repositories" && entry.IsDir():
			inner, err := os.ReadDir(bundles)
			if err != nil {
				return err
			}
			for _, bundle := range inner {
				if !bundle.Type().IsRegular() || !strings.HasSuffix(bundle.Name(), bundleSuffix) {
					return fmt.Errorf("%s holds %s, which a backup does not write", dir, filepath.Join("repositories", bundle.Name()))
				}
				files = append(files, filepath.Join(bundles, bundle.Name()))
			}
		default:
			return fmt.Errorf("%s holds %s, which a backup does not write", dir, entry.Name())
		}
	}
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			return err
		}
	}
	if err := os.Remove(bundles); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(dir, manifestName)); err != nil {
		return err
	}
	return os.Remove(dir)
}

// CheckBackupRoom refuses a new backup in the folder parent when its file
// system has less room than the backup previous, another backup there, took
// for the repositories that keep says still exist. The new backup is made
// before an older one may be removed, so it needs that room besides.
func CheckBackupRoom(parent, previous string, keep func(id string) bool) error {
	entries, err := os.ReadDir(filepath.Join(previous, "repositories"))
	if err != nil {
		return err
	}
	var sizes []uint64
	for _, entry := range entries {
		id, isBundle := strings.CutSuffix(entry.Name(), bundleSuffix)
		if !isBundle || !entry.Type().IsRegular() || !keep(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		sizes = append(sizes, uint64(info.Size()))
	}
	needed := sumSizes(sizes)
	free, known, err := diskFreeSpace(parent)
	if err != nil {
		return fmt.Errorf("read the free space in %s: %w", parent, err)
	}
	if known && free < needed {
		return &SpaceError{Dir: parent, Needed: needed, Free: free, FromLastBackup: true}
	}
	return nil
}
