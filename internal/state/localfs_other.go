//go:build !darwin && !linux && !windows

package state

import "os"

func ensureLocalStateFilesystem(string) error { return nil }

// ownershipEnforced is false where OwnGit cannot tell a local filesystem
// from a network one.
func ownershipEnforced(*os.File) (bool, error) { return false, nil }
