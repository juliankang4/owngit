package recovery

import (
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strings"
)

// SpaceError is a restore or a backup that a folder has no room for. It
// says nothing about the backup or the state being copied.
type SpaceError struct {
	Dir string
	// Needed and Free are the estimate that refused the restore or backup
	// before it started, in bytes; Err is the error of one that ran out of
	// room. FromLastBackup says that Needed is the size of the last backup
	// in Dir.
	Needed, Free   uint64
	FromLastBackup bool
	Err            error
}

func (e *SpaceError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("not enough free space in %s: %v", e.Dir, e.Err)
	}
	if e.FromLastBackup {
		return fmt.Sprintf("not enough free space in %s: a new backup needs about %d MiB, the size of the last backup here, and %d MiB is free", e.Dir, mebibytes(e.Needed), e.Free>>20)
	}
	return fmt.Sprintf("not enough free space in %s: at least %d MiB is needed and %d MiB is free", e.Dir, mebibytes(e.Needed), e.Free>>20)
}

func (e *SpaceError) Unwrap() error { return e.Err }

// mebibytes rounds size up to whole MiB, without wrapping around for the
// largest sizes.
func mebibytes(size uint64) uint64 {
	whole := size >> 20
	if size&(1<<20-1) != 0 {
		whole++
	}
	return whole
}

// checkSpace refuses a restore of repositories into dir when its file
// system has less room than they need at least (roomNeeded). The sizes are
// those of the files now; a bundle that cannot be inspected is left to the
// restore to report.
func checkSpace(input *backupInput, dir string, repositories []RepositoryManifest) error {
	var sizes []uint64
	for _, item := range repositories {
		if item.Empty {
			continue
		}
		file, info, err := input.openBundle(item)
		if err != nil {
			continue
		}
		file.Close()
		sizes = append(sizes, uint64(info.Size()))
	}
	return CheckFreeSpace(dir, roomNeeded(sizes))
}

// roomNeeded is the room that restoring bundles of these sizes needs at
// least: the bundles, which the restored repositories take about as much
// as, and the largest once more for the copy that restore checks and reads
// (copyBundle).
func roomNeeded(sizes []uint64) uint64 {
	var largest uint64
	for _, size := range sizes {
		largest = max(largest, size)
	}
	return sumSizes(append(slices.Clone(sizes), largest))
}

// sumSizes adds sizes. A sum that 64 bits cannot hold, which only files far
// larger than any disk reach, is the largest value, so no disk has that
// room.
func sumSizes(sizes []uint64) uint64 {
	var sum uint64
	for _, size := range sizes {
		next, carry := bits.Add64(sum, size, 0)
		if carry != 0 {
			return math.MaxUint64
		}
		sum = next
	}
	return sum
}

// diskFull reports whether err says that a disk was full, as Go, Git or
// SQLite say it.
func diskFull(err error) bool {
	if err == nil {
		return false
	}
	if diskFullError(err) {
		return true
	}
	message := err.Error()
	for _, text := range []string{"No space left on device", "There is not enough space on the disk", "database or disk is full", "Out of diskspace"} {
		if strings.Contains(message, text) {
			return true
		}
	}
	return false
}

// CheckFreeSpace refuses when the file system of dir has less than needed
// bytes free. A system that does not tell its free space is not refused.
func CheckFreeSpace(dir string, needed uint64) error {
	free, known, err := diskFreeSpace(dir)
	if err != nil {
		return fmt.Errorf("read the free space in %s: %w", dir, err)
	}
	if known && free < needed {
		return &SpaceError{Dir: dir, Needed: needed, Free: free}
	}
	return nil
}
