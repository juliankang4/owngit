package markdown

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// peakMemory is not measured on Windows; the child's own heap limit still
// applies there.
func peakMemory(*os.ProcessState) int64 { return 0 }

// processCPU is the processor time this process has used. Timing checks use
// it rather than the wall clock, which a busy machine stretches.
func processCPU() time.Duration {
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user) != nil {
		return 0
	}
	ticks := func(t windows.Filetime) int64 { return int64(t.HighDateTime)<<32 | int64(t.LowDateTime) }
	return time.Duration(ticks(kernel)+ticks(user)) * 100
}
