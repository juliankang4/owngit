package importsync

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/state"
)

// ownerResolvedReason is recorded on every owner-resolved intent. It states
// what happened without claiming that the planned publication completed.
const ownerResolvedReason = "owner resolved: the destination was accepted as found; the planned publication was not confirmed"

// ResolveResult reports an owner resolution and the resulting status.
type ResolveResult struct {
	Resolved []string `json:"resolved"`
	Status   Status   `json:"status"`
}

// ResolveUnresolved records the owner's decision to accept the repository as
// it is after a publication whose outcome could not be settled. Under the
// repository lock it reads the destination refs named by each unresolved
// intent and HEAD by exact kind, records them as the intent's receipt, and
// moves the intent to owner_resolved. It writes nothing to Git, replays
// nothing, and rolls nothing back. Run history keeps the unresolved run as it
// was. The next refresh then plans from the actual destination against the
// last confirmed source observations, so refs that differ from both stay
// divergent instead of being overwritten.
func (s *Service) ResolveUnresolved(ctx context.Context, repositoryID string) (ResolveResult, error) {
	if s.Store == nil || s.Repositories == nil {
		return ResolveResult{}, newProblem(CodeRuntimeUnavailable, "import service is unavailable", nil)
	}
	s.lifecycle.RLock()
	closing := s.closing
	s.lifecycle.RUnlock()
	if closing {
		return ResolveResult{}, newProblem(CodeRuntimeUnavailable, "OwnGit is shutting down", ErrShuttingDown)
	}
	s.beginRuntimeOperation()
	defer s.endRuntimeOperation()
	if err := s.prepareRuntime(ctx); err != nil {
		return ResolveResult{}, err
	}
	root, err := s.currentRuntime("")
	if err != nil {
		return ResolveResult{}, err
	}
	now := s.clock()
	lock := s.Repositories.Locks.For(repositoryID)
	if err := lockBeforeDeadline(ctx, lock); err != nil {
		return ResolveResult{}, newProblem(CodeBusy, "another Git operation holds the repository; nothing was resolved", err)
	}
	resolved, err := s.resolveLocked(ctx, repositoryID, root.generation, now)
	lock.Unlock()
	if err != nil {
		return ResolveResult{}, err
	}
	s.forgetResolvedStartupProblem(repositoryID)
	return ResolveResult{Resolved: resolved, Status: s.mustStatus(ctx, repositoryID)}, nil
}

// lockBeforeDeadline takes the repository write lock unless ctx ends first,
// so a long writer such as a push cannot hold an owner request past its
// deadline.
func lockBeforeDeadline(ctx context.Context, lock *gitexec.RepositoryLock) error {
	for !lock.TryLock() {
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (s *Service) resolveLocked(ctx context.Context, repositoryID, generation string, now time.Time) ([]string, error) {
	path, _, exists, err := s.Repositories.ExistingPath(ctx, repositoryID)
	if err != nil {
		return nil, newProblem(CodeStateUnavailable, "repository could not be read", err)
	}
	if !exists {
		return nil, newProblem(CodeRepositoryMissing, "the repository does not exist, so there is no destination to accept", nil)
	}
	if _, active, err := s.Store.ActiveImportRun(ctx, repositoryID); err != nil {
		return nil, newProblem(CodeStateUnavailable, "active import run could not be read", err)
	} else if active {
		return nil, newProblem(CodeBusy, "an import run is active; wait for it or cancel it before resolving", ErrBusy)
	}
	intents, err := s.Store.UnresolvedImportIntents(ctx, repositoryID)
	if err != nil {
		return nil, newProblem(CodeStateUnavailable, "unresolved publication intents could not be read", err)
	}
	if len(intents) == 0 {
		return nil, newProblem(CodeNothingToResolve, "the repository has no unresolved publication to resolve", nil)
	}
	var names []string
	for _, intent := range intents {
		if err := s.initialIntentSettled(ctx, intent, path, generation); err != nil {
			return nil, err
		}
		for _, values := range []map[string]string{intent.Expected, intent.Desired, intent.Retained} {
			for ref := range values {
				if ref != state.ImportHeadRef {
					names = append(names, ref)
				}
			}
		}
	}
	sort.Strings(names)
	refs, symrefs, err := s.readPublicationRefs(ctx, path, names)
	if err != nil {
		return nil, err
	}
	headSymref, headOID, err := s.Repositories.ReadHead(ctx, path)
	if err != nil {
		return nil, newProblem(CodeRepositoryMissing, "destination HEAD could not be read", err)
	}
	head, err := destinationHEADIdentity(headSymref, headOID)
	if err != nil {
		return nil, newProblem(CodeUnsupported, "destination HEAD representation is unsupported", err)
	}
	resolutions := make([]state.ImportIntentResolution, 0, len(intents))
	resolved := make([]string, 0, len(intents))
	for _, intent := range intents {
		receipt := map[string]string{state.ImportHeadRef: head.encode()}
		for _, values := range []map[string]string{intent.Expected, intent.Desired, intent.Retained} {
			for ref := range values {
				if ref == state.ImportHeadRef {
					continue
				}
				value := refs[ref]
				if target := symrefs[ref]; target != "" {
					value = "symbolic " + target + " " + refs[ref]
				}
				receipt[ref] = value
			}
		}
		encoded, err := json.Marshal(receipt)
		if err != nil {
			return nil, newProblem(CodeStateUnavailable, "owner resolution receipt could not be encoded", err)
		}
		resolutions = append(resolutions, state.ImportIntentResolution{ID: intent.ID, ReceiptJSON: string(encoded), Reason: ownerResolvedReason})
		resolved = append(resolved, intent.ID)
	}
	if err := s.Store.ResolveImportIntents(ctx, repositoryID, resolutions, now); err != nil {
		switch {
		case errors.Is(err, state.ErrImportRunActive):
			return nil, newProblem(CodeBusy, "an import run became active; nothing was resolved", ErrBusy)
		case errors.Is(err, state.ErrImportIntentNotUnresolved):
			return nil, newProblem(CodeNothingToResolve, "the unresolved publication changed; nothing was resolved", err)
		default:
			return nil, newProblem(CodeStateUnavailable, "owner resolution could not be recorded", err)
		}
	}
	return resolved, nil
}

// initialIntentSettled refuses an initial-import intent whose unpublished
// directory is still pending. Only the published repository may be accepted.
func (s *Service) initialIntentSettled(ctx context.Context, intent state.ImportIntent, repositoryPath, generation string) error {
	rows, err := s.Store.ImportInitialDestinationsForRun(ctx, intent.RunID)
	if err != nil {
		return newProblem(CodeStateUnavailable, "initial destination ownership could not be read", err)
	}
	for _, row := range rows {
		if row.State != state.ImportInitialPublished && row.State != state.ImportInitialReleased {
			return newProblem(CodeUnresolved, "an unpublished initial destination of an earlier import still belongs to this publication; restart OwnGit so reconciliation can settle it, and if reconciliation preserves that .owngit-create-* directory, move it out of the repository root and restart again", nil)
		}
	}
	allowed, err := s.initialIntentObservationAllowed(ctx, intent, repositoryPath, generation)
	if err != nil {
		return err
	}
	if !allowed {
		return newProblem(CodeUnresolved, "this publication belongs to an earlier initial import that never became this repository; restart OwnGit so reconciliation can settle it", nil)
	}
	return nil
}
