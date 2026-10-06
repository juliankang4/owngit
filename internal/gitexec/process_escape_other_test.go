//go:build !linux

package gitexec

// runSessionFixture has no mode here: only Linux ends a descendant that left
// the process group (Windows jobs hold every descendant).
func runSessionFixture(string) bool { return false }
