package importsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// publicationPlan is the divergence-safe decision for one publication. It is
// computed under the repository write lock from current destination refs and
// the recorded observations. Desired holds the intended destination value for
// every advertised branch and tag, including refs that stay unchanged because
// the local branch diverged. A name left uncreated because the destination
// holds a case-only variant has an expected absence and no desired value.
type deferredImportLog struct {
	format    string
	arguments []any
}

func appendDeferredImportLog(logs *[]deferredImportLog, format string, arguments ...any) {
	*logs = append(*logs, deferredImportLog{format: format, arguments: arguments})
}

func (s *Service) flushDeferredImportLogs(logs []deferredImportLog) {
	for _, log := range logs {
		s.logf(log.format, log.arguments...)
	}
}

const headDivergenceEvidence = "destination HEAD preserved as independent local state"

type publicationPlan struct {
	expected        map[string]string
	desired         map[string]string
	observed        map[string]string
	retained        map[string]string
	headExpected    headIdentity
	headDesired     headIdentity
	headChange      bool
	headOwned       bool
	created         int64
	updated         int64
	unchanged       int64
	divergent       int64
	deletedUpstream int64
	divergentRefs   []string
	deletedRefs     []string
	skipped         []string
}

// planPublication applies the divergence rule:
//
//   - a missing destination ref is created;
//   - an identical ref is unchanged;
//   - a branch owned by this source generation follows an exact replacement or
//     a proven fast-forward, retaining rewritten history;
//   - a tag follows only when its exact previously observed object is still at
//     the destination, and every replaced tag object is retained;
//   - anything else is divergent and stays local.
//
// Upstream deletions never remove a local ref.
func (s *Service) planPublication(ctx context.Context, run *runState, repositoryPath string, dest, destSymrefs map[string]string, destHEAD headIdentity, observations priorObservations) (*publicationPlan, error) {
	plan := &publicationPlan{
		expected: map[string]string{}, desired: map[string]string{}, observed: map[string]string{}, retained: map[string]string{},
		skipped: run.selected.skipped, headExpected: destHEAD, headDesired: destHEAD,
	}
	caseVariants := destinationCaseVariants(dest, destSymrefs)
	caseBlocked := map[string]bool{}
	for _, ref := range run.selected.refs {
		upstream := ref.OID
		destination := dest[ref.Name]
		plan.expected[ref.Name] = destination
		plan.observed[ref.Name] = upstream
		switch {
		case destSymrefs[ref.Name] != "" && destination == "":
			return nil, newProblem(CodeUnsupported, fmt.Sprintf("destination ref %q is an unresolved symbolic alias to %q", ref.Name, destSymrefs[ref.Name]), nil)
		case destSymrefs[ref.Name] != "":
			plan.desired[ref.Name] = destination
			if destination == upstream {
				plan.unchanged++
			} else {
				plan.divergent++
				plan.divergentRefs = append(plan.divergentRefs, ref.Name)
			}
		case destination == "" && hasCaseVariant(caseVariants, ref.Name):
			// A destination ref whose name differs only by case is local state.
			// On a case-insensitive filesystem the loose file created for this
			// name would also answer for a packed variant and silently replace
			// that local branch or tag, so the name is left uncreated. A loose
			// variant is the same file and was already read as this ref. The
			// exact name stays expected absent and has no desired value.
			plan.divergent++
			plan.divergentRefs = append(plan.divergentRefs, ref.Name)
			caseBlocked[ref.Name] = true
		case destination == "":
			plan.desired[ref.Name] = upstream
			plan.created++
		case destination == upstream:
			plan.desired[ref.Name] = upstream
			plan.unchanged++
		case observations.refs[ref.Name] != "" && observations.refs[ref.Name] == destination:
			plan.desired[ref.Name] = upstream
			plan.updated++
			if err := s.addRetention(ctx, run, repositoryPath, plan, dest, destSymrefs, ref.Name, destination, upstream); err != nil {
				return nil, err
			}
		case observations.refs[ref.Name] != "" && strings.HasPrefix(ref.Name, "refs/heads/") &&
			s.isAncestor(ctx, run, repositoryPath, observations.refs[ref.Name], destination) && s.isAncestor(ctx, run, repositoryPath, destination, upstream):
			plan.desired[ref.Name] = upstream
			plan.updated++
		default:
			plan.desired[ref.Name] = destination
			plan.divergent++
			plan.divergentRefs = append(plan.divergentRefs, ref.Name)
		}
	}
	for ref := range observations.refs {
		if _, exists := plan.expected[ref]; !exists {
			plan.deletedUpstream++
			plan.deletedRefs = append(plan.deletedRefs, ref)
		}
	}

	sourceHEAD := sourceHEADIdentity(run.advertisement)
	plan.observed[state.ImportHeadRef] = sourceHEAD.encode()
	switch {
	case run.initialDestination != nil && sourceHEAD.kind != headAbsent:
		// The unpublished directory was created by this run after its ownership
		// row. Its init HEAD is not an independent local choice, so the initial
		// HEAD may be written directly. Run kind alone is not that proof, and an
		// existing destination never has initialDestination set.
		plan.headDesired = sourceHEAD
		plan.headChange = !sameHEADIdentity(destHEAD, sourceHEAD)
		plan.headOwned = true
	case sourceHEAD.kind != headAbsent && observations.headKnown && observations.headOwned && sameHEADIdentity(observations.head, destHEAD) &&
		!caseBlocked[sourceHEAD.target]:
		// An owned HEAD follows the source unless its target was left
		// uncreated because of a case-only variant; that HEAD stays local.
		plan.headDesired = sourceHEAD
		plan.headChange = !sameHEADIdentity(destHEAD, sourceHEAD)
		plan.headOwned = true
		if plan.headChange && destHEAD.kind == headDetached {
			for _, name := range detachedHEADRetentionNames(destHEAD.oid) {
				if err := addRequiredRetention(plan, dest, destSymrefs, name, destHEAD.oid); err != nil {
					return nil, err
				}
			}
		}
	case sourceHEAD.kind != headAbsent && ((observations.headKnown && !sameHEADIdentity(observations.head, destHEAD)) || !sameHEADIdentity(sourceHEAD, destHEAD)):
		plan.divergent++
		plan.divergentRefs = append(plan.divergentRefs, state.ImportHeadRef)
	}
	if plan.headDesired.kind == headSymbolic {
		if oid, exists := plan.desired[plan.headDesired.target]; exists {
			plan.headDesired.oid = oid
		} else {
			terminal, symbolic, err := s.Repositories.ReadSymbolicRefTarget(ctx, repositoryPath, plan.headDesired.target)
			if err != nil {
				return nil, newProblem(CodeRepositoryMissing, "destination symbolic HEAD chain could not be resolved", err)
			}
			if symbolic {
				if oid, exists := plan.desired[terminal]; exists {
					plan.headDesired.oid = oid
				}
			}
		}
	}
	plan.expected[state.ImportHeadRef] = plan.headExpected.encode()
	plan.desired[state.ImportHeadRef] = plan.headDesired.encode()
	sort.Strings(plan.divergentRefs)
	sort.Strings(plan.deletedRefs)
	return plan, nil
}

