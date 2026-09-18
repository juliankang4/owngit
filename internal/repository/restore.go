package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"owngit/internal/gitexec"
)

var (
	ErrRestoreInvalid     = errors.New("invalid restore request")
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
	repository    string
	currentExists bool
	currentOID    string
}

type restoreTreeEntry struct {
	Mode string
	Type string
	OID  string
}

func (m *Manager) PreviewRestore(ctx context.Context, id string, request RestoreRequest) (RestorePreview, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return RestorePreview{}, err
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	plan, err := m.prepareRestore(ctx, repositoryPath, request, false)
	if err != nil {
		return RestorePreview{}, err
	}
	return plan.preview, nil
}

func (m *Manager) ApplyRestore(ctx context.Context, id string, request RestoreRequest) (RestoreResult, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return RestoreResult{}, err
	}
	lock := m.Locks.For(id)
	lock.Lock()
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
		commit, err := m.Git.Run(ctx, "", strings.NewReader(message+"\n"),
			"-c", "user.name=OwnGit", "-c", "user.email=owngit@localhost",
			"--git-dir", repositoryPath, "commit-tree", plan.preview.ResultTree, "-p", plan.currentOID, "-F", "-")
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
			return RestoreResult{CommitOID: newOID, Created: created}, nil
		case readErr == nil && exists == plan.currentExists && current == plan.currentOID:
			return RestoreResult{}, fmt.Errorf("publish restore commit: %w", err)
		case readErr == nil:
			return RestoreResult{}, ErrRestoreConflict
		default:
			return RestoreResult{}, fmt.Errorf("publish restore commit and verify its result: %v; verification failed: %w", err, readErr)
		}
	}
	return RestoreResult{CommitOID: newOID, Created: created}, nil
}

func (m *Manager) publishRestoreRef(ctx context.Context, repositoryPath, targetRef, newOID, expected string) error {
	if m.restorePublisher != nil {
		return m.restorePublisher(ctx, repositoryPath, targetRef, newOID, expected)
	}
	_, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "update-ref", targetRef, newOID, expected)
	return err
}

func (m *Manager) prepareRestore(ctx context.Context, repositoryPath string, request RestoreRequest, enforceExpected bool) (restorePlan, error) {
	sourceOID, err := m.restoreSourceCommit(ctx, repositoryPath, request.Source)
	if err != nil {
		return restorePlan{}, err
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
		result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", sourceOID+"^{tree}")
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
		result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", currentOID+"^{tree}")
		if err != nil {
			return restorePlan{}, fmt.Errorf("resolve target tree: %w", err)
		}
		oldTree = strings.TrimSpace(string(result.Stdout))
	}
	changes, err := m.restoreChanges(ctx, repositoryPath, oldTree, resultTree)
	if err != nil {
		return restorePlan{}, err
	}
	patches, truncated, err := m.restorePatches(ctx, repositoryPath, oldTree, resultTree, changes)
	if err != nil {
		return restorePlan{}, err
	}
	preview := RestorePreview{
		SourceOID: sourceOID, TargetRef: targetRef, ExpectedHead: expected, CreatesBranch: !currentExists,
		ResultTree: resultTree, Changes: changes, Patches: patches, Selected: selected,
		CanApply: !currentExists || resultTree != oldTree, DiffTruncated: truncated,
	}
	return restorePlan{preview: preview, repository: repositoryPath, currentExists: currentExists, currentOID: currentOID}, nil
}

func (m *Manager) restoreSourceCommit(ctx context.Context, repositoryPath, source string) (string, error) {
	if !isOID(source) {
		return "", fmt.Errorf("%w: source must be a full object ID", ErrRestoreInvalid)
	}
	objectType, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "cat-file", "-t", source)
	if err != nil || strings.TrimSpace(string(objectType.Stdout)) != "commit" {
		return "", fmt.Errorf("%w: source is not a commit", ErrRestoreInvalid)
	}
	resolved, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", source+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: source commit is unavailable", ErrRestoreInvalid)
	}
	oid := strings.TrimSpace(string(resolved.Stdout))
	if oid != source {
		return "", fmt.Errorf("%w: source object did not resolve exactly", ErrRestoreInvalid)
	}
	return oid, nil
}

func (m *Manager) readBranch(ctx context.Context, repositoryPath, targetRef string) (string, bool, error) {
	_, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "show-ref", "--verify", "--quiet", targetRef)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read target branch: %w", err)
	}
	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", targetRef)
	if err != nil {
		return "", false, fmt.Errorf("resolve target branch: %w", err)
	}
	return strings.TrimSpace(string(result.Stdout)), true, nil
}

