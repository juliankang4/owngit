package recovery

import (
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
)

// SpaceError is a restore that a folder has no room for. It says nothing
// about the backup.
type SpaceError struct {
	Dir string
	// Needed and Free are the estimate that refused the restore before it
	// started, in bytes; Err is the error of a restore that ran out of room.
	Needed, Free uint64
	Err          error
}

func (e *SpaceError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("not enough free space in %s: %v", e.Dir, e.Err)
	}
	return fmt.Sprintf("not enough free space in %s: the restore needs at least %d MiB and %d MiB is free", e.Dir, mebibytes(e.Needed), e.Free>>20)
}

func (e *SpaceError) Unwrap() error { return e.Err }

func mebibytes(size uint64) uint64 { return (size + 1<<20 - 1) >> 20 }

// checkSpace refuses a restore of repositories into dir when its file
// system has less room than they need at least (roomNeeded). The sizes are
// those of the files now; a bundle that cannot be inspected is left to the
// restore to report.
func checkSpace(inputRoot, dir string, repositories []RepositoryManifest) error {
	var sizes []uint64
	for _, item := range repositories {
		if item.Empty {
			continue
		}
		info, err := os.Lstat(filepath.Join(inputRoot, filepath.FromSlash(item.Bundle)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sizes = append(sizes, uint64(info.Size()))
	}
	needed := roomNeeded(sizes)
	free, known, err := diskFreeSpace(dir)
	if err != nil {
		return fmt.Errorf("read the free space in %s: %w", dir, err)
	}
	if known && free < needed {
		return &SpaceError{Dir: dir, Needed: needed, Free: free}
	}
	return nil
}

// roomNeeded is the room that restoring bundles of these sizes needs at
// least: the bundles, which the restored repositories take about as much
// as, and the largest once more for the copy that restore checks and reads
// (copyBundle). A sum that 64 bits cannot hold, which only files far larger
// than any disk reach, is the largest value, so no disk has that room.
func roomNeeded(sizes []uint64) uint64 {
	var needed, largest uint64
	add := func(size uint64) {
		sum, carry := bits.Add64(needed, size, 0)
		if carry != 0 {
			sum = math.MaxUint64
		}
		needed = sum
	}
	for _, size := range sizes {
		add(size)
		largest = max(largest, size)
	}
	add(largest)
	return needed
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
	for _, text := range []string{"No space left on device", "There is not enough space on the disk", "database or disk is full"} {
		if strings.Contains(message, text) {
			return true
		}
	}
	return false
}
