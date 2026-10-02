package repository

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"owngit/internal/gitexec"
)

type ActivityDay struct {
	Day   string
	Count int
}

type Activity struct {
	Records    []ActivityRecord
	Days       []ActivityDay
	Commits    int
	Incomplete bool
	// Key identifies the refs this observation was computed from. Two
	// observations with the same Key and limit are identical, so callers may
	// cache an observation until RefSnapshot reports a different key.
	Key string
}

type RetainedRef struct {
	Kind string
	// Source is the full name of the branch or tag whose earlier value this
	// is, such as refs/heads/main.
	Source    string
	OID       string
	CommitOID string
	Commit    Commit
}

type retainedRunner interface {
	Run(context.Context, string, io.Reader, ...string) (gitexec.Result, error)
	RunWithOutputLimit(context.Context, string, io.Reader, int64, ...string) (gitexec.Result, error)
	StreamGit(context.Context, string, func(io.Reader) error, ...string) ([]byte, error)
}

type ActivityRecord struct {
	OID        string
	Source     string
	Retained   bool
	Uncertain  bool
	AuthorName string
	AuthoredAt time.Time
	Subject    string
}

// Activity uses each commit author's recorded calendar day, including its
// recorded UTC offset. A commit reachable from multiple roots is counted once.
// Current history is scanned first, then retained history that is not reachable
// from a current root, so a capped observation stays current-history-first and
// reports itself incomplete instead of claiming a differently ordered scan.
func (m *Manager) Activity(ctx context.Context, id string, maximumCommits int) (Activity, error) {
	if maximumCommits <= 0 {
		maximumCommits = 200_000
	}
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return Activity{}, err
	}
	if !exists {
		return Activity{}, ErrRepositoryNotFound
	}
	lock := m.Locks.For(id)
	if err := readLock(ctx, lock); err != nil {
		return Activity{}, err
	}
	defer lock.RUnlock()
	// The ref snapshot supplies the roots, the retained provenance, and the
	// key, so the key describes exactly the refs this observation used. A
	// page that listed the repository's refs has cached it, so the history
	// walk is the only Git process here.
	snapshot, err := m.lockedRefSnapshot(ctx, id, repositoryPath, lock)
	if err != nil {
		return Activity{}, err
	}
	var current, retained []string
	for _, ref := range snapshot.activityRefs {
		if strings.HasPrefix(ref.name, "refs/heads/") {
			current = append(current, ref.name)
		} else if strings.HasPrefix(ref.name, "refs/owngit/retained/heads/") {
			retained = append(retained, ref.name)
		}
	}
	key := snapshot.ActivityKey
	if len(current) == 0 && len(retained) == 0 {
		return Activity{Key: key}, nil
	}
	provenance := parseRetainedProvenance(snapshot.activityRefs, "heads")

	// Current history is scanned first. Retained history explicitly excludes
	// every current root, so a reachable commit is never mislabeled as detached
	// merely because it is also protected by a hidden retention ref.
	records, currentMore, err := m.activityLog(ctx, repositoryPath, current, nil, maximumCommits)
	if err != nil {
		return Activity{}, err
	}
	incomplete := currentMore
	if !incomplete && len(retained) != 0 {
		remaining := maximumCommits - len(records)
		if remaining > 0 {
			lost, retainedMore, err := m.activityLog(ctx, repositoryPath, retained, current, remaining)
			if err != nil {
				return Activity{}, err
			}
			for index := range lost {
				rootOID := strings.TrimPrefix(lost[index].Source, "refs/owngit/retained/heads/")
				lost[index].Source = provenance[rootOID]
				lost[index].Retained = lost[index].Source != ""
				lost[index].Uncertain = lost[index].Source == ""
			}
			records = append(records, lost...)
			incomplete = retainedMore
		} else {
			// The current walk filled the budget exactly, so retained history
			// was not observed and the observation is not complete.
			incomplete = true
		}
	}
	activity := Activity{Records: records, Commits: len(records), Incomplete: incomplete, Key: key}
	counts := make(map[string]int)
	for _, record := range records {
		counts[record.AuthoredAt.Format("2006-01-02")]++
	}
	activity.Days = make([]ActivityDay, 0, len(counts))
	for day, count := range counts {
		activity.Days = append(activity.Days, ActivityDay{Day: day, Count: count})
	}
	slicesSortDays(activity.Days)
	return activity, nil
}

