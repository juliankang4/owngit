package importsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// runState carries one in-flight import. It is owned by a single goroutine; the
// repository write lock is only taken during publication.
type activeExecution struct {
	repositoryID       string
	authorityRevision  int64
	credentialRevision uint64
	cancel             context.CancelCauseFunc
}

type runState struct {
	service                     *Service
	limits                      Limits
	run                         state.ImportRun
	source                      state.ImportSource
	name                        string
	description                 string
	staging                     stagingDir
	stagingPath                 string
	runtimeGeneration           string
	credentialRevision          uint64
	requestAuthorityPinned      bool
	requestCredentialPresent    bool
	requestCredentialGeneration string
	credentialAuthorityLocked   bool
	advertisement               *importgit.Advertisement
	selected                    selectedRefs
	objectFormat                string
	newDestination              bool
	bindingSnapshot             *state.ImportBindingSnapshot
	writtenBinding              state.ImportSource
	initialDestination          *initialDestination
	destinationKeep             string
	inspection                  lfsInspection
	consumeErr                  error
	stagingSettled              bool
	publicationFinalized        bool
}

// execute runs one import or refresh from start to terminal state. The
// per-repository run mutex is held for the whole run so two imports cannot
// stage and publish the same repository concurrently.
func (s *Service) execute(parent context.Context, repositoryID, name, description, kind string, limits Limits, newDestination bool, snapshot *state.ImportBindingSnapshot, written state.ImportSource) (state.ImportRun, error) {
	limits, err := limits.effective()
	if err != nil {
		return state.ImportRun{}, newProblem(CodeRuntimeUnavailable, err.Error(), err)
	}
	if s.Store == nil || s.Repositories == nil || s.Repositories.Git == nil {
		return state.ImportRun{}, newProblem(CodeRuntimeUnavailable, "import runtime is not configured", nil)
	}
	mutex := s.repositoryLock(repositoryID)
	if !mutex.TryLock() {
		return state.ImportRun{}, newProblem(CodeBusy, "an import is already running for this repository", ErrBusy)
	}
	defer mutex.Unlock()

	ctx := parent
	source, exists, err := s.Store.ImportSource(ctx, repositoryID)
	if err != nil {
		return state.ImportRun{}, newProblem(CodeStateUnavailable, "import source could not be read", err)
	}
	if !exists {
		return state.ImportRun{}, newProblem(CodeNotConfigured, "configure an import source first", ErrNotConfigured)
	}
	credentialRevision, credentialBlocked := s.Store.ImportCredentialAuthority(repositoryID)
	if credentialBlocked {
		return state.ImportRun{}, newProblem(CodeStateUnavailable, "an import credential mutation must be retried before another run", nil)
	}
	if name == "" {
		name = deriveRepositoryName(source.URL)
	}
	if err := repository.ValidateName(name, description); err != nil {
		return state.ImportRun{}, newProblem(CodeInvalidSource, err.Error(), err)
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	if limits.RunTimeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, limits.RunTimeout)
		defer timeoutCancel()
	}

	now := s.clock()
	runID, err := newImportID()
	if err != nil {
		return state.ImportRun{}, err
	}
	// Admission: register the run and commit its durable row under the lifecycle
	// read side, so Reconcile can never snapshot between the two and interrupt a
	// run that is already live. The transport is not touched here.
	s.lifecycle.RLock()
	if s.closing {
		s.lifecycle.RUnlock()
		return state.ImportRun{}, newProblem(CodeRuntimeUnavailable, "OwnGit is shutting down", ErrShuttingDown)
	}
	currentSource, currentExists, currentErr := s.Store.ImportSource(ctx, repositoryID)
	if currentErr != nil {
		s.lifecycle.RUnlock()
		return state.ImportRun{}, newProblem(CodeStateUnavailable, "import authority could not be confirmed at admission", currentErr)
	}
	currentCredentialRevision, currentCredentialBlocked := s.Store.ImportCredentialAuthority(repositoryID)
	// A ConfigureSource that lands after bindNewImport must not become the
	// identity used to restore the pre-import snapshot. Do not restore here.
	if newDestination && !importBindingStillWritten(currentExists, currentSource, written) {
		s.lifecycle.RUnlock()
		return state.ImportRun{}, newProblem(CodeSuperseded, "import authority changed before admission", ErrSuperseded)
	}
	if !currentExists || currentSource.SourceGeneration != source.SourceGeneration || currentSource.AuthorityRevision != source.AuthorityRevision ||
		currentSource.CredentialGeneration != source.CredentialGeneration || currentCredentialBlocked || currentCredentialRevision != credentialRevision {
		s.lifecycle.RUnlock()
		return state.ImportRun{}, newProblem(CodeSuperseded, "import authority changed before admission", ErrSuperseded)
	}
	s.active.Store(runID, activeExecution{
		repositoryID: repositoryID, authorityRevision: source.AuthorityRevision,
		credentialRevision: credentialRevision, cancel: cancel,
	})
	defer s.active.Delete(runID)
	staging, err := s.acquireStaging(ctx, runID, repositoryID, now)
	if err != nil {
		s.lifecycle.RUnlock()
		return state.ImportRun{}, err
	}
	run := &runState{
		service: s, limits: limits, source: source, name: name, description: description, newDestination: newDestination, bindingSnapshot: snapshot, writtenBinding: written,
		staging: staging, stagingPath: staging.path, runtimeGeneration: staging.generation, credentialRevision: credentialRevision,
		run: state.ImportRun{
			ID: runID, RepositoryID: repositoryID, SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
			Kind: kind, Status: state.ImportRunPreparing, StartedAt: now, CreatedAt: now, StagingName: staging.name,
		},
	}
	if s.beforeRunRecord != nil {
		s.beforeRunRecord()
	}
	beginErr := s.Store.BeginImportRun(ctx, run.run)
	s.lifecycle.RUnlock()
	if beginErr != nil {
		finishCtx := context.WithoutCancel(parent)
		run.run = s.settleStaging(finishCtx, staging, run.run)
		if errors.Is(beginErr, state.ErrImportActive) {
			return run.run, newProblem(CodeBusy, "an import is already running for this repository", ErrBusy)
		}
		if errors.Is(beginErr, state.ErrImportSourceChanged) {
			return run.run, newProblem(CodeSuperseded, "import authority changed before admission", ErrSuperseded)
		}
		return run.run, newProblem(CodeStateUnavailable, "import run could not be recorded", beginErr)
	}

	pipelineErr := s.runPipeline(ctx, run)
	pipelineErr = stoppedStageFailure(ctx, run.run.Status, pipelineErr)
	return s.finishRun(context.WithoutCancel(parent), run, pipelineErr)
}

