package repository

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"owngit/internal/hostmem"
)

// rebuildOverhead covers what a rebuilt delta costs beyond the object bytes it
// holds: Git's inflate window, its zlib state and the reader's buffer. The cost
// of one file is the object bytes Git holds while rebuilding it, times this.
// Measured on Git 2.47.3 under the 512 MiB host settings (see REPORT.md for the
// fixtures):
//
//	one 16 MiB object, depth 1:                         39 MiB peak
//	one 16 MiB object, depth 2:                         55 MiB peak
//	one 16 MiB object, depth 14:                        55 MiB peak
//	one 4 MiB object, depth 2:                          20 MiB peak
//	one 4 MiB object, depth 31:                         21 MiB peak
//	one 32 MiB object, depth 0:                         10 MiB peak
//	one 32 MiB object, depth 1:                         75 MiB peak
//	one 32 MiB object, depth 2:                        108 MiB peak
//	one 4 MiB object deleted, 96 MiB base, depth 1:    200 MiB peak
//	one 7 MiB object deleted, 192 MiB base, depth 1:   396 MiB peak
//	one 220 MiB object, depth 1:                       477 MiB peak
//
// A read holds the stored base twice, once as the base and once as the delta
// that applies to it, so the largest size of a chain counts twice whatever the
// depth; the deeper sizes count once each, at most chainCopies all told. The
// charge adds rebuildFixedBytes for what a rebuild holds whatever the sizes
// are. The worst measured case is then a quarter above the charge (the 192 MiB
// base: 497 MiB charged, 396 MiB peak) and the deepest listed case is a fifth
// below it. Depth 0 is listed for scale only: an object Git does not store as a
// delta is streamed or mapped, costs a tenth of its size and is not charged
// here.
const rebuildOverhead = 5

// rebuildFixedBytes is what rebuilding a delta holds whatever its sizes are:
// Git's buffers, its zlib state and the delta in flight. Measured on Git
// 2.47.3: one 4 MiB object peaks at 20 to 21 MiB at depths 2 to 31, which is
// three copies of it and about 6 MiB besides.
const rebuildFixedBytes = 8 << 20

// chainCopies is how many object sizes of one delta chain Git holds at once:
// the stored base (twice, see rebuildOverhead), the base of the delta it applies
// and the rebuilt result. A deeper chain does not grow the peak, measured on
// Git 2.47.3: one 16 MiB object peaks at 39 MiB at depth 1 and at 55 MiB at
// depths 2, 5, 9 and 12, while the sum of the chain grows without end.
const chainCopies = 3

// maxChainDepth bounds how far a delta chain is followed. Git's default pack
// depth is 50 and OwnGit's own packing keeps it, so a longer chain means the
// object metadata disagrees with itself and its cost cannot be bounded. It is a
// variable so a test can lower it instead of building a repository no packer
// would produce.
var maxChainDepth = 250

// rebuiltCost returns the memory Git needs to rebuild an object while holding
// heldBytes of it: the overhead above plus what a rebuild holds whatever its
// sizes are. An object at depth 0 is not rebuilt and is not charged at all.
func rebuiltCost(heldBytes int64) int64 { return heldBytes*rebuildOverhead/4 + rebuildFixedBytes }

// chainSizes keeps the largest object sizes of one delta chain, at most
// chainCopies of them, smallest first. A chain is a path from an object
// through its bases, so the sizes arrive one at a time.
type chainSizes struct {
	values [chainCopies]int64
	count  int
}

// add records one size of the chain and keeps the largest chainCopies of them.
func (sizes *chainSizes) add(size int64) {
	if sizes.count == chainCopies && size <= sizes.values[0] {
		// The set is full and the new size is the smallest of the chain: Git
		// drops it before the peak, so it is not charged.
		return
	}
	position := sizes.count
	if position > chainCopies-1 {
		position = chainCopies - 1
		copy(sizes.values[:], sizes.values[1:])
	}
	for position > 0 && sizes.values[position-1] > size {
		sizes.values[position] = sizes.values[position-1]
		position--
	}
	sizes.values[position] = size
	if sizes.count < chainCopies {
		sizes.count++
	}
}

// costBytes is what Git holds while rebuilding the object: the largest size of
// the chain twice, because the read holds the stored base and the delta that
// applies to it, plus the next largest sizes (at most chainCopies of them all
// told), because a deeper chain also holds the base of its base and the object
// it rebuilds.
func (sizes chainSizes) costBytes() int64 {
	largest := int64(0)
	if sizes.count > 0 {
		largest = sizes.values[sizes.count-1]
	}
	total := largest
	for _, size := range sizes.values[:sizes.count] {
		total += size
	}
	return total
}

// DeltaRebuildError names one tree entry that Git stores as a delta and that
// this computer cannot rebuild within the memory it can give Git at once. It
// carries the path, the cost and the bound so a caller can tell the owner what
// happened.
type DeltaRebuildError struct {
	Path  string
	Cost  int64
	Bound int64
}

