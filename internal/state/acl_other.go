//go:build !windows && !linux && !darwin

package state

import "os"

// Access lists are not examined on other Unix systems; mode bits and owners
// still are.
func accessListFix(string, os.FileInfo) (string, error) { return "", nil }
