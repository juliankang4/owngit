//go:build !darwin

package state

import "os"

// Only macOS replaces log files and examines access lists here.
func lockFolder(*os.File, string) (bool, string) { return true, "" }

func accessListPermits(*os.File) (bool, error) { return false, nil }

func clearFolderAccessList(*os.File) error { return nil }

func replaceWithPrivateCopy(*os.File, string) error { return nil }

func createLogFile(dir *os.File, name string) (*os.File, error) {
	return OpenOwnFile(dir, name, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
}

func sharedFolderNote(string) string { return "" }
