//go:build !windows && !linux && !darwin

package checksource

import "os"

// Access lists are not examined on other Unix systems; mode bits and owners
// still are.
func ancestorACLFix(string, os.FileInfo) (string, error) { return "", nil }

func rootACLFix(string) (string, error) { return "", nil }
