//go:build !windows

package publishdir

import (
	"context"
	"os"
)

// Rename is os.Rename on this platform. The context is unused.
func Rename(_ context.Context, oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
