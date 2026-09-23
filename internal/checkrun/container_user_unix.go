//go:build !windows

package checkrun

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func containerUserForWorkspace(workspace string) (string, error) {
	uid, gid := os.Geteuid(), os.Getegid()
	if uid != 0 {
		return fmt.Sprintf("%d:%d", uid, gid), nil
	}
	// A root service still runs repository commands as a fixed unprivileged
	// container identity. Root can transfer only this newly materialized,
	// job-owned workspace without granting broader host access.
	const containerID = 65532
	if err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Lchown(path, containerID, containerID)
	}); err != nil {
		return "", fmt.Errorf("assign private workspace to nonroot container identity: %w", err)
	}
	return "65532:65532", nil
}
