package repository

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"owngit/internal/gitexec"
)

// SetDefaultBranch points HEAD at an existing branch. It accepts a short name
// or a full refs/heads/ name, never creates a ref, and leaves retained history
// untouched. A name that is invalid or does not name an existing branch
// returns ErrBranchNotFound. A repository that another Git operation holds
// for longer than a short wait returns ErrRepositoryInUse. Any other Git
// failure is returned as it is, never as ErrBranchNotFound.
func (m *Manager) SetDefaultBranch(ctx context.Context, id, branch string) error {
	if ValidateID(id) != nil {
		return ErrRepositoryNotFound
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if !utf8.ValidString(branch) || strings.ContainsAny(branch, "\x00\r\n\t") || branch == "HEAD" || validateShortRef(branch) != nil {
		return fmt.Errorf("%w: invalid branch name", ErrBranchNotFound)
	}
	ref := "refs/heads/" + branch
	if _, err := m.Git.Run(ctx, "", nil, "check-ref-format", ref); err != nil {
		if gitAnsweredNo(ctx, err) {
			return fmt.Errorf("%w: invalid branch name", ErrBranchNotFound)
		}
		return fmt.Errorf("check branch name: %w", err)
	}
	// Like Delete, wait only briefly for Git operations that hold the
	// repository, and report it in use instead of outliving the request.
	lock := m.Locks.For(id)
	if err := lockWithin(ctx, lock, deleteLockWait); err != nil {
		return err
	}
	defer lock.Unlock()
	// Resolved under the lock, so a deletion that finished first reports a
	// missing repository instead of a missing branch.
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return ErrRepositoryNotFound
	}
	if _, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "show-ref", "--verify", "--quiet", ref); err != nil {
		if gitAnsweredNo(ctx, err) {
			return ErrBranchNotFound
		}
		return fmt.Errorf("check branch: %w", err)
	}
	if _, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "HEAD", ref); err != nil {
		return fmt.Errorf("change default branch: %w", err)
	}
	return nil
}

// gitAnsweredNo reports whether Git ran to completion and answered no with
// exit status 1, which check-ref-format and show-ref --verify use for an
// invalid or missing ref. A cancelled request, a process that could not run
// and a fatal Git error (status 128) are failures, not answers.
func gitAnsweredNo(ctx context.Context, err error) bool {
	code, ok := gitexec.ExitCode(err)
	return ok && code == 1 && ctx.Err() == nil
}