// destinationCaseVariants groups destination ref names by their case-folded
// form.
func destinationCaseVariants(dest, destSymrefs map[string]string) map[string][]string {
	variants := make(map[string][]string, len(dest))
	for _, names := range []map[string]string{dest, destSymrefs} {
		for name := range names {
			folded := strings.ToLower(name)
			variants[folded] = append(variants[folded], name)
		}
	}
	return variants
}

// hasCaseVariant reports whether the destination holds a ref whose name equals
// name except for case.
func hasCaseVariant(variants map[string][]string, name string) bool {
	for _, existing := range variants[strings.ToLower(name)] {
		if existing != name {
			return true
		}
	}
	return false
}

func (s *Service) isAncestor(ctx context.Context, run *runState, repositoryPath, oldOID, newOID string) bool {
	if oldOID == "" || newOID == "" {
		return false
	}
	_, err := s.Repositories.Git.RunWithLimits(ctx, repositoryPath, nil, run.limits.commandLimits(run.limits.PublishTimeout),
		"--git-dir", ".", "merge-base", "--is-ancestor", "--end-of-options", oldOID, newOID)
	if err == nil {
		return true
	}
	if code, ok := gitexec.ExitCode(err); ok && code == 1 {
		return false
	}
	// The old tip may be absent from the pack when history was rewritten.
	return false
}

func (s *Service) addRetention(ctx context.Context, run *runState, repositoryPath string, plan *publicationPlan, dest, destSymrefs map[string]string, refName, oldOID, newOID string) error {
	kind, ok := refKind(refName)
	if kind == "heads" && s.isAncestor(ctx, run, repositoryPath, oldOID, newOID) {
		return nil
	}
	if !ok {
		return fmt.Errorf("ref %q has no retention kind", refName)
	}
	for _, name := range []string{repository.RetainedRefName(kind, oldOID), repository.ProvenanceRefName(kind, refShortName(refName), oldOID)} {
		if err := addRequiredRetention(plan, dest, destSymrefs, name, oldOID); err != nil {
			return err
		}
	}
	return nil
}

func addRequiredRetention(plan *publicationPlan, dest, destSymrefs map[string]string, name, oid string) error {
	if target := destSymrefs[name]; target != "" {
		return fmt.Errorf("required retention ref %q is independently symbolic to %q", name, target)
	}
	existing := dest[name]
	if existing != "" && existing != oid {
		return fmt.Errorf("retention ref %s already holds a different tip", name)
	}
	plan.expected[name] = existing
	plan.desired[name] = oid
	plan.retained[name] = oid
	return nil
}

// ensureDestination returns the repository path, creating a SHA-1 or SHA-256
// repository that matches the source when it does not exist yet.
func (s *Service) ensureDestination(ctx context.Context, run *runState) (string, error) {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return "", err
	}
	if run.newDestination {
		if s.beforeInitialDestinationCheck != nil {
			if err := s.beforeInitialDestinationCheck(); err != nil {
				return "", err
			}
		}
		// A concurrent ConfigureSource cancels this run. The collision decision
		// still has to see the destination and the current binding.
		checkCtx := context.WithoutCancel(ctx)
		taken, err := s.destinationTaken(checkCtx, run.run.RepositoryID)
		if err != nil {
			return "", err
		}
		if taken {
			return "", s.refuseTakenNewDestination(checkCtx, run)
		}
		if err := ctx.Err(); err != nil {
			return "", cancellationProblem(ctx, "import stopped before destination creation", err)
		}
		return s.prepareInitialDestination(ctx, run)
	}
	path, _, exists, err := s.Repositories.ExistingPath(ctx, run.run.RepositoryID)
	if err != nil {
		return "", newProblem(CodeRepositoryMissing, "repository storage is unavailable", err)
	}
	if !exists {
		if err := s.runtimeCurrentForRun(run); err != nil {
			return "", err
		}
		_, createErr := s.Repositories.CreateWithOptions(ctx, run.name, run.description, repository.CreateOptions{ObjectFormat: run.objectFormat})
		if createErr != nil {
			// Another accepted creator may have won the race.
			path, _, exists, err = s.Repositories.ExistingPath(ctx, run.run.RepositoryID)
			if err != nil || !exists {
				return "", newProblem(CodeRepositoryTaken, "repository could not be created", createErr)
			}
		} else {
			path, _, exists, err = s.Repositories.ExistingPath(ctx, run.run.RepositoryID)
			if err != nil || !exists {
				return "", newProblem(CodeRepositoryMissing, "created repository could not be inspected", err)
			}
		}
	}
	if err := s.runtimeCurrentForRun(run); err != nil {
		return "", err
	}
	format, err := s.Repositories.ObjectFormat(ctx, path)
	if err != nil {
		return "", newProblem(CodeRepositoryMissing, "repository object format could not be read", err)
	}
	if format != run.objectFormat {
		return "", newProblem(CodeUnsupportedFormat, fmt.Sprintf("source is %s but the destination repository is %s", run.objectFormat, format), nil)
	}
	return path, nil
}

// ensureObjects places every wanted object in the destination before the write
// lock is taken, so network and indexing work never block pushes.
func (s *Service) ensureObjects(ctx context.Context, run *runState, repositoryPath string) error {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	wanted := wantedOIDs(run.advertisement)
	if len(wanted) == 0 {
		return nil
	}
	present, err := s.objectsPresent(ctx, run, repositoryPath, wanted)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	packPath, err := singleStagingPack(run.stagingPath)
	if err != nil {
		return err
	}
	if packPath != "" {
		if err := s.runtimeCurrentForRun(run); err != nil {
			return err
		}
		file, err := os.Open(packPath)
		if err != nil {
			return newProblem(CodePublishFailed, "staged pack could not be opened", err)
		}
		result, indexErr := s.Repositories.Git.RunWithLimits(ctx, repositoryPath, file, run.limits.commandLimits(run.limits.PublishTimeout),
			strictIndexPackArguments("owngit import "+run.run.ID)...)
		// Record the .keep file even when the command reported a failure after
		// creating it, so releaseDestinationKeep still removes it.
		if hash, created := createdPackKeep(result.Stdout); created {
			run.destinationKeep = hash
		}
		closeErr := file.Close()
		if indexErr != nil {
			return newProblem(CodePublishFailed, "destination pack indexing failed", indexErr)
		}
		if closeErr != nil {
			return newProblem(CodePublishFailed, "staged pack could not be closed", closeErr)
		}
	}
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	present, err = s.objectsPresent(ctx, run, repositoryPath, wanted)
	if err != nil {
		return err
	}
	if !present {
		return newProblem(CodeVerifyFailed, "destination is still missing wanted objects after indexing", nil)
	}
	return nil
}

