package importsync

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
)

const importPackKeepMaxSize = int64(len("owngit import ") + 32 + 1)

// ReconcilePackKeeps removes proven abandoned import keeps in the background.
// Busy repositories are left for the next lifecycle notification.
func (s *Service) ReconcilePackKeeps(ctx context.Context) error {
	s.beginRuntimeOperation()
	defer s.endRuntimeOperation()
	if err := s.prepareRuntime(ctx); err != nil {
		return err
	}
	root, err := s.currentRuntime("")
	if err != nil {
		return err
	}
	generation := root.generation
	repositories, err := s.Store.Repositories(ctx)
	if err != nil {
		return err
	}
	var problems []error
	for _, stored := range repositories {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(problems, err)...)
		}
		runs, _, err := s.Store.ImportRuns(ctx, stored.ID, 1)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if len(runs) == 0 || s.Repositories.Preparing(stored.ID) {
			continue
		}
		path, _, exists, err := s.Repositories.ExistingPath(ctx, stored.ID)
		if err != nil {
			problems = append(problems, &repositoryReconcileError{repositoryID: stored.ID, err: err})
			continue
		}
		if !exists {
			continue
		}
		lock := s.Repositories.Locks.For(stored.ID)
		waitContext, cancel := context.WithTimeout(ctx, destinationKeepCleanupTimeout)
		err = lock.LockContext(waitContext)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}
			problems = append(problems, &repositoryReconcileError{repositoryID: stored.ID, err: err})
			continue
		}
		err = s.removeAbandonedPackKeepsLocked(ctx, generation, stored.ID, path)
		lock.Unlock()
		if err != nil {
			problems = append(problems, &repositoryReconcileError{repositoryID: stored.ID, err: err})
		}
	}
	return errors.Join(problems...)
}

func (s *Service) removeAbandonedPackKeepsLocked(ctx context.Context, generation, repositoryID, path string) error {
	if err := s.Repositories.VerifyRepositoryStorage(repositoryID); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	packs, err := root.OpenRoot("objects/pack")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer packs.Close()
	directory, err := packs.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		hash := strings.TrimSuffix(strings.TrimPrefix(name, "pack-"), ".keep")
		if name != "pack-"+hash+".keep" || (len(hash) != 40 && len(hash) != 64) || !isLowerHexString(hash) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.removeAbandonedPackKeep(ctx, packs, generation, repositoryID, name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) removeAbandonedPackKeep(ctx context.Context, packs *os.Root, generation, repositoryID, name string) error {
	info, err := packs.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > importPackKeepMaxSize {
		return err
	}
	file, err := packs.Open(name)
	if err != nil {
		return err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, importPackKeepMaxSize+1))
	held, statErr := file.Stat()
	if err := errors.Join(readErr, statErr, file.Close()); err != nil {
		return err
	}
	if !os.SameFile(info, held) {
		return nil
	}
	runID, owned := strings.CutPrefix(strings.TrimSuffix(string(content), "\n"), "owngit import ")
	if !owned || len(runID) != 32 || !isLowerHexString(runID) || s.runIsLive(runID) {
		return nil
	}
	run, exists, err := s.Store.ImportRun(ctx, runID)
	if err != nil {
		return err
	}
	if !exists || run.RepositoryID != repositoryID || !terminalImportRun(run.Status) {
		return nil
	}
	if _, err := s.currentRuntime(generation); err != nil {
		return err
	}
	if err := s.Repositories.VerifyRepositoryStorage(repositoryID); err != nil {
		return err
	}
	current, err := packs.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(held, current) {
		return nil
	}
	return packs.Remove(name)
}
