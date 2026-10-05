// Package hostmem finds the memory OwnGit may use and divides it between
// OwnGit's own heap and the Git processes it starts.
//
// One rule, so a small computer keeps working and a large one is not held
// back. Of the memory ceiling C:
//
//   - OwnGit's Go heap may use C/2 (a soft limit, which does not count Git).
//   - Git packing may use 3C/8, shared by P processes that build a pack at
//     once: the Git transfers that build a pack (clones, fetches, archives)
//     plus one backup and one maintenance job.
//   - C/8 stays free for the operating system and file cache.
//
// A packing process costs about baseGit of its own (pack maps, object
// tables; 50 to 75 MiB measured) plus what the settings allow: pack.windowMemory
// is per thread, so threads x window, plus pack.deltaCacheSize. Its share
// A = 3C/8/P is therefore spent as baseGit + threads x window + cache, with
// window memory and cache half of the rest each, and threads chosen so that
// each window is at least smallestWindow. The same cache size bounds the delta
// base cache of index-pack, which receives a push.
//
// At a small ceiling the number of requests that build a pack at once
// (PackSlots) is lowered until every process can still have the smallest
// useful window and cache, so further clones wait for a slot instead of the
// kernel killing OwnGit. Pushes and ref advertisements are not counted and
// never wait for that limit. At 512 MiB the three smallest processes (one
// clone, backup, maintenance: 3 x 80 MiB) exceed the 192 MiB Git share by
// 48 MiB; measured with a backup, a clone and a repack together, the
// cgroup stayed about 5% below the ceiling, covered by the C/8 margin. A
// slower successful clone or backup is better than a dead server.
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
	// unknownCeiling is what an unreadable ceiling is treated as: an
	// ordinary computer, with settings that are safe and fast.
	unknownCeiling = 4 * gib
	// baseGit is the memory a packing Git process uses before any window
	// or cache. Measured 50 to 75 MiB.
	baseGit = 64 * mib
	// smallestWindow is the window below which another thread is not worth
	// splitting the window memory for.
	smallestWindow = 16 * mib
	// smallestPart is the smallest window total and the smallest delta cache.
	smallestPart = 8 * mib
	// DefaultTransfers is the number of Git transfers admitted at once
	// while the owner has not saved other limits (4 per repository + 1).
	DefaultTransfers = 5
	// otherPackers are the backup and the maintenance job, which pack
	// outside transfer admission.
	otherPackers = 2
)

func known(ceiling uint64) uint64 {
	if ceiling == 0 {
		return unknownCeiling
	}
	return ceiling
}

// HeapLimit returns the Go memory limit for the serving process, or 0 when
// nothing should be set: the ceiling is unknown, or the owner chose a limit
// with GOMEMLIMIT (ownerSetting is its value), which is always kept.
func HeapLimit(ceiling uint64, ownerSetting string) int64 {
	if ownerSetting != "" {
		return 0
	}
	return int64(ceiling / 2)
}

func gitShare(ceiling uint64) uint64 { return known(ceiling) / 8 * 3 }

// maxPackers is the most requests that build a pack the ceiling lets run at
// once, never fewer than one.
func maxPackers(ceiling uint64) int {
	processes := gitShare(ceiling) / (baseGit + 2*smallestPart)
	return int(max(processes, otherPackers+1)) - otherPackers
}

// PackSlots is the limit on requests that build a pack that this ceiling
// needs, or 0 for no limit: the ceiling is unknown, so OwnGit keeps the
// owner's saved limits.
func PackSlots(ceiling uint64) int {
	if ceiling == 0 {
		return 0
	}
	return maxPackers(ceiling)
}

// DefaultPackers is how many requests build a pack at once under the default
// transfer limits on this ceiling.
func DefaultPackers(ceiling uint64) int { return min(DefaultTransfers, maxPackers(ceiling)) }

// PackingConfig returns the Git settings that bound one packing process
// when packers requests build a pack at once (see the package comment for
// the arithmetic). They change how well Git compresses and how fast, never
// what a pack contains.
func PackingConfig(ceiling uint64, processors, packers int) [][2]string {
	processes := uint64(max(packers, 1) + otherPackers)
	part := max(gitShare(ceiling)/processes, baseGit+2*smallestPart) - baseGit
	threads := min(max(part/2/smallestWindow, 1), uint64(max(processors, 1)))
	window := min(max(part/2/threads, smallestPart), 256*mib)
	cache := min(max(part/2, smallestPart), 256*mib)
	return [][2]string{
		{"pack.threads", strconv.FormatUint(threads, 10)},
		{"pack.windowMemory", strconv.FormatUint(window, 10)},
		{"pack.deltaCacheSize", strconv.FormatUint(cache, 10)},
		{"core.deltaBaseCacheLimit", strconv.FormatUint(cache, 10)},
	}
}