// releaseDestinationKeep removes the .keep file that this run's destination
// index-pack created, as git receive-pack does after its ref updates. The file
// only protects the new pack from a concurrent repack until the ref
// transaction has ended; left in place, it would exclude the pack from every
// later repack. The caller runs this after publication ended or was never
// reached, whatever its outcome, and never holds the repository lock.
//
// A pre-existing .keep file is never removed: index-pack reports "keep" only
// for a file it created. A crash between indexing and this step leaves the
// file behind; its content names the import run, and removing it by hand is
// safe once no import is running.
func (s *Service) releaseDestinationKeep(run *runState, repositoryPath string) {
	if run.destinationKeep == "" {
		return
	}
	hash := run.destinationKeep
	run.destinationKeep = ""
	lock := s.Repositories.Locks.For(run.run.RepositoryID)
	lock.Lock()
	directory := repositoryPath
	if dest := run.initialDestination; dest != nil && dest.finalPath != "" {
		// The unpublished directory was renamed into place.
		directory = dest.finalPath
	}
	path := filepath.Join(directory, "objects", "pack", "pack-"+hash+".keep")
	// A discarded unpublished directory no longer holds the file.
	err := os.Remove(path)
	lock.Unlock()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logf("import %s could not remove its pack keep file %s: %v", run.run.RepositoryID, path, err)
	}
}

func singleStagingPack(stagingPath string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(stagingPath, "objects", "pack", "pack-*.pack"))
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", newProblem(CodeVerifyFailed, "staging contains more than one pack", nil)
	}
}

func (s *Service) objectsPresent(ctx context.Context, run *runState, repositoryPath string, wanted []string) (bool, error) {
	input := strings.NewReader(strings.Join(wanted, "\n") + "\n")
	outputLimit := int64(len(wanted))*192 + (1 << 20)
	stdout, truncated, err := s.boundedCommand(ctx, repositoryPath, input, run.limits.commandLimitsFor(run.limits.PublishTimeout, outputLimit),
		"--git-dir", ".", "cat-file", "--batch-check")
	if err != nil {
		return false, newProblem(CodeRepositoryMissing, "destination objects could not be inspected", err)
	}
	if truncated {
		return false, newProblem(CodeVerifyFailed, "destination object inspection exceeded its output bound", nil)
	}
	lines := splitBoundedLines(stdout, 0)
	if len(lines) != len(wanted) {
		return false, newProblem(CodeVerifyFailed, "destination object inspection returned an unexpected record count", nil)
	}
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != wanted[index] {
			return false, nil
		}
		switch fields[1] {
		case "commit", "tree", "blob", "tag":
		default:
			return false, nil
		}
	}
	return true, nil
}

// publish performs the destination writes. Network transfer and pack indexing
// already happened; the repository write lock is held only here. External
// callbacks run before locking or after every publication lock is released.
func (s *Service) publish(ctx context.Context, run *runState, repositoryPath string) (publicationPlan, error) {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return publicationPlan{}, err
	}
	now := s.clock()
	if s.beforeFinalAuthorityCheck != nil {
		s.beforeFinalAuthorityCheck()
	}
	var logs []deferredImportLog
	lock := s.Repositories.Locks.For(run.run.RepositoryID)
	lock.Lock()
	plan, err := s.publishRepositoryLocked(ctx, run, repositoryPath, now)
	if err != nil {
		appendDeferredImportLog(&logs, "import publication for %s remains incomplete: %v", run.run.RepositoryID, err)
	}
	lock.Unlock()
	s.flushDeferredImportLogs(logs)
	return plan, err
}

func (s *Service) publishRepositoryLocked(ctx context.Context, run *runState, repositoryPath string, now time.Time) (publicationPlan, error) {
	if err := s.authorityCurrent(ctx, run); err != nil {
		return publicationPlan{}, err
	}
	// A new publication never proceeds while an older outcome is unknown.
	if err := s.reconcileRepositoryIntentsLocked(ctx, repositoryPath, run.run.RepositoryID, run.runtimeGeneration, now); err != nil {
		return publicationPlan{}, err
	}
	dest, destSymrefs, destHEAD, err := s.readDestinationState(ctx, repositoryPath)
	if err != nil {
		return publicationPlan{}, err
	}
	if err := s.inspectPublicationRefKinds(ctx, run, repositoryPath, dest, destSymrefs, destHEAD); err != nil {
		return publicationPlan{}, err
	}
	observations, err := s.observationMap(ctx, run)
	if err != nil {
		return publicationPlan{}, err
	}
	plan, err := s.planPublication(ctx, run, repositoryPath, dest, destSymrefs, destHEAD, observations)
	if err != nil {
		return publicationPlan{}, err
	}

	releaseCredentialAuthority := s.Store.LockImportCredentialAuthority(run.run.RepositoryID)
	run.credentialAuthorityLocked = true
	defer func() {
		run.credentialAuthorityLocked = false
		releaseCredentialAuthority()
	}()
	if err := s.authorityCurrent(ctx, run); err != nil {
		return *plan, err
	}
	intentID, err := newImportID()
	if err != nil {
		return *plan, newProblem(CodeStateUnavailable, "publication intent identity could not be generated", err)
	}
	sourceHEAD := sourceHEADIdentity(run.advertisement)
	intent := state.ImportIntent{
		ID: intentID, RepositoryID: run.run.RepositoryID, RunID: run.run.ID,
		SourceGeneration: run.run.SourceGeneration, AuthorityRevision: run.run.AuthorityRevision, Status: state.ImportIntentPlanning,
		Expected: plan.expected, Desired: plan.desired, Observed: plan.observed, Retained: plan.retained,
		CreatedAt: now, UpdatedAt: now,
	}
	if sourceHEAD.kind == headSymbolic {
		intent.HeadSymref = sourceHEAD.target
	} else if sourceHEAD.kind == headDetached {
		intent.HeadDetach = sourceHEAD.oid
	}
	for _, divergent := range plan.divergentRefs {
		if divergent == state.ImportHeadRef {
			intent.Reason = appendIntentEvidence(intent.Reason, headDivergenceEvidence)
			break
		}
	}
	intent.HeadOwned = plan.headOwned && !plan.headChange
	if err := s.Store.CreateImportIntent(ctx, intent); err != nil {
		return *plan, newProblem(CodeStateUnavailable, "publication intent could not be recorded", err)
	}
	run.run.RefsCreated = plan.created
	run.run.RefsUpdated = plan.updated
	run.run.RefsUnchanged = plan.unchanged
	run.run.RefsDivergent = plan.divergent
	run.run.RefsDeletedUpstream = plan.deletedUpstream
	run.run.RefsSkipped = int64(len(run.selected.skipped))
	if err := s.applyIntent(ctx, run, repositoryPath, plan, intent, now); err != nil {
		return *plan, err
	}
	return *plan, nil
}

