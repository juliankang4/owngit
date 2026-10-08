package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/gitexec"
)

var (
	ErrRestoreInvalid = errors.New("invalid restore request")
	// ErrRestoreFilesNone reports a selected-files restore that named no file.
	// It wraps ErrRestoreInvalid, because the selection is refused like any
	// other invalid one; its own identity is what lets a page say what to do
	// about it instead of blaming the commit and the branch.
	ErrRestoreFilesNone   = fmt.Errorf("%w: select at least one changed path", ErrRestoreInvalid)
	ErrRestoreConflict    = errors.New("restore target changed")
	ErrRestoreNoChanges   = errors.New("restore has no changes")
	ErrRestoreUnsupported = errors.New("restore selection is unsupported")
)

const (
	RestoreAll   = "all"
	RestoreFiles = "files"
)

type RestoreRequest struct {
	Source       string
	Target       string
	Mode         string
	Paths        []string
	ExpectedHead string
}

type RestorePreview struct {
	SourceOID     string
	TargetRef     string
	ExpectedHead  string
	CreatesBranch bool
	ResultTree    string
	Changes       []ChangedFile
	Patches       map[string]string
	Selected      map[string]bool
	CanApply      bool
	DiffTruncated bool
}

type RestoreResult struct {
	CommitOID string
	Created   bool
}

type restorePlan struct {
	preview       RestorePreview
	currentExists bool
	currentOID    string
	// oldTree is the target branch's tree before the restore, the left side
	// of a preview's per-path patches. Only a preview reads it.
	oldTree string
}

type restoreTreeEntry struct {
	Mode string
	Type string
	OID  string
}

// PreviewRestore answers what restoring request would change, with one patch
// per changed path for the page that shows it. A preview writes nothing, so
// it takes the read lock.
func (m *Manager) PreviewRestore(ctx context.Context, id string, request RestoreRequest) (RestorePreview, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return RestorePreview{}, err
	}
	if !exists {
		return RestorePreview{}, ErrRepositoryNotFound
	}
	lock := m.Locks.For(id)
	if err := readLock(ctx, lock); err != nil {
		return RestorePreview{}, err
	}
	defer lock.RUnlock()
	plan, err := m.prepareRestore(ctx, repositoryPath, request, false)
	if err != nil {
		return RestorePreview{}, err
	}
	preview := plan.preview
	preview.Patches, preview.DiffTruncated, err = m.restorePatches(ctx, repositoryPath, plan.oldTree, preview.ResultTree, preview.Changes)
	if err != nil {
		return RestorePreview{}, err
	}
	return preview, nil
}

func (m *Manager) ApplyRestore(ctx context.Context, id string, request RestoreRequest) (RestoreResult, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return RestoreResult{}, err
	}
	if !exists {
		return RestoreResult{}, ErrRepositoryNotFound
	}
	lock := m.Locks.For(id)
	if err := writeLock(ctx, lock); err != nil {
		return RestoreResult{}, err
	}
	defer lock.Unlock()
	plan, err := m.prepareRestore(ctx, repositoryPath, request, true)
	if err != nil {
		return RestoreResult{}, err
	}
	if !plan.preview.CanApply {
		return RestoreResult{}, ErrRestoreNoChanges
	}

	newOID := plan.preview.SourceOID
	created := !plan.currentExists
	if plan.currentExists {
		message := "Restore files from " + shortObjectID(plan.preview.SourceOID)
		if request.Mode == RestoreAll {
			message = "Restore tree from " + shortObjectID(plan.preview.SourceOID)
		}
		date := gitexec.CommitDate(time.Now())
		commit, err := m.Git.RunWithEnvironment(ctx, repositoryPath, strings.NewReader(message+"\n"),
			[]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date},
			"-c", "user.name=OwnGit", "-c", "user.email=owngit@localhost",
			"--git-dir", ".", "commit-tree", plan.preview.ResultTree, "-p", plan.currentOID, "-F", "-")
		if err != nil {
			return RestoreResult{}, fmt.Errorf("create restore commit: %w", err)
		}
		newOID = strings.TrimSpace(string(commit.Stdout))
	}
	zero := strings.Repeat("0", len(plan.preview.SourceOID))
	expected := plan.currentOID
	if !plan.currentExists {
		expected = zero
	}
	if err := m.publishRestoreRef(ctx, repositoryPath, plan.preview.TargetRef, newOID, expected); err != nil {
		verificationCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		current, exists, readErr := m.readBranch(verificationCtx, repositoryPath, plan.preview.TargetRef)
		switch {
		case readErr == nil && exists && current == newOID:
			if m.OnChange != nil {
				m.OnChange(id)
			}
			return RestoreResult{CommitOID: newOID, Created: created}, nil
		case readErr == nil && exists == plan.currentExists && current == plan.currentOID:
			return RestoreResult{}, fmt.Errorf("publish restore commit: %w", err)
		case readErr == nil:
			return RestoreResult{}, ErrRestoreConflict
		default:
			return RestoreResult{}, fmt.Errorf("publish restore commit and verify its result: %v; verification failed: %w", err, readErr)
		}
	}
	if m.OnChange != nil {
		m.OnChange(id)
	}
	return RestoreResult{CommitOID: newOID, Created: created}, nil
}