func (m *Manager) selectedRestoreTree(ctx context.Context, repositoryPath, sourceOID, targetOID string, paths []string) (string, map[string]bool, error) {
	if len(paths) == 0 || len(paths) > 10_000 {
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
	resultPaths := make([]string, 0, len(result))
	for filePath := range result {
		resultPaths = append(resultPaths, filePath)
	}
	sort.Strings(resultPaths)
	for index := 0; index+1 < len(resultPaths); index++ {
		if strings.HasPrefix(resultPaths[index+1], resultPaths[index]+"/") {
			return "", nil, fmt.Errorf("%w: selection would discard unselected descendants", ErrRestoreUnsupported)
		}
	}

	indexFile, err := os.CreateTemp(m.Git.TempDir, "owngit-restore-index-*")
	if err != nil {
		return "", nil, fmt.Errorf("create private restore index: %w", err)
	}
	indexPath := indexFile.Name()
	if err := indexFile.Close(); err != nil {
		return "", nil, err
	}
	if err := os.Remove(indexPath); err != nil {
		return "", nil, err
	}
	defer os.Remove(indexPath)
	environment := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := m.Git.RunWithEnvironment(ctx, "", nil, environment, "--git-dir", repositoryPath, "read-tree", targetOID); err != nil {
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
		if _, err := m.Git.RunWithEnvironment(ctx, "", &removals, environment, "--git-dir", repositoryPath, "update-index", "-z", "--index-info"); err != nil {
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
		if _, err := m.Git.RunWithEnvironment(ctx, "", &additions, environment, "--git-dir", repositoryPath, "update-index", "-z", "--index-info"); err != nil {
			return "", nil, fmt.Errorf("add source paths to private index: %w", err)
		}
	}
	written, err := m.Git.RunWithEnvironment(ctx, "", nil, environment, "--git-dir", repositoryPath, "write-tree")
	if err != nil {
		return "", nil, fmt.Errorf("write selected restore tree: %w", err)
	}
	return strings.TrimSpace(string(written.Stdout)), selected, nil
}

func (m *Manager) restoreTreeEntries(ctx context.Context, repositoryPath, commitOID string) (map[string]restoreTreeEntry, error) {
	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "ls-tree", "-r", "-z", "--full-tree", commitOID)
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
		if !ok || len(fields) != 3 || validateTreePath(string(name)) != nil {
			return nil, errors.New("Git returned malformed restore tree data")
		}
		entries[string(name)] = restoreTreeEntry{Mode: fields[0], Type: fields[1], OID: fields[2]}
	}
	return entries, nil
}

func (m *Manager) emptyTree(ctx context.Context, repositoryPath string) (string, error) {
	result, err := m.Git.Run(ctx, "", strings.NewReader(""), "--git-dir", repositoryPath, "mktree")
	if err != nil {
		return "", fmt.Errorf("create empty comparison tree: %w", err)
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func (m *Manager) restoreChanges(ctx context.Context, repositoryPath, oldTree, newTree string) ([]ChangedFile, error) {
	statusResult, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "diff-tree", "--no-commit-id", "--name-status", "--no-renames", "-r", "-z", oldTree, newTree)
	if err != nil {
		return nil, fmt.Errorf("read restore changes: %w", err)
	}
	tokens := bytes.Split(statusResult.Stdout, []byte{0})
	var changes []ChangedFile
	for index := 0; index+1 < len(tokens) && len(tokens[index]) != 0; index += 2 {
		status := tokens[index]
		if len(status) != 1 || len(tokens[index+1]) == 0 {
			return nil, errors.New("Git returned malformed restore changes")
		}
		changes = append(changes, ChangedFile{Path: string(tokens[index+1]), Status: changedStatus(status[0])})
	}
	numResult, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "diff-tree", "--no-commit-id", "--numstat", "--no-renames", "-r", "-z", oldTree, newTree)
	if err != nil {
		return nil, fmt.Errorf("read restore change sizes: %w", err)
	}
	counts := parseNumstat(numResult.Stdout)
	for index := range changes {
		count := counts[changes[index].Path]
		changes[index].Additions = count.additions
		changes[index].Deletions = count.deletions
		changes[index].Binary = count.binary
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
		if change.Binary {
			continue
		}
		result, err := m.Git.RunWithOutputLimit(ctx, "", nil, 256<<10,
			"--git-dir", repositoryPath, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=3", oldTree, newTree, "--", ":(top,literal)"+change.Path)
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