// applyIntent writes the planned refs and HEAD, reads every related fact back,
// and commits portable bookkeeping atomically. A failed command is never proof
// that no write occurred.
func (s *Service) applyIntent(ctx context.Context, run *runState, repositoryPath string, plan *publicationPlan, intent state.ImportIntent, now time.Time) error {
	var commands []string
	transactionRefs := map[string]string{}
	for _, ref := range sortedKeys(plan.desired) {
		if ref == state.ImportHeadRef {
			continue
		}
		desired := plan.desired[ref]
		expected := plan.expected[ref]
		if desired == expected {
			continue
		}
		transactionRefs[ref] = expected
		if expected == "" {
			commands = append(commands, "create "+ref+" "+desired)
		} else {
			commands = append(commands, "update "+ref+" "+desired+" "+expected)
		}
	}
	for _, ref := range sortedKeys(plan.retained) {
		if _, planned := transactionRefs[ref]; !planned && plan.expected[ref] == plan.desired[ref] {
			commands = append(commands, "verify "+ref+" "+plan.expected[ref])
			transactionRefs[ref] = plan.expected[ref]
		}
	}
	refTransaction := len(commands) > 0
	var lock *headLock
	var commandErr error
	var finalizationBlocked bool
	var processUnreaped bool
	if plan.headChange {
		preflight, err := s.acquireHEADLock(ctx, run, repositoryPath, plan.headExpected)
		if err != nil {
			commandErr = err
			finalizationBlocked = true
		} else {
			if s.beforeHEADPreflightRelease != nil {
				s.beforeHEADPreflightRelease()
			}
			if err := preflight.rollback(); err != nil {
				commandErr = newProblem(CodePublishFailed, "HEAD preflight lock could not be released safely", err)
				finalizationBlocked = true
			}
		}
	}
	if commandErr == nil && refTransaction {
		storage, err := s.repositoryRefStorage(ctx, run, repositoryPath)
		if err != nil {
			commandErr = err
			finalizationBlocked = true
		} else if storage != "files" {
			commandErr = newProblem(CodeUnsupported, fmt.Sprintf("destination ref storage %q does not support exact ref-kind publication", storage), nil)
			finalizationBlocked = true
		} else if err := s.authorityCurrent(ctx, run); err != nil {
			commandErr = err
			finalizationBlocked = true
		} else {
			if s.beforeRefTransaction != nil {
				s.beforeRefTransaction()
			}
			// The callback reads run, plan and the held credential authority
			// guard. The runner joins it for one grace interval; if it is still
			// running after that, wait here so nothing below runs, and no guard
			// is released, while the callback can still observe them. Every
			// blocking step in the callback takes preparedCtx, so this wait is
			// bounded by the transaction deadline.
			callbackDone := make(chan struct{})
			_, err := s.Repositories.Git.RunPreparedUpdateContext(ctx, repositoryPath, commands,
				run.limits.commandLimits(run.limits.PublishTimeout), func(preparedCtx context.Context) error {
					defer close(callbackDone)
					if s.whileRefsPrepared != nil {
						s.whileRefsPrepared()
					}
					if err := s.validatePreparedRefKinds(preparedCtx, repositoryPath, plan, transactionRefs); err != nil {
						return err
					}
					return s.authorityCurrent(preparedCtx, run)
				})
			if errors.Is(err, gitexec.ErrPreparedCallbackDetached) {
				<-callbackDone
			}
			if err == nil && s.afterPreparedRefResult != nil {
				err = s.afterPreparedRefResult()
			}
			if err != nil {
				var problem *Problem
				if errors.Is(err, gitexec.ErrPreparedProcessNotReaped) {
					commandErr = newProblem(CodeUnresolved, "destination ref transaction process could not be reaped; its outcome is left for reconciliation", err)
					processUnreaped = true
					run.refProcessUnreaped = true
				} else if errors.As(err, &problem) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					commandErr = err
				} else {
					commandErr = newProblem(CodePublishFailed, "destination ref transaction failed", err)
				}
			} else if err := s.Store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentApplied, "", "", intent.Reason, now); err != nil {
				commandErr = newProblem(CodeStateUnavailable, "applied publication state could not be recorded", err)
				finalizationBlocked = true
			}
		}
	}
	// A failed ref transaction or mandatory applied-state write stops the HEAD
	// write. Acquiring HEAD.lock after the ref transaction avoids colliding with
	// Git's implicit HEAD lock when the transaction advances its current branch.
	// Exact readback below classifies the intentionally sequential boundary.
	if commandErr == nil && plan.headChange {
		if s.beforeFinalHEADLock != nil {
			s.beforeFinalHEADLock()
		}
		lock, commandErr = s.acquireHEADLock(ctx, run, repositoryPath, plan.headExpected)
		if commandErr != nil {
			finalizationBlocked = true
		}
		if commandErr == nil {
			if err := s.authorityCurrent(ctx, run); err != nil {
				commandErr = err
				finalizationBlocked = true
			} else if err := lock.commit(plan.headDesired); err != nil {
				commandErr = newProblem(CodePublishFailed, "HEAD exact-old write failed", err)
			} else {
				intent.HeadOwned = true
				if err := s.Store.UpdateImportIntentHEADOwnership(ctx, intent.ID, state.ImportIntentApplied, intent.Reason, now); err != nil {
					commandErr = newProblem(CodeStateUnavailable, "applied HEAD state could not be recorded", err)
					finalizationBlocked = true
				}
			}
		}
	}
	if lock != nil {
		if err := lock.rollback(); err != nil {
			commandErr = errors.Join(commandErr, fmt.Errorf("release owned HEAD lock: %w", err))
			finalizationBlocked = true
		}
	}

	// A run that was cancelled or reached its deadline during the writes still
	// reads its outcome back and records an honest intent state. This
	// bookkeeping context never authorizes another destination write.
	settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), run.limits.PublishTimeout)
	defer cancelSettle()
	if processUnreaped {
		// The update-ref process may still run in the repository directory and
		// change it after any readback, so this run settles nothing from
		// readback: it neither publishes, marks not-applied, nor removes an
		// unpublished initial directory. The intent stays unresolved and the
		// destination stays as it is. Reconciliation after restart, when the
		// process and its kill-on-close job are gone, settles it.
		reason := importIntentReason(intent, "the ref transaction process could not be reaped; its outcome is left for reconciliation")
		if err := s.Store.UpdateImportIntent(settleCtx, intent.ID, state.ImportIntentUnresolved, "", "", reason, now); err != nil {
			return newProblem(CodeStateUnavailable, "unresolved publication state could not be recorded", errors.Join(commandErr, err))
		}
		return commandErr
	}
	observation, observeErr := s.observeIntent(settleCtx, repositoryPath, intent)
	if observeErr != nil {
		reason := importIntentReason(intent, observeErr.Error())
		stateErr := s.Store.UpdateImportIntent(settleCtx, intent.ID, state.ImportIntentUnresolved, "", "", reason, now)
		if stateErr != nil {
			return newProblem(CodeStateUnavailable, "unresolved publication state could not be recorded", errors.Join(observeErr, stateErr))
		}
		return newProblem(CodeUnresolved, "destination state could not be verified after publication", errors.Join(commandErr, observeErr))
	}
	if observation.matchesDesired && observation.retentionComplete && observation.headMatches {
		if finalizationBlocked {
			return commandErr
		}
		// Once the refs are in a visible repository, the import is published
		// and a cancellation comes too late: the rest records the outcome
		// without the run's cancellation. A changed authority still stops it.
		// A first import is visible only after finishInitialDestination renames
		// its directory into place, so a cancel before that still stops it.
		publishedCtx, cancelPublished := context.WithTimeout(context.WithoutCancel(ctx), run.limits.PublishTimeout)
		defer cancelPublished()
		if run.initialDestination == nil {
			ctx = publishedCtx
			if err := s.authorityUnchanged(ctx, run); err != nil {
				return err
			}
		} else if err := s.authorityCurrent(ctx, run); err != nil {
			return err
		}
		if run.initialDestination != nil {
			if err := s.finishInitialDestination(ctx, run, &intent, observation, now); err != nil {
				return err
			}
			ctx = publishedCtx
			confirmed, confirmErr := s.observeIntent(ctx, run.initialDestination.finalPath, intent)
			if confirmErr != nil || !confirmed.matchesDesired || !confirmed.retentionComplete || !confirmed.headMatches {
				cause := confirmErr
				if cause == nil {
					cause = errors.New("renamed destination did not match the publication receipt")
				}
				return ownerRecoveryProblem(run.initialDestination.finalPath, cause)
			}
			observation = confirmed
		}
		run.run.FinishedAt = now
		run.run.Status = state.ImportRunComplete
		run.run.ErrorClass = ""
		run.run = s.settleStagingAt(ctx, run.staging, run.run, now)
		run.stagingSettled = true
		if err := s.authorityUnchanged(ctx, run); err != nil {
			return err
		}
		receipt, digest := buildReceipt(intent, observation)
		observations, err := intentObservations(intent, now)
		if err != nil {
			return newProblem(CodeUnresolved, "source observations could not be reconstructed", err)
		}
		finalReason := intent.Reason
		if commandErr != nil {
			finalReason = importIntentReason(intent, "a write command reported failure, but exact readback proved the desired outcome")
		}
		if err := s.Store.FinalizeImportPublication(ctx, intent.ID, receipt, digest, finalReason, observations, run.run); err != nil {
			return newProblem(CodeStateUnavailable, "publication receipt, observations, and run outcome could not be finalized", err)
		}
		run.publicationFinalized = true
		return nil
	}
	if observation.matchesExpected {
		reason := "no destination change was observed"
		if commandErr != nil {
			reason = importIntentReason(intent, commandErr.Error())
		} else {
			reason = importIntentReason(intent, reason)
		}
		if err := s.Store.UpdateImportIntent(settleCtx, intent.ID, state.ImportIntentNotApplied, "", "", reason, now); err != nil {
			return newProblem(CodeStateUnavailable, "not-applied publication state could not be recorded", errors.Join(commandErr, err))
		}
		if commandErr != nil {
			return commandErr
		}
		return newProblem(CodeDestinationChanged, "no destination change was observed", nil)
	}
	reason := importIntentReason(intent, describeIntentMismatch(plan, observation))
	if run.initialDestination != nil {
		return s.abandonUnpublishedInitial(ctx, settleCtx, run.initialDestination, intent, commandErr, reason, now)
	}
	if err := s.Store.UpdateImportIntent(settleCtx, intent.ID, state.ImportIntentUnresolved, "", "", reason, now); err != nil {
		return newProblem(CodeStateUnavailable, "partial publication state could not be recorded", errors.Join(commandErr, err))
	}
	if commandErr == nil {
		commandErr = errors.New(reason)
	}
	return newProblem(CodeUnresolved, "destination contains a partial or independently changed publication", commandErr)
}

