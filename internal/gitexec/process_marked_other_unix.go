//go:build !windows && !linux

package gitexec

import "os/exec"

// Only the process group contains an owned run here. macOS does not show the
// environment of system binaries, so a mark could not find their descendants.
func markOwnedRun(*exec.Cmd)                   {}
func ownedRunToken(*exec.Cmd) string           { return "" }
func processStartTime(int) uint64              { return 0 }
func killMarkedProcesses(string, uint64) error { return nil }
