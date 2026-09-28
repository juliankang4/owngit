//go:build !windows

package markdown

import (
	"syscall"
	"time"
)

// processCPU is the processor time this process has used. Timing checks use
// it rather than the wall clock, which a busy machine stretches.
func processCPU() time.Duration {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}
