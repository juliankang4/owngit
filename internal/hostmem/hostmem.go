// Package hostmem finds the memory OwnGit may use and divides it between
// OwnGit's own heap and the Git processes it starts.
//
// One rule, so a small computer keeps working and a large one is not held
// back. Of the memory ceiling, OwnGit's Go heap may use five eighths (a soft
// limit that Go does not apply to Git) and Git packing a quarter, shared by
// the Git processes that can pack at once. The rest is left for the
// operating system and file cache. A slower successful clone or backup is
// better than a process the kernel kills.
package hostmem

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// noLimit is where a cgroup version 1 limit that was never set reads as a
// very large number.
const noLimit = 1 << 60

// Ceiling returns the bytes of memory this process may use: the smallest of
// its control group limits and the computer's physical memory. It returns 0
// when nothing can be read, which callers treat as unknown, and always on
// systems other than Linux, where small hosts are rare.
var Ceiling = sync.OnceValue(func() uint64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	return ceilingIn("/")
})

// ceilingIn reads the Linux files below root.
func ceilingIn(root string) uint64 {
	var smallest uint64
	consider := func(bytes uint64) {
		if bytes > 0 && bytes < noLimit && (smallest == 0 || bytes < smallest) {
			smallest = bytes
		}
	}
	consider(physicalMemory(filepath.Join(root, "proc/meminfo")))
	lines, _ := os.ReadFile(filepath.Join(root, "proc/self/cgroup"))
	for _, line := range strings.Split(string(lines), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		switch controllers := strings.Split(fields[1], ","); {
		case fields[1] == "":
			// Version 2: the limit may sit on any ancestor.
			base := filepath.Join(root, "sys/fs/cgroup")
			for path := filepath.Clean("/" + fields[2]); ; path = filepath.Dir(path) {
				consider(readNumber(filepath.Join(base, path, "memory.max")))
				if path == "/" {
					break
				}
			}
		case contains(controllers, "memory"):
			base := filepath.Join(root, "sys/fs/cgroup/memory")
			consider(readNumber(filepath.Join(base, filepath.Clean("/"+fields[2]), "memory.limit_in_bytes")))
			consider(readNumber(filepath.Join(base, "memory.limit_in_bytes")))
		}
	}
	return smallest
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// readNumber returns the number in a file, or 0 for a missing file, "max"
// or anything else.
func readNumber(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func physicalMemory(path string) uint64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if rest, ok := strings.CutPrefix(scanner.Text(), "MemTotal:"); ok {
			fields := strings.Fields(rest)
			if len(fields) == 2 && fields[1] == "kB" {
				if kib, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
					return kib << 10
				}
			}
		}
	}
	return 0
}

const (
	mib = 1 << 20
	gib = 1 << 30
	// unknownCeiling is what an unreadable ceiling is treated as for Git
	// packing: an ordinary computer, with settings that are safe and fast.
	unknownCeiling = 4 * gib
	// packingProcesses is how many Git processes that can pack run at once:
	// the default five transfers and one backup.
	packingProcesses = 6
)

// HeapLimit returns the Go memory limit for the serving process, or 0 when
// nothing should be set: the ceiling is unknown, or the owner chose a limit
// with GOMEMLIMIT (ownerSetting is its value), which is always kept.
func HeapLimit(ceiling uint64, ownerSetting string) int64 {
	if ownerSetting != "" {
		return 0
	}
	return int64(ceiling / 8 * 5)
}

// PackingConfig returns the Git settings that bound one packing process:
// one thread per gibibyte of ceiling up to the processor count, and window
// and delta cache memory that keeps six processes inside a quarter of the
// ceiling. They change how well Git compresses and how fast, never what a
// pack contains.
func PackingConfig(ceiling uint64, processors int) [][2]string {
	if ceiling == 0 {
		ceiling = unknownCeiling
	}
	threads := min(max(ceiling/gib, 1), uint64(max(processors, 1)))
	share := ceiling / 4 / packingProcesses
	window := min(max(share/threads, 8*mib), 256*mib)
	cache := min(max(share, 8*mib), 256*mib)
	return [][2]string{
		{"pack.threads", strconv.FormatUint(threads, 10)},
		{"pack.windowMemory", strconv.FormatUint(window, 10)},
		{"pack.deltaCacheSize", strconv.FormatUint(cache, 10)},
	}
}