func (m *Manager) publishRestoreRef(ctx context.Context, repositoryPath, targetRef, newOID, expected string) error {
	if m.restorePublisher != nil {
		return m.restorePublisher(ctx, repositoryPath, targetRef, newOID, expected)
	}
	_, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "update-ref", targetRef, newOID, expected)
	return err
}

// prepareRestore resolves request into the work a preview and an apply share:
// the source commit, the compare-and-swap expectation, the tree the restore
// would publish, and the paths that differ from it. It reads no file contents
// for display; the per-path patches are built only for a preview, which is the
// only caller that shows them.
func (m *Manager) prepareRestore(ctx context.Context, repositoryPath string, request RestoreRequest, enforceExpected bool) (restorePlan, error) {
	sourceOID, err := m.restoreSourceCommit(ctx, repositoryPath, request.Source)
	if err != nil {
		return restorePlan{}, err
	}
	// The target is a branch name such as main. A full ref name or HEAD
	// would name a different branch below refs/heads, such as
	// refs/heads/refs/heads/main, rather than the one meant.
	if strings.HasPrefix(request.Target, "refs/") || request.Target == "HEAD" {
		return restorePlan{}, fmt.Errorf("%w: target must be a branch name such as main", ErrRestoreInvalid)
	}
	if err := validateShortRef(request.Target); err != nil {
		return restorePlan{}, fmt.Errorf("%w: invalid target branch", ErrRestoreInvalid)
	}
	targetRef := "refs/heads/" + request.Target
	currentOID, currentExists, err := m.readBranch(ctx, repositoryPath, targetRef)
	if err != nil {
		return restorePlan{}, err
	}
	zero := strings.Repeat("0", len(sourceOID))
	expected := currentOID
	if !currentExists {
		expected = zero
	}
	if enforceExpected && request.ExpectedHead != expected {
		return restorePlan{}, ErrRestoreConflict
	}

	selected := make(map[string]bool)
	var resultTree string
	switch request.Mode {
	case RestoreAll:
		if len(request.Paths) != 0 {
			return restorePlan{}, fmt.Errorf("%w: whole-tree restore cannot include paths", ErrRestoreInvalid)
		}
		result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "rev-parse", "--verify", sourceOID+"^{tree}")
		if err != nil {
			return restorePlan{}, fmt.Errorf("resolve source tree: %w", err)
		}
		resultTree = strings.TrimSpace(string(result.Stdout))
	case RestoreFiles:
		if !currentExists {
			return restorePlan{}, fmt.Errorf("%w: selected files require an existing target branch", ErrRestoreUnsupported)
		}
		resultTree, selected, err = m.selectedRestoreTree(ctx, repositoryPath, sourceOID, currentOID, request.Paths)
		if err != nil {
			return restorePlan{}, err
		}
	default:
		return restorePlan{}, fmt.Errorf("%w: invalid restore mode", ErrRestoreInvalid)
	}

	oldTree, err := m.emptyTree(ctx, repositoryPath)
	if err != nil {
		return restorePlan{}, err
	}
	if currentExists {
		result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "rev-parse", "--verify", currentOID+"^{tree}")
		if err != nil {
			return restorePlan{}, fmt.Errorf("resolve target tree: %w", err)
		}
		oldTree = strings.TrimSpace(string(result.Stdout))
	}
	changes, err := m.restoreChanges(ctx, repositoryPath, oldTree, resultTree)
	if err != nil {
		return restorePlan{}, err
	}
	preview := RestorePreview{
		SourceOID: sourceOID, TargetRef: targetRef, ExpectedHead: expected, CreatesBranch: !currentExists,
		ResultTree: resultTree, Changes: changes, Selected: selected,
		CanApply: !currentExists || resultTree != oldTree,
	}
	return restorePlan{preview: preview, currentExists: currentExists, currentOID: currentOID, oldTree: oldTree}, nil
}

