package repository

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"owngit/internal/gitexec"
)

// Comparison is what a source commit changes relative to a target commit,
// counted from their merge base, as a pull request shows it.
type Comparison struct {
	// Bases is the number of merge bases Git found. Only with exactly one is
	// there a comparison; with none or several, Files and Patch are empty.
	Bases int
	// Base is the single merge base.
	Base string
	// Files lists every changed file, with its line counts.
	Files []ChangedFile
	// Patch is the text diff of all files, as git diff prints it.
	Patch string
	// PatchTruncated is set when Patch stopped at a limit, so its last file
	// and the files after it are missing from it. It is also set, with an
	// empty Patch, when the comparison holds more files above the memory line
	// than one command line can leave out: the patch is then not read at all
	// and every file is offered on its own (see the excludedPathLimit).
	PatchTruncated bool
	// PatchTooLarge says that the patch is empty because too many of the
	// changed files are above the memory line to be left out by name, so no
	// read of it was attempted. A caller then names the files that are missing
	// from it instead of reporting a limit that was never reached.
	PatchTooLarge bool
	// FilesTruncated is set when the diff stopped before Git had listed every
	// file. Files then holds only the files read in full.
	FilesTruncated bool
	// TimedOut is set when the time limit, not the size limit, cut the diff.
	// Another attempt may read more.
	TimedOut bool
}