func (e *DeltaRebuildError) Error() string {
	return fmt.Sprintf("%s needs about %d bytes to rebuild from its stored delta, above the %d bytes this computer can give Git at once",
		e.Path, e.Cost, e.Bound)
}

// ErrTreeCheckTooLarge reports that a tree check could not bound what it read:
// the tree holds more objects than the memory the check may use for their
// metadata, or one of its delta chains is deeper than Git builds. A check that
// did not succeed proves nothing about the tree, so a caller refuses the work
// instead of reading it unchecked. It is also reported when a page cannot
// bound the files whose text comparison it left out.
var ErrTreeCheckTooLarge = errors.New("too many objects to check within the memory this computer gives Git")

// objectChainError names the shape one of those chains has.
var objectChainError = errors.New("the delta chain of an object is deeper than Git builds")

// ErrDeltaChainTooDeep reports a delta chain deeper than Git builds, which
// means the object metadata of the repository disagrees with itself. A tree
// that holds such a chain cannot be checked either, so this error is also an
// ErrTreeCheckTooLarge; it is separate so a caller can name the case instead of
// reporting a file count.
var ErrDeltaChainTooDeep = fmt.Errorf("%w: %w", ErrTreeCheckTooLarge, objectChainError)

// objectMetadata is what reads object metadata: its inflated size and its
// delta base (empty when Git does not store it as a delta).
type objectMetadata struct {
	size      int64
	deltaBase string
}

// readObjectMetadata answers the size and the delta base of each object ID
// with one Git process that reads no object content, so it is safe on a
// computer whose memory is small. limit bounds the answer: above it the call
// fails with ErrTreeCheckTooLarge rather than reading an unbounded amount, and
// the caller refuses the work. A missing object is reported as absent; the
// caller decides what that means.
func (m *Manager) readObjectMetadata(ctx context.Context, repositoryPath string, oids []string, limit int64) (map[string]objectMetadata, error) {
	metadata := make(map[string]objectMetadata, len(oids))
	if len(oids) == 0 {
		return metadata, nil
	}
	if int64(len(oids))*metadataLineBytes > limit {
		return nil, fmt.Errorf("%w: %d objects need about %d bytes of metadata", ErrTreeCheckTooLarge, len(oids), int64(len(oids))*metadataLineBytes)
	}
	input := strings.Join(oids, "\n") + "\n"
	// Git answers one line per object, so the estimate above is an upper bound
	// for a repository of any hash algorithm.
	output, err := m.Git.RunWithOutputLimit(ctx, repositoryPath, strings.NewReader(input), int64(len(oids))*metadataLineBytes+4096,
		"--git-dir", ".", "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize) %(deltabase)")
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output.Stdout)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		fact := objectMetadata{}
		if size, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			fact.size = size
		}
		if len(fields) >= 4 && !allZeroes(fields[3]) {
			fact.deltaBase = fields[3]
		}
		metadata[fields[0]] = fact
	}
	return metadata, nil
}

// metadataLineBytes is the room one metadata answer needs: two object IDs of
// the widest hash algorithm, an object type, a size and separators.
const metadataLineBytes = 200

// blobRebuildCost answers the memory Git needs to rebuild one blob, reading
// object metadata only. A file below the size at which this computer reads a
// file whole can still be stored as a delta on a much larger base: measured on
// Git 2.47.3, a 7 MiB file whose base holds 192 MiB uses 395 MiB to read, while
// its own size stays far below the line. The answer is cached with the object,
// so a repeated view of one file starts no Git process; a repack can make a
// chain shorter in the meantime and the object then reads as expensive until
// the cache drops the entry (the same trade-off as the change-page mark).
func (m *Manager) blobRebuildCost(ctx context.Context, id, oid string) (int64, error) {
	if !isOID(oid) {
		return 0, nil
	}
	key := oid + "\x00" + strconv.FormatInt(memoryBudget(), 10)
	result, err := m.cachedRead(ctx, id, "blob-cost", key, func(repositoryPath string) (cachedResult, bool, error) {
		cost, err := m.blobRebuildCostWithin(ctx, repositoryPath, oid)
		if err != nil {
			return cachedResult{}, false, err
		}
		return cachedResult{data: []byte(strconv.FormatInt(cost, 10))}, true, nil
	})
	if err != nil {
		return 0, err
	}
	cost, err := strconv.ParseInt(string(result.data), 10, 64)
	if err != nil {
		return 0, err
	}
	return cost, nil
}

// blobRebuildCostWithin does the work of blobRebuildCost for a caller that
// holds the repository path and reads its own view, with no cache between: the
// pinned blob reads take their own lock.
func (m *Manager) blobRebuildCostWithin(ctx context.Context, repositoryPath, oid string) (int64, error) {
	costs, _, _, err := m.rebuildCosts(ctx, repositoryPath, []string{oid}, hostmem.TreeMetadataBound(hostmem.Ceiling()))
	if err != nil {
		return 0, err
	}
	return costs[oid], nil
}

