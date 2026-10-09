package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"owngit/internal/gitexec"
	"owngit/internal/importgit"
)

// SetDefaultBranch points HEAD at an existing branch. It accepts a short name
// or a full refs/heads/ name, never creates a ref, and leaves retained history
// untouched. A name that is invalid or does not name an existing branch
// returns ErrBranchNotFound. Distinct existing interpretations return
// AmbiguousBranchError. A repository that another Git operation holds
// for longer than a short wait returns ErrRepositoryInUse. Any other Git
// failure is returned as it is, never as ErrBranchNotFound.
func (m *Manager) SetDefaultBranch(ctx context.Context, id, branch string) error {
	_, err := m.SetDefaultBranchInput(ctx, id, branch, false)
	return err
}

// SetDefaultBranchInput returns the selected full ref. Exact browser input is
// used as is; legacy API operands refuse distinct existing interpretations.
func (m *Manager) SetDefaultBranchInput(ctx context.Context, id, value string, exact bool) (string, error) {
	if ValidateID(id) != nil {
		return "", ErrRepositoryNotFound
	}
	if err := m.validateDefaultBranchInput(ctx, value, exact); err != nil {
		return "", err
	}
	// Like Delete, wait only briefly for Git operations that hold the
	// repository, and report it in use instead of outliving the request.
	lock := m.Locks.For(id)
	if err := lockWithin(ctx, lock); err != nil {
		return "", err
	}
	defer lock.Unlock()
	// Resolved under the lock, so a deletion that finished first reports a
	// missing repository instead of a missing branch.
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrRepositoryNotFound
	}
	ref, err := m.SelectBranchRefWithEligibility(ctx, repositoryPath, value, exact, func(operand string) (bool, error) {
		err := m.validateDefaultBranchInput(ctx, operand, false)
		if errors.Is(err, ErrBranchNotFound) {
			return false, nil
		}
		return err == nil, err
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrBranchNotFound
		}
		return "", err
	}
	if _, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "HEAD", ref); err != nil {
		return "", fmt.Errorf("change default branch: %w", err)
	}
	return ref, nil
}

// DefaultBranchEligible reports whether a branch name can be offered and saved.
// Names are checked as full refs, so HEAD and a leading dash are legal.
func DefaultBranchEligible(branch string) bool {
	return importgit.ValidBranchName(branch)
}

// validateDefaultBranchInput is the save and advice eligibility rule. Its
// namespace removal is validation only, never branch identity selection.
func (m *Manager) validateDefaultBranchInput(ctx context.Context, value string, exact bool) error {
	branch := strings.TrimPrefix(value, "refs/heads/")
	if !DefaultBranchEligible(branch) {
		return fmt.Errorf("%w: invalid branch name", ErrBranchNotFound)
	}
	if exact && !strings.HasPrefix(value, "refs/heads/") {
		return ErrBranchNotFound
	}
	ref := "refs/heads/" + branch
	if _, err := m.Git.Run(ctx, "", nil, "check-ref-format", ref); err != nil {
		if gitAnsweredNo(ctx, err) {
			return fmt.Errorf("%w: invalid branch name", ErrBranchNotFound)
		}
		return fmt.Errorf("check branch name: %w", err)
	}
	return nil
}

// SelectBranchRef resolves a branch while the caller holds the repository lock.
// It reads live refs, so a write never selects from a stale browse snapshot.
func (m *Manager) SelectBranchRef(ctx context.Context, repositoryPath, value string, exact bool) (string, error) {
	return m.SelectBranchRefWithEligibility(ctx, repositoryPath, value, exact, nil)
}

// SelectBranchRefWithEligibility offers only advice the caller can accept.
// Eligibility filters suggested operands, never existing candidate identities.
func (m *Manager) SelectBranchRefWithEligibility(ctx context.Context, repositoryPath, value string, exact bool, eligible func(string) (bool, error)) (string, error) {
	return selectRefName(value, exact, false, func(full string) (bool, error) {
		if _, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "show-ref", "--verify", "--quiet", full); err != nil {
			if gitAnsweredNo(ctx, err) {
				return false, nil
			}
			return false, fmt.Errorf("check branch: %w", err)
		}
		return true, nil
	}, eligible)
}

// gitAnsweredNo reports whether Git ran to completion and answered no with
// exit status 1, which check-ref-format and show-ref --verify use for an
// invalid or missing ref. A cancelled request, a process that could not run
// and a fatal Git error (status 128) are failures, not answers.
func gitAnsweredNo(ctx context.Context, err error) bool {
	code, ok := gitexec.ExitCode(err)
	return ok && code == 1 && ctx.Err() == nil
}
