package repository

import (
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
}

type RetainedRef struct {
	Kind      string
	OID       string
	CommitOID string
	Commit    Commit
}

type retainedRunner interface {
	Run(context.Context, string, io.Reader, ...string) (gitexec.Result, error)
	RunWithOutputLimit(context.Context, string, io.Reader, int64, ...string) (gitexec.Result, error)
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
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return Activity{}, err
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	refsResult, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/owngit/retained/heads")
	if err != nil {
		return Activity{}, err
	}
	var current, retained []string
	for _, ref := range strings.Fields(string(refsResult.Stdout)) {
		if strings.HasPrefix(ref, "refs/heads/") {
			current = append(current, ref)
		} else if strings.HasPrefix(ref, "refs/owngit/retained/heads/") {
			retained = append(retained, ref)
		}
	}
	if len(current) == 0 && len(retained) == 0 {
		return Activity{}, nil
	}
	provenance, err := m.retainedProvenance(ctx, repositoryPath, "heads")
	if err != nil {
		return Activity{}, err
	}

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
	activity := Activity{Records: records, Commits: len(records), Incomplete: incomplete}
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
	format := "%H%x00%S%x00%aI%x00%an%x00%s"
	args := []string{"--git-dir", repositoryPath, "log", "--stdin", "-z", "--source", "--no-decorate", "--max-count=" + strconv.Itoa(maximum+1), "--format=" + format}
	result, err := m.Git.RunWithOutputLimit(ctx, "", strings.NewReader(input), 64<<20, args...)
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
		authored, err := time.Parse(time.RFC3339, string(record[2]))
		if err != nil {
			return nil, false, fmt.Errorf("parse activity author date: %w", err)
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

func (m *Manager) retainedProvenance(ctx context.Context, repositoryPath, kind string) (map[string]string, error) {
	return retainedProvenance(ctx, m.Git, repositoryPath, kind)
}

func retainedProvenance(ctx context.Context, runner retainedRunner, repositoryPath, kind string) (map[string]string, error) {
	if kind != "heads" && kind != "tags" {
		return nil, errors.New("invalid retained ref kind")
	}
	prefix := "refs/owngit/provenance/" + kind + "/"
	result, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(objectname)%00%(refname)", strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return nil, err
	}
	provenance := make(map[string]string)
	for _, line := range bytes.Split(bytes.TrimSpace(result.Stdout), []byte{'\n'}) {
		parts := bytes.SplitN(line, []byte{0}, 2)
		if len(parts) != 2 {
			continue
		}
		oid, ref := string(parts[0]), string(parts[1])
		remainder := strings.TrimPrefix(ref, prefix)
		last := strings.LastIndex(remainder, "/")
		if ref == remainder || last <= 0 || !isOID(remainder[last+1:]) {
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
	return provenance, nil
}

func (m *Manager) RetainedRefs(ctx context.Context, id string) ([]RetainedRef, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		return nil, errors.New("repository not found")
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	return retainedRefs(ctx, m.Git, repositoryPath)
}

func retainedRefs(ctx context.Context, runner retainedRunner, repositoryPath string) ([]RetainedRef, error) {
	format := "%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(*objecttype)"
	result, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format="+format, "refs/owngit/retained")
	if err != nil {
		return nil, err
	}
	currentResult, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	current := make(map[string]string)
	for _, line := range bytes.Split(bytes.TrimSpace(currentResult.Stdout), []byte{'\n'}) {
		parts := bytes.SplitN(line, []byte{0}, 2)
		if len(parts) == 2 {
			current[string(parts[0])] = string(parts[1])
		}
	}
	headSources, err := retainedProvenance(ctx, runner, repositoryPath, "heads")
	if err != nil {
		return nil, err
	}
	tagSources, err := retainedProvenance(ctx, runner, repositoryPath, "tags")
	if err != nil {
		return nil, err
	}
	type candidate struct {
		kind, source, oid, objectType, peeledOID, peeledType string
	}
	var candidates []candidate
	currentBranchOIDs := make(map[string]bool)
	for _, line := range bytes.Split(bytes.TrimSpace(result.Stdout), []byte{'\n'}) {
		parts := bytes.SplitN(line, []byte{0}, 5)
		if len(parts) != 5 {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			return nil, errors.New("Git returned malformed retained ref data")
		}
		name, oid := string(parts[0]), string(parts[1])
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
			kind: kind, source: source, oid: oid, objectType: string(parts[2]), peeledOID: string(parts[3]), peeledType: string(parts[4]),
		})
		if kind == "branch" && current[source] != "" {
			currentBranchOIDs[current[source]] = true
		}
	}
	var annotatedTagOIDs []string
	for _, candidate := range candidates {
		if candidate.objectType == "tag" {
			annotatedTagOIDs = append(annotatedTagOIDs, candidate.oid)
		}
	}
	if len(annotatedTagOIDs) != 0 {
		terminal, err := batchPeelRetainedTags(ctx, runner, repositoryPath, annotatedTagOIDs)
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

	unmerged := make(map[string]map[string]bool, len(currentBranchOIDs))
	for currentOID := range currentBranchOIDs {
		unmergedResult, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--no-merged="+currentOID, "--format=%(objectname)", "refs/owngit/retained/heads")
		if err != nil {
			return nil, err
		}
		unmerged[currentOID] = make(map[string]bool)
		for _, oid := range strings.Fields(string(unmergedResult.Stdout)) {
			unmerged[currentOID][oid] = true
		}
	}

	var retained []RetainedRef
	var commitOIDs []string
	for _, candidate := range candidates {
		if currentOID := current[candidate.source]; candidate.kind == "branch" && currentOID != "" && !unmerged[currentOID][candidate.oid] {
			continue
		}
		commitOID, err := retainedCommitFromRef(candidate.oid, candidate.objectType, candidate.peeledOID, candidate.peeledType)
		if err != nil {
			return nil, err
		}
		retained = append(retained, RetainedRef{Kind: candidate.kind, OID: candidate.oid, CommitOID: commitOID})
		if commitOID != "" {
			commitOIDs = append(commitOIDs, commitOID)
		}
	}
	metadata, err := commitMetadataByOID(ctx, runner, repositoryPath, commitOIDs)
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
	result, err := runner.Run(ctx, "", strings.NewReader(input.String()), "--git-dir", repositoryPath, "cat-file", "--batch-check=%(objectname) %(objecttype)")
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