func (m *Manager) activityLog(ctx context.Context, repositoryPath string, roots, excluded []string, maximum int) ([]ActivityRecord, bool, error) {
	if len(roots) == 0 || maximum <= 0 {
		return nil, len(roots) != 0, nil
	}
	input := activityRevisionInput(roots, excluded)
	format := "%H%x00%S%x00%ad%x00%an%x00%s"
	args := []string{"--git-dir", ".", "log", "--stdin", "-z", "--source", "--no-decorate", "--max-count=" + strconv.Itoa(maximum+1), GitDateOption, "--format=" + format}
	result, err := m.Git.RunWithOutputLimit(ctx, repositoryPath, strings.NewReader(input), 64<<20, args...)
	if err != nil {
		return nil, false, err
	}
	fields := bytes.Split(bytes.TrimSuffix(result.Stdout, []byte{0}), []byte{0})
	if len(fields) == 1 && len(fields[0]) == 0 {
		return nil, false, nil
	}
	if len(fields)%5 != 0 {
		return nil, false, errors.New("Git returned malformed activity metadata")
	}
	records := make([]ActivityRecord, 0, len(fields)/5)
	for offset := 0; offset < len(fields); offset += 5 {
		record := fields[offset : offset+5]
		authored, err := ParseGitDate(record[2])
		if err != nil {
			return nil, false, unreadableCommit(string(record[0]), fmt.Errorf("author date: %w", err))
		}
		records = append(records, ActivityRecord{
			OID: string(record[0]), Source: string(record[1]), AuthorName: string(record[3]), AuthoredAt: authored, Subject: string(record[4]),
		})
	}
	incomplete := len(records) > maximum
	if incomplete {
		records = records[:maximum]
	}
	return records, incomplete, nil
}

func activityRevisionInput(roots, excluded []string) string {
	var input strings.Builder
	for _, root := range roots {
		input.WriteString(root)
		input.WriteByte('\n')
	}
	for _, root := range excluded {
		input.WriteByte('^')
		input.WriteString(root)
		input.WriteByte('\n')
	}
	return input.String()
}

// parseRetainedProvenance maps retained object IDs to the branch or tag they
// were retained from. Refs outside the kind's provenance namespace are
// ignored. An object retained from more than one source maps to "" because
// its source is ambiguous.
func parseRetainedProvenance(refs []activityKeyRef, kind string) map[string]string {
	prefix := "refs/owngit/provenance/" + kind + "/"
	provenance := make(map[string]string)
	for _, listed := range refs {
		oid, ref := listed.oid, listed.name
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(ref, prefix)
		last := strings.LastIndex(remainder, "/")
		if last <= 0 || !isOID(remainder[last+1:]) {
			continue
		}
		source := "refs/" + kind + "/" + remainder[:last]
		if validateShortRef(remainder[:last]) != nil {
			continue
		}
		if previous, exists := provenance[oid]; exists && previous != source {
			provenance[oid] = ""
		} else if !exists {
			provenance[oid] = source
		}
	}
	return provenance
}

func (m *Manager) RetainedRefs(ctx context.Context, id string) ([]RetainedRef, error) {
	snapshot, err := m.RefSnapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.RetainedRefsAt(ctx, id, snapshot)
}

func retainedRefs(ctx context.Context, runner retainedRunner, repositoryPath string) ([]RetainedRef, error) {
	result, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "for-each-ref", "--format=%(refname)%00%(objectname)%00%(objecttype)", "refs/heads", "refs/tags", "refs/owngit/retained", "refs/owngit/provenance")
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range bytes.Split(bytes.TrimSpace(result.Stdout), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{0}, 3)
		if len(parts) != 3 {
			return nil, errors.New("Git returned malformed retained ref data")
		}
		refs = append(refs, Ref{Name: string(parts[0]), OID: string(parts[1]), Type: string(parts[2])})
	}
	return retainedRefsFromSnapshot(ctx, runner, repositoryPath, refs, nil)
}