// activeCancel returns the stored cancel function for a run.
func (s *Service) activeCancel(runID string) context.CancelCauseFunc {
	value, exists := s.active.Load(runID)
	if !exists {
		return nil
	}
	execution, _ := value.(activeExecution)
	return execution.cancel
}

func (s *Service) staleActiveExecution(repositoryID string, authorityRevision int64, credentialRevision uint64) (string, context.CancelCauseFunc) {
	var staleID string
	var staleCancel context.CancelCauseFunc
	s.active.Range(func(key, value any) bool {
		execution, ok := value.(activeExecution)
		id, idOK := key.(string)
		if ok && idOK && execution.repositoryID == repositoryID &&
			(execution.authorityRevision < authorityRevision || execution.credentialRevision < credentialRevision) {
			staleID, staleCancel = id, execution.cancel
			return false
		}
		return true
	})
	return staleID, staleCancel
}

func (s *Service) runPipeline(ctx context.Context, run *runState) error {
	if err := s.authorityCurrent(ctx, run); err != nil {
		return err
	}
	if err := s.fetchAndStage(ctx, run); err != nil {
		return err
	}
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	if err := s.setStatus(ctx, run, state.ImportRunInspecting); err != nil {
		return err
	}
	if s.beforeStagingVerification != nil {
		s.beforeStagingVerification(ctx)
	}
	if err := s.verifyStaging(ctx, run); err != nil {
		return err
	}
	if err := s.inspectLFS(ctx, run); err != nil {
		return err
	}
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	if err := s.recordInspection(run); err != nil {
		return err
	}
	if (run.inspection.Pointers > 0 || !run.inspection.Complete) && !run.source.GitOnlyConsent {
		detail := fmt.Sprintf("source contains %d Git LFS pointer(s)", run.inspection.Pointers)
		if !run.inspection.Complete {
			detail = fmt.Sprintf("Git LFS inspection was incomplete after scanning %d blob(s) and finding %d pointer(s)", run.inspection.Candidates, run.inspection.Pointers)
		}
		return newProblem(CodeLFSRequired, detail+"; enable Git-only consent to accept incomplete content", nil)
	}
	if err := s.authorityCurrent(ctx, run); err != nil {
		return err
	}
	repositoryPath, err := s.ensureDestination(ctx, run)
	if err != nil {
		return err
	}
	if run.initialDestination != nil && s.beforeInitialPublication != nil {
		if err := s.beforeInitialPublication(); err != nil {
			return newProblem(CodeUnresolved, "initial destination publication stopped before objects were indexed", err)
		}
	}
	if err := s.authorityCurrent(ctx, run); err != nil {
		return err
	}
	defer s.releaseDestinationKeep(run, repositoryPath)
	if err := s.ensureObjects(ctx, run, repositoryPath); err != nil {
		return err
	}
	if err := s.authorityCurrent(ctx, run); err != nil {
		return err
	}
	if err := s.setStatus(ctx, run, state.ImportRunPublishing); err != nil {
		return err
	}
	plan, err := s.publish(ctx, run, repositoryPath)
	// Record the planned counts even when publication failed after planning, so
	// a failed run still reports what it saw and intended.
	run.run.RefsCreated = plan.created
	run.run.RefsUpdated = plan.updated
	run.run.RefsUnchanged = plan.unchanged
	run.run.RefsDivergent = plan.divergent
	run.run.RefsDeletedUpstream = plan.deletedUpstream
	run.run.RefsSkipped = int64(len(run.selected.skipped))
	if err != nil {
		return err
	}
	if s.Repositories.OnChange != nil {
		s.Repositories.OnChange(run.run.RepositoryID)
	}
	return nil
}

