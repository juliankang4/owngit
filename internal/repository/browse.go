package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"owngit/internal/gitexec"
)

type Ref struct {
	Name string
	OID  string
	Type string
}

type Summary struct {
	DefaultBranch string
	DefaultOID    string
	Branches      []Ref
	Tags          []Ref
	Empty         bool
}

type TreeEntry struct {
	Name string
	Path string
	OID  string
	Type string
	Mode string
	Size int64
}

type Blob struct {
	Path      string
	OID       string
	Content   []byte
	Binary    bool
	Truncated bool
}

const commitLogFormat = "%H%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s%x00%b"

var errTreeEntryNotFound = errors.New("tree entry not found")

type Commit struct {
	OID            string
	Parents        []string
	AuthorName     string
	AuthorEmail    string
	AuthoredAt     time.Time
	CommitterName  string
	CommitterEmail string
	CommittedAt    time.Time
	Subject        string
	Body           string
}

type ChangedFile struct {
	Path      string
	OldPath   string
	Status    string
	Additions int
	Deletions int
	Binary    bool
}

type CommitDetail struct {
	Commit
	Diff      string
	Truncated bool
}

func (m *Manager) Summary(ctx context.Context, id string) (Summary, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return Summary{}, err
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()

	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(objecttype)", "refs/heads", "refs/tags", "refs/owngit/retained")
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	hasRetained := false
	for _, line := range bytes.Split(bytes.TrimSpace(result.Stdout), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{0}, 3)
		if len(parts) != 3 {
			return Summary{}, errors.New("Git returned a malformed ref record")
		}
		name, oid, objectType := string(parts[0]), string(parts[1]), string(parts[2])
		switch {
		case strings.HasPrefix(name, "refs/heads/"):
			summary.Branches = append(summary.Branches, Ref{Name: strings.TrimPrefix(name, "refs/heads/"), OID: oid, Type: objectType})
		case strings.HasPrefix(name, "refs/tags/"):
			summary.Tags = append(summary.Tags, Ref{Name: strings.TrimPrefix(name, "refs/tags/"), OID: oid, Type: objectType})
		case strings.HasPrefix(name, "refs/owngit/retained/"):
			hasRetained = true
		}
	}
	summary.Empty = len(summary.Branches) == 0 && len(summary.Tags) == 0 && !hasRetained
	head, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		full := strings.TrimSpace(string(head.Stdout))
		if strings.HasPrefix(full, "refs/heads/") {
			summary.DefaultBranch = strings.TrimPrefix(full, "refs/heads/")
			for _, branch := range summary.Branches {
				if branch.Name == summary.DefaultBranch {
					summary.DefaultOID = branch.OID
					break
				}
			}
		}
	}
	return summary, nil
}

func (m *Manager) ResolveRef(ctx context.Context, id, requested string) (string, string, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return "", "", err
	}
	if requested == "" {
		result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "symbolic-ref", "--quiet", "HEAD")
		if err != nil {
			return "", "", errors.New("repository has no default branch")
		}
		requested = strings.TrimSpace(string(result.Stdout))
		if !strings.HasPrefix(requested, "refs/heads/") {
			return "", "", errors.New("repository HEAD is not a branch")
		}
	}
	if strings.HasPrefix(requested, "refs/heads/") || strings.HasPrefix(requested, "refs/tags/") {
		short := strings.TrimPrefix(strings.TrimPrefix(requested, "refs/heads/"), "refs/tags/")
		if err := validateShortRef(short); err != nil {
			return "", "", err
		}
		result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", requested+"^{commit}")
		if err == nil {
			return requested, strings.TrimSpace(string(result.Stdout)), nil
		}
		return "", "", errors.New("branch or tag not found")
	}
	if err := validateShortRef(requested); err != nil {
		return "", "", err
	}
	// Keep accepting historical short URLs, with the documented branch-first
	// precedence. Newly generated URLs always carry the full ref identity.
	for _, namespace := range []string{"refs/heads/", "refs/tags/"} {
		full := namespace + requested
		result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", full+"^{commit}")
		if err == nil {
			return full, strings.TrimSpace(string(result.Stdout)), nil
		}
	}
	return "", "", errors.New("branch or tag not found")
}