func (s *Service) inspectPublicationRefKinds(ctx context.Context, run *runState, repositoryPath string, refs, symrefs map[string]string, head headIdentity) error {
	storage, err := s.repositoryRefStorage(ctx, run, repositoryPath)
	if err != nil {
		return err
	}
	if storage != "files" {
		return newProblem(CodeUnsupported, fmt.Sprintf("destination ref storage %q does not support exact ref-kind publication", storage), nil)
	}
	names := make([]string, 0, len(run.selected.refs))
	for _, ref := range run.selected.refs {
		names = append(names, ref.Name)
	}
	if err := s.readExactRefKinds(ctx, repositoryPath, refs, symrefs, names); err != nil {
		return newProblem(CodeRepositoryMissing, "destination candidate ref kinds could not be read", err)
	}
	retentionNames := make([]string, 0, len(names)*2+2)
	for _, ref := range run.selected.refs {
		oid := refs[ref.Name]
		kind, ok := refKind(ref.Name)
		if oid == "" || !ok {
			continue
		}
		retentionNames = append(retentionNames, repository.RetainedRefName(kind, oid), repository.ProvenanceRefName(kind, refShortName(ref.Name), oid))
	}
	if head.kind == headDetached {
		retentionNames = append(retentionNames, detachedHEADRetentionNames(head.oid)...)
	}
	if err := s.readExactRefKinds(ctx, repositoryPath, refs, symrefs, retentionNames); err != nil {
		return newProblem(CodeRepositoryMissing, "destination retention ref kinds could not be read", err)
	}
	return nil
}

func (s *Service) readExactRefKinds(ctx context.Context, repositoryPath string, refs, symrefs map[string]string, names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		identity, err := readLooseRefIdentity(repositoryPath, name)
		if err != nil {
			return fmt.Errorf("inspect ref %q: %w", name, err)
		}
		if !identity.exists {
			if symrefs[name] != "" {
				return fmt.Errorf("ref %q changed while its loose identity was inspected", name)
			}
			if refs[name] != "" {
				if err := validatePackedRefsPath(repositoryPath); err != nil {
					return fmt.Errorf("inspect packed identity for ref %q: %w", name, err)
				}
			}
			continue
		}
		if identity.symbolic {
			if _, err := s.Repositories.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "check-ref-format", identity.target); err != nil {
				return fmt.Errorf("ref %q has invalid symbolic target %q: %w", name, identity.target, err)
			}
			symrefs[name] = identity.target
			continue
		}
		delete(symrefs, name)
		refs[name] = identity.oid
	}
	return nil
}

// publicationRefPrefixes bounds every destination ref query to fixed namespace
// arguments. Selection by name happens in memory, so argv size never depends
// on the number or length of transaction ref names.
var publicationRefPrefixes = []string{"refs/heads", "refs/tags", "refs/owngit"}