func (m *Manager) restoreSourceCommit(ctx context.Context, repositoryPath, source string) (string, error) {
	if !isOID(source) {
		return "", fmt.Errorf("%w: source must be a full object ID", ErrRestoreInvalid)
	}
	// The source must be a commit itself, not a tag that points to one. Only
	// Git's answer makes the source invalid; a lookup that failed is not an
	// answer about the source.
	commitOID, err := m.peelCommit(ctx, repositoryPath, source)
	if err != nil {
		return "", fmt.Errorf("read restore source: %w", err)
	}
	if commitOID != source {
		return "", fmt.Errorf("%w: source is not a commit of this repository", ErrRestoreInvalid)
	}
	return source, nil
}

// readBranch returns the tip of targetRef, found by its exact name among the
// repository's refs, so storage that ignores letter case cannot answer with
// a look-alike branch. A target that shares its RefNameKey, or that of a
// folder, with another ref or with the branch HEAD names is refused: on such
// storage the two are one file, and updating one would change the other.
// The caller holds the repository lock.
func (m *Manager) readBranch(ctx context.Context, repositoryPath, targetRef string) (string, bool, error) {
	limits := gitexec.CommandLimits{OutputLimit: 64 << 20}
	result, err := m.Git.RunWithLimits(ctx, repositoryPath, nil, limits, "--git-dir", ".", "for-each-ref", "--format=%(refname)%00%(objectname)")
	if err != nil {
		return "", false, fmt.Errorf("read repository refs: %w", err)
	}
	tips := map[string]string{}
	var names []string
	for _, line := range strings.Split(strings.TrimSuffix(string(result.Stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		name, oid, ok := strings.Cut(line, "\x00")
		if !ok || !isOID(oid) {
			return "", false, errors.New("Git returned a malformed ref record")
		}
		tips[name] = oid
		names = append(names, name)
	}
	head, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "--quiet", "HEAD")
	switch {
	case err == nil:
		names = append(names, strings.TrimSpace(string(head.Stdout)))
	case !gitAnsweredNo(ctx, err):
		return "", false, fmt.Errorf("read the default branch: %w", err)
	}
	if other := lookAlikeRef(names, targetRef); other != "" {
		return "", false, fmt.Errorf("%w: the branch %s and the existing %s have names that some file systems treat as the same; delete or rename one of them first",
			ErrRestoreInvalid, strings.TrimPrefix(targetRef, "refs/heads/"), strings.TrimPrefix(other, "refs/heads/"))
	}
	oid, exists := tips[targetRef]
	return oid, exists, nil
}

// lookAlikeRef returns a name in existing, or a folder of one, that is
// spelled differently from name or one of its folders but shares its
// RefNameKey, or "" when there is none (see RefNameConflicts).
func lookAlikeRef(existing []string, name string) string {
	levels := map[string]string{}
	for _, level := range refNameLevels(name) {
		levels[RefNameKey(level)] = level
	}
	for _, other := range existing {
		for _, level := range refNameLevels(other) {
			if spelling, ok := levels[RefNameKey(level)]; ok && spelling != level {
				return level
			}
		}
	}
	return ""
}

func (m *Manager) selectedRestoreTree(ctx context.Context, repositoryPath, sourceOID, targetOID string, paths []string) (string, map[string]bool, error) {
	if len(paths) == 0 {
		return "", nil, ErrRestoreFilesNone
	}
	if len(paths) > 10_000 {
		return "", nil, fmt.Errorf("%w: select at least one changed path", ErrRestoreInvalid)
	}
	source, err := m.restoreTreeEntries(ctx, repositoryPath, sourceOID)
	if err != nil {
		return "", nil, err
	}
	target, err := m.restoreTreeEntries(ctx, repositoryPath, targetOID)
	if err != nil {
		return "", nil, err
	}
	selected := make(map[string]bool, len(paths))
	for _, filePath := range paths {
		if filePath == "" || validateTreePath(filePath) != nil {
			return "", nil, fmt.Errorf("%w: invalid selected path", ErrRestoreInvalid)
		}
		selected[filePath] = true
	}
	for filePath := range selected {
		sourceEntry, sourceOK := source[filePath]
		targetEntry, targetOK := target[filePath]
		if !sourceOK && !targetOK {
			return "", nil, fmt.Errorf("%w: selected path is absent from both trees", ErrRestoreInvalid)
		}
		if (sourceOK && sourceEntry.Type == "commit") || (targetOK && targetEntry.Type == "commit") {
			return "", nil, fmt.Errorf("%w: selected gitlinks cannot be restored", ErrRestoreUnsupported)
		}
	}

	result := make(map[string]restoreTreeEntry, len(target)+len(selected))
	for filePath, entry := range target {
		result[filePath] = entry
	}
	for filePath := range selected {
		if entry, ok := source[filePath]; ok {
			result[filePath] = entry
		} else {
			delete(result, filePath)
		}
	}
	for filePath := range result {
		// Every ancestor must be a directory, not another resulting entry.
		for slash := strings.LastIndexByte(filePath, '/'); slash >= 0; slash = strings.LastIndexByte(filePath[:slash], '/') {
			if _, exists := result[filePath[:slash]]; exists {
				return "", nil, fmt.Errorf("%w: selection would discard unselected descendants", ErrRestoreUnsupported)
			}
		}
	}

	indexFile, err := os.CreateTemp(m.Git.TempDir, "owngit-restore-index-*")
	if err != nil {
		return "", nil, fmt.Errorf("create private restore index: %w", err)
	}
	indexPath, err := filepath.Abs(indexFile.Name())
	if err != nil {
		closeErr := indexFile.Close()
		removeErr := os.Remove(indexFile.Name())
		return "", nil, errors.Join(fmt.Errorf("resolve private restore index: %w", err), closeErr, removeErr)
	}
	if err := indexFile.Close(); err != nil {
		return "", nil, err
	}
	if err := os.Remove(indexPath); err != nil {
		return "", nil, err
	}
	defer os.Remove(indexPath)
	environment := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := m.Git.RunWithEnvironment(ctx, repositoryPath, nil, environment, "--git-dir", ".", "read-tree", targetOID); err != nil {
		return "", nil, fmt.Errorf("read target into private index: %w", err)
	}
	zero := strings.Repeat("0", len(sourceOID))
	var removals bytes.Buffer
	for filePath := range selected {
		if _, ok := target[filePath]; ok {
			fmt.Fprintf(&removals, "0 %s\t%s%c", zero, filePath, byte(0))
		}
	}
	if removals.Len() != 0 {
		if _, err := m.Git.RunWithEnvironment(ctx, repositoryPath, &removals, environment, "--git-dir", ".", "update-index", "-z", "--index-info"); err != nil {
			return "", nil, fmt.Errorf("remove selected paths from private index: %w", err)
		}
	}
	var additions bytes.Buffer
	for filePath := range selected {
		if entry, ok := source[filePath]; ok {
			fmt.Fprintf(&additions, "%s %s\t%s%c", entry.Mode, entry.OID, filePath, byte(0))
		}
	}
	if additions.Len() != 0 {
		if _, err := m.Git.RunWithEnvironment(ctx, repositoryPath, &additions, environment, "--git-dir", ".", "update-index", "-z", "--index-info"); err != nil {
			return "", nil, fmt.Errorf("add source paths to private index: %w", err)
		}
	}
	written, err := m.Git.RunWithEnvironment(ctx, repositoryPath, nil, environment, "--git-dir", ".", "write-tree")
	if err != nil {
		return "", nil, fmt.Errorf("write selected restore tree: %w", err)
	}
	return strings.TrimSpace(string(written.Stdout)), selected, nil
}

func (m *Manager) restoreTreeEntries(ctx context.Context, repositoryPath, commitOID string) (map[string]restoreTreeEntry, error) {
	result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "ls-tree", "-r", "-z", "--full-tree", commitOID)
	if err != nil {
		return nil, fmt.Errorf("read restore tree: %w", err)
	}
	entries := make(map[string]restoreTreeEntry)
	for _, record := range bytes.Split(result.Stdout, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, name, ok := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		filePath := string(name)
		if !ok || len(fields) != 3 || validateTreePath(filePath) != nil {
			return nil, errors.New("Git returned malformed restore tree data")
		}
		if _, duplicate := entries[filePath]; duplicate {
			return nil, errors.New("Git returned malformed restore tree data")
		}
		entries[filePath] = restoreTreeEntry{Mode: fields[0], Type: fields[1], OID: fields[2]}
	}
	return entries, nil
}