func (m *Manager) CommitReachableFrom(ctx context.Context, id, rootOID, commitOID string) (bool, error) {
	if !isOID(rootOID) || !isOID(commitOID) {
		return false, errors.New("invalid commit ID")
	}
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		return false, errors.New("repository not found")
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	_, err = m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "merge-base", "--is-ancestor", commitOID, rootOID)
	if err == nil {
		return true, nil
	}
	if code, ok := gitexec.ExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, err
}

func (m *Manager) Tree(ctx context.Context, id, requestedRef, directory string) (string, []TreeEntry, error) {
	if err := validateTreePath(directory); err != nil {
		return "", nil, err
	}
	_, commitOID, err := m.ResolveRef(ctx, id, requestedRef)
	if err != nil {
		return "", nil, err
	}
	repositoryPath, _, _, _ := m.ExistingPath(ctx, id)
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	treeOID := commitOID
	if directory != "" {
		entry, err := m.lookupTreeEntry(ctx, repositoryPath, commitOID, directory)
		if err != nil {
			if errors.Is(err, errTreeEntryNotFound) {
				return "", nil, errors.New("directory not found")
			}
			return "", nil, err
		}
		if entry.Type != "tree" {
			return "", nil, errors.New("directory not found")
		}
		treeOID = entry.OID
	}
	entries, err := m.listTree(ctx, repositoryPath, treeOID, directory)
	if err != nil {
		return "", nil, err
	}
	return commitOID, entries, nil
}

func (m *Manager) lookupTreeEntry(ctx context.Context, repositoryPath, rootOID, filePath string) (TreeEntry, error) {
	if !isOID(rootOID) {
		return TreeEntry{}, errors.New("invalid tree ID")
	}
	// The explicit magic prefix was verified with Git for Windows. It keeps the
	// repository path literal without disabling pathspec magic parsing globally.
	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "ls-tree", "-z", "-l", rootOID, "--", ":(top,literal)"+filePath)
	if err != nil {
		return TreeEntry{}, err
	}
	records := bytes.Split(bytes.TrimSuffix(result.Stdout, []byte{0}), []byte{0})
	if len(records) == 1 && len(records[0]) == 0 {
		return TreeEntry{}, errTreeEntryNotFound
	}
	if len(records) != 1 {
		return TreeEntry{}, errors.New("Git returned ambiguous tree lookup data")
	}
	entry, err := parseTreeEntry(records[0])
	if err != nil {
		return TreeEntry{}, err
	}
	if entry.Name != filePath {
		return TreeEntry{}, errors.New("Git returned mismatched tree lookup data")
	}
	entry.Path = filePath
	if separator := strings.LastIndexByte(filePath, '/'); separator >= 0 {
		entry.Name = filePath[separator+1:]
	}
	return entry, nil
}

