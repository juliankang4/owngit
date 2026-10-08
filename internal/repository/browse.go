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
	Path    string
	OID     string
	Content []byte
	Binary  bool
	// Truncated is true when a longer file was cut at the read limit.
	Truncated bool
	// TooLarge is true when a known memory bound refused the read before
	// Git ran. The result has no content and is not a prefix, so callers
	// show their own too-large state instead of an empty file.
	TooLarge bool
	// TooLargeMemory is true when the refusal above is not the size of this
	// file but the memory its stored delta needs to rebuild: a small file on
	// a much larger base costs more to read than this computer gives Git.
	TooLargeMemory bool
}

const commitLogFormat = "%H%x00%P%x00%an%x00%ae%x00%ad%x00%cn%x00%ce%x00%cd%x00%s%x00%b"

// Overview and kept-history consumers use only the OID, author, dates and
// subject. Empty unused fields retain the common parser and date validation.
const tipLogFormat = "%H%x00%x00%an%x00%x00%ad%x00%x00%x00%cd%x00%s%x00"

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
	Path    string
	OldPath string
	Status  string
	// OldMode and NewMode are the Git file modes before and after the
	// change, such as 100644, 100755, 120000 (a symbolic link) or 160000 (a
	// submodule), and 000000 where the path did not exist.
	OldMode             string
	NewMode             string
	Additions           int
	Deletions           int
	Binary              bool
	TextDiffUnavailable bool
	// CountsRead is true when the added and deleted line counts were read. A
	// change left out of that read, because it is above the memory line or
	// because more changes are than one command line can leave out, keeps a
	// zero count that no read established, and callers show it as unknown.
	CountsRead bool
	oldOID     string
	newOID     string
}

// readLockedPath resolves a repository and takes its read lock. The caller
// calls unlock when done.
func (m *Manager) readLockedPath(ctx context.Context, id string) (path string, unlock func(), err error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return "", nil, err
	}
	lock := m.Locks.For(id)
	if err := readLock(ctx, lock); err != nil {
		return "", nil, err
	}
	return repositoryPath, lock.RUnlock, nil
}

// RefState identifies the ref writes OwnGit had made to a repository: every
// release of the write lock after a ref change advances Generation, and
// Incarnation changes when the repository is removed.
type RefState struct{ Generation, Incarnation uint64 }

// BranchHeads lists the branch heads of a repository with one Git process. It
// waits for a writer like Summary does, and reads the refs as they are. When
// seen is not nil and the repository's RefState still equals it once the
// writer is done, no OwnGit writer changed a ref since, so it returns
// unchanged without running Git. The returned state is the one the read
// belongs to.
func (m *Manager) BranchHeads(ctx context.Context, id string, seen *RefState) (heads []Ref, current RefState, unchanged bool, err error) {
	repositoryPath, unlock, err := m.readLockedPath(ctx, id)
	if err != nil {
		return nil, RefState{}, false, err
	}
	defer unlock()
	lock := m.Locks.For(id)
	current = RefState{Generation: lock.Generation(), Incarnation: lock.Incarnation()}
	if seen != nil && *seen == current {
		return nil, current, true, nil
	}
	result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "for-each-ref", "--format=%(refname)%00%(objectname)%00%(objecttype)", "refs/heads")
	if err != nil {
		return nil, current, false, err
	}
	heads = nil
	for _, line := range bytes.Split(bytes.TrimSpace(result.Stdout), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		// The object type makes Git read each object, so a ref to a missing
		// object fails here exactly as it does in Summary.
		parts := bytes.SplitN(line, []byte{0}, 3)
		if len(parts) != 3 {
			return nil, current, false, errors.New("Git returned a malformed ref record")
		}
		heads = append(heads, Ref{Name: strings.TrimPrefix(string(parts[0]), "refs/heads/"), OID: string(parts[1]), Type: string(parts[2])})
	}
	return heads, current, false, nil
}

