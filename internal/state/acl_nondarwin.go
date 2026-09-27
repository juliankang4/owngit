//go:build !darwin && !windows

package state

import "os"

func ChangeAccessListFix(string) (string, error) { return "", nil }

func clearAccessList(*os.File) error { return nil }