func (s *Service) recordInspection(run *runState) error {
	run.run.LFSDetected = run.inspection.Pointers
	run.run.LFSInspectionDone = run.inspection.Complete
	run.run.LFSScannedBlobs = run.inspection.Candidates
	run.run.LFSScannedBytes = run.inspection.BytesRead
	return nil
}

// finishRun settles the terminal record. Bookkeeping uses an uncancelled
// context so a cancelled run still records its honest outcome and cleanup.
func (s *Service) finishRun(ctx context.Context, run *runState, pipelineErr error) (state.ImportRun, error) {
	if run.publicationFinalized {
		stored, exists, err := s.Store.ImportRun(ctx, run.run.ID)
		if err != nil {
			return run.run, newProblem(CodeStateUnavailable, "finalized import run could not be confirmed", err)
		}
		if !exists || stored.Status != state.ImportRunComplete {
			return run.run, newProblem(CodeStateUnavailable, "finalized import run is missing its complete outcome", nil)
		}
		run.run = stored
		return run.run, pipelineErr
	}
	run.run.FinishedAt = s.clock()
	if !run.stagingSettled {
		run.run = s.settleStaging(ctx, run.staging, run.run)
		run.stagingSettled = true
	}
	if pipelineErr == nil {
		run.run.Status = state.ImportRunComplete
		run.run.ErrorClass = ""
	} else {
		code := problemCode(pipelineErr)
		switch code {
		case CodeCancelled:
			run.run.Status = state.ImportRunCancelled
		case CodeSuperseded:
			run.run.Status = state.ImportRunSuperseded
		case CodeUnresolved:
			run.run.Status = state.ImportRunUnresolved
		default:
			run.run.Status = state.ImportRunFailed
		}
		run.run.ErrorClass = code
		run.run.Message = boundedImportMessage(pipelineErr.Error())
	}
	if err := s.Store.FinishImportRun(ctx, run.run); err != nil {
		return run.run, errors.Join(pipelineErr, newProblem(CodeStateUnavailable, "import run outcome could not be recorded", err))
	}
	stored, exists, err := s.Store.ImportRun(ctx, run.run.ID)
	if err != nil {
		return run.run, errors.Join(pipelineErr, newProblem(CodeStateUnavailable, "import run outcome could not be confirmed", err))
	}
	if exists {
		run.run.CancelRequestedAt = stored.CancelRequestedAt
	}
	return run.run, pipelineErr
}