func (m *Manager) Summary(ctx context.Context, id string) (Summary, error) {
	repositoryPath, unlock, err := m.readLockedPath(ctx, id)
	if err != nil {
		return Summary{}, err
	}
	defer unlock()

	result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "for-each-ref", "--format=%(refname)%00%(objectname)%00%(objecttype)", "refs/heads", "refs/tags", "refs/owngit/retained")
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
	head, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "--quiet", "HEAD")
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
	if err := readLock(ctx, lock); err != nil {
		return nil, err
	}
	defer lock.RUnlock()
	return refTips(ctx, m.Git, repositoryPath, refs)
}

func refTips(ctx context.Context, runner retainedRunner, repositoryPath string, refs []Ref) (map[string]Commit, error) {
	return refTipsWithReads(ctx, runner, repositoryPath, refs, nil)
}

func refTipsWithReads(ctx context.Context, runner retainedRunner, repositoryPath string, refs []Ref, reads *snapshotReads) (map[string]Commit, error) {
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
		peeled, err := readPeeledTags(ctx, runner, repositoryPath, annotated, reads)
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
	metadata, err := readCommitMetadata(ctx, runner, repositoryPath, oids, reads)
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
	return commitMetadataWithFormat(ctx, runner, repositoryPath, oids, commitLogFormat)
}

func commitMetadataWithFormat(ctx context.Context, runner retainedRunner, repositoryPath string, oids []string, format string) (map[string]Commit, error) {
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
	result, err := runner.RunWithOutputLimit(ctx, repositoryPath, strings.NewReader(strings.Join(unique, "\n")+"\n"), 64<<20,
		"--git-dir", ".", "log", "--no-walk", "--stdin", "-z", "--no-decorate", GitDateOption, "--format="+format)
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

type lineCount struct {
	additions int
	deletions int
	binary    bool
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
	var unreadable *UnreadableCommitError
	for offset := 0; offset < len(fields); offset += 10 {
		record := fields[offset : offset+10]
		oid := string(record[0])
		var cause error
		authored, err := ParseGitDate(record[4])
		if err != nil {
			cause = fmt.Errorf("author date: %w", err)
		}
		committed, err := ParseGitDate(record[7])
		if err != nil && cause == nil {
			cause = fmt.Errorf("committer date: %w", err)
		}
		switch {
		case cause == nil:
			commits = append(commits, Commit{
				OID: oid, Parents: strings.Fields(string(record[1])), AuthorName: string(record[2]),
				AuthorEmail: string(record[3]), AuthoredAt: authored, CommitterName: string(record[5]), CommitterEmail: string(record[6]),
				CommittedAt: committed, Subject: string(record[8]), Body: strings.TrimSpace(string(record[9])),
			})
		case unreadable == nil:
			unreadable = unreadableCommit(oid, cause)
		default:
			unreadable.OIDs = append(unreadable.OIDs, oid)
		}
	}
	if unreadable != nil {
		return commits, unreadable
	}
	return commits, nil
}

// UnreadableCommitError names commits that exist but whose metadata OwnGit
// cannot show, such as a pushed commit without an author line, whose author
// date Git prints empty. A reader that returns it also returns, in order,
// every other commit it read, so a page can show those and name these.
type UnreadableCommitError struct {
	OIDs []string
	// Cause says why the first of them could not be read.
	Cause error
}

func (e *UnreadableCommitError) Error() string {
	if len(e.OIDs) == 1 {
		return "commit " + e.OIDs[0] + " could not be read: " + e.Cause.Error()
	}
	return "commits " + strings.Join(e.OIDs, ", ") + " could not be read: " + e.Cause.Error()
}

func (e *UnreadableCommitError) Unwrap() error { return e.Cause }

func unreadableCommit(oid string, cause error) *UnreadableCommitError {
	return &UnreadableCommitError{OIDs: []string{oid}, Cause: cause}
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
	if len(value) > MaximumTreePathBytes || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
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
