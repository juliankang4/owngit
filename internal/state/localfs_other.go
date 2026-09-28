//go:build !darwin && !linux && !windows

package state

func ensureLocalStateFilesystem(string) error { return nil }

// ownershipEnforcedHere is false where OwnGit cannot tell a local filesystem
// from a network one.
func ownershipEnforcedHere(string) (bool, error) { return false, nil }
