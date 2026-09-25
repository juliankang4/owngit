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

func (m *Manager) Summary(ctx context.Context, id string) (Summary, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return Summary{}, err
	}
	lock := m.Locks.For(id)
	if err := readLock(ctx, lock); err != nil {
		return Summary{}, err
	}
	defer lock.RUnlock()

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
	result, err := runner.RunWithOutputLimit(ctx, repositoryPath, strings.NewReader(strings.Join(unique, "\n")+"\n"), 64<<20,
		"--git-dir", ".", "log", "--no-walk", "--stdin", "-z", "--no-decorate", "--format="+commitLogFormat)
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