// readPublicationRefs enumerates the fixed namespaces and then reads the exact
// immediate kind of every named ref, including dangling and cyclic aliases that
// enumeration omits.
func (s *Service) readPublicationRefs(ctx context.Context, repositoryPath string, names []string) (map[string]string, map[string]string, error) {
	records, _, err := s.Repositories.ReadRefs(ctx, repositoryPath, 0, publicationRefPrefixes...)
	if err != nil {
		return nil, nil, newProblem(CodeRepositoryMissing, "destination ref kinds could not be read", err)
	}
	refs := make(map[string]string, len(records))
	symrefs := make(map[string]string)
	for _, record := range records {
		refs[record.Name] = record.OID
		if record.SymrefTarget != "" {
			symrefs[record.Name] = record.SymrefTarget
		}
	}
	if err := s.readExactRefKinds(ctx, repositoryPath, refs, symrefs, names); err != nil {
		return nil, nil, newProblem(CodeDestinationChanged, "destination ref identity is unsafe", err)
	}
	return refs, symrefs, nil
}

func (s *Service) validatePreparedRefKinds(ctx context.Context, repositoryPath string, plan *publicationPlan, transactionRefs map[string]string) error {
	names := sortedKeys(transactionRefs)
	refs, symrefs, err := s.readPublicationRefs(ctx, repositoryPath, names)
	if err != nil {
		return err
	}
	for _, name := range names {
		if target := symrefs[name]; target != "" {
			return newProblem(CodeDestinationChanged, fmt.Sprintf("destination ref %q became symbolic to %q", name, target), nil)
		}
		actual, exists := refs[name]
		if plan.expected[name] != "" && (!exists || actual != plan.expected[name]) {
			return newProblem(CodeDestinationChanged, fmt.Sprintf("destination ref %q changed while its transaction was prepared", name), nil)
		}
		if plan.expected[name] == "" && exists {
			return newProblem(CodeDestinationChanged, fmt.Sprintf("destination ref %q appeared while its transaction was prepared", name), nil)
		}
	}
	return nil
}

type intentObservation struct {
	refs              map[string]string
	matchesDesired    bool
	matchesExpected   bool
	retentionComplete bool
	head              headIdentity
	headMatches       bool
}

func (s *Service) observeIntent(ctx context.Context, repositoryPath string, intent state.ImportIntent) (intentObservation, error) {
	expectedHEADValue, hasExpectedHEAD := intent.Expected[state.ImportHeadRef]
	desiredHEADValue, hasDesiredHEAD := intent.Desired[state.ImportHeadRef]
	if !hasExpectedHEAD || !hasDesiredHEAD {
		return intentObservation{}, errors.New("legacy publication intent has no exact old and desired HEAD identity")
	}
	expectedHEAD, err := decodeHeadIdentity(expectedHEADValue)
	if err != nil {
		return intentObservation{}, err
	}
	desiredHEAD, err := decodeHeadIdentity(desiredHEADValue)
	if err != nil {
		return intentObservation{}, err
	}
	names := make([]string, 0, len(intent.Expected)+len(intent.Desired)+len(intent.Retained))
	for _, source := range []map[string]string{intent.Expected, intent.Desired, intent.Retained} {
		for ref := range source {
			if ref != state.ImportHeadRef {
				names = append(names, ref)
			}
		}
	}
	sort.Strings(names)
	refs, symrefs, err := s.readPublicationRefs(ctx, repositoryPath, names)
	if err != nil {
		return intentObservation{}, err
	}
	observation := intentObservation{refs: refs, matchesDesired: true, matchesExpected: true, retentionComplete: true}
	// A planned write (expected differs from desired) is proven only by an
	// exact direct ref or exact absence. A ref that was left alone keeps
	// whatever immediate kind resolves to the recorded value, so a pre-existing
	// resolved alias stays independent.
	exactState := func(ref, value string) bool {
		if value == "" {
			return refs[ref] == "" && symrefs[ref] == ""
		}
		return refs[ref] == value && symrefs[ref] == ""
	}
	for ref, desired := range intent.Desired {
		if ref == state.ImportHeadRef {
			continue
		}
		if intent.Expected[ref] == desired {
			if refs[ref] != desired {
				observation.matchesDesired = false
			}
		} else if !exactState(ref, desired) {
			observation.matchesDesired = false
		}
	}
	for ref, expected := range intent.Expected {
		if ref == state.ImportHeadRef {
			continue
		}
		if intent.Desired[ref] == expected {
			if refs[ref] != expected {
				observation.matchesExpected = false
			}
		} else if !exactState(ref, expected) {
			observation.matchesExpected = false
		}
	}
	for ref, oid := range intent.Retained {
		if !exactState(ref, oid) {
			observation.retentionComplete = false
		}
	}
	headSymref, headOID, err := s.Repositories.ReadHead(ctx, repositoryPath)
	if err != nil {
		return intentObservation{}, err
	}
	observation.head, err = destinationHEADIdentity(headSymref, headOID)
	if err != nil {
		return intentObservation{}, err
	}
	observation.headMatches = sameHEADObservation(observation.head, desiredHEAD)
	if !observation.headMatches {
		observation.matchesDesired = false
	}
	if !sameHEADObservation(observation.head, expectedHEAD) {
		observation.matchesExpected = false
	}
	return observation, nil
}

func buildReceipt(intent state.ImportIntent, observation intentObservation) (string, string) {
	receipt := map[string]string{}
	for ref, desired := range intent.Desired {
		if ref == state.ImportHeadRef {
			if observation.headMatches {
				receipt[ref] = desired
			}
			continue
		}
		if observation.refs[ref] == desired {
			receipt[ref] = desired
		}
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return "", ""
	}
	body := string(encoded)
	return body, state.ImportReceiptDigest(body)
}

func describeIntentMismatch(plan *publicationPlan, observation intentObservation) string {
	var mismatches []string
	for _, ref := range sortedKeys(plan.desired) {
		if ref == state.ImportHeadRef {
			if !observation.headMatches {
				mismatches = append(mismatches, ref)
			}
			continue
		}
		if observation.refs[ref] != plan.desired[ref] {
			mismatches = append(mismatches, ref)
		}
	}
	if len(mismatches) > 5 {
		mismatches = append(mismatches[:5], "...")
	}
	reason := "destination refs do not match the intent"
	if len(mismatches) > 0 {
		reason += ": " + strings.Join(mismatches, ", ")
	}
	return reason
}

// reconcileRepositoryIntentsLocked verifies every open intent against actual
// refs. A receipt is written only for values that were read back.
func (s *Service) reconcileRepositoryIntentsLocked(ctx context.Context, repositoryPath, repositoryID, generation string, now time.Time) error {
	intents, err := s.Store.PendingImportIntents(ctx, repositoryID)
	if err != nil {
		return err
	}
	return s.reconcileIntentListLocked(ctx, repositoryPath, repositoryID, generation, now, intents)
}

