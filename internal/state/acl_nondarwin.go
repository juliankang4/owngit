//go:build !darwin && !windows

package state

import "os"

func ChangeAccessListFix(string) (string, error) { return "", nil }

func validatePrivateInputAccessList(*os.File, os.FileInfo) error { return nil }

func clearAccessList(*os.File) error { return nil }

func privateAccessListFingerprint(*os.File) (string, error) { return "", nil }
