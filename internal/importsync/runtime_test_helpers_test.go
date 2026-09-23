package importsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// induceRuntimeMarkerMismatch keeps the Unix rename stimulus when the platform
// permits it. Windows sharing protection is not treated as identity loss, so
// that branch writes a different valid generation through the owning handle.
func induceRuntimeMarkerMismatch(service *Service, suffix string) (func() error, error) {
	root, prepared := service.preparedRuntime()
	if !prepared {
		return nil, errors.New("runtime is not prepared")
	}
	preservedPath := root.markerPath + suffix
	if err := os.Rename(root.markerPath, preservedPath); err == nil {
		return func() error { return os.Rename(preservedPath, root.markerPath) }, nil
	} else if !runtimeSharingViolation(err) {
		return nil, fmt.Errorf("rename runtime marker: %w", err)
	}

	original, err := readRuntimeMarkerFile(root.markerFile)
	if err != nil {
		return nil, fmt.Errorf("read owning runtime marker: %w", err)
	}
	changed := original
	changed.LockGeneration = strings.Repeat("a", 32)
	if changed.LockGeneration == original.LockGeneration {
		changed.LockGeneration = strings.Repeat("b", 32)
	}
	if err := writeRuntimeMarkerHandleForTest(root.markerFile, changed); err != nil {
		return nil, fmt.Errorf("change owning runtime marker generation: %w", err)
	}
	return func() error {
		return writeRuntimeMarkerHandleForTest(root.markerFile, original)
	}, nil
}

func writeRuntimeMarkerHandleForTest(file *os.File, marker runtimeMarker) error {
	content, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	content = append(content, '\n')
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != int64(len(content)) {
		return fmt.Errorf("runtime marker size is %d, replacement is %d", info.Size(), len(content))
	}
	if _, err := file.WriteAt(content, 0); err != nil {
		return err
	}
	return file.Sync()
}

func assertRuntimeStillOwned(service *Service) error {
	root, prepared := service.preparedRuntime()
	if !prepared {
		return errors.New("runtime is not prepared")
	}
	owned, err := root.stillOwned()
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("runtime is not still owned")
	}
	return nil
}

func runtimeLockPath(service *Service) string {
	return filepath.Join(service.stagingRootPath(), runtimeRootLockName)
}