func (s *Service) setStatus(ctx context.Context, run *runState, status string) error {
	if err := s.authorityCurrent(ctx, run); err != nil {
		return err
	}
	if err := s.Store.SetImportRunStatus(ctx, run.run.ID, status); err != nil {
		return newProblem(CodeStateUnavailable, "import run stage could not be recorded", err)
	}
	run.run.Status = status
	return nil
}

// fetchAndStage performs the only network work. The consumer creates the
// staging repository with the advertised object format and streams the pack
// into strict local indexing.
func (s *Service) fetchAndStage(ctx context.Context, run *runState) error {
	if err := s.setStatus(ctx, run, state.ImportRunFetching); err != nil {
		return err
	}
	request, err := s.requestFor(ctx, run)
	if err != nil {
		return err
	}
	consumer := func(consumerCtx context.Context, advertisement *importgit.Advertisement, reader io.Reader) error {
		if err := s.authorityCurrent(consumerCtx, run); err != nil {
			run.consumeErr = err
			return err
		}
		if err := s.adoptAdvertisement(run, advertisement); err != nil {
			run.consumeErr = err
			return err
		}
		if err := s.setStatus(consumerCtx, run, state.ImportRunIndexing); err != nil {
			run.consumeErr = err
			return err
		}
		if err := s.createStagingRepository(consumerCtx, run.stagingPath, run.objectFormat, run.limits); err != nil {
			run.consumeErr = err
			return err
		}
		if err := s.authorityCurrent(consumerCtx, run); err != nil {
			run.consumeErr = err
			return err
		}
		if err := s.indexStagingPack(consumerCtx, run.stagingPath, reader, run.limits); err != nil {
			run.consumeErr = err
			return err
		}
		if err := s.runtimeCurrentForRun(run); err != nil {
			run.consumeErr = err
			return err
		}
		return nil
	}
	result, err := s.fetcher()(ctx, request, consumer)
	if err != nil {
		if ctx.Err() != nil {
			return stoppedProblem(ctx, "during source transfer", err)
		}
		if run.consumeErr != nil {
			return run.consumeErr
		}
		return classifyFetchError(err)
	}
	if run.advertisement == nil {
		if result == nil || result.Advertisement == nil {
			return newProblem(CodeProtocol, "source returned no advertisement facts", nil)
		}
		if err := s.authorityCurrent(ctx, run); err != nil {
			return err
		}
		if err := s.adoptAdvertisement(run, result.Advertisement); err != nil {
			return err
		}
		if err := s.createStagingRepository(ctx, run.stagingPath, run.objectFormat, run.limits); err != nil {
			return err
		}
	}
	if err := s.authorityCurrent(ctx, run); err != nil {
		return err
	}
	run.run.ObjectFormat = run.objectFormat
	run.run.PackBytes = result.PackBytes
	run.run.HTTPBodyBytes = result.HTTPBodyBytes
	return nil
}