func (s *Service) reconcileIntentListLocked(ctx context.Context, repositoryPath, repositoryID, generation string, now time.Time, intents []state.ImportIntent) error {
	if _, err := s.currentRuntime(generation); err != nil {
		return err
	}
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return err
		}
		if intent.RepositoryID != repositoryID {
			return errors.New("pending import intent belongs to another repository")
		}
		run, exists, err := s.Store.ImportRun(ctx, intent.RunID)
		if err != nil {
			return err
		}
		if !exists || run.RepositoryID != intent.RepositoryID || run.SourceGeneration != intent.SourceGeneration || run.AuthorityRevision != intent.AuthorityRevision {
			return errors.New("pending import intent does not match its run")
		}
		allowed, err := s.initialIntentObservationAllowed(ctx, intent, repositoryPath, generation)
		if err != nil {
			return err
		}
		if !allowed {
			// An initial publication is judged only in the directory proven to
			// belong to its run. Another empty repository must not mark it
			// not-applied or complete. An earlier initial import of this name
			// whose directory is gone is settled as never published.
			if _, err := s.settleGoneInitialIntent(ctx, intent, now); err != nil {
				return err
			}
			continue
		}
		source, sourceExists, err := s.Store.ImportSource(ctx, repositoryID)
		if err != nil {
			return err
		}
		authorityMatches := sourceExists && source.SourceGeneration == intent.SourceGeneration && source.AuthorityRevision == intent.AuthorityRevision
		observation, err := s.observeIntent(ctx, repositoryPath, intent)
		if err != nil {
			if _, ownershipErr := s.currentRuntime(generation); ownershipErr != nil {
				return ownershipErr
			}
			reason := importIntentReason(intent, err.Error())
			if updateErr := s.Store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentUnresolved, "", "", reason, now); updateErr != nil {
				return errors.Join(err, updateErr)
			}
			if err := s.markRunUnresolved(ctx, intent, reason, now); err != nil {
				return err
			}
			return newProblem(CodeUnresolved, "legacy or unreadable publication intent lacks required reconciliation facts", err)
		}
		if _, err := s.currentRuntime(generation); err != nil {
			return err
		}
		switch {
		case observation.matchesDesired && observation.retentionComplete && observation.headMatches:
			receipt, digest := buildReceipt(intent, observation)
			reason := "confirmed during reconciliation"
			if !authorityMatches {
				reason = "publication confirmed after its execution authority was invalidated"
			}
			reason = importIntentReason(intent, reason)
			completeRunFromIntent(&run, intent, now)
			facts, err := intentObservations(intent, now)
			if err != nil {
				return err
			}
			if err := s.Store.FinalizeImportPublication(ctx, intent.ID, receipt, digest, reason, facts, run); err != nil {
				return err
			}
		case observation.matchesExpected:
			status := state.ImportIntentNotApplied
			reason := "publication had not been applied"
			if !authorityMatches {
				status = state.ImportIntentInvalidated
				reason = "publication authority changed before any destination write"
			}
			reason = importIntentReason(intent, reason)
			if err := s.Store.UpdateImportIntent(ctx, intent.ID, status, "", "", reason, now); err != nil {
				return err
			}
		default:
			reason := importIntentReason(intent, describeIntentMismatch(mapsToDesiredPlan(intent), observation))
			if err := s.Store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentUnresolved, "", "", reason, now); err != nil {
				return err
			}
			if err := s.markRunUnresolved(ctx, intent, reason, now); err != nil {
				return err
			}
			return newProblem(CodeUnresolved, "prior publication remains partial or independently changed", nil)
		}
	}
	return nil
}

func appendIntentEvidence(reason, evidence string) string {
	if strings.Contains(reason, evidence) {
		return reason
	}
	if reason == "" {
		return evidence
	}
	return boundedImportMessage(evidence + "; " + reason)
}

func importIntentReason(intent state.ImportIntent, outcome string) string {
	if strings.Contains(intent.Reason, headDivergenceEvidence) {
		outcome = appendIntentEvidence(outcome, headDivergenceEvidence)
	}
	return boundedImportMessage(outcome)
}

func mapsToDesiredPlan(intent state.ImportIntent) *publicationPlan {
	return &publicationPlan{desired: intent.Desired, expected: intent.Expected}
}

func completeRunFromIntent(run *state.ImportRun, intent state.ImportIntent, now time.Time) {
	run.Status = state.ImportRunComplete
	run.FinishedAt = now
	run.ErrorClass = ""
	run.Message = "publication confirmed during reconciliation"
	run.RefsCreated = 0
	run.RefsUpdated = 0
	run.RefsUnchanged = 0
	run.RefsDivergent = 0
	run.RefsDeletedUpstream = 0
	for ref, source := range intent.Observed {
		if ref == state.ImportHeadRef {
			continue
		}
		desired, exists := intent.Desired[ref]
		if !exists {
			// planPublication records an observed ref with an expected absence
			// and no desired value only when a case-only destination variant
			// left it uncreated, which it counts as divergent.
			if expected, planned := intent.Expected[ref]; planned && expected == "" {
				run.RefsDivergent++
			} else {
				run.RefsDeletedUpstream++
			}
			continue
		}
		expected := intent.Expected[ref]
		switch {
		case expected == desired:
			if source != desired {
				run.RefsDivergent++
			} else {
				run.RefsUnchanged++
			}
		case expected == "":
			run.RefsCreated++
		default:
			run.RefsUpdated++
		}
	}
	if strings.Contains(intent.Reason, headDivergenceEvidence) {
		run.RefsDivergent++
	}
}

func (s *Service) markRunUnresolved(ctx context.Context, intent state.ImportIntent, reason string, now time.Time) error {
	run, exists, err := s.Store.ImportRun(ctx, intent.RunID)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("import intent run is missing")
	}
	if run.Status != state.ImportRunInterrupted && run.Status != state.ImportRunUnresolved {
		return nil
	}
	run.Status = state.ImportRunUnresolved
	run.FinishedAt = now
	run.ErrorClass = CodeUnresolved
	if run.Message == "" {
		run.Message = boundedImportMessage(reason)
	}
	return s.Store.FinishImportRun(ctx, run)
}

func (s *Service) readDestinationState(ctx context.Context, repositoryPath string) (map[string]string, map[string]string, headIdentity, error) {
	records, _, err := s.Repositories.ReadRefs(ctx, repositoryPath, 0, publicationRefPrefixes...)
	if err != nil {
		return nil, nil, headIdentity{}, newProblem(CodeRepositoryMissing, "destination refs could not be read", err)
	}
	refs := make(map[string]string, len(records))
	symrefs := make(map[string]string)
	for _, record := range records {
		refs[record.Name] = record.OID
		if record.SymrefTarget != "" {
			symrefs[record.Name] = record.SymrefTarget
		}
	}
	headSymref, headOID, err := s.Repositories.ReadHead(ctx, repositoryPath)
	if err != nil {
		return nil, nil, headIdentity{}, newProblem(CodeRepositoryMissing, "destination HEAD could not be read", err)
	}
	head, err := destinationHEADIdentity(headSymref, headOID)
	if err != nil {
		return nil, nil, headIdentity{}, newProblem(CodeUnsupported, "destination HEAD representation is unsupported", err)
	}
	return refs, symrefs, head, nil
}