// blobBelowMemoryLine reports whether a blob can be read without rebuilding a
// stored delta beyond the memory this computer gives Git at once. It is the
// same line the change pages use to leave a file out of a text comparison, so a
// file that a page refuses is not read here either. With an unknown ceiling
// nothing is refused this way.
func (m *Manager) blobBelowMemoryLine(ctx context.Context, id, oid string) (bool, error) {
	bound := memoryBudget()
	if bound <= 0 {
		return true, nil
	}
	cost, err := m.blobRebuildCost(ctx, id, oid)
	if err != nil {
		return false, err
	}
	return cost <= bound, nil
}

// blobsAboveMemoryLineWithin reports whether any of oids would rebuild a stored
// delta in memory beyond what this computer gives Git at once, for a caller
// that already holds the repository path and its own lock. One metadata walk
// answers for the whole list: a caller that reads many objects of one
// repository pays one process per chain level, whatever the number of objects
// and the depth of each chain. A chain whose cost cannot be bounded is an
// error, not a refusal. With an unknown ceiling nothing is refused this way.
func (m *Manager) blobsAboveMemoryLineWithin(ctx context.Context, repositoryPath string, oids []string) (bool, error) {
	bound := memoryBudget()
	if bound <= 0 || len(oids) == 0 {
		return false, nil
	}
	costs, _, _, err := m.rebuildCosts(ctx, repositoryPath, oids, hostmem.TreeMetadataBound(hostmem.Ceiling()))
	if err != nil {
		return false, err
	}
	for _, oid := range oids {
		if costs[oid] > bound {
			return true, nil
		}
	}
	return false, nil
}

// rebuildCosts answers, for each of oids, the memory Git needs to rebuild it:
// the largest object sizes of its delta chain, at most chainCopies of them,
// charged with the measured overhead. An object Git does not store as a delta
// needs no rebuild and costs nothing. It also answers the size of every
// requested object, which says whether a file is over the size at which this
// computer compares text. It reads metadata only, one Git process per chain
// depth, and stops with ErrDeltaChainTooDeep when a chain is deeper than
// maxChainDepth. allowance bounds the metadata of one pass: each level is
// released before the next, so the walk fails with ErrTreeCheckTooLarge when
// one level needs more than that, whatever the total it read. The second return
// value is how many bytes of metadata the walk read altogether.
func (m *Manager) rebuildCosts(ctx context.Context, repositoryPath string, oids []string, allowance int64) (map[string]int64, map[string]int64, int64, error) {
	costs := make(map[string]int64, len(oids))
	// level holds, for each object whose size is still wanted, the objects
	// that wait for it: the object itself and every base read so far.
	level := make(map[string][]string, len(oids))
	objects := make(map[string]chainSizes, len(oids))
	own := make(map[string]int64, len(oids))
	deltas := make(map[string]bool, len(oids))
	for _, oid := range oids {
		if _, seen := level[oid]; !seen {
			level[oid] = nil
		}
		level[oid] = append(level[oid], oid)
	}
	read := int64(0)
	for depth := 0; len(level) > 0; depth++ {
		if depth >= maxChainDepth {
			return nil, nil, read, fmt.Errorf("%w (over %d bases)", ErrDeltaChainTooDeep, maxChainDepth)
		}
		wanted := make([]string, 0, len(level))
		for oid := range level {
			wanted = append(wanted, oid)
		}
		pass := int64(len(wanted)) * metadataLineBytes
		if pass > allowance {
			return nil, nil, read, fmt.Errorf("%w: about %d bytes of metadata for one level", ErrTreeCheckTooLarge, pass)
		}
		read += pass
		metadata, err := m.readObjectMetadata(ctx, repositoryPath, wanted, allowance)
		if err != nil {
			return nil, nil, read, err
		}
		next := make(map[string][]string, len(level))
		for oid, waiting := range level {
			fact, ok := metadata[oid]
			if !ok {
				// Git answered nothing about a wanted object: a repository
				// without it cannot be measured, so the caller refuses.
				return nil, nil, read, fmt.Errorf("Git answered nothing about object %s", oid)
			}
			if depth == 0 {
				own[oid] = fact.size
			}
			for _, root := range waiting {
				sizes := objects[root]
				sizes.add(fact.size)
				objects[root] = sizes
			}
			if fact.deltaBase != "" {
				// The object is a delta, so Git rebuilds it and holds its chain:
				// its cost grows with the sizes of the chain, not with its own
				// size alone.
				for _, root := range waiting {
					deltas[root] = true
				}
				next[fact.deltaBase] = append(next[fact.deltaBase], waiting...)
			}
		}
		level = next
	}
	for _, oid := range oids {
		if sizes, ok := objects[oid]; ok && deltas[oid] {
			costs[oid] = rebuiltCost(sizes.costBytes())
		}
	}
	return costs, own, read, nil
}

// allZeroes reports whether value is the all-zero object ID Git writes for an
// object that is not a delta.
func allZeroes(value string) bool {
	for _, character := range value {
		if character != '0' {
			return false
		}
	}
	return value != ""
}