func (m *Manager) listTree(ctx context.Context, repositoryPath, treeOID, prefix string) ([]TreeEntry, error) {
	if !isOID(treeOID) {
		return nil, errors.New("invalid tree ID")
	}
	// Listing a tree object without a pathspec avoids host path normalization.
	// Runner output limits keep each untrusted tree listing bounded.
	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "ls-tree", "-z", "-l", treeOID)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, record := range bytes.Split(result.Stdout, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		entry, err := parseTreeEntry(record)
		if err != nil {
			return nil, err
		}
		entry.Path = entry.Name
		if prefix != "" {
			entry.Path = prefix + "/" + entry.Name
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseTreeEntry(record []byte) (TreeEntry, error) {
	metadata, nameBytes, ok := bytes.Cut(record, []byte{'\t'})
	if !ok || len(nameBytes) == 0 {
		return TreeEntry{}, errors.New("Git returned a malformed tree entry")
	}
	fields := strings.Fields(string(metadata))
	if len(fields) != 4 || !isOID(fields[2]) {
		return TreeEntry{}, errors.New("Git returned malformed tree metadata")
	}
	size := int64(-1)
	if fields[3] != "-" {
		parsed, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || parsed < 0 {
			return TreeEntry{}, errors.New("Git returned an invalid tree entry size")
		}
		size = parsed
	}
	return TreeEntry{Name: string(nameBytes), Mode: fields[0], Type: fields[1], OID: fields[2], Size: size}, nil
}

func (m *Manager) ReadBlob(ctx context.Context, id, requestedRef, filePath string, limit int64) (string, Blob, error) {
	if err := validateTreePath(filePath); err != nil || filePath == "" {
		if err == nil {
			err = errors.New("file path is required")
		}
		return "", Blob{}, err
	}
	_, commitOID, err := m.ResolveRef(ctx, id, requestedRef)
	if err != nil {
		return "", Blob{}, err
	}
	repositoryPath, _, _, _ := m.ExistingPath(ctx, id)
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	entry, err := m.lookupTreeEntry(ctx, repositoryPath, commitOID, filePath)
	if err != nil {
		if errors.Is(err, errTreeEntryNotFound) {
			return "", Blob{}, errors.New("file not found")
		}
		return "", Blob{}, err
	}
	if entry.Type != "blob" {
		return "", Blob{}, errors.New("file not found")
	}
	if limit <= 0 {
		limit = 2 << 20
	}
	result, runErr := m.Git.RunWithOutputLimit(ctx, "", nil, limit+1, "--git-dir", repositoryPath, "cat-file", "blob", entry.OID)
	blob := Blob{Path: filePath, OID: entry.OID, Content: result.Stdout, Binary: bytes.IndexByte(result.Stdout, 0) >= 0}
	var limitErr *gitexec.LimitError
	if runErr != nil {
		if errors.As(runErr, &limitErr) && len(result.Stdout) >= int(limit) {
			blob.Content = result.Stdout[:limit]
			blob.Truncated = true
			return commitOID, blob, nil
		}
		return "", Blob{}, runErr
	}
	if int64(len(blob.Content)) > limit {
		blob.Content = blob.Content[:limit]
		blob.Truncated = true
	}
	return commitOID, blob, nil
}

func (m *Manager) Commits(ctx context.Context, id, requestedRef string, limit int) (string, []Commit, error) {
	_, commitOID, err := m.ResolveRef(ctx, id, requestedRef)
	if err != nil {
		return "", nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return "", nil, err
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "log", "-z", "--no-decorate", "--max-count="+strconv.Itoa(limit), "--format="+commitLogFormat, commitOID)
	if err != nil {
		return "", nil, err
	}
	commits, err := parseCommits(result.Stdout)
	return commitOID, commits, err
}

// RefTips resolves commit metadata for branch and tag refs in two bounded Git
// calls instead of two per ref. Refs that do not resolve to a commit are
// omitted. The returned map is keyed by the ref's short name, so callers pass
// branches and tags separately when their names can collide.
func (m *Manager) RefTips(ctx context.Context, id string, refs []Ref) (map[string]Commit, error) {
	if len(refs) == 0 {
		return map[string]Commit{}, nil
	}
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return nil, err
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	commitOIDs := make(map[string]string, len(refs))
	var annotated []string
	for _, ref := range refs {
		switch ref.Type {
		case "commit":
			commitOIDs[ref.Name] = ref.OID
		case "tag":
			annotated = append(annotated, ref.OID)
		}
	}
	if len(annotated) != 0 {
		peeled, err := batchPeelRetainedTags(ctx, m.Git, repositoryPath, annotated)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			if ref.Type != "tag" {
				continue
			}
			if terminal, ok := peeled[ref.OID]; ok && terminal.objectType == "commit" {
				commitOIDs[ref.Name] = terminal.oid
			}
		}
	}
	oids := make([]string, 0, len(commitOIDs))
	for _, oid := range commitOIDs {
		oids = append(oids, oid)
	}
	metadata, err := commitMetadataByOID(ctx, m.Git, repositoryPath, oids)
	if err != nil {
		return nil, err
	}
	tips := make(map[string]Commit, len(commitOIDs))
	for name, oid := range commitOIDs {
		if commit, ok := metadata[oid]; ok {
			tips[name] = commit
		}
	}
	return tips, nil
}

func commitMetadataByOID(ctx context.Context, runner retainedRunner, repositoryPath string, oids []string) (map[string]Commit, error) {
	metadata := make(map[string]Commit)
	if len(oids) == 0 {
		return metadata, nil
	}
	unique := make([]string, 0, len(oids))
	for _, oid := range oids {
		if !isOID(oid) {
			return nil, errors.New("invalid commit ID")
		}
		if _, exists := metadata[oid]; !exists {
			metadata[oid] = Commit{}
			unique = append(unique, oid)
		}
	}
	result, err := runner.RunWithOutputLimit(ctx, "", strings.NewReader(strings.Join(unique, "\n")+"\n"), 64<<20,
		"--git-dir", repositoryPath, "log", "--no-walk", "--stdin", "-z", "--no-decorate", "--format="+commitLogFormat)
	if err != nil {
		return nil, err
	}
	commits, err := parseCommits(result.Stdout)
	if err != nil {
		return nil, err
	}
	for _, commit := range commits {
		metadata[commit.OID] = commit
	}
	for _, oid := range unique {
		if metadata[oid].OID == "" {
			return nil, errors.New("Git omitted requested commit metadata")
		}
	}
	return metadata, nil
}

func (m *Manager) Commit(ctx context.Context, id, oid, filePath string) (CommitDetail, error) {
	if !isOID(oid) {
		return CommitDetail{}, errors.New("invalid commit ID")
	}
	if filePath != "" {
		if err := validateTreePath(filePath); err != nil {
			return CommitDetail{}, err
		}
	}
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		return CommitDetail{}, errors.New("repository not found")
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "show", "-z", "--quiet", "--format="+commitLogFormat, oid)
	if err != nil {
		return CommitDetail{}, errors.New("commit not found")
	}
	commits, err := parseCommits(result.Stdout)
	if err != nil || len(commits) != 1 {
		return CommitDetail{}, errors.New("Git returned malformed commit metadata")
	}
	args := []string{"--git-dir", repositoryPath, "show", "--format=", "--no-ext-diff", "--no-textconv", "--find-renames=50%", "--unified=3", oid}
	if filePath != "" {
		args = append(args, "--", ":(top,literal)"+filePath)
	}
	diffResult, diffErr := m.Git.Run(ctx, "", nil, args...)
	detail := CommitDetail{Commit: commits[0], Diff: string(diffResult.Stdout)}
	if diffErr != nil {
		var limitErr *gitexec.LimitError
		if errors.As(diffErr, &limitErr) {
			detail.Truncated = true
			return detail, nil
		}
		return CommitDetail{}, diffErr
	}
	return detail, nil
}

