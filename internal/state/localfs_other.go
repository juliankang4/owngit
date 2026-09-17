//go:build !darwin && !linux && !windows

package state

func ensureLocalStateFilesystem(string) error { return nil }