func (m *Manager) emptyTree(ctx context.Context, repositoryPath string) (string, error) {
	result, err := m.Git.Run(ctx, repositoryPath, strings.NewReader(""), "--git-dir", ".", "mktree")
	if err != nil {
		return "", fmt.Errorf("create empty comparison tree: %w", err)
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

// restoreChanges lists every path whose Git entry differs between oldTree
// and newTree, with its modes and line counts, so a symbolic link or a mode
// change is reported as the Git data it is. The entries are read before the
// counts, so a file this computer cannot compare as text is known first and
// its counts are not read (see markBinaryBySize).
func (m *Manager) restoreChanges(ctx context.Context, repositoryPath, oldTree, newTree string) ([]ChangedFile, error) {
	// --no-abbrev keeps the object IDs of the records complete, so a file this
	// computer cannot compare as text is marked as such on the preview page.
	result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "diff-tree", "--no-commit-id", "--raw", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-textconv", "-r", "-z", oldTree, newTree)
	if err != nil {
		return nil, fmt.Errorf("read restore changes: %w", err)
	}
	changes, end, complete := parseChanges(result.Stdout)
	if !complete || end != len(result.Stdout) {
		return nil, errors.New("Git returned malformed restore changes")
	}
	// The preview shows these changes, so a file this computer compares as
	// binary by size says so instead of looking like a binary file.
	if err := m.markBinaryBySizeAt(ctx, repositoryPath, changes); err != nil {
		return nil, err
	}
	excluded, ok := pathExclusions(changes)
	if !ok {
		return nil, ErrTreeCheckTooLarge
	}
	if len(excluded) == len(changes) {
		return changes, nil
	}
	args := exclusionSpecs([]string{"--git-dir", ".", "diff-tree", "--no-commit-id", "--numstat", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-textconv", "-r", "-z", oldTree, newTree}, excluded)
	result, err = m.Git.Run(ctx, repositoryPath, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("read restore line counts: %w", err)
	}
	lineCounts, err := changeLineCounts(result.Stdout)
	if err != nil {
		return nil, err
	}
	for index := range changes {
		count, ok := lineCounts[changes[index].Path]
		if !ok {
			continue
		}
		changes[index].Additions, changes[index].Deletions, changes[index].Binary = count.additions, count.deletions, count.binary
		changes[index].CountsRead = true
	}
	return changes, nil
}

func (m *Manager) restorePatches(ctx context.Context, repositoryPath, oldTree, newTree string, changes []ChangedFile) (map[string]string, bool, error) {
	patches := make(map[string]string)
	truncated := false
	for index, change := range changes {
		if index >= 200 {
			truncated = true
			continue
		}
		if change.Binary || change.TextDiffUnavailable {
			// A file this computer did not compare as text has no patch: reading
			// it would cost the memory the mark says it cannot have.
			continue
		}
		result, err := m.Git.RunWithOutputLimit(ctx, repositoryPath, nil, 256<<10,
			"--git-dir", ".", "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=3", oldTree, newTree, "--", ":(top,literal)"+change.Path)
		if err != nil {
			var limitErr *gitexec.LimitError
			if !errors.As(err, &limitErr) {
				return nil, false, fmt.Errorf("read restore patch for %q: %w", change.Path, err)
			}
			truncated = true
		}
		patches[change.Path] = string(result.Stdout)
	}
	return patches, truncated, nil
}

func shortObjectID(oid string) string {
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}