func (m *Manager) ChangedFiles(ctx context.Context, id, oid string) ([]ChangedFile, error) {
	if !isOID(oid) {
		return nil, errors.New("invalid commit ID")
	}
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		return nil, errors.New("repository not found")
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	statusResult, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "diff-tree", "--root", "--no-commit-id", "--name-status", "-r", "-z", oid)
	if err != nil {
		return nil, err
	}
	tokens := bytes.Split(statusResult.Stdout, []byte{0})
	var files []ChangedFile
	for index := 0; index < len(tokens) && len(tokens[index]) != 0; {
		statusToken := string(tokens[index])
		index++
		if index >= len(tokens) {
			return nil, errors.New("Git returned malformed changed-file status")
		}
		code := statusToken[0]
		file := ChangedFile{Status: changedStatus(code)}
		if code == 'R' || code == 'C' {
			if index+1 >= len(tokens) {
				return nil, errors.New("Git returned malformed rename status")
			}
			file.OldPath = string(tokens[index])
			file.Path = string(tokens[index+1])
			index += 2
		} else {
			file.Path = string(tokens[index])
			index++
		}
		files = append(files, file)
	}
	numResult, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "diff-tree", "--root", "--no-commit-id", "--numstat", "-r", "-z", oid)
	if err != nil {
		return nil, err
	}
	counts := parseNumstat(numResult.Stdout)
	for index := range files {
		if count, ok := counts[files[index].Path]; ok {
			files[index].Additions = count.additions
			files[index].Deletions = count.deletions
			files[index].Binary = count.binary
		}
	}
	return files, nil
}

