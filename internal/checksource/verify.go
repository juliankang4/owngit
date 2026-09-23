package checksource

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var errWorkspaceDirty = errors.New("materialized workspace changed")

// VerifyResult compares every tracked materialized file with its current
// private workspace. Generated untracked output is ignored, while a removed,
// replaced, resized, rehashed, or executable-mode-changed tracked entry is
// dirty. Operational read failures return an error so callers can report an
// unknown submitted worktree state.
func VerifyResult(ctx context.Context, result *Result) (bool, error) {
	if result == nil || result.Destination == "" || !filepath.IsAbs(result.Destination) {
		return false, errors.New("materialization result is unavailable")
	}
	files := make(map[string]FileRecord, len(result.Files))
	directories := map[string]bool{".": true}
	for _, record := range result.Files {
		files[filepath.FromSlash(record.Path)] = record
		for directory := filepath.Dir(filepath.FromSlash(record.Path)); directory != "."; directory = filepath.Dir(directory) {
			directories[directory] = true
		}
	}
	seen := make(map[string]bool, len(files))
	err := filepath.WalkDir(result.Destination, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(result.Destination, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			if !directories[relative] {
				return filepath.SkipDir
			}
			return nil
		}
		record, expected := files[relative]
		if !expected {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return errWorkspaceDirty
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() != record.Size {
			return errWorkspaceDirty
		}
		if !result.ExecutableBitsUnsupported && (info.Mode().Perm()&0o100 != 0) != record.Executable {
			return errWorkspaceDirty
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(io.LimitReader(file, record.Size+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if int64(len(content)) != record.Size {
			return errWorkspaceDirty
		}
		oid, err := blobObjectID(result.ObjectFormat, content)
		if err != nil {
			return err
		}
		if oid != record.OID {
			return errWorkspaceDirty
		}
		seen[relative] = true
		return nil
	})
	if errors.Is(err, errWorkspaceDirty) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(seen) == len(files), nil
}