// adoptAdvertisement validates the advertisement and keeps its facts.
func (s *Service) adoptAdvertisement(run *runState, advertisement *importgit.Advertisement) error {
	// The run record stores the HEAD target, so an over-long target is refused
	// before it is recorded.
	if target := advertisement.Head.SymrefTarget; len(target) > maxSelectedRefNameBytes {
		return newProblem(CodeUnsupportedRefs, "source HEAD "+refNameTooLong(target), nil)
	}
	run.advertisement = advertisement
	run.objectFormat = advertisement.ObjectFormat
	run.run.RefsSeen = int64(len(advertisement.Refs))
	run.run.HeadAdvertised = advertisement.Head.Advertised
	run.run.HeadSymref = advertisement.Head.SymrefTarget
	switch advertisement.ObjectFormat {
	case importgit.FormatSHA1, importgit.FormatSHA256:
		run.run.ObjectFormat = advertisement.ObjectFormat
	default:
		return newProblem(CodeUnsupportedFormat, "source advertised an unsupported object format", nil)
	}
	if target := advertisement.Head.SymrefTarget; target != "" && !strings.HasPrefix(target, "refs/heads/") {
		return newProblem(CodeUnsupportedRefs, fmt.Sprintf("source HEAD targets unsupported ref %q", target), nil)
	}
	selected, err := selectRefs(advertisement)
	if err != nil {
		return newProblem(CodeUnsupportedRefs, err.Error(), err)
	}
	run.selected = selected
	return nil
}

func (s *Service) requestFor(ctx context.Context, run *runState) (importfetch.Request, error) {
	lock := s.Repositories.Locks.For(run.run.RepositoryID)
	lock.Lock()
	defer lock.Unlock()
	releaseCredentialAuthority := s.Store.LockImportCredentialAuthority(run.run.RepositoryID)
	run.credentialAuthorityLocked = true
	defer func() {
		run.credentialAuthorityLocked = false
		releaseCredentialAuthority()
	}()
	if err := s.authorityCurrent(ctx, run); err != nil {
		return importfetch.Request{}, err
	}
	request := importfetch.Request{
		URL:                 run.source.URL,
		AllowPrivateNetwork: run.source.AllowPrivateNetwork,
		Limits:              run.limits.Fetch,
	}
	credential, exists, err := s.Store.LoadImportCredentials(ctx, run.run.RepositoryID)
	if err != nil {
		return importfetch.Request{}, runStateReadProblem("stored import credential could not be read", err)
	}
	run.requestAuthorityPinned = true
	run.requestCredentialPresent = exists && credential.Bound(run.source)
	run.requestCredentialGeneration = ""
	if !run.requestCredentialPresent {
		return request, nil
	}
	run.requestCredentialGeneration = credential.CredentialGeneration
	if credential.Basic != nil {
		request.Authentication.Basic = &importfetch.BasicAuth{Username: credential.Basic.Username, Password: credential.Basic.Password}
	}
	if credential.BearerToken != "" {
		request.Authentication.BearerToken = credential.BearerToken
	}
	request.RootCAPEM = credential.RootCAPEM
	return request, nil
}

func (s *Service) fetcher() Fetcher {
	if s.Fetch != nil {
		return s.Fetch
	}
	return importfetch.Fetch
}

// Fetcher is the transport seam. The production value is importfetch.Fetch.
type Fetcher func(context.Context, importfetch.Request, importfetch.PackConsumer) (*importfetch.Result, error)

func (s *Service) clock() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) logf(format string, arguments ...any) {
	if s.Logf != nil {
		s.Logf(format, arguments...)
	}
}