func retainedRefsFromSnapshot(ctx context.Context, runner retainedRunner, repositoryPath string, refs []Ref, reads *snapshotReads) ([]RetainedRef, error) {
	current := make(map[string]string)
	var provenance []activityKeyRef
	for _, ref := range refs {
		if strings.HasPrefix(ref.Name, "refs/heads/") || strings.HasPrefix(ref.Name, "refs/tags/") {
			current[ref.Name] = ref.OID
		}
		provenance = append(provenance, activityKeyRef{name: ref.Name, oid: ref.OID})
	}
	headSources := parseRetainedProvenance(provenance, "heads")
	tagSources := parseRetainedProvenance(provenance, "tags")
	type candidate struct {
		kind, source, oid, objectType, peeledOID, peeledType string
	}
	var candidates []candidate
	for _, ref := range refs {
		name, oid := ref.Name, ref.OID
		kind, source := "", ""
		switch {
		case strings.HasPrefix(name, "refs/owngit/retained/heads/"):
			kind, source = "branch", headSources[oid]
		case strings.HasPrefix(name, "refs/owngit/retained/tags/"):
			kind, source = "tag", tagSources[oid]
		}
		if source == "" || current[source] == oid {
			continue
		}
		candidates = append(candidates, candidate{
			kind: kind, source: source, oid: oid, objectType: ref.Type,
		})
	}
	var annotatedTagOIDs []string
	for _, candidate := range candidates {
		if candidate.objectType == "tag" {
			annotatedTagOIDs = append(annotatedTagOIDs, candidate.oid)
		}
	}
	if len(annotatedTagOIDs) != 0 {
		terminal, err := readPeeledTags(ctx, runner, repositoryPath, annotatedTagOIDs, reads)
		if err != nil {
			return nil, err
		}
		for index := range candidates {
			if peeled, exists := terminal[candidates[index].oid]; exists {
				candidates[index].peeledOID = peeled.oid
				candidates[index].peeledType = peeled.objectType
			}
		}
	}

	// Walk each current branch tip once. Keep only candidate OIDs, so even
	// a long history has bounded output memory and no per-retained-ref process.
	ancestors := make(map[string]map[string]bool)
	for _, candidate := range candidates {
		currentOID := current[candidate.source]
		if candidate.kind != "branch" || currentOID == "" {
			continue
		}
		if ancestors[currentOID] == nil {
			ancestors[currentOID] = make(map[string]bool)
		}
		ancestors[currentOID][candidate.oid] = false
	}
	for currentOID, found := range ancestors {
		_, err := runner.StreamGit(ctx, repositoryPath, func(output io.Reader) error {
			scanner := bufio.NewScanner(output)
			scanner.Buffer(make([]byte, 128), 128)
			for scanner.Scan() {
				oid := scanner.Text()
				if !isOID(oid) {
					return errors.New("Git returned malformed ancestry data")
				}
				if _, candidate := found[oid]; candidate {
					found[oid] = true
				}
			}
			return scanner.Err()
		}, "--git-dir", ".", "rev-list", currentOID, "--")
		if err != nil {
			return nil, err
		}
	}
	var retained []RetainedRef
	var commitOIDs []string
	for _, candidate := range candidates {
		if candidate.kind == "branch" && ancestors[current[candidate.source]][candidate.oid] {
			continue
		}
		commitOID, err := retainedCommitFromRef(candidate.oid, candidate.objectType, candidate.peeledOID, candidate.peeledType)
		if err != nil {
			return nil, err
		}
		retained = append(retained, RetainedRef{Kind: candidate.kind, Source: candidate.source, OID: candidate.oid, CommitOID: commitOID})
		if commitOID != "" {
			commitOIDs = append(commitOIDs, commitOID)
		}
	}
	metadata, err := readCommitMetadata(ctx, runner, repositoryPath, commitOIDs, reads)
	if err != nil {
		return nil, err
	}
	for index := range retained {
		if retained[index].CommitOID == "" {
			continue
		}
		commit, exists := metadata[retained[index].CommitOID]
		if !exists {
			return nil, errors.New("Git omitted retained commit metadata")
		}
		retained[index].Commit = commit
	}
	return retained, nil
}

type peeledRetainedObject struct {
	oid        string
	objectType string
}

func batchPeelRetainedTags(ctx context.Context, runner retainedRunner, repositoryPath string, oids []string) (map[string]peeledRetainedObject, error) {
	var input strings.Builder
	for _, oid := range oids {
		if !isOID(oid) {
			return nil, errors.New("Git returned an invalid retained tag ID")
		}
		input.WriteString(oid)
		input.WriteString("^{}\n")
	}
	result, err := runner.Run(ctx, repositoryPath, strings.NewReader(input.String()), "--git-dir", ".", "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, fmt.Errorf("peel retained tags: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(result.Stdout)), "\n")
	if len(lines) != len(oids) {
		return nil, errors.New("Git returned incomplete retained tag peel data")
	}
	peeled := make(map[string]peeledRetainedObject, len(oids))
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || !isOID(fields[0]) || fields[1] != "commit" && fields[1] != "blob" && fields[1] != "tree" {
			return nil, errors.New("Git returned malformed retained tag peel data")
		}
		peeled[oids[index]] = peeledRetainedObject{oid: fields[0], objectType: fields[1]}
	}
	return peeled, nil
}

func retainedCommitFromRef(oid, objectType, peeledOID, peeledType string) (string, error) {
	if !isOID(oid) {
		return "", errors.New("Git returned an invalid retained object ID")
	}
	switch objectType {
	case "commit":
		return oid, nil
	case "tag":
		if peeledType == "commit" && isOID(peeledOID) {
			return peeledOID, nil
		}
		if peeledType == "blob" || peeledType == "tree" {
			return "", nil
		}
		return "", errors.New("Git returned invalid retained tag peel data")
	case "blob", "tree":
		return "", nil
	default:
		return "", errors.New("Git returned an invalid retained object type")
	}
}

func slicesSortDays(days []ActivityDay) {
	for left := 1; left < len(days); left++ {
		for right := left; right > 0 && days[right].Day < days[right-1].Day; right-- {
			days[right], days[right-1] = days[right-1], days[right]
		}
	}
}