// Compare reads what sourceOID changes since it branched from targetOID. It
// finds their merge bases with one Git process and, with exactly one base,
// reads the changed files, their line counts and the patch with three more:
// four processes, and fewer when reads are cached. Renames are not detected.
//
// The file records are read before any content, so that a file this computer
// cannot compare as text (see markBinaryBySize) is left out of both the counts
// and the patch read: Git reads such a file whole while it writes its patch,
// even with the large-file threshold, and one 220 MiB file stored as a delta
// used 665 MiB of resident memory (Git 2.47.3, measured with the lab fixture of
// this release). The page says why the file has no lines instead.
//
// Each read is bounded by outputLimit bytes and timeLimit, and by ctx. A read
// cut by its size limit is cached with the limit, like a complete one, so a
// different limit reads again. A read cut by the time limit is returned as
// incomplete and not cached, since another attempt may finish.
func (m *Manager) Compare(ctx context.Context, id, targetOID, sourceOID string, outputLimit int64, timeLimit time.Duration) (Comparison, error) {
	if !isOID(targetOID) || !isOID(sourceOID) {
		return Comparison{}, errors.New("invalid commit ID")
	}
	bases, err := m.mergeBases(ctx, id, targetOID, sourceOID)
	if err != nil {
		return Comparison{}, err
	}
	comparison := Comparison{Bases: len(bases)}
	if len(bases) != 1 {
		return comparison, nil
	}
	comparison.Base = bases[0]
	// --raw without --numstat names every changed file and its objects without
	// reading any content (see commitFileCounts). --no-abbrev keeps the object
	// IDs of the records complete, so the size of each changed file can be
	// looked up by its object.
	records := []string{"--git-dir", ".", "diff", "--raw", "--no-abbrev", "-z", "--no-renames", "--no-ext-diff", "--no-textconv",
		comparison.Base, sourceOID}
	read, err := m.readDiff(ctx, id, "compare-records", records, outputLimit, timeLimit)
	if err != nil {
		return Comparison{}, err
	}
	files, _, complete := parseChanges(read.data)
	comparison.Files = files
	comparison.TimedOut = read.timedOut
	if err := m.markBinaryBySize(ctx, id, files); err != nil {
		return Comparison{}, err
	}
	// A records read cut by its size or time limit may have stopped inside a
	// record, so the file list is not known in full. The patch is not read
	// then: it would name changes the page cannot place.
	if !complete || read.truncated {
		if !read.truncated {
			return Comparison{}, errors.New("Git returned malformed comparison records")
		}
		comparison.FilesTruncated, comparison.PatchTruncated = true, true
		return comparison, nil
	}
	counts := []string{"--git-dir", ".", "diff", "--numstat", "--no-abbrev", "-z", "--no-renames", "--no-ext-diff", "--no-textconv",
		comparison.Base, sourceOID}
	timedOut, err := m.diffFileCounts(ctx, id, "compare-counts", counts, files, outputLimit, timeLimit)
	if errors.Is(err, ErrTreeCheckTooLarge) {
		// More files are above the memory line than one command line can leave
		// out, so neither their counts nor the patch are read. Every file is
		// listed with an unknown count and offered on its own (see
		// PatchTooLarge).
		comparison.PatchTruncated, comparison.PatchTooLarge = true, true
		return comparison, nil
	}
	if err != nil {
		return Comparison{}, err
	}
	comparison.TimedOut = comparison.TimedOut || timedOut
	excluded, ok := pathExclusions(files)
	if !ok {
		// Every excluded file is one more pathspec. A read that includes the
		// files it must leave out could not be bounded, so it is left out.
		comparison.PatchTruncated, comparison.PatchTooLarge = true, true
		return comparison, nil
	}
	patch := exclusionSpecs([]string{"--git-dir", ".", "diff", "--patch", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--unified=3", "--src-prefix=a/", "--dst-prefix=b/", comparison.Base, sourceOID}, excluded)
	read, err = m.readDiff(ctx, id, "compare-patch", patch, outputLimit, timeLimit)
	if err != nil {
		return Comparison{}, err
	}
	comparison.Patch = string(read.data)
	comparison.PatchTruncated = read.truncated
	comparison.TimedOut = comparison.TimedOut || read.timedOut
	return comparison, nil
}

// diffFileCounts reads the added and deleted line counts of the changed files
// of one comparison into files, leaving out the files this computer did not
// compare as text (see markBinaryBySize). Counting the lines of a file reads
// its content, and Git rebuilds a stored delta in memory to do it, so such a
// file keeps an unknown count rather than passing for an empty one. args
// select the comparison and must not carry a listing format; the exclusions
// are added here. A count that a cut read left out stays unknown, and a read
// cut by the time limit is reported as timed out.
func (m *Manager) diffFileCounts(ctx context.Context, id, kind string, args []string, files []ChangedFile, outputLimit int64, timeLimit time.Duration) (bool, error) {
	excluded, ok := pathExclusions(files)
	if !ok {
		// The caller reads nothing rather than a read that could not leave out
		// the files above the memory line.
		return false, ErrTreeCheckTooLarge
	}
	if len(excluded) == len(files) {
		return false, nil
	}
	read, err := m.readDiff(ctx, id, kind, exclusionSpecs(args, excluded), outputLimit, timeLimit)
	if err != nil {
		return false, err
	}
	counts, err := changeLineCounts(read.data)
	if err != nil {
		return false, err
	}
	for index := range files {
		count, ok := counts[files[index].Path]
		if !ok {
			continue
		}
		files[index].Additions, files[index].Deletions, files[index].Binary = count.additions, count.deletions, count.binary
		files[index].CountsRead = true
	}
	return read.timedOut, nil
}

// excludedPathLimit is how many paths one patch read leaves out by name, which
// keeps the Git command line short. Above it the patch is not read at all,
// and a caller marks every file it did not compare instead.
const excludedPathLimit = 100

// readDiff runs one diff read of repository id and answers its output. A run
// cut by the size limit is marked truncated; one cut by the time limit is
// marked truncated and timed out, and is not cached, because another attempt
// may read it in full. kind and args must name everything the result depends
// on, outputLimit included.
func (m *Manager) readDiff(ctx context.Context, id, kind string, args []string, outputLimit int64, timeLimit time.Duration) (cachedResult, error) {
	key := strings.Join(args[3:], "\x00") + "\x00" + strconv.FormatInt(outputLimit, 10)
	return m.cachedRead(ctx, id, kind, key, func(repositoryPath string) (cachedResult, bool, error) {
		limits := gitexec.CommandLimits{OutputLimit: outputLimit, Timeout: timeLimit, StopAtOutputLimit: true}
		output, err := m.Git.RunWithLimits(ctx, repositoryPath, nil, limits, args...)
		var limitErr *gitexec.LimitError
		switch {
		case err == nil:
			return cachedResult{data: output.Stdout}, true, nil
		case errors.As(err, &limitErr):
			return cachedResult{data: output.Stdout, truncated: true}, true, nil
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && !errors.Is(err, gitexec.ErrProcessCleanup):
			// The time limit, not the request, ended the diff. What Git wrote
			// so far is shown as incomplete, provided the stopped Git was
			// cleaned up.
			return cachedResult{data: output.Stdout, truncated: true, timedOut: true}, false, nil
		}
		return cachedResult{}, false, err
	})
}

// MergeBases returns every merge base of two commits, the starting points of
// a Comparison, without reading any changes.
func (m *Manager) MergeBases(ctx context.Context, id, targetOID, sourceOID string) ([]string, error) {
	if !isOID(targetOID) || !isOID(sourceOID) {
		return nil, errors.New("invalid commit ID")
	}
	return m.mergeBases(ctx, id, targetOID, sourceOID)
}

// CompareFile reads one path of a comparison on its own: its change record,
// its line counts and its text diff between baseOID and sourceOID, within
// outputLimit and timeLimit. The patch may also hold the diffs of paths below
// path, which the caller separates by file. found is false when the path
// itself has no change. A diff cut by the limit sets truncated; its last lines
// may be missing. It never depends on the list of the other changed files.
//
// The change record is read before the counts and the patch, so a file this
// computer cannot compare as text is answered with no counts and no patch
// instead of letting Git read the file whole (see Compare).
func (m *Manager) CompareFile(ctx context.Context, id, baseOID, sourceOID, path string, outputLimit int64, timeLimit time.Duration) (file ChangedFile, patch string, truncated, found bool, err error) {
	if !isOID(baseOID) || !isOID(sourceOID) {
		return ChangedFile{}, "", false, false, errors.New("invalid commit ID")
	}
	records := []string{"--git-dir", ".", "diff", "--raw", "--no-abbrev", "-z", "--no-renames", "--no-ext-diff", "--no-textconv",
		baseOID, sourceOID, "--", ":(top,literal)" + path}
	read, err := m.readDiff(ctx, id, "compare-file-records", records, outputLimit, timeLimit)
	if err != nil {
		return ChangedFile{}, "", false, false, err
	}
	files, _, complete := parseChanges(read.data)
	if !complete || read.truncated {
		if !read.truncated {
			return ChangedFile{}, "", false, false, errors.New("Git returned malformed comparison records")
		}
		return ChangedFile{}, "", false, false, errors.New("the change record of the file exceeds the read limit")
	}
	// A path names everything below it too, so a file that became a folder
	// lists the folder's files as well. Only the exact path is this file.
	for index := range files {
		if files[index].Path != path {
			continue
		}
		if err := m.markBinaryBySize(ctx, id, files); err != nil {
			return ChangedFile{}, "", false, false, err
		}
		if files[index].TextDiffUnavailable {
			// Git reads such a file whole while it writes its patch, however
			// large the memory this computer gives one Git process is.
			return files[index], "", false, true, nil
		}
		counts := []string{"--git-dir", ".", "diff", "--numstat", "--no-abbrev", "-z", "--no-renames", "--no-ext-diff", "--no-textconv",
			baseOID, sourceOID, "--", ":(top,literal)" + path}
		if _, err := m.diffFileCounts(ctx, id, "compare-file-counts", counts, files, outputLimit, timeLimit); err != nil && !errors.Is(err, ErrTreeCheckTooLarge) {
			// A folder holds more files above the memory line than one command
			// line can leave out: the counts of this file stay unknown, and its
			// own patch is still read, since only this path is in it.
			return ChangedFile{}, "", false, false, err
		}
		patchArgs := []string{"--git-dir", ".", "diff", "--patch", "--no-renames", "--no-ext-diff", "--no-textconv",
			"--unified=3", "--src-prefix=a/", "--dst-prefix=b/", baseOID, sourceOID, "--", ":(top,literal)" + path}
		patchRead, err := m.readDiff(ctx, id, "compare-file-patch", patchArgs, outputLimit, timeLimit)
		if err != nil {
			return ChangedFile{}, "", false, false, err
		}
		return files[index], string(patchRead.data), patchRead.truncated, true, nil
	}
	return ChangedFile{}, "", false, false, nil
}

// ComparePatch reads the text diff of paths alone between the merge base and
// sourceOID of a Comparison, so a page of a large change reads the patch of
// its own files within the size limit instead of sharing it with every file
// before them. Output past outputLimit is cut and truncated is set; the last
// file's part may then be incomplete.
func (m *Manager) ComparePatch(ctx context.Context, id, baseOID, sourceOID string, paths []string, outputLimit int64, timeLimit time.Duration) (patch string, truncated bool, err error) {
	if !isOID(baseOID) || !isOID(sourceOID) {
		return "", false, errors.New("invalid commit ID")
	}
	args := []string{"--git-dir", ".", "diff", "--patch", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--unified=3", "--src-prefix=a/", "--dst-prefix=b/", baseOID, sourceOID, "--"}
	for _, path := range paths {
		args = append(args, ":(top,literal)"+path)
	}
	key := strings.Join(args[3:], "\x00") + "\x00" + strconv.FormatInt(outputLimit, 10)
	result, err := m.cachedRead(ctx, id, "compare-patch", key, func(repositoryPath string) (cachedResult, bool, error) {
		limits := gitexec.CommandLimits{OutputLimit: outputLimit, Timeout: timeLimit, StopAtOutputLimit: true}
		output, err := m.Git.RunWithLimits(ctx, repositoryPath, nil, limits, args...)
		var limitErr *gitexec.LimitError
		switch {
		case err == nil:
			return cachedResult{data: output.Stdout}, true, nil
		case errors.As(err, &limitErr):
			return cachedResult{data: output.Stdout, truncated: true}, true, nil
		}
		return cachedResult{}, false, err
	})
	if err != nil {
		return "", false, err
	}
	return string(result.data), result.truncated, nil
}

// mergeBases returns every merge base of two commits. The answer never
// changes for the two IDs, so it is cached.
func (m *Manager) mergeBases(ctx context.Context, id, left, right string) ([]string, error) {
	result, err := m.cachedRead(ctx, id, "merge-base", left+"\x00"+right, func(repositoryPath string) (cachedResult, bool, error) {
		output, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "merge-base", "--all", left, right)
		if err != nil {
			// Status 1 with no output is Git's answer that the commits share
			// no history.
			if code, ok := gitexec.ExitCode(err); ok && code == 1 && len(bytes.TrimSpace(output.Stdout)) == 0 && ctx.Err() == nil {
				return cachedResult{}, true, nil
			}
			return cachedResult{}, false, err
		}
		return cachedResult{data: output.Stdout}, true, nil
	})
	if err != nil {
		return nil, err
	}
	bases := strings.Fields(string(result.data))
	for _, base := range bases {
		if !isOID(base) {
			return nil, errors.New("Git returned an invalid merge base")
		}
	}
	return bases, nil
}
