package checksource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"owngit/internal/repository"
)

// PinnedTree is the subset of a pinned repository this package reads. It is an
// interface so tests can supply a double; *repository.PinnedRepository
// satisfies it.
type PinnedTree interface {
	BaseOID() string
	HeadOID() string
	ObjectFormat() string
	ListTreeRecursive(ctx context.Context, side repository.PinnedSide, limit int64) ([]repository.TreeEntry, error)
	ReadBlobObject(ctx context.Context, oid string, size int64) ([]byte, error)
}

// PinnedSource reads one pinned commit through raw Git object access. It
// resolves no ref, touches no working tree, and runs no checkout, filter, or
// hook, so committed attributes cannot change the bytes it reports.
type PinnedSource struct {
	tree      PinnedTree
	side      repository.PinnedSide
	commitOID string
}

// NewPinnedSource binds one already pinned commit side as a materialization
// source. The commit ID is captured here so a later call reports the same
// source identity it read.
func NewPinnedSource(tree PinnedTree, side repository.PinnedSide) (*PinnedSource, error) {
	if tree == nil {
		return nil, errors.New("pinned repository is required")
	}
	var commitOID string
	switch side {
	case repository.PinnedBase:
		commitOID = tree.BaseOID()
	case repository.PinnedHead:
		commitOID = tree.HeadOID()
	default:
		return nil, errors.New("invalid pinned side")
	}
	if commitOID == "" {
		return nil, errors.New("pinned commit is unavailable")
	}
	return &PinnedSource{tree: tree, side: side, commitOID: commitOID}, nil
}

func (s *PinnedSource) CommitOID() string { return s.commitOID }

func (s *PinnedSource) ObjectFormat() string { return s.tree.ObjectFormat() }

func (s *PinnedSource) ListTree(ctx context.Context, metadataLimit int64) ([]Entry, error) {
	listed, err := s.tree.ListTreeRecursive(ctx, s.side, metadataLimit)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(listed))
	for _, entry := range listed {
		entries = append(entries, Entry{
			Path: entry.Path, OID: entry.OID, Mode: entry.Mode, Type: entry.Type, Size: entry.Size,
		})
	}
	return entries, nil
}

func (s *PinnedSource) ReadBlob(ctx context.Context, oid string, size int64) ([]byte, error) {
	return s.tree.ReadBlobObject(ctx, oid, size)
}

// RetryWhileRepositoryBusy runs operation again while it fails with
// repository.ErrPinnedRepositoryBusy. Pinned reads refuse instead of waiting
// behind a repository writer such as a push, so a caller that must not lose a
// check to a brief write waits here, up to limit and while ctx lasts. The
// operation must start from scratch each time it runs.
func RetryWhileRepositoryBusy(ctx context.Context, limit time.Duration, operation func() error) error {
	deadline := time.Now().Add(limit)
	delay := 20 * time.Millisecond
	for {
		err := operation()
		if !errors.Is(err, repository.ErrPinnedRepositoryBusy) {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("the repository stayed busy with other writes for %s: %w", limit, err)
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, 500*time.Millisecond)
	}
}

// MaterializePinned materializes one pinned commit side into a new directory.
// Ownership and failure semantics match Materialize: the caller owns removing
// a partial destination, and no Result is returned for an incomplete export.
func MaterializePinned(ctx context.Context, tree PinnedTree, side repository.PinnedSide, destination string, options Options) (*Result, error) {
	source, err := NewPinnedSource(tree, side)
	if err != nil {
		return nil, err
	}
	result, err := Materialize(ctx, source, destination, options)
	if err != nil {
		return nil, fmt.Errorf("materialize pinned check source: %w", err)
	}
	return result, nil
}
