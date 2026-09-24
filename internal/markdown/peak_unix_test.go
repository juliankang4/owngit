//go:build !windows

package markdown

import (
	"os"
	"runtime"
	"syscall"
	"time"
)

// peakMemory is the most resident memory the finished process used.
func peakMemory(state *os.ProcessState) int64 {
	if state == nil {
		return 0
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return usage.Maxrss // bytes
	}
	return usage.Maxrss << 10 // kilobytes
}

// processCPU is the processor time this process has used. Timing checks use
// it rather than the wall clock, which a busy machine stretches.
func processCPU() time.Duration {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}
