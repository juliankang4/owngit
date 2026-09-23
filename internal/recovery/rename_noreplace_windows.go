//go:build windows

package recovery

import (
	"context"

	"owngit/internal/publishdir"
)

// renameNoReplace publishes a staged directory. publishdir.Rename refuses any
// existing destination on Windows and retries a transient hold by another
// process.
func renameNoReplace(oldPath, newPath string) error {
	return publishdir.Rename(context.Background(), oldPath, newPath)
}
