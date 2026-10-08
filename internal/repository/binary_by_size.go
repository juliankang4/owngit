package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"owngit/internal/hostmem"
)

// memoryBudget is the memory this computer can give Git at once, the line
// above which a file's text comparison is left out of a read and the page says
// why (see markBinaryBySize). It is 0 when the memory is unknown: nothing is
// left out then, and a computer without a known limit keeps its behavior. It is
// a variable so a test can hold a small fixture to a smaller line.
var memoryBudget = func() int64 { return int64(hostmem.GitBudget(hostmem.Ceiling())) }

// pathExclusions returns the paths to leave out of a diff read: the files this
// computer did not compare as text because reading them would need more memory
// than it gives Git, sorted. ok is false when there are more of them than one
// command line should carry, and a caller then reads nothing rather than a
// read that could not be bounded.
func pathExclusions(files []ChangedFile) (excluded []string, ok bool) {
	for _, file := range files {
		if file.BinaryBySize {
			excluded = append(excluded, file.Path)
		}
	}
	if len(excluded) > excludedPathLimit {
		return nil, false
	}
	sort.Strings(excluded)
	return excluded, true
}

// exclusionSpecs appends the pathspecs that leave the named paths out of a
// diff read.
func exclusionSpecs(args, excluded []string) []string {
	if len(excluded) == 0 {
		return args
	}
	args = append(args, "--")
	for _, path := range excluded {
		args = append(args, ":(exclude,top,literal)"+path)
	}
	return args
}

// markBinaryBySize marks the changed files this computer does not compare as
// text, so the pages can say why such a file has no line by line change instead
// of calling it a binary file. A file is left out for one of two reasons:
//
//   - Rebuilding it as a stored delta would need more memory than this computer
//     gives Git at once. No threshold bounds that read: one 220 MiB file stored
//     as a delta used 665 MiB while its patch was written, and a 7 MiB file
//     whose base held 192 MiB used 396 MiB while its line counts were read (Git
//     2.47.3, measured with the lab fixtures of this release).
//   - One of its sides is larger than core.bigFileThreshold, the size above
//     which this computer stops comparing text. Git answers "binary" for such
//     a file without reading it, and reading it for a patch can still cost its
//     whole size.
//
// Both sides of the change are measured: Git reads the old object of a deleted
// file or of an old large version while it writes the patch, and that costs the
// same as a new one. A file left out this way is left out of the line counts
// and of the patch reads as well (see Compare), and its line counts are unknown
// rather than zero.
//
// The answer is kept with the read that listed the files, keyed by their
// objects and the line, so a repeated view of the same change starts no Git
// process.
func (m *Manager) markBinaryBySize(ctx context.Context, id string, files []ChangedFile) error {
	bound := memoryBudget()
	if bound <= 0 || len(files) == 0 {
		return nil
	}
	var key strings.Builder
	fmt.Fprintf(&key, "%d\x00%d", bound, m.Git.ReadBound())
	for _, file := range files {
		fmt.Fprintf(&key, "\x00%s\x00%s\x00%s", file.Path, file.oldOID, file.newOID)
	}
	result, err := m.cachedRead(ctx, id, "binary-by-size", key.String(), func(repositoryPath string) (cachedResult, bool, error) {
		marked, err := m.markedPathsWithin(ctx, repositoryPath, files, bound)
		if err != nil {
			return cachedResult{}, false, err
		}
		return cachedResult{data: []byte(strings.Join(marked, "\x00"))}, true, nil
	})
	if err != nil {
		return err
	}
	marked := make(map[string]struct{})
	for _, path := range strings.Split(string(result.data), "\x00") {
		if path != "" {
			marked[path] = struct{}{}
		}
	}
	for index := range files {
		if _, over := marked[files[index].Path]; over {
			files[index].BinaryBySize = true
		}
	}
	return nil
}

// markBinaryBySizeAt does the work of markBinaryBySize for a caller that
// already holds the repository path and reads its own view, with no cache
// between: the restore preview reads its file list fresh every time.
func (m *Manager) markBinaryBySizeAt(ctx context.Context, repositoryPath string, files []ChangedFile) error {
	return m.markBinaryBySizeWithin(ctx, repositoryPath, files, memoryBudget())
}

// markBinaryBySizeWithin marks the changed files whose object or base is large
// enough that rebuilding it would exceed bound bytes, the memory this computer
// can give Git at once. A file whose cost cannot be bounded (see
// ErrTreeCheckTooLarge) is an error, not a label: the caller reports that it
// could not be read instead of a reason.
func (m *Manager) markBinaryBySizeWithin(ctx context.Context, repositoryPath string, files []ChangedFile, bound int64) error {
	marked, err := m.markedPathsWithin(ctx, repositoryPath, files, bound)
	if err != nil {
		return err
	}
	for index := range files {
		for _, path := range marked {
			if files[index].Path == path {
				files[index].BinaryBySize = true
			}
		}
	}
	return nil
}

// markedPathsWithin returns the path of every changed file this computer does
// not compare as text, reading object metadata only. A file is left out when
// rebuilding it as a stored delta would need more memory than bound bytes, or
// when one of its sides is larger than the size above which this computer stops
// comparing text. Both sides of every change are measured, and a chain the
// metadata cannot bound is an error.
func (m *Manager) markedPathsWithin(ctx context.Context, repositoryPath string, files []ChangedFile, bound int64) ([]string, error) {
	if bound <= 0 || len(files) == 0 {
		return nil, nil
	}
	oids := make([]string, 0, 2*len(files))
	seen := make(map[string]struct{}, 2*len(files))
	for _, file := range files {
		for _, oid := range []string{file.oldOID, file.newOID} {
			if !isOID(oid) || allZeroes(oid) {
				continue
			}
			if _, known := seen[oid]; known {
				continue
			}
			seen[oid] = struct{}{}
			oids = append(oids, oid)
		}
	}
	if len(oids) == 0 {
		return nil, nil
	}
	costs, sizes, _, err := m.rebuildCosts(ctx, repositoryPath, oids, hostmem.TreeMetadataBound(hostmem.Ceiling()))
	if err != nil {
		return nil, err
	}
	// A file larger than the size at which this computer stops comparing text
	// is not compared as text either: the page says so instead of calling it a
	// binary file, and Git never reads it whole while it writes a patch.
	threshold := m.Git.ReadBound()
	var marked []string
	for _, file := range files {
		overLine := max(costs[file.oldOID], costs[file.newOID]) > bound
		overSize := threshold > 0 && max(sizes[file.oldOID], sizes[file.newOID]) > threshold
		if overLine || overSize {
			marked = append(marked, file.Path)
		}
	}
	return marked, nil
}