type lineCount struct {
	additions int
	deletions int
	binary    bool
}

func parseNumstat(output []byte) map[string]lineCount {
	counts := make(map[string]lineCount)
	tokens := bytes.Split(output, []byte{0})
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if len(token) == 0 {
			continue
		}
		fields := bytes.SplitN(token, []byte{'\t'}, 3)
		if len(fields) != 3 {
			continue
		}
		path := string(fields[2])
		if path == "" && index+2 < len(tokens) {
			path = string(tokens[index+2])
			index += 2
		}
		count := lineCount{}
		if string(fields[0]) == "-" || string(fields[1]) == "-" {
			count.binary = true
		} else {
			count.additions, _ = strconv.Atoi(string(fields[0]))
			count.deletions, _ = strconv.Atoi(string(fields[1]))
		}
		counts[path] = count
	}
	return counts
}

func changedStatus(code byte) string {
	switch code {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	default:
		return "modified"
	}
}

func parseCommits(output []byte) ([]Commit, error) {
	fields := bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0})
	if len(fields) == 1 && len(fields[0]) == 0 {
		return nil, nil
	}
	if len(fields)%10 != 0 {
		return nil, errors.New("Git returned malformed commit metadata")
	}
	commits := make([]Commit, 0, len(fields)/10)
	for offset := 0; offset < len(fields); offset += 10 {
		record := fields[offset : offset+10]
		authored, err := time.Parse(time.RFC3339, string(record[4]))
		if err != nil {
			return nil, fmt.Errorf("parse commit author date: %w", err)
		}
		committed, err := time.Parse(time.RFC3339, string(record[7]))
		if err != nil {
			return nil, fmt.Errorf("parse commit committer date: %w", err)
		}
		commits = append(commits, Commit{
			OID: string(record[0]), Parents: strings.Fields(string(record[1])), AuthorName: string(record[2]),
			AuthorEmail: string(record[3]), AuthoredAt: authored, CommitterName: string(record[5]), CommitterEmail: string(record[6]),
			CommittedAt: committed, Subject: string(record[8]), Body: strings.TrimSpace(string(record[9])),
		})
	}
	return commits, nil
}

func validateShortRef(value string) error {
	if value == "" || len(value) > 255 || strings.HasPrefix(value, "-") || strings.Contains(value, "..") || strings.ContainsAny(value, " ~^:?*[\\") || strings.HasSuffix(value, ".") || strings.HasSuffix(value, "/") || strings.Contains(value, "@{") {
		return errors.New("invalid branch or tag name")
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || strings.HasSuffix(component, ".lock") {
			return errors.New("invalid branch or tag name")
		}
	}
	return nil
}

func validateTreePath(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 4096 || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return errors.New("invalid repository path")
	}
	return nil
}

func isOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
