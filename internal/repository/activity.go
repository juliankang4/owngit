package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	Days       []ActivityDay
	Commits    int
	Incomplete bool
}

type RetainedRef struct {
	Kind string
	OID  string
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
	rootsResult, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(objectname)", "refs/heads", "refs/owngit/retained/heads")
	if err != nil {
		return Activity{}, err
	}
	roots := strings.Fields(string(rootsResult.Stdout))
	if len(roots) == 0 {
		return Activity{}, nil
	}
	args := []string{"--git-dir", repositoryPath, "rev-list", "--stdin", "--format=%aI", "--max-count=" + strconv.Itoa(maximumCommits+1)}
	result, err := m.Git.RunWithOutputLimit(ctx, "", strings.NewReader(strings.Join(roots, "\n")+"\n"), 64<<20, args...)
	if err != nil {
		return Activity{}, err
	}
	lines := bytes.Split(bytes.TrimSpace(result.Stdout), []byte{'\n'})
	counts := make(map[string]int)
	seen := make(map[string]struct{})
	activity := Activity{}
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(string(lines[index]))
		if !strings.HasPrefix(line, "commit ") {
			continue
		}
		oid := strings.TrimPrefix(line, "commit ")
		if _, exists := seen[oid]; exists {
			continue
		}
		if index+1 >= len(lines) {
			return Activity{}, errors.New("Git returned activity without an author date")
		}
		index++
		authored, err := time.Parse(time.RFC3339, strings.TrimSpace(string(lines[index])))
		if err != nil {
			return Activity{}, fmt.Errorf("parse activity author date: %w", err)
		}
		if len(seen) >= maximumCommits {
			activity.Incomplete = true
			break
		}
		seen[oid] = struct{}{}
		counts[authored.Format("2006-01-02")]++
	}
	activity.Commits = len(seen)
	activity.Days = make([]ActivityDay, 0, len(counts))
	for day, count := range counts {
		activity.Days = append(activity.Days, ActivityDay{Day: day, Count: count})
	}
	slicesSortDays(activity.Days)
	return activity, nil
}

func (m *Manager) ActivityRecords(ctx context.Context, id string, maximumCommits int) ([]ActivityRecord, bool, error) {
	if maximumCommits <= 0 {
		maximumCommits = 200_000
	}
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		return nil, false, errors.New("repository not found")
	}
	lock := m.Locks.For(id)
	lock.RLock()
	defer lock.RUnlock()
	refsResult, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/owngit/retained/heads")
	if err != nil {
		return nil, false, err
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
		return nil, false, nil
	}
	provenance, err := m.retainedProvenance(ctx, repositoryPath, "heads")
	if err != nil {
		return nil, false, err
	}

	// Current history is scanned first. Retained history explicitly excludes
	// every current root, so a reachable commit is never mislabeled as detached
	// merely because it is also protected by a hidden retention ref.
	records, currentMore, err := m.activityLog(ctx, repositoryPath, current, nil, maximumCommits)
	if err != nil {
		return nil, false, err
	}
	incomplete := currentMore
	if !incomplete && len(records) < maximumCommits && len(retained) != 0 {
		remaining := maximumCommits - len(records)
		lost, retainedMore, err := m.activityLog(ctx, repositoryPath, retained, current, remaining)
		if err != nil {
			return nil, false, err
		}
		for index := range lost {
			rootOID := strings.TrimPrefix(lost[index].Source, "refs/owngit/retained/heads/")
			lost[index].Source = provenance[rootOID]
			lost[index].Retained = lost[index].Source != ""
			lost[index].Uncertain = lost[index].Source == ""
		}
		records = append(records, lost...)
		incomplete = retainedMore
	}
	return records, incomplete, nil
}

func (m *Manager) activityLog(ctx context.Context, repositoryPath string, roots, excluded []string, maximum int) ([]ActivityRecord, bool, error) {
	if len(roots) == 0 || maximum <= 0 {
		return nil, len(roots) != 0, nil
	}
	input := strings.Join(roots, "\n") + "\n"
	if len(excluded) != 0 {
		input += "--not\n" + strings.Join(excluded, "\n") + "\n"
	}
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

func (m *Manager) retainedProvenance(ctx context.Context, repositoryPath, kind string) (map[string]string, error) {
	if kind != "heads" && kind != "tags" {
		return nil, errors.New("invalid retained ref kind")
	}
	prefix := "refs/owngit/provenance/" + kind + "/"
	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(objectname)%00%(refname)", strings.TrimSuffix(prefix, "/"))
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
	result, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/owngit/retained")
	if err != nil {
		return nil, err
	}
	currentResult, err := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads", "refs/tags")
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
	headSources, err := m.retainedProvenance(ctx, repositoryPath, "heads")
	if err != nil {
		return nil, err
	}
	tagSources, err := m.retainedProvenance(ctx, repositoryPath, "tags")
	if err != nil {
		return nil, err
	}
	var retained []RetainedRef
	for _, line := range bytes.Split(bytes.TrimSpace(result.Stdout), []byte{'\n'}) {
		parts := bytes.SplitN(line, []byte{0}, 2)
		if len(parts) != 2 {
			continue
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
		if kind == "branch" && current[source] != "" {
			_, ancestorErr := m.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "merge-base", "--is-ancestor", oid, current[source])
			if ancestorErr == nil {
				continue
			}
			if code, ok := gitexec.ExitCode(ancestorErr); !ok || code != 1 {
				return nil, ancestorErr
			}
		}
		retained = append(retained, RetainedRef{Kind: kind, OID: oid})
	}
	return retained, nil
}

func slicesSortDays(days []ActivityDay) {
	for left := 1; left < len(days); left++ {
		for right := left; right > 0 && days[right].Day < days[right-1].Day; right-- {
			days[right], days[right-1] = days[right-1], days[right]
		}
	}
}
