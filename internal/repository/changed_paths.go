package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"owngit/internal/gitexec"
)

const (
	MaximumChangedPaths  = 3000
	maximumPathDiffBytes = 1 << 20
	pathDiffTimeout      = 10 * time.Second
)

type PathChangeRequest struct {
	PreviousOID string
	DefaultOID  string
	PullRequest bool
}

type PathChanges struct {
	Files    []string
	Complete bool
	Reason   string
}

// ChangedPaths reads raw tree records only. Missing bases and cut reads remain
// undecided unless a caller can decide its filter from a listed path.
func (p *PinnedRepository) ChangedPaths(ctx context.Context, request PathChangeRequest) (PathChanges, error) {
	ctx, cancel := context.WithTimeout(ctx, pathDiffTimeout)
	defer cancel()
	changes := PathChanges{}
	err := p.withReadLock(ctx, func(repositoryPath string) error {
		base := request.PreviousOID
		merge := request.PullRequest || base == "" && request.DefaultOID != "" && request.DefaultOID != p.headOID
		if request.PullRequest {
			base = p.baseOID
		} else if merge {
			base = request.DefaultOID
		}
		if base != "" {
			if !isOID(base) || len(base) != len(p.headOID) {
				changes.Reason = "the base commit is not usable"
				return nil
			}
			if err := verifyPinnedCommit(ctx, p.manager.Git, repositoryPath, base); err != nil {
				changes.Reason = "the base commit is unavailable"
				return nil
			}
		}
		if merge {
			result, err := p.manager.Git.RunWithLimits(ctx, repositoryPath, nil, gitexec.CommandLimits{OutputLimit: maximumPathDiffBytes, StopAtOutputLimit: true}, "--git-dir", ".", "merge-base", "--all", base, p.headOID)
			bases := strings.Fields(string(result.Stdout))
			if err != nil && !(len(bases) == 0 && gitexecExitOne(err)) {
				changes.Reason = "merge bases could not be read"
				return nil
			}
			if len(bases) > 1 || request.PullRequest && len(bases) != 1 {
				changes.Reason = "there is not exactly one merge base"
				return nil
			}
			base = ""
			if len(bases) == 1 && isOID(bases[0]) && len(bases[0]) == len(p.headOID) {
				base = bases[0]
			}
		}
		if base == "" {
			result, err := p.manager.Git.RunWithLimits(ctx, repositoryPath, strings.NewReader(""), gitexec.CommandLimits{OutputLimit: 128}, "--git-dir", ".", "hash-object", "-t", "tree", "--stdin")
			if err != nil {
				return err
			}
			base = strings.TrimSpace(string(result.Stdout))
			if !isOID(base) || len(base) != len(p.headOID) {
				return errors.New("Git returned an invalid empty tree ID")
			}
		}
		result, err := p.manager.Git.RunWithLimits(ctx, repositoryPath, nil, gitexec.CommandLimits{OutputLimit: maximumPathDiffBytes, StopAtOutputLimit: true}, "--git-dir", ".", "diff", "--raw", "-z", "--no-renames", "--no-abbrev", "--no-ext-diff", "--no-textconv", base, p.headOID, "--")
		var limit *gitexec.LimitError
		cut := errors.As(err, &limit) || errors.Is(err, context.DeadlineExceeded)
		if err != nil && !cut {
			changes.Reason = "the tree diff could not be read"
			return nil
		}
		files, end, complete := parseChanges(result.Stdout)
		changes.Complete = !cut && complete && end == len(result.Stdout) && len(files) <= MaximumChangedPaths
		for _, file := range files[:min(len(files), MaximumChangedPaths)] {
			changes.Files = append(changes.Files, file.Path)
		}
		if !changes.Complete {
			changes.Reason = "the changed-file list was cut at its file, byte or time limit"
		}
		return nil
	})
	if errors.Is(err, context.DeadlineExceeded) {
		changes.Complete, changes.Reason = false, "the changed-file read reached its time limit"
		return changes, nil
	}
	return changes, err
}

func gitexecExitOne(err error) bool {
	code, ok := gitexec.ExitCode(err)
	return ok && code == 1
}
