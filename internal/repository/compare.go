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
	// and the files after it are missing from it.
	PatchTruncated bool
	// FilesTruncated is set when the diff stopped before Git had listed every
	// file. Files then holds only the files read in full.
	FilesTruncated bool
	// TimedOut is set when the time limit, not the size limit, cut the diff.
	// Another attempt may read more.
	TimedOut bool
}

// Compare reads what sourceOID changes since it branched from targetOID. It
// finds their merge bases with one Git process and, with exactly one base,
// reads the changed files, their line counts and the patch with a second:
// two processes, and none when both are cached. Renames are not detected.
//
// The diff output is bounded by outputLimit bytes and timeLimit, and by ctx.
// A diff cut by its size limit is cached with the limit, like a complete
// one, so a different limit reads again. A diff cut by the time limit is
// returned as incomplete and not cached, since another attempt may finish.
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
	args := []string{"--git-dir", ".", "diff", "--raw", "--numstat", "--patch", "-z", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--unified=3", "--src-prefix=a/", "--dst-prefix=b/", comparison.Base, sourceOID}
	limit := outputLimit
	key := strings.Join(args[3:], "\x00") + "\x00" + strconv.FormatInt(limit, 10)
	result, err := m.cachedRead(ctx, id, "compare", key, func(repositoryPath string) (cachedResult, bool, error) {
		limits := gitexec.CommandLimits{OutputLimit: limit, Timeout: timeLimit, StopAtOutputLimit: true}
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
	if err != nil {
		return Comparison{}, err
	}
	files, end, complete, separated := parseChanges(result.data)
	comparison.Files = files
	comparison.TimedOut = result.timedOut
	// A cut output lists every file only when Git got as far as the NUL it
	// writes between the file records and the patch.
	if !complete || result.truncated && !separated {
		if !result.truncated {
			return Comparison{}, errors.New("Git returned malformed comparison records")
		}
		comparison.FilesTruncated, comparison.PatchTruncated = true, true
		return comparison, nil
	}
	comparison.Patch = string(result.data[end:])
	comparison.PatchTruncated = result.truncated
	return comparison, nil
}

// MergeBases returns every merge base of two commits, the starting points of
// a Comparison, without reading any changes.
func (m *Manager) MergeBases(ctx context.Context, id, targetOID, sourceOID string) ([]string, error) {
	if !isOID(targetOID) || !isOID(sourceOID) {
		return nil, errors.New("invalid commit ID")
	}
	return m.mergeBases(ctx, id, targetOID, sourceOID)
}

// CompareFile reads one path of a comparison on its own: its change record
// and its text diff between baseOID and sourceOID, within outputLimit and
// timeLimit. The patch may also hold the diffs of paths below path, which the
// caller separates by file. found is false when the path itself has no
// change. A diff cut by the limit sets truncated; its last lines may be
// missing. It never depends on
// the list of the other changed files.
func (m *Manager) CompareFile(ctx context.Context, id, baseOID, sourceOID, path string, outputLimit int64, timeLimit time.Duration) (file ChangedFile, patch string, truncated, found bool, err error) {
	if !isOID(baseOID) || !isOID(sourceOID) {
		return ChangedFile{}, "", false, false, errors.New("invalid commit ID")
	}
	args := []string{"--git-dir", ".", "diff", "--raw", "--numstat", "--patch", "-z", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--unified=3", "--src-prefix=a/", "--dst-prefix=b/", baseOID, sourceOID, "--", ":(top,literal)" + path}
	key := strings.Join(args[3:], "\x00") + "\x00" + strconv.FormatInt(outputLimit, 10)
	result, err := m.cachedRead(ctx, id, "compare-file", key, func(repositoryPath string) (cachedResult, bool, error) {
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
		return ChangedFile{}, "", false, false, err
	}
	files, end, complete, separated := parseChanges(result.data)
	if !complete || result.truncated && !separated {
		if !result.truncated {
			return ChangedFile{}, "", false, false, errors.New("Git returned malformed comparison records")
		}
		return ChangedFile{}, "", false, false, errors.New("the change record of the file exceeds the read limit")
	}
	// A path names everything below it too, so a file that became a folder
	// lists the folder's files as well. Only the exact path is this file.
	for _, candidate := range files {
		if candidate.Path == path {
			return candidate, string(result.data[end:]), result.truncated, true, nil
		}
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
