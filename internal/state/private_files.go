package state

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"owngit/internal/statepath"
)

func readPrivateBytes(ctx context.Context, path string, maximum int64, limitError string, open func(string) (*os.File, error)) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	file, err := open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(content)) > maximum {
		return nil, false, errors.New(limitError)
	}
	return content, true, nil
}

func writePrivateBytesLocked(ctx context.Context, path, repositoryID, operation string, content []byte, description string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	failure := func(action, object string, err error) error {
		if description == "" {
			return err
		}
		return fmt.Errorf("%s %s %s: %w", action, description, object, err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return failure("create", "directory", err)
	}
	if err := ProtectPrivatePath(directory, true); err != nil {
		return failure("protect", "directory", err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(directory, statepath.CredentialTemporary(repositoryID, operation, suffix))
	file, err := CreatePrivateFile(temporary)
	if err != nil {
		return failure("create", "file", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(content); err != nil {
		file.Close()
		return failure("write", "file", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return failure("sync", "file", err)
	}
	if err := file.Close(); err != nil {
		return failure("close", "file", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return failure("publish", "file", err)
	}
	removeTemporary = false
	if err := syncPrivateDirectory(directory); err != nil {
		return failure("sync", "directory", err)
	}
	return nil
}

func removePrivateFileLocked(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := syncPrivateDirectory(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