type priorObservations struct {
	refs      map[string]string
	head      headIdentity
	headKnown bool
	headOwned bool
}

func (s *Service) observationMap(ctx context.Context, run *runState) (priorObservations, error) {
	if s.beforeObservationRead != nil {
		s.beforeObservationRead(ctx)
	}
	records, err := s.Store.ImportObservations(ctx, run.run.RepositoryID, run.run.SourceGeneration)
	if err != nil {
		return priorObservations{}, runStateReadProblem("recorded observations could not be read", err)
	}
	observations := priorObservations{refs: make(map[string]string, len(records))}
	for _, record := range records {
		if record.RunID == "" {
			// A legacy fact with no related run cannot establish write ownership.
			continue
		}
		observedRun, exists, err := s.Store.ImportRun(ctx, record.RunID)
		if err != nil {
			return priorObservations{}, runStateReadProblem("observation run could not be read", err)
		}
		if !exists || observedRun.Status != state.ImportRunComplete || observedRun.RepositoryID != record.RepositoryID || observedRun.SourceGeneration != record.SourceGeneration {
			continue
		}
		if record.RefName == state.ImportHeadRef {
			switch {
			case record.SymrefTarget != "":
				observations.head = headIdentity{kind: headSymbolic, target: record.SymrefTarget, oid: record.OID}
			case record.OID != "":
				observations.head = headIdentity{kind: headDetached, oid: record.OID}
			default:
				observations.head = headIdentity{kind: headAbsent}
			}
			observations.headKnown = true
			intent, exists, err := s.Store.CompletedImportIntentForRun(ctx, record.RunID)
			if err != nil {
				return priorObservations{}, runStateReadProblem("HEAD observation receipt could not be read", err)
			}
			observations.headOwned = exists && intent.RepositoryID == record.RepositoryID &&
				intent.SourceGeneration == record.SourceGeneration && intent.AuthorityRevision == observedRun.AuthorityRevision &&
				intentProvesHEADOwnership(intent, observations.head)
			continue
		}
		observations.refs[record.RefName] = record.OID
	}
	return observations, nil
}

func intentProvesHEADOwnership(intent state.ImportIntent, observed headIdentity) bool {
	if !intent.HeadOwned {
		return false
	}
	observedFact, exists := intent.Observed[state.ImportHeadRef]
	if !exists {
		return false
	}
	intentObserved, err := decodeHeadIdentity(observedFact)
	if err != nil || !sameHEADIdentity(intentObserved, observed) {
		return false
	}
	var receipt map[string]string
	if err := json.Unmarshal([]byte(intent.ReceiptJSON), &receipt); err != nil {
		return false
	}
	receiptFact, exists := receipt[state.ImportHeadRef]
	if !exists {
		return false
	}
	receiptHEAD, err := decodeHeadIdentity(receiptFact)
	return err == nil && sameHEADIdentity(receiptHEAD, observed)
}

// recordIntentObservations stores the source facts the intent captured. A
// divergent local ref still advances the observation, because the next refresh
// must compare against the current source tip.
func intentObservations(intent state.ImportIntent, now time.Time) ([]state.ImportObservation, error) {
	list := make([]state.ImportObservation, 0, len(intent.Observed))
	for _, ref := range sortedKeys(intent.Observed) {
		fact := intent.Observed[ref]
		observation := state.ImportObservation{
			RepositoryID: intent.RepositoryID, SourceGeneration: intent.SourceGeneration,
			RefName: ref, ObservedAt: now, RunID: intent.RunID,
		}
		if ref == state.ImportHeadRef {
			head, err := decodeHeadIdentity(fact)
			if err != nil {
				return nil, err
			}
			observation.OID = head.oid
			if head.kind == headSymbolic {
				observation.SymrefTarget = head.target
			}
		} else {
			observation.OID = fact
		}
		list = append(list, observation)
	}
	return list, nil
}

func (s *Service) authorityCurrent(ctx context.Context, run *runState) error {
	return s.checkAuthority(ctx, run, true)
}

// authorityUnchanged is authorityCurrent for the steps after publication, when
// the refs are already visible: a changed source, credential, or runtime still
// stops the run, but a cancellation request comes too late and is ignored.
func (s *Service) authorityUnchanged(ctx context.Context, run *runState) error {
	return s.checkAuthority(ctx, run, false)
}

func (s *Service) checkAuthority(ctx context.Context, run *runState, honorCancel bool) error {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return cancellationProblem(ctx, "import was cancelled before a sensitive boundary", err)
		}
		return newProblem(CodeLimit, "import deadline expired before a sensitive boundary", err)
	}
	source, exists, err := s.Store.ImportSource(ctx, run.run.RepositoryID)
	if err != nil {
		return runStateReadProblem("import source could not be re-read", err)
	}
	var credentialRevision uint64
	var credentialBlocked bool
	if run.credentialAuthorityLocked {
		credentialRevision, credentialBlocked = s.Store.ImportCredentialAuthorityLocked(run.run.RepositoryID)
	} else {
		credentialRevision, credentialBlocked = s.Store.ImportCredentialAuthority(run.run.RepositoryID)
	}
	if !exists || source.SourceGeneration != run.run.SourceGeneration || source.AuthorityRevision != run.run.AuthorityRevision ||
		source.CredentialGeneration != run.source.CredentialGeneration || credentialBlocked || credentialRevision != run.credentialRevision {
		return newProblem(CodeSuperseded, "the import authority changed while the run was preparing", ErrSuperseded)
	}
	if run.requestAuthorityPinned {
		credential, stored, err := s.Store.LoadImportCredentials(ctx, run.run.RepositoryID)
		if err != nil {
			return runStateReadProblem("stored import credential could not be revalidated", err)
		}
		present := stored && credential.Bound(source)
		generation := ""
		if present {
			generation = credential.CredentialGeneration
		}
		if present != run.requestCredentialPresent || generation != run.requestCredentialGeneration {
			return newProblem(CodeSuperseded, "the import credential changed while the run was preparing", ErrSuperseded)
		}
	}
	current, exists, err := s.Store.ImportRun(ctx, run.run.ID)
	if err != nil {
		return runStateReadProblem("import run could not be re-read", err)
	}
	if honorCancel && exists && current.CancelRequestedAt != nil {
		return newProblem(CodeCancelled, "import was cancelled before publication", nil)
	}
	return nil
}

// reconcileRepositoryIntents verifies open intents under the repository write
// lock so its reads cannot race a publication.
func (s *Service) reconcileRepositoryIntents(ctx context.Context, repositoryPath, repositoryID, generation string) error {
	now := s.clock()
	lock := s.Repositories.Locks.For(repositoryID)
	lock.Lock()
	err := s.reconcileRepositoryIntentsLocked(ctx, repositoryPath, repositoryID, generation, now)
	lock.Unlock()
	return err
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
