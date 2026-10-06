// Package importsync performs bounded inbound initial import and manual or
// scheduled refresh for OwnGit repositories.
//
// The package owns source configuration, staging, strict local verification,
// divergence-safe publication, durable reconciliation, and opt-in scheduling.
// It never gives Git the source URL, credentials, or network access: the
// confined importfetch transport obtains one snapshot and Git only ever runs
// against task-owned staging or the selected OwnGit repository through the
// isolated gitexec runner.
//
// Import is inbound-only. It never writes to the source and creates no check
// consent or attempt record.
package importsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// Service is the small typed contract a later HTTP, CLI, or app binding uses.
// Explicit mutations and passive reads are separate methods.
//
// Store and Repositories, with the Git runner and locks it carries, are
// required. Every constructor sets them, so no method treats one as absent.
type Service struct {
	Store        *state.Store
	Repositories *repository.Manager
	// Fetch is the transport seam; nil selects importfetch.Fetch.
	Fetch  Fetcher
	Limits Limits
	Clock  func() time.Time
	Logf   func(string, ...any)

	mutexes sync.Map
	active  sync.Map

	// lifecycle is the admission/reconciliation barrier. A run takes the read
	// side only while it registers itself, creates and claims its staging
	// directory, and commits its durable row; Reconcile takes the write side
	// while it snapshots live runs and records what a stopped process left
	// unfinished; Close takes the write side to decide whether the lease may be
	// released; Cancel takes the write side so a cancel cannot miss a run that is
	// being admitted. The critical sections are short and never cover the network:
	// Fetch runs after admission, and the whole pipeline runs outside. Lock order
	// is lifecycle, then runtimeMu, then store; nothing takes runtimeMu and then
	// lifecycle. The same barrier guards operations, the count of explicit
	// runtime operations that hold or are about to hold the lease.
	//
	// Inside the admission section the live registration happens before any
	// staging authorization row exists, so the staging scan that reconciliation
	// runs later and outside the barrier can never observe an authorized row
	// whose run is not yet live.
	lifecycle sync.RWMutex

	// operations counts Prepare and Reconcile while they hold or are about to
	// hold the lease. Guarded by lifecycle.
	operations int

	// afterLiveSnapshot is an optional test seam. Reconcile calls it inside the
	// lifecycle write section between the liveness snapshot and the interruption
	// update, so a test can pin that boundary.
	afterLiveSnapshot func()

	// beforeRunRecord is an optional test seam called with the run context
	// after admission checks the source and immediately before the run row is
	// recorded.
	beforeRunRecord func(ctx context.Context)

	// beforeFinalAuthorityCheck is an optional test seam called under the
	// repository write lock immediately before publication rechecks authority.
	beforeFinalAuthorityCheck func()

	// These optional test seams pin publication race boundaries.
	beforeHEADPreflightRelease func()
	beforeRefTransaction       func()
	whileRefsPrepared          func()
	afterPreparedRefResult     func() error
	beforeFinalHEADLock        func()
	beforeHEADRename           func()
	// beforeObservationRead runs with the run context before publication reads
	// the recorded observations, so a test can stop the run at a state read.
	beforeObservationRead func(context.Context)

	// beforeImportConfiguration runs after the unlocked destination pre-check
	// and before the locked snapshot and configuration. It is outside the
	// repository lock so a test can create the competing repository.
	beforeImportConfiguration func() error
	// afterImportBinding runs after bindNewImport returns and before execute
	// reads the source row. It is outside the repository lock.
	afterImportBinding func() error
	// afterReconcileIntentPage records each pending-intent page. Tests use it to
	// prove the cursor advances.
	afterReconcileIntentPage func(ids []string)
	// beforeStagingVerification runs with the run context after the run enters
	// inspection and before staged refs are verified.
	beforeStagingVerification func(context.Context)
	// beforeInitialDestinationCheck runs after fetch and before the new
	// destination collision check. It is outside the repository lock.
	beforeInitialDestinationCheck func() error
	// mkdirInitialDirectory optionally replaces state.MkdirPrivate in tests.
	mkdirInitialDirectory func(string) error
	// afterInitialDirectoryCreated runs right after the unpublished initial
	// directory was created, before its marker and repository are written.
	afterInitialDirectoryCreated func()
	// afterRunStage runs outside every lock each time a run records a stage,
	// starting with preparing.
	afterRunStage func(status string)
	// beforeRecord runs with the run context before a run record is written
	// with recordContext: a stage after preparing (named by its status), the
	// initial destination ("initial destination"), the publication intent
	// ("publication intent"), and the intent after the ref transaction
	// ("applied publication") and after the HEAD write ("applied HEAD").
	beforeRecord func(ctx context.Context, record string)
	// beforeInitialPublication runs after the unpublished initial directory
	// exists and before objects are indexed. It is outside the repository lock.
	beforeInitialPublication func() error
	// beforeInitialRename runs under the repository write lock immediately
	// before the final collision check and rename.
	beforeInitialRename func() error
	// beforeInitialRepositoryRecord runs under the repository write lock after
	// a successful rename readback and before the repository row write.
	beforeInitialRepositoryRecord func() error

	runtimeMu   sync.Mutex
	runtime     *runtimeRoot
	runtimeLost bool

	// closing is set by Shutdown under the lifecycle write side. Admission
	// refuses new runs once it is set.
	closing bool

	startupMu        sync.Mutex
	startupFailure   error
	schedulerRunning bool
	schedulerFailure error
}

// ErrShuttingDown is the cancellation cause of runs stopped because the
// serving process is exiting.
var ErrShuttingDown = errors.New("OwnGit stopped serving")

// NoteStartupFailure records a startup reconciliation failure so Status and
// Availability can report it. It does not disable ordinary Git service.
func (s *Service) NoteStartupFailure(err error) {
	if s == nil || err == nil {
		return
	}
	s.startupMu.Lock()
	s.startupFailure = err
	s.startupMu.Unlock()
}

// noteScheduler records whether this process runs the scheduler and, when it
// could not start, why.
func (s *Service) noteScheduler(running bool, failure error) {
	s.startupMu.Lock()
	s.schedulerRunning = running
	if running || failure != nil {
		s.schedulerFailure = failure
	}
	s.startupMu.Unlock()
}

// RuntimeStatus reports this process's import runtime to owners. Schedules
// run only while SchedulerRunning is true.
type RuntimeStatus struct {
	SchedulerRunning bool `json:"scheduler_running"`
	// SchedulerFailed reports that this process tried to start the scheduler
	// and could not. Code and Reason then describe that failure; otherwise
	// they describe a failed startup reconciliation, if any.
	SchedulerFailed bool   `json:"scheduler_failed,omitempty"`
	Code            string `json:"code,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

func (s *Service) runtimeStatus() RuntimeStatus {
	if s == nil {
		return RuntimeStatus{}
	}
	s.startupMu.Lock()
	defer s.startupMu.Unlock()
	status := RuntimeStatus{SchedulerRunning: s.schedulerRunning}
	switch {
	case s.schedulerFailure != nil:
		status.SchedulerFailed = true
		status.Code = problemCode(s.schedulerFailure)
		status.Reason = "the import scheduler did not start; scheduled refreshes do not run until OwnGit restarts, and ordinary Git service remains available"
	case s.startupFailure != nil:
		status.Code = problemCode(s.startupFailure)
		status.Reason = "import reconciliation failed; ordinary Git service remains available"
	}
	return status
}

// repositoryReconcileError is one repository's intent reconciliation problem.
type repositoryReconcileError struct {
	repositoryID string
	err          error
}

func (e *repositoryReconcileError) Error() string {
	return fmt.Sprintf("reconcile import intents for %s: %v", e.repositoryID, e.err)
}

func (e *repositoryReconcileError) Unwrap() error { return e.err }

// forgetResolvedStartupProblem drops the startup reconciliation problem that
// reported repositoryID's unresolved publication after the owner resolved it.
// Every other recorded problem stays.
func (s *Service) forgetResolvedStartupProblem(repositoryID string) {
	s.startupMu.Lock()
	defer s.startupMu.Unlock()
	if s.startupFailure == nil {
		return
	}
	parts := []error{s.startupFailure}
	if joined, ok := s.startupFailure.(interface{ Unwrap() []error }); ok {
		parts = joined.Unwrap()
	}
	var kept []error
	for _, part := range parts {
		var repositoryProblem *repositoryReconcileError
		if errors.As(part, &repositoryProblem) && repositoryProblem.repositoryID == repositoryID && problemCode(part) == CodeUnresolved {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) != len(parts) {
		s.startupFailure = errors.Join(kept...)
	}
}

func (s *Service) startupProblem() (string, string) {
	status := s.runtimeStatus()
	return status.Code, status.Reason
}

// Credentials is one explicit credential mutation. At most one of a Basic pair
// or a bearer token is accepted, and RootCAPEM optionally adds trust anchors.
type Credentials struct {
	Username    string
	Password    string
	BearerToken string
	RootCAPEM   []byte
}

// ConfigureInput is one explicit source configuration change.
type ConfigureInput struct {
	RepositoryID        string
	URL                 string
	Mode                Mode
	GitOnlyConsent      bool
	AllowPrivateNetwork bool
	// Options changes the connection options, limits and refresh choices.
	// What it leaves unchanged keeps its saved value, except that a changed
	// URL resets the transport options and the choices to overwrite diverged
	// refs and follow upstream deletions: consent given for one address never
	// carries over to another.
	Options OptionsChange
	// RepeatsSaved marks Options as a form that sends every transport choice
	// again, as drawn for the saved address. With a new URL, only the choices
	// that differ from the saved ones are consent given for it; the repeated
	// ones are carried from the old address and dropped (see WithoutRepeated).
	RepeatsSaved bool
}

// OptionsChange changes some of a source's options. A nil field keeps the
// value it applies to. Limits holds only the limits to change, by their
// state.ImportLimitFields name. OverwriteDiverged, FollowUpstreamDeletions
// and ExtraRefPrefixes are the refresh choices of state.ImportSource.
type OptionsChange struct {
	AllowPlainHTTP          *bool
	Redirects               *string
	ApprovedRedirectOrigin  *string
	AllowReservedAddresses  *bool
	Limits                  map[string]int64
	OverwriteDiverged       *bool
	FollowUpstreamDeletions *bool
	ExtraRefPrefixes        *[]string
}

// WithoutRepeated returns the change without the address-bound choices that
// repeat saved: a plain HTTP, exceptional destination, overwrite or
// upstream deletion choice equal to the saved one, and a redirect policy and
// origin that both equal the saved ones. What remains was chosen in this
// change.
func (c OptionsChange) WithoutRepeated(saved state.ImportSource) OptionsChange {
	repeats := func(choice **bool, saved bool) {
		if *choice != nil && **choice == saved {
			*choice = nil
		}
	}
	repeats(&c.AllowPlainHTTP, saved.Options.AllowPlainHTTP)
	repeats(&c.AllowReservedAddresses, saved.Options.AllowReservedAddresses)
	repeats(&c.OverwriteDiverged, saved.OverwriteDiverged)
	repeats(&c.FollowUpstreamDeletions, saved.FollowUpstreamDeletions)
	redirects, origin := saved.Options.Redirects, saved.Options.ApprovedRedirectOrigin
	if c.Redirects != nil {
		redirects = *c.Redirects
	}
	if c.ApprovedRedirectOrigin != nil {
		origin = *c.ApprovedRedirectOrigin
	}
	if redirects == saved.Options.Redirects && origin == saved.Options.ApprovedRedirectOrigin {
		c.Redirects, c.ApprovedRedirectOrigin = nil, nil
	}
	return c
}

// apply makes this change to the options and refresh choices of input.
func (c OptionsChange) apply(input *state.ImportSourceInput) error {
	if c.OverwriteDiverged != nil {
		input.OverwriteDiverged = *c.OverwriteDiverged
	}
	if c.FollowUpstreamDeletions != nil {
		input.FollowUpstreamDeletions = *c.FollowUpstreamDeletions
	}
	if c.ExtraRefPrefixes != nil {
		if err := state.ValidateExtraRefPrefixes(*c.ExtraRefPrefixes); err != nil {
			return err
		}
		input.ExtraRefPrefixes = slices.Clone(*c.ExtraRefPrefixes)
	}
	options := &input.Options
	if c.AllowPlainHTTP != nil {
		options.AllowPlainHTTP = *c.AllowPlainHTTP
	}
	if c.Redirects != nil {
		options.Redirects = *c.Redirects
	}
	if c.ApprovedRedirectOrigin != nil {
		// An origin given with the change is refused when malformed, even if
		// the redirect policy then drops it. Whether its scheme fits the plain
		// HTTP choice matters only for the approved policy (checkOptions).
		if *c.ApprovedRedirectOrigin != "" {
			if _, err := importfetch.ParseRedirectOrigin(*c.ApprovedRedirectOrigin, true); err != nil {
				return err
			}
		}
		options.ApprovedRedirectOrigin = *c.ApprovedRedirectOrigin
	}
	if c.AllowReservedAddresses != nil {
		options.AllowReservedAddresses = *c.AllowReservedAddresses
	}
	for name, value := range c.Limits {
		// A limit is set to a value in its range; setting its default value
		// returns it to the default.
		if err := state.CheckImportLimit(name, value); err != nil {
			return err
		}
		field, _ := options.Limits.Field(name)
		*field = value
	}
	return nil
}

// replaces reports whether the change sets the named saved option (see
// state.ImportSourceSettingError), so an unreadable one can be repaired.
func (c OptionsChange) replaces(setting string) bool {
	switch setting {
	case "limits":
		return len(c.Limits) > 0
	case "extra_ref_prefixes":
		return c.ExtraRefPrefixes != nil
	}
	return c.Redirects != nil
}

// ConfigureSource persists source identity and consent without importing. The
// repository write lock serializes the mutation with final publication checks;
// network work never holds this lock.
func (s *Service) ConfigureSource(ctx context.Context, input ConfigureInput) (state.ImportSource, error) {
	if input.RepositoryID == "" {
		return state.ImportSource{}, newProblem(CodeInvalidSource, "repository identifier is required", nil)
	}
	mode := input.Mode
	if mode == "" {
		mode = ModeStandalone
	}
	if !mode.valid() {
		return state.ImportSource{}, newProblem(CodeInvalidSource, "mode must be standalone or coexistence", nil)
	}
	// The URL's form is checked here and its scheme against the options in
	// configure, once the options it goes with are known.
	url, err := canonicalSourceURL(input.URL, s.effectiveLimits().Fetch.MaxURLBytes, true)
	if err != nil {
		return state.ImportSource{}, newProblem(CodeInvalidSource, err.Error(), err)
	}
	return s.configure(ctx, input.RepositoryID, input.Options.replaces, func(current state.ImportSource, exists bool) (state.ImportSourceInput, error) {
		source := state.ImportSourceInput{
			RepositoryID: input.RepositoryID, URL: url, Mode: string(mode),
			GitOnlyConsent: input.GitOnlyConsent, AllowPrivateNetwork: input.AllowPrivateNetwork, Options: state.DefaultImportOptions(),
		}
		change := input.Options
		switch {
		case exists && current.URL == url:
			source.Options = current.Options
			source.OverwriteDiverged, source.FollowUpstreamDeletions = current.OverwriteDiverged, current.FollowUpstreamDeletions
			source.ExtraRefPrefixes = current.ExtraRefPrefixes
		case exists:
			source.Options = current.Options.WithoutTransport()
			source.ExtraRefPrefixes = current.ExtraRefPrefixes
			if input.RepeatsSaved {
				change = change.WithoutRepeated(current)
			}
		}
		err := change.apply(&source)
		return source, err
	})
}

// ChangeOptions changes some options of a configured source and keeps the
// rest, including options that cannot be read only when the change replaces
// them.
func (s *Service) ChangeOptions(ctx context.Context, repositoryID string, change OptionsChange) (state.ImportSource, error) {
	return s.configure(ctx, repositoryID, change.replaces, func(current state.ImportSource, exists bool) (state.ImportSourceInput, error) {
		if !exists {
			return state.ImportSourceInput{}, newProblem(CodeNotConfigured, "configure an import source first", ErrNotConfigured)
		}
		source := state.ImportSourceInput{
			RepositoryID: repositoryID, URL: current.URL, Mode: current.Mode,
			GitOnlyConsent: current.GitOnlyConsent, AllowPrivateNetwork: current.AllowPrivateNetwork, Options: current.Options,
			OverwriteDiverged: current.OverwriteDiverged, FollowUpstreamDeletions: current.FollowUpstreamDeletions, ExtraRefPrefixes: current.ExtraRefPrefixes,
		}
		err := change.apply(&source)
		return source, err
	})
}

// preWriteLockProblem reports a repository lock wait that ended before a
// change took the lock, so the change wrote nothing. An expired caller
// deadline means the repository is busy and the caller can retry; a cancelled
// caller stays cancelled, or superseded when a newer authority took over.
func preWriteLockProblem(ctx context.Context, err error) *Problem {
	if errors.Is(err, context.DeadlineExceeded) {
		return newProblem(CodeBusy, "another Git operation holds the repository; nothing was changed", err)
	}
	return cancellationProblem(ctx, "the change stopped before the repository became free", err)
}

// configure is the one locked read, change and write of a source. The
// change sees the saved source. A saved option that cannot be read reaches
// it as its default, and the change is refused unless it replaces that
// option: replaces reports whether it does, by the setting's name.
func (s *Service) configure(ctx context.Context, repositoryID string, replaces func(setting string) bool, change func(current state.ImportSource, exists bool) (state.ImportSourceInput, error)) (state.ImportSource, error) {
	now := s.clock()
	lock := s.Repositories.Locks.For(repositoryID)
	// The change waits for the repository lock at most until the caller's
	// context ends, so a change queued behind another writer is not held past
	// its deadline.
	if err := lock.LockContext(ctx); err != nil {
		return state.ImportSource{}, preWriteLockProblem(ctx, err)
	}
	previous, exists, err := s.readSource(ctx, repositoryID)
	var settingErr *state.ImportSourceSettingError
	for _, problem := range settingErrors(err) {
		if replaces(problem.Setting) {
			continue
		}
		lock.Unlock()
		return state.ImportSource{}, newProblem(CodeStateUnavailable, problem.Advice(), problem)
	}
	if err != nil && !errors.As(err, &settingErr) {
		lock.Unlock()
		return state.ImportSource{}, sourceReadProblem(err)
	}
	input, err := change(previous, exists)
	if err == nil {
		input.Options, err = s.checkOptions(input.URL, input.Options)
	}
	if err != nil {
		lock.Unlock()
		var problem *Problem
		if errors.As(err, &problem) {
			return state.ImportSource{}, err
		}
		return state.ImportSource{}, newProblem(CodeInvalidSource, err.Error(), err)
	}
	input.Now = now
	source, err := s.Store.ConfigureImportSource(ctx, input)
	lock.Unlock()
	if err != nil {
		return state.ImportSource{}, newProblem(CodeStateUnavailable, "import source could not be recorded", err)
	}
	if source.AuthorityRevision == previous.AuthorityRevision {
		return source, nil
	}
	credentialRevision, _ := s.Store.ImportCredentialAuthority(source.RepositoryID)
	if err := s.cancelSuperseded(ctx, source, credentialRevision, now); err != nil {
		return source, err
	}
	return source, nil
}

// SavedLimits returns the limits the owner set for a source, for bounding a
// request that waits for its run.
func (s *Service) SavedLimits(ctx context.Context, repositoryID string) (state.ImportLimits, error) {
	source, _, err := s.readSource(ctx, repositoryID)
	if err != nil {
		return state.ImportLimits{}, sourceReadProblem(err)
	}
	return source.Options.Limits, nil
}

// readSource reads a source for status, a change or a run. Beyond what the
// state layer checks when it reads the row, it holds the saved options to
// the rules a change is held to (usableOptions), so a saved option that
// could not be saved now is reported the same way: as the named setting of
// this source, read as its default.
func (s *Service) readSource(ctx context.Context, repositoryID string) (state.ImportSource, bool, error) {
	source, exists, err := s.Store.ImportSource(ctx, repositoryID)
	if !exists || (err != nil && len(settingErrors(err)) == 0) {
		return source, exists, err
	}
	reported := map[string]bool{}
	for _, setting := range settingErrors(err) {
		reported[setting.Setting] = true
	}
	problems := []error{err}
	unusable := usableOptions(source.Options)
	for _, setting := range []string{"redirects", "limits"} {
		problem := unusable[setting]
		if problem == nil || reported[setting] {
			continue
		}
		problems = append(problems, &state.ImportSourceSettingError{RepositoryID: repositoryID, Setting: setting, Cause: problem})
		defaults := state.DefaultImportOptions()
		if setting == "limits" {
			source.Options.Limits = defaults.Limits
		} else {
			source.Options.Redirects, source.Options.ApprovedRedirectOrigin = defaults.Redirects, defaults.ApprovedRedirectOrigin
		}
	}
	return source, exists, errors.Join(problems...)
}

// usableOptions checks saved options the way checkOptions checks a change:
// an approved origin in canonical form that fits the plain HTTP choice, and
// stage times that fit inside the run. It names each unusable setting.
func usableOptions(options state.ImportOptions) map[string]error {
	problems := map[string]error{}
	if options.Redirects == state.ImportRedirectApproved {
		origin, err := importfetch.ParseRedirectOrigin(options.ApprovedRedirectOrigin, options.AllowPlainHTTP)
		if err == nil && origin != options.ApprovedRedirectOrigin {
			err = errors.New("the approved redirect origin is not in its canonical form")
		}
		if err != nil {
			problems["redirects"] = err
		}
	}
	if err := checkSourceLimits(options.Limits); err != nil {
		problems["limits"] = err
	}
	return problems
}

// settingErrors lists the unusable saved options a source read reported.
func settingErrors(err error) []*state.ImportSourceSettingError {
	if err == nil {
		return nil
	}
	var all []*state.ImportSourceSettingError
	errs := []error{err}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs = joined.Unwrap()
	}
	for _, one := range errs {
		var setting *state.ImportSourceSettingError
		if errors.As(one, &setting) {
			all = append(all, setting)
		}
	}
	return all
}

// checkOptions checks a source URL with its options and returns the options
// in stored form: the URL and an approved origin follow the transport's
// rules, an origin is kept only for the approved-origin policy, a limit equal
// to its default is not stored, and the stages fit in the run.
func (s *Service) checkOptions(rawURL string, options state.ImportOptions) (state.ImportOptions, error) {
	if _, err := canonicalSourceURL(rawURL, s.effectiveLimits().Fetch.MaxURLBytes, options.AllowPlainHTTP); err != nil {
		return options, err
	}
	switch options.Redirects {
	case "", state.ImportRedirectRefuse, state.ImportRedirectSameOrigin:
		if options.Redirects == "" {
			options.Redirects = state.ImportRedirectRefuse
		}
		options.ApprovedRedirectOrigin = ""
	case state.ImportRedirectApproved:
		origin, err := importfetch.ParseRedirectOrigin(options.ApprovedRedirectOrigin, options.AllowPlainHTTP)
		if err != nil {
			return options, err
		}
		options.ApprovedRedirectOrigin = origin
	default:
		return options, errors.New("redirects must be refuse, same_origin, or approved")
	}
	if err := options.Limits.Validate(); err != nil {
		return options, err
	}
	defaults := DefaultSourceLimits()
	for _, field := range state.ImportLimitFields {
		value, _ := options.Limits.Field(field.Name)
		standard, _ := defaults.Field(field.Name)
		if *value == *standard {
			*value = 0
		}
	}
	return options, checkSourceLimits(options.Limits)
}

// SetCredentials stores machine-local credentials bound to the current source
// configuration. nil clears them.
func (s *Service) SetCredentials(ctx context.Context, repositoryID string, credential *Credentials) error {
	now := s.clock()
	lock := s.Repositories.Locks.For(repositoryID)
	// As in configure, the wait ends at the caller's deadline instead of when
	// the other writer finishes.
	if err := lock.LockContext(ctx); err != nil {
		return preWriteLockProblem(ctx, err)
	}
	source, exists, err := s.Store.ImportSource(ctx, repositoryID)
	if err != nil {
		lock.Unlock()
		return sourceReadProblem(err)
	}
	if !exists {
		lock.Unlock()
		return newProblem(CodeNotConfigured, "configure an import source first", ErrNotConfigured)
	}
	var changed state.ImportSource
	if credential == nil {
		changed, err = s.Store.DeleteImportCredentials(ctx, repositoryID, now)
	} else {
		record := state.ImportCredentials{
			RepositoryID: repositoryID, URL: source.URL, SourceGeneration: source.SourceGeneration,
			ExpectedAuthorityRevision: source.AuthorityRevision, RootCAPEM: append([]byte(nil), credential.RootCAPEM...),
		}
		if credential.Username != "" || credential.Password != "" {
			record.Basic = &state.ImportBasicAuth{Username: credential.Username, Password: credential.Password}
		}
		record.BearerToken = credential.BearerToken
		// Saving changes only the parts supplied: a new token or Basic pair
		// keeps the stored CA, and a CA alone keeps the stored secret. Only an
		// explicit clear removes them. Material bound to an earlier source
		// configuration is never carried over.
		current, stored, loadErr := s.Store.LoadImportCredentials(ctx, repositoryID)
		if loadErr != nil {
			lock.Unlock()
			return newProblem(CodeStateUnavailable, "stored import credential could not be read", loadErr)
		}
		if stored && current.Bound(source) {
			if record.Basic == nil && record.BearerToken == "" {
				record.Basic, record.BearerToken = current.Basic, current.BearerToken
			}
			if len(record.RootCAPEM) == 0 {
				record.RootCAPEM = current.RootCAPEM
			}
		}
		if validateErr := record.Validate(); validateErr != nil {
			lock.Unlock()
			return newProblem(CodeInvalidSource, validateErr.Error(), validateErr)
		}
		changed, err = s.Store.SaveImportCredentials(ctx, record, now)
	}
	lock.Unlock()
	credentialRevision, _ := s.Store.ImportCredentialAuthority(repositoryID)
	currentAuthority := source
	if err == nil {
		currentAuthority = changed
	}
	cancellationErr := s.cancelSuperseded(ctx, currentAuthority, credentialRevision, now)
	if err != nil {
		return errors.Join(newProblem(CodeStateUnavailable, "import credentials could not be changed", err), cancellationErr)
	}
	return cancellationErr
}

func (s *Service) cancelSuperseded(ctx context.Context, source state.ImportSource, credentialRevision uint64, now time.Time) error {
	// Match Cancel's admission barrier, but select only stale portable or local
	// authority so a run admitted after this mutation cannot be cancelled.
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	var problems []error
	durableRun, durableExists, err := s.Store.RequestImportCancelBeforeAuthority(ctx, source.RepositoryID, source.AuthorityRevision, now)
	if err != nil {
		problems = append(problems, err)
	} else if durableExists {
		if cancel := s.activeCancel(durableRun.ID); cancel != nil {
			cancel(ErrSuperseded)
		}
	}
	staleID, staleCancel := s.staleActiveExecution(source.RepositoryID, source.AuthorityRevision, credentialRevision)
	if staleID != "" {
		if !durableExists || durableRun.ID != staleID {
			if _, _, err := s.Store.RequestImportRunCancel(ctx, staleID, now); err != nil {
				problems = append(problems, err)
			}
		}
		if staleCancel != nil {
			staleCancel(ErrSuperseded)
		}
	}
	if err := errors.Join(problems...); err != nil {
		return newProblem(CodeStateUnavailable, "stale import cancellation could not be recorded", err)
	}
	return nil
}

// ImportInput describes one initial import. Name is required when the
// repository does not exist yet; URL is always required.
type ImportInput struct {
	Name                string
	Description         string
	URL                 string
	Mode                Mode
	GitOnlyConsent      bool
	AllowPrivateNetwork bool
	Credentials         *Credentials
	// Options changes the new source's connection options, limits and
	// refresh choices from their defaults.
	Options OptionsChange
	Limits  Limits
}

// ImportResult reports the repository and the run record of an import,
// including when the run failed.
type ImportResult struct {
	RepositoryID string
	Run          state.ImportRun
}

// Import refuses an existing repository row or final directory before any
// source change. It configures the source, creates an unpublished destination
// when that destination does not exist, and performs a bounded initial import.
// Reconfiguration of an existing repository is ConfigureSource plus Refresh.
func (s *Service) Import(ctx context.Context, input ImportInput) (ImportResult, error) {
	source := state.ImportSourceInput{Options: state.DefaultImportOptions()}
	if err := input.Options.apply(&source); err != nil {
		return ImportResult{}, newProblem(CodeInvalidSource, err.Error(), err)
	}
	url, err := canonicalSourceURL(input.URL, s.effectiveLimits().Fetch.MaxURLBytes, source.Options.AllowPlainHTTP)
	if err != nil {
		return ImportResult{}, newProblem(CodeInvalidSource, err.Error(), err)
	}
	if source.Options, err = s.checkOptions(url, source.Options); err != nil {
		return ImportResult{}, newProblem(CodeInvalidSource, err.Error(), err)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = deriveRepositoryName(url)
	}
	if err := repository.ValidateName(name, input.Description); err != nil {
		return ImportResult{}, newProblem(CodeInvalidSource, err.Error(), err)
	}
	repositoryID := strings.ToLower(name)
	if taken, err := s.destinationTaken(ctx, repositoryID); err != nil {
		return ImportResult{}, err
	} else if taken {
		return ImportResult{}, newProblem(CodeRepositoryTaken, "repository destination already exists", nil)
	}
	if s.beforeImportConfiguration != nil {
		if err := s.beforeImportConfiguration(); err != nil {
			return ImportResult{}, err
		}
	}
	source.RepositoryID, source.URL, source.Mode = repositoryID, url, string(input.Mode)
	source.GitOnlyConsent, source.AllowPrivateNetwork = input.GitOnlyConsent, input.AllowPrivateNetwork
	snapshot, written, err := s.bindNewImport(ctx, source, input.Credentials)
	if err != nil {
		return ImportResult{}, err
	}
	if s.afterImportBinding != nil {
		if err := s.afterImportBinding(); err != nil {
			return ImportResult{}, err
		}
	}
	kind, err := s.kindFor(ctx, repositoryID)
	if err != nil {
		return ImportResult{}, err
	}
	run, runErr := s.execute(ctx, repositoryID, name, input.Description, kind, input.Limits, true, snapshot, written)
	if runErr != nil {
		s.forgetFailedNewImport(ctx, repositoryID, written)
	}
	return ImportResult{RepositoryID: repositoryID, Run: run}, runErr
}

// failedImportCleanupTimeout bounds the wait for the repository that removing a
// failed first import's binding and credentials needs, so the answer to the
// import never waits for an unrelated writer. It is long enough for the short
// holders such an import meets, and a binding that it leaves behind is removed
// by the next start (see forgetFailedNewImport).
const failedImportCleanupTimeout = 5 * time.Second

// forgetFailedNewImport removes the source binding and credentials that a
// failed first import wrote, so no secret stays behind for a repository that
// does not exist, and neither a retry nor a later repository with the same
// name inherits them. The run history stays. It leaves everything in place
// when another run or change took over the binding. A refusal or failure is
// not reported: the run's own error is what the caller reports.
//
// The cleanup ignores the caller's cancellation, so a client that left still
// does not leave a secret behind, and it waits for the repository with its own
// bound instead of for as long as a writer holds it. A binding that this bound
// leaves behind is removed at the next start: Reconcile calls
// forgetOrphanImports, which lists the bindings of names that have no
// repository (state.OrphanImportBindings) and removes each through
// ForgetOrphanImport and state.ForgetUnpublishedImport.
func (s *Service) forgetFailedNewImport(ctx context.Context, repositoryID string, written state.ImportSource) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failedImportCleanupTimeout)
	defer cancel()
	_, _ = s.forgetOrphanImport(cleanupCtx, repositoryID, func() bool {
		current, exists, err := s.Store.ImportSource(cleanupCtx, repositoryID)
		return err == nil && importBindingStillWritten(exists, current, written)
	})
}

// ForgetOrphanImport removes the import source and credentials stored for a
// name that has no repository, for example after a first import was
// interrupted. It reports false when nothing is stored. It refuses with
// repository_taken when the repository exists and with busy while a run or
// recovery still needs the state.
func (s *Service) ForgetOrphanImport(ctx context.Context, repositoryID string) (bool, error) {
	return s.forgetOrphanImport(ctx, repositoryID, nil)
}

func (s *Service) forgetOrphanImport(ctx context.Context, repositoryID string, stillApplies func() bool) (bool, error) {
	now := s.clock()
	mutex := s.repositoryLock(repositoryID)
	if !mutex.TryLock() {
		return false, newProblem(CodeBusy, "an import is already running for this repository", ErrBusy)
	}
	defer mutex.Unlock()
	lock := s.Repositories.Locks.For(repositoryID)
	// The wait for the repository ends at the caller's deadline or
	// cancellation, before any stored settings are removed.
	if err := lock.LockContext(ctx); err != nil {
		return false, preWriteLockProblem(ctx, err)
	}
	defer lock.Unlock()
	binding, err := s.Store.ReadImportBinding(ctx, repositoryID)
	if err != nil {
		return false, newProblem(CodeStateUnavailable, "import binding could not be read", err)
	}
	if !binding.SourceExists && !binding.CredentialExists {
		return false, nil
	}
	if stillApplies != nil && !stillApplies() {
		return false, nil
	}
	if taken, err := s.destinationTaken(ctx, repositoryID); err != nil {
		return false, err
	} else if taken {
		return false, newProblem(CodeRepositoryTaken, "repository destination already exists", nil)
	}
	if err := s.settleStrandedInitialRuns(ctx, repositoryID, now); err != nil {
		return false, newProblem(CodeStateUnavailable, "stopped first imports could not be settled", err)
	}
	if err := s.Store.ForgetUnpublishedImport(ctx, repositoryID); errors.Is(err, state.ErrImportNotForgettable) {
		return false, newProblem(CodeBusy, "an earlier import for this name is still running or needs recovery; restart OwnGit or try again later", err)
	} else if err != nil {
		return false, newProblem(CodeStateUnavailable, "import settings could not be removed", err)
	}
	return true, nil
}

// forgetOrphanImports runs after startup recovery settled interrupted runs. It
// removes the import settings and credentials of every name that has no
// repository, such as a first import that a crash interrupted. A name that
// still needs recovery is kept and logged.
func (s *Service) forgetOrphanImports(ctx context.Context) error {
	names, err := s.Store.OrphanImportBindings(ctx)
	if err != nil {
		return fmt.Errorf("read orphan import settings: %w", err)
	}
	for _, name := range names {
		forgotten, err := s.ForgetOrphanImport(ctx, name)
		switch {
		case err != nil && problemCode(err) != CodeRepositoryTaken:
			s.logf("import settings for %s, which has no repository, were kept: %v", name, err)
		case forgotten:
			s.logf("removed import settings and credentials for %s, which has no repository", name)
		}
	}
	return nil
}

// bindNewImport snapshots any existing source and credential binding under the
// repository lock, refuses a destination that appeared after the unlocked
// pre-check, and only then writes this import's source and credentials.
func (s *Service) bindNewImport(ctx context.Context, input state.ImportSourceInput, credentials *Credentials) (*state.ImportBindingSnapshot, state.ImportSource, error) {
	now := s.clock()
	lock := s.Repositories.Locks.For(input.RepositoryID)
	// The wait for the repository ends at the caller's deadline or
	// cancellation, before any binding is written.
	if err := lock.LockContext(ctx); err != nil {
		return nil, state.ImportSource{}, preWriteLockProblem(ctx, err)
	}
	snapshot, err := s.Store.ReadImportBinding(ctx, input.RepositoryID)
	if err != nil {
		lock.Unlock()
		return nil, state.ImportSource{}, newProblem(CodeStateUnavailable, "import binding could not be snapshotted", err)
	}
	taken, err := s.destinationTaken(ctx, input.RepositoryID)
	if err != nil {
		lock.Unlock()
		return nil, state.ImportSource{}, err
	}
	if taken {
		lock.Unlock()
		return nil, state.ImportSource{}, newProblem(CodeRepositoryTaken, "repository destination already exists", nil)
	}
	if input.Mode == "" {
		input.Mode = string(ModeStandalone)
	}
	if !Mode(input.Mode).valid() {
		lock.Unlock()
		return nil, state.ImportSource{}, newProblem(CodeInvalidSource, "mode must be standalone or coexistence", nil)
	}
	input.Now = now
	source, err := s.Store.ConfigureImportSource(ctx, input)
	if err != nil {
		lock.Unlock()
		return nil, state.ImportSource{}, newProblem(CodeStateUnavailable, "import source could not be recorded", err)
	}
	if credentials != nil {
		record := state.ImportCredentials{
			RepositoryID: input.RepositoryID, URL: source.URL, SourceGeneration: source.SourceGeneration,
			ExpectedAuthorityRevision: source.AuthorityRevision, RootCAPEM: append([]byte(nil), credentials.RootCAPEM...),
			BearerToken: credentials.BearerToken,
		}
		if credentials.Username != "" || credentials.Password != "" {
			record.Basic = &state.ImportBasicAuth{Username: credentials.Username, Password: credentials.Password}
		}
		if validateErr := record.Validate(); validateErr != nil {
			lock.Unlock()
			return nil, state.ImportSource{}, newProblem(CodeInvalidSource, validateErr.Error(), validateErr)
		}
		source, err = s.Store.SaveImportCredentials(ctx, record, now)
		if err != nil {
			lock.Unlock()
			return nil, state.ImportSource{}, newProblem(CodeStateUnavailable, "import credentials could not be recorded", err)
		}
	} else if snapshot.CredentialExists {
		// A new import uses only the credentials supplied with it, never a
		// secret left by an earlier attempt under the same name.
		source, err = s.Store.DeleteImportCredentials(ctx, input.RepositoryID, now)
		if err != nil {
			lock.Unlock()
			return nil, state.ImportSource{}, newProblem(CodeStateUnavailable, "stale import credentials could not be removed", err)
		}
	}
	lock.Unlock()
	credentialRevision, _ := s.Store.ImportCredentialAuthority(input.RepositoryID)
	if err := s.cancelSuperseded(ctx, source, credentialRevision, now); err != nil {
		return &snapshot, source, err
	}
	return &snapshot, source, nil
}

// Refresh performs one manual refresh of an already configured source.
func (s *Service) Refresh(ctx context.Context, repositoryID string, limits Limits) (state.ImportRun, error) {
	kind, err := s.kindFor(ctx, repositoryID)
	if err != nil {
		return state.ImportRun{}, err
	}
	return s.execute(ctx, repositoryID, "", "", kind, limits, false, nil, state.ImportSource{})
}

// RefreshScheduled is used by the scheduler. It is the same pipeline under the
// scheduled kind, which stamps schedule fairness state.
func (s *Service) RefreshScheduled(ctx context.Context, repositoryID string, limits Limits) (state.ImportRun, error) {
	return s.execute(ctx, repositoryID, "", "", state.ImportKindScheduled, limits, false, nil, state.ImportSource{})
}

func (s *Service) kindFor(ctx context.Context, repositoryID string) (string, error) {
	source, exists, err := s.Store.ImportSource(ctx, repositoryID)
	if err != nil {
		return "", sourceReadProblem(err)
	}
	if !exists {
		return "", newProblem(CodeNotConfigured, "configure an import source first", ErrNotConfigured)
	}
	completed, err := s.Store.HasCompletedImportRun(ctx, repositoryID, source.SourceGeneration)
	if err != nil {
		return "", newProblem(CodeStateUnavailable, "import history could not be read", err)
	}
	if completed {
		return state.ImportKindRefresh, nil
	}
	return state.ImportKindInitial, nil
}

// Cancel marks the active run for cancellation and cancels its context when it
// runs in this process. A run already past publication records complete.
func (s *Service) Cancel(ctx context.Context, repositoryID string) (bool, error) {
	// The external clock callback stays outside the barrier. The barrier makes
	// the row lookup and the cancel one admission-atomic step, so a cancel cannot
	// miss a run that is being admitted right now.
	now := s.clock()
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	run, exists, err := s.Store.RequestImportCancel(ctx, repositoryID, now)
	if err != nil {
		return false, newProblem(CodeStateUnavailable, "import cancellation could not be recorded", err)
	}
	if !exists {
		return false, nil
	}
	if cancel := s.activeCancel(run.ID); cancel != nil {
		cancel(ErrCancelled)
	}
	return true, nil
}

// RunView is a bounded, credential-free view of one history record.
type RunView struct {
	ID                  string     `json:"id"`
	RowID               int64      `json:"row_id,omitempty"`
	AuthorityRevision   int64      `json:"authority_revision"`
	Kind                string     `json:"kind"`
	Status              string     `json:"status"`
	StartedAt           time.Time  `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	CancelRequestedAt   *time.Time `json:"cancel_requested_at,omitempty"`
	ObjectFormat        string     `json:"object_format,omitempty"`
	RefsSeen            int64      `json:"refs_seen"`
	RefsCreated         int64      `json:"refs_created"`
	RefsUpdated         int64      `json:"refs_updated"`
	RefsUnchanged       int64      `json:"refs_unchanged"`
	RefsDivergent       int64      `json:"refs_divergent"`
	RefsDeletedUpstream int64      `json:"refs_deleted_upstream"`
	RefsSkipped         int64      `json:"refs_skipped"`
	PackBytes           int64      `json:"pack_bytes"`
	HTTPBodyBytes       int64      `json:"http_body_bytes"`
	HeadAdvertised      bool       `json:"head_advertised"`
	HeadSymref          string     `json:"head_symref,omitempty"`
	ErrorClass          string     `json:"error_class,omitempty"`
	Message             string     `json:"message,omitempty"`
	LFSDetected         int64      `json:"lfs_detected"`
	LFSInspectionDone   bool       `json:"lfs_inspection_complete"`
	LFSScannedBlobs     int64      `json:"lfs_scanned_blobs"`
	LFSScannedBytes     int64      `json:"lfs_scanned_bytes"`
	CleanupError        string     `json:"cleanup_error,omitempty"`
}

// ContentStatus reports the newest successfully applied import snapshot. It
// does not rescan independent destination edits or retained historical refs.
type ContentStatus struct {
	LFSDetected        int64 `json:"lfs_detected"`
	InspectionComplete bool  `json:"inspection_complete"`
	Incomplete         bool  `json:"incomplete"`
}

// RefStatus is one observed source ref compared with the destination.
type RefStatus struct {
	Name      string `json:"name"`
	SourceOID string `json:"source_oid,omitempty"`
	LocalOID  string `json:"local_oid,omitempty"`
	State     string `json:"state"`
}

// RefreshEffect is a local ref that the next refresh would change if the
// choice it needs is on, as the source stood at its last complete refresh.
type RefreshEffect struct {
	Name string `json:"name"`
	// Effect is "replace" for a diverged ref that the source's value
	// replaces, which needs overwrite_diverged, or "delete" for a ref the
	// source deleted, which needs follow_upstream_deletions, and
	// overwrite_diverged too when LocalChanged. It is "refused" for the
	// protected default branch when it differs here: with
	// overwrite_diverged on, a refresh stops there and changes nothing.
	Effect   string `json:"effect"`
	LocalOID string `json:"local_oid"`
	// LocalChanged reports that the local ref no longer holds the value the
	// source had when it was last observed.
	LocalChanged bool `json:"local_changed"`
	// History is "kept" when the repository's kept history keeps the local
	// tip, and "not_kept" when it does not (kept history is off, or the ref
	// is in an extra namespace). When the kept history setting cannot be
	// read, no effect is listed and Status.RefreshEffectsUnknown is set.
	History string `json:"history"`
}

// ScheduleStatus reports the opt-in schedule and whether it is due.
type ScheduleStatus struct {
	Enabled        bool       `json:"enabled"`
	Interval       string     `json:"interval"`
	LastStartedAt  *time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt *time.Time `json:"last_finished_at,omitempty"`
	Due            bool       `json:"due"`
}

// Status is a bounded passive read. It never triggers a refresh.
type Status struct {
	Configured        bool            `json:"configured"`
	RepositoryID      string          `json:"repository_id"`
	RepositoryExists  bool            `json:"repository_exists"`
	URL               string          `json:"url,omitempty"`
	Mode              string          `json:"mode,omitempty"`
	Generation        int64           `json:"generation,omitempty"`
	AuthorityRevision int64           `json:"authority_revision,omitempty"`
	ObjectFormat      string          `json:"object_format,omitempty"`
	GitOnlyConsent    bool            `json:"git_only_consent"`
	TransportConsent  bool            `json:"transport_consent"`
	CredentialForm    string          `json:"credential_form"`
	CredentialBound   bool            `json:"credential_bound"`
	CAPresent         bool            `json:"ca_present"`
	Content           ContentStatus   `json:"content"`
	Refs              []RefStatus     `json:"refs,omitempty"`
	RefsTruncated     bool            `json:"refs_truncated"`
	LastRun           *RunView        `json:"last_run,omitempty"`
	ActiveRun         *RunView        `json:"active_run,omitempty"`
	UnresolvedIntents int             `json:"unresolved_intents"`
	StagingIssues     int             `json:"staging_issues"`
	UpstreamDeletions int64           `json:"upstream_deletions"`
	Schedule          *ScheduleStatus `json:"schedule,omitempty"`
	Options           *OptionsStatus  `json:"options,omitempty"`
	Runtime           RuntimeStatus   `json:"runtime"`

	// RefreshEffects lists the local refs that the choices to overwrite
	// diverged refs and follow upstream deletions would change now.
	// RefreshEffectsUnknown reports that they could not be worked out, for
	// example while the repository is being written.
	RefreshEffects        []RefreshEffect `json:"refresh_effects,omitempty"`
	RefreshEffectsUnknown bool            `json:"refresh_effects_unknown,omitempty"`
}

// OptionsStatus reports a source's connection options and the limits its next
// run uses.
type OptionsStatus struct {
	AllowPlainHTTP         bool   `json:"allow_plain_http"`
	Redirects              string `json:"redirects"`
	ApprovedRedirectOrigin string `json:"approved_redirect_origin,omitempty"`
	AllowReservedAddresses bool   `json:"allow_reserved_addresses"`
	// Limits are the limits in force, the owner's or the built-in ones.
	// ChangedLimits names the ones the owner set.
	Limits        state.ImportLimits `json:"limits"`
	ChangedLimits []string           `json:"changed_limits,omitempty"`
	// OverwriteDiverged, FollowUpstreamDeletions and ExtraRefPrefixes are
	// the refresh choices, which a backup keeps.
	OverwriteDiverged       bool     `json:"overwrite_diverged"`
	FollowUpstreamDeletions bool     `json:"follow_upstream_deletions"`
	ExtraRefPrefixes        []string `json:"extra_ref_prefixes"`
	// Problem says which saved option cannot be used and where to set it
	// again. The options above then show what could be read.
	Problem string `json:"problem,omitempty"`
}

// DescribeOptions describes a source's options, the limits a run uses and
// its refresh choices. readErr is the error the source read reported, if
// any; unusable saved options in it become the Problem.
func DescribeOptions(source state.ImportSource, readErr error) *OptionsStatus {
	options := source.Options
	status := &OptionsStatus{
		AllowPlainHTTP: options.AllowPlainHTTP, Redirects: options.Redirects,
		ApprovedRedirectOrigin: options.ApprovedRedirectOrigin, AllowReservedAddresses: options.AllowReservedAddresses,
		OverwriteDiverged: source.OverwriteDiverged, FollowUpstreamDeletions: source.FollowUpstreamDeletions,
		ExtraRefPrefixes: append([]string{}, source.ExtraRefPrefixes...),
	}
	var problems []string
	for _, setting := range settingErrors(readErr) {
		problems = append(problems, setting.Advice())
	}
	limits, err := SourceLimits(options.Limits)
	if err != nil {
		problems = append(problems, "This source's import limits cannot be used: "+err.Error())
		limits, _ = SourceLimits(state.ImportLimits{})
	}
	status.Limits = limitValues(limits)
	for _, field := range state.ImportLimitFields {
		if value, _ := options.Limits.Field(field.Name); *value != 0 {
			status.ChangedLimits = append(status.ChangedLimits, field.Name)
		}
	}
	status.Problem = strings.Join(problems, " ")
	return status
}

const (
	statusRefLimit    = 500
	maxHistoryPage    = 100
	maxListPage       = 100
	reconcilePageSize = 32
)

// reconcilePageLimit is the production page size. Tests may lower it and must
// restore the constant.
var reconcilePageLimit = reconcilePageSize

// Status returns the bounded current state. Git reads are attempted only when
// the repository exists; a missing or unavailable repository is reported, never
// guessed.
func (s *Service) Status(ctx context.Context, repositoryID string) (Status, error) {
	status := Status{RepositoryID: repositoryID, Runtime: s.runtimeStatus()}
	source, exists, err := s.readSource(ctx, repositoryID)
	if err != nil && len(settingErrors(err)) == 0 {
		return Status{}, sourceReadProblem(err)
	}
	if !exists {
		return status, nil
	}
	// A saved option that cannot be used is reported beside the rest of the
	// status, so the owner can see and set it again.
	status.Options = DescribeOptions(source, err)
	status.Configured = true
	status.URL = source.URL
	status.Mode = source.Mode
	status.Generation = source.SourceGeneration
	status.AuthorityRevision = source.AuthorityRevision
	status.GitOnlyConsent = source.GitOnlyConsent
	status.TransportConsent = source.AllowPrivateNetwork

	credential, credentialExists, err := s.Store.LoadImportCredentials(ctx, repositoryID)
	if err != nil {
		return Status{}, newProblem(CodeStateUnavailable, "stored import credential could not be read", err)
	}
	if credentialExists {
		status.CredentialBound = credential.Bound(source)
		switch {
		case credential.BearerToken != "":
			status.CredentialForm = "bearer"
		case credential.Basic != nil:
			status.CredentialForm = "basic"
		default:
			status.CredentialForm = "none"
		}
		status.CAPresent = len(credential.RootCAPEM) > 0
	} else {
		status.CredentialForm = "none"
	}

	last, active, accepted, err := s.boundedRunViews(ctx, repositoryID)
	if err != nil {
		return Status{}, err
	}
	status.LastRun, status.ActiveRun = last, active
	if accepted != nil {
		status.Content.LFSDetected = accepted.LFSDetected
		status.Content.InspectionComplete = accepted.LFSInspectionDone
		status.Content.Incomplete = accepted.LFSDetected > 0 || !accepted.LFSInspectionDone
		status.ObjectFormat = accepted.ObjectFormat
		status.UpstreamDeletions = accepted.RefsDeletedUpstream
	}
	if count, err := s.Store.UnresolvedImportIntentCount(ctx, repositoryID); err != nil {
		return Status{}, newProblem(CodeStateUnavailable, "unresolved import intents could not be counted", err)
	} else {
		status.UnresolvedIntents = count
	}
	if count, err := s.Store.ImportStagingIssueCount(ctx); err != nil {
		return Status{}, newProblem(CodeStateUnavailable, "import staging records could not be read", err)
	} else {
		status.StagingIssues = count
	}
	if schedule, scheduleExists, err := s.Store.ImportSchedule(ctx, repositoryID); err != nil {
		return Status{}, newProblem(CodeStateUnavailable, "import schedule could not be read", err)
	} else if scheduleExists {
		due := schedule.Enabled && (schedule.LastStartedAt == nil || schedule.LastStartedAt.Add(time.Duration(schedule.IntervalSeconds)*time.Second).Before(s.clock()))
		status.Schedule = &ScheduleStatus{
			Enabled: schedule.Enabled, Interval: (time.Duration(schedule.IntervalSeconds) * time.Second).String(),
			LastStartedAt: schedule.LastStartedAt, LastFinishedAt: schedule.LastFinishedAt, Due: due,
		}
	}

	if err := s.fillStatusRefs(ctx, &status, source); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (s *Service) boundedRunViews(ctx context.Context, repositoryID string) (*RunView, *RunView, *RunView, error) {
	runs, _, err := s.Store.ImportRuns(ctx, repositoryID, 1)
	if err != nil {
		return nil, nil, nil, newProblem(CodeStateUnavailable, "import history could not be read", err)
	}
	var last, accepted *RunView
	if len(runs) > 0 {
		view := runView(runs[0])
		last = &view
		if runs[0].Status == state.ImportRunComplete {
			accepted = last
		}
	}
	if accepted == nil {
		completed, completedExists, err := s.Store.LatestCompletedImportRun(ctx, repositoryID)
		if err != nil {
			return nil, nil, nil, newProblem(CodeStateUnavailable, "accepted import snapshot could not be read", err)
		}
		if completedExists {
			view := runView(completed)
			accepted = &view
		}
	}
	active, exists, err := s.Store.ActiveImportRun(ctx, repositoryID)
	if err != nil {
		return nil, nil, nil, newProblem(CodeStateUnavailable, "active import run could not be read", err)
	}
	if !exists {
		return last, nil, accepted, nil
	}
	view := runView(active)
	return last, &view, accepted, nil
}

func runView(run state.ImportRun) RunView {
	view := RunView{
		ID: run.ID, RowID: run.RowID, AuthorityRevision: run.AuthorityRevision, Kind: run.Kind, Status: run.Status, StartedAt: run.StartedAt,
		CancelRequestedAt: run.CancelRequestedAt, ObjectFormat: run.ObjectFormat,
		RefsSeen: run.RefsSeen, RefsCreated: run.RefsCreated, RefsUpdated: run.RefsUpdated,
		RefsUnchanged: run.RefsUnchanged, RefsDivergent: run.RefsDivergent,
		RefsDeletedUpstream: run.RefsDeletedUpstream, RefsSkipped: run.RefsSkipped,
		PackBytes: run.PackBytes, HTTPBodyBytes: run.HTTPBodyBytes,
		HeadAdvertised: run.HeadAdvertised, HeadSymref: run.HeadSymref,
		ErrorClass: run.ErrorClass, Message: run.Message,
		LFSDetected: run.LFSDetected, LFSInspectionDone: run.LFSInspectionDone,
		LFSScannedBlobs: run.LFSScannedBlobs, LFSScannedBytes: run.LFSScannedBytes,
		CleanupError: run.CleanupError,
	}
	if !run.FinishedAt.IsZero() {
		finished := run.FinishedAt
		view.FinishedAt = &finished
	}
	return view
}

var errDestinationBusy = errors.New("destination repository is being written")

func (s *Service) fillStatusRefs(ctx context.Context, status *Status, source state.ImportSource) error {
	var destination map[string]string
	path, _, exists, err := s.Repositories.ExistingPath(ctx, source.RepositoryID)
	status.RepositoryExists = err == nil && exists
	// Storage that cannot be read gives no plan, so the effects are unknown;
	// only a destination that does not exist has none.
	status.RefreshEffectsUnknown = err != nil
	// Status never waits for a writer such as an import publication or a
	// push. While one holds the repository lock, local refs are reported as
	// unknown instead of a possibly half-applied view, and the active run
	// shows what is happening.
	if err == nil && exists {
		lock := s.Repositories.Locks.For(source.RepositoryID)
		var records []repository.RefRecord
		readErr, effectsErr := errDestinationBusy, errDestinationBusy
		if lock.TryRLock() {
			records, _, readErr = s.Repositories.ReadRefs(ctx, path, 0, append([]string{"refs/heads", "refs/tags"}, extraRefNamespaces(source.ExtraRefPrefixes)...)...)
			if readErr == nil && status.Options.Problem == "" {
				status.RefreshEffects, effectsErr = s.refreshEffects(ctx, source, path)
			}
			lock.RUnlock()
		}
		status.RefreshEffectsUnknown = effectsErr != nil
		if readErr == nil {
			destination = make(map[string]string, len(records))
			for _, record := range records {
				destination[record.Name] = record.OID
			}
			if status.ObjectFormat == "" {
				if format, formatErr := s.Repositories.ObjectFormat(ctx, path); formatErr == nil {
					status.ObjectFormat = format
				}
			}
		}
	}
	observations, err := s.Store.ImportObservationsBounded(ctx, source.RepositoryID, statusRefLimit+1)
	if err != nil {
		return newProblem(CodeStateUnavailable, "import observations could not be read", err)
	}
	if len(observations) == 0 {
		return nil
	}
	if len(observations) > statusRefLimit {
		observations = observations[:statusRefLimit]
		status.RefsTruncated = true
	}
	// Every completed publication records HEAD and each ref the source then
	// advertised under that run's identity. A ref kept from an older run of
	// the same source was no longer advertised: it was deleted at the source.
	latestRunID := ""
	for _, observation := range observations {
		if observation.RefName == state.ImportHeadRef && observation.SourceGeneration == source.SourceGeneration {
			latestRunID = observation.RunID
		}
	}
	for _, observation := range observations {
		if observation.RefName == state.ImportHeadRef {
			continue
		}
		ref := RefStatus{Name: observation.RefName, SourceOID: observation.OID}
		switch {
		case observation.SourceGeneration != source.SourceGeneration:
			ref.State = "earlier_source"
		case !importedRef(observation.RefName, source.ExtraRefPrefixes):
			// An extra namespace the source no longer imports: its refs are
			// left as they are, not deleted.
			ref.State = "not_imported"
		case latestRunID != "" && observation.RunID != latestRunID:
			ref.State = "deleted_at_source"
			ref.LocalOID = destination[observation.RefName]
		case destination == nil:
			ref.State = "unknown_local"
		default:
			local, present := destination[observation.RefName]
			ref.LocalOID = local
			switch {
			case !present:
				ref.State = "absent_locally"
			case local == observation.OID:
				ref.State = "tracked"
			default:
				ref.State = "diverged"
			}
		}
		status.Refs = append(status.Refs, ref)
	}
	return nil
}

// History returns bounded run records, newest first.
func (s *Service) History(ctx context.Context, repositoryID string, limit int) ([]RunView, bool, error) {
	return s.HistoryBefore(ctx, repositoryID, limit, 0)
}

// HistoryBefore returns the next older page after afterRowID. A zero cursor
// starts at the newest run. History is not pruned.
func (s *Service) HistoryBefore(ctx context.Context, repositoryID string, limit int, afterRowID int64) ([]RunView, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > maxHistoryPage {
		limit = maxHistoryPage
	}
	runs, more, err := s.Store.ImportRunsBefore(ctx, repositoryID, limit, afterRowID)
	if err != nil {
		return nil, false, newProblem(CodeStateUnavailable, "import history could not be read", err)
	}
	views := make([]RunView, 0, len(runs))
	for _, run := range runs {
		views = append(views, runView(run))
	}
	return views, more, nil
}

// Availability is the runtime report. A preparation failure here must not
// disable healthy core Git, so the caller only disables import features.
// Availability is passive: it never creates a directory.
type Availability struct {
	Available bool       `json:"available"`
	Prepared  bool       `json:"prepared"`
	RootID    string     `json:"root_id,omitempty"`
	Code      string     `json:"code,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Limits    LimitsView `json:"limits"`
}

// Availability reports whether import work can start and what bounds apply.
// Prepared reports only this process's lease and only while the lease identity
// still matches the current filesystem; an unprepared or replaced root is not an
// error until an explicit mutation calls Prepare.
func (s *Service) Availability(ctx context.Context) Availability {
	limits := s.effectiveLimits()
	view := limits.view()
	report := func(code, reason string) Availability {
		return Availability{Code: code, Reason: reason, Limits: view}
	}
	if s.Repositories.RepositoryRoot() == "" {
		return report(CodeRuntimeUnavailable, "repository storage root is not configured")
	}
	if err := ctx.Err(); err != nil {
		return report(CodeCancelled, err.Error())
	}
	for _, path := range []string{s.runtimeRootPath(), s.stagingRootPath()} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return report(CodeRuntimeUnavailable, "import runtime root could not be inspected")
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return report(CodeRuntimeUnsafe, "import runtime root is not a real directory")
		}
	}
	availability := Availability{Available: true, Limits: view}
	if root, err := s.currentRuntime(""); err == nil {
		availability.Prepared = true
		availability.RootID = root.rootID
	} else if errors.Is(err, ErrRuntimeLost) {
		return report(CodeRuntimeUnavailable, "import runtime ownership was lost; close the idle service before preparing it again")
	}
	if code, reason := s.startupProblem(); code != "" {
		availability.Code = code
		availability.Reason = reason
	}
	return availability
}

// RunRecordView is the credential-free history view of one stored run.
func RunRecordView(run state.ImportRun) RunView {
	return runView(run)
}

// Reconcile resolves what a stopped process left behind: interrupted runs,
// unconfirmed publication intents, and staging ownership. It is safe to call at
// every server start and never starts an import by itself. Runs this process is
// still executing are neither interrupted nor deleted.
//
// The lease identity is revalidated before the interruption decision, and
// snapshotting live runs and interrupting abandoned ones happen under the
// lifecycle write side, so an admission cannot slip between them. A run that
// admits before the snapshot is excluded by id; a run that admits after it has
// no durable row yet. The rest of reconciliation, which may run Git commands,
// holds no barrier, but the whole operation keeps the lease from being released
// while a scan or intent reconciliation is still running.
func (s *Service) Reconcile(ctx context.Context) error {
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
	// The external clock callback runs outside the barrier, so a caller that
	// blocks in it can still admit a run.
	now := s.clock()
	s.lifecycle.Lock()
	// Revalidation uses the generation captured at operation start. Reconcile
	// never rebinds to a restored or replacement marker midway through work.
	if _, err := s.currentRuntime(generation); err != nil {
		s.lifecycle.Unlock()
		return err
	}
	liveRunIDs := s.liveRunIDs()
	if s.afterLiveSnapshot != nil {
		s.afterLiveSnapshot()
	}
	if _, err := s.currentRuntime(generation); err != nil {
		s.lifecycle.Unlock()
		return err
	}
	_, _, err = s.Store.InterruptImportAuthority(ctx, now, liveRunIDs...)
	s.lifecycle.Unlock()
	if err != nil {
		return newProblem(CodeStateUnavailable, "interrupted import authority could not be recorded", err)
	}
	var problems []error
	if err := s.reconcileRecordedRefLocks(ctx, generation); err != nil {
		if errors.Is(err, ErrRuntimeLost) {
			return err
		}
		problems = append(problems, err)
	}
	if issues, err := s.reconcileStaging(ctx, generation); err != nil {
		if errors.Is(err, ErrRuntimeLost) {
			return err
		}
		problems = append(problems, fmt.Errorf("reconcile import staging: %w", err))
	} else if issues > 0 {
		s.logf("import staging reconciliation reported %d preserved or failed directories", issues)
	}
	if _, err := s.currentRuntime(generation); err != nil {
		return err
	}
	if issues, err := s.reconcileInitialDestinations(ctx, generation); err != nil {
		if errors.Is(err, ErrRuntimeLost) {
			return err
		}
		problems = append(problems, fmt.Errorf("reconcile initial destinations: %w", err))
	} else if issues > 0 {
		s.logf("import initial destination reconciliation reported %d preserved or failed directories", issues)
	}
	if _, err := s.currentRuntime(generation); err != nil {
		return err
	}
	if err := s.reconcilePendingIntentPages(ctx, generation, &problems); err != nil {
		return err
	}
	if _, err := s.currentRuntime(generation); err != nil {
		return err
	}
	if err := s.forgetOrphanImports(ctx); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

func (s *Service) reconcilePendingIntentPages(ctx context.Context, generation string, problems *[]error) error {
	var afterRowID int64
	for {
		if err := ctx.Err(); err != nil {
			*problems = append(*problems, err)
			return nil
		}
		page, err := s.Store.PendingImportIntentsPage(ctx, afterRowID, reconcilePageLimit)
		if err != nil {
			*problems = append(*problems, fmt.Errorf("read pending import intents: %w", err))
			return nil
		}
		if len(page) == 0 {
			return nil
		}
		if s.afterReconcileIntentPage != nil {
			ids := make([]string, len(page))
			for index, intent := range page {
				ids[index] = intent.ID
			}
			s.afterReconcileIntentPage(ids)
		}
		grouped := map[string][]state.ImportIntent{}
		order := make([]string, 0)
		for _, intent := range page {
			if _, seen := grouped[intent.RepositoryID]; !seen {
				order = append(order, intent.RepositoryID)
			}
			grouped[intent.RepositoryID] = append(grouped[intent.RepositoryID], intent)
		}
		now := s.clock()
		for _, repositoryID := range order {
			var logs []deferredImportLog
			path, _, exists, err := s.Repositories.ExistingPath(ctx, repositoryID)
			if err != nil {
				appendDeferredImportLog(&logs, "import intent reconciliation for %s: repository unavailable: %v", repositoryID, err)
				s.flushDeferredImportLogs(logs)
				continue
			}
			if !exists {
				// An initial import that never created its repository is
				// settled once none of its unpublished directories remains.
				lock := s.Repositories.Locks.For(repositoryID)
				if err := lock.LockContext(ctx); err != nil {
					*problems = append(*problems, &repositoryReconcileError{repositoryID: repositoryID, err: err})
					continue
				}
				for _, intent := range grouped[repositoryID] {
					if _, err := s.settleGoneInitialIntent(ctx, intent, now); err != nil {
						*problems = append(*problems, &repositoryReconcileError{repositoryID: repositoryID, err: err})
						break
					}
				}
				lock.Unlock()
				continue
			}
			lock := s.Repositories.Locks.For(repositoryID)
			if err := lock.LockContext(ctx); err != nil {
				*problems = append(*problems, &repositoryReconcileError{repositoryID: repositoryID, err: err})
				continue
			}
			err = s.reconcileIntentListLocked(ctx, path, repositoryID, generation, now, grouped[repositoryID])
			lock.Unlock()
			s.flushDeferredImportLogs(logs)
			if err != nil {
				if errors.Is(err, ErrRuntimeLost) {
					return err
				}
				*problems = append(*problems, &repositoryReconcileError{repositoryID: repositoryID, err: err})
			}
		}
		afterRowID = page[len(page)-1].RowID
		if len(page) < reconcilePageLimit {
			return nil
		}
	}
}

// SetSchedule stores the machine-local opt-in schedule for one repository.
func (s *Service) SetSchedule(ctx context.Context, repositoryID string, enabled bool, interval time.Duration) (state.ImportSchedule, error) {
	_, exists, err := s.Store.ImportSource(ctx, repositoryID)
	if err != nil {
		return state.ImportSchedule{}, sourceReadProblem(err)
	}
	if !exists {
		return state.ImportSchedule{}, newProblem(CodeNotConfigured, "configure an import source first", ErrNotConfigured)
	}
	_, _, destExists, err := s.Repositories.ExistingPath(ctx, repositoryID)
	if err != nil || !destExists {
		return state.ImportSchedule{}, newProblem(CodeRepositoryMissing, "repository destination does not exist", err)
	}
	schedule, err := s.Store.SetImportSchedule(ctx, repositoryID, enabled, interval, s.clock())
	if errors.Is(err, state.ErrInvalidImportSchedule) {
		return state.ImportSchedule{}, newProblem(CodeInvalidSchedule, err.Error(), err)
	}
	if err != nil {
		return state.ImportSchedule{}, newProblem(CodeStateUnavailable, "import schedule could not be saved", err)
	}
	return schedule, nil
}

// Schedule reads the stored schedule.
func (s *Service) Schedule(ctx context.Context, repositoryID string) (state.ImportSchedule, bool, error) {
	schedule, exists, err := s.Store.ImportSchedule(ctx, repositoryID)
	if err != nil {
		return state.ImportSchedule{}, false, newProblem(CodeStateUnavailable, "import schedule could not be read", err)
	}
	return schedule, exists, nil
}

func (s *Service) recordScheduledClaimFailure(ctx context.Context, repositoryID, message string, now time.Time) error {
	source, exists, err := s.Store.ImportSource(ctx, repositoryID)
	if err != nil || !exists {
		return err
	}
	runID, err := newImportID()
	if err != nil {
		return err
	}
	run := state.ImportRun{
		ID: runID, RepositoryID: repositoryID, SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
		Kind: state.ImportKindScheduled, Status: state.ImportRunPreparing, StartedAt: now, CreatedAt: now,
	}
	if err := s.Store.BeginImportRun(ctx, run); err != nil {
		return err
	}
	run.Status = state.ImportRunFailed
	run.FinishedAt = now
	run.ErrorClass = CodeRuntimeUnavailable
	run.Message = boundedImportMessage(message)
	return s.Store.FinishImportRun(ctx, run)
}

// Schedules returns one bounded page of machine-local schedules.
func (s *Service) Schedules(ctx context.Context, limit int, afterID string) ([]state.ImportSchedule, bool, error) {
	limit = clampListPage(limit)
	records, err := s.Store.ImportSchedulesPage(ctx, afterID, limit+1)
	if err != nil {
		return nil, false, newProblem(CodeStateUnavailable, "import schedules could not be read", err)
	}
	if len(records) > limit {
		return records[:limit], true, nil
	}
	return records, false, nil
}

// StagingIssues returns one bounded page of preserved staging records.
func (s *Service) StagingIssues(ctx context.Context, limit int, afterName string) ([]state.ImportStaging, bool, error) {
	limit = clampListPage(limit)
	records, err := s.Store.ImportStagingsPage(ctx, afterName, limit+1)
	if err != nil {
		return nil, false, newProblem(CodeStateUnavailable, "import staging records could not be read", err)
	}
	if len(records) > limit {
		return records[:limit], true, nil
	}
	return records, false, nil
}

func clampListPage(limit int) int {
	if limit <= 0 || limit > maxListPage {
		return maxListPage
	}
	return limit
}

func (s *Service) effectiveLimits() Limits {
	limits, err := s.Limits.effective()
	if err != nil {
		return DefaultLimits()
	}
	return limits
}

func (s *Service) repositoryLock(repositoryID string) *sync.Mutex {
	value, _ := s.mutexes.LoadOrStore(repositoryID, &sync.Mutex{})
	mutex, _ := value.(*sync.Mutex)
	return mutex
}

// liveRunIDs lists the runs this process is currently executing. A run is
// registered before its durable row is committed, so a run that is live is
// never observed as abandoned. Call it while holding the write side of the
// lifecycle barrier: no admission is in flight then, so the list is exact.
func (s *Service) liveRunIDs() []string {
	var ids []string
	s.active.Range(func(key, _ any) bool {
		if id, ok := key.(string); ok {
			ids = append(ids, id)
		}
		return true
	})
	return ids
}

// runIsLive re-reads liveness at decision time. A reconciliation snapshot can
// be older than a run that has just registered itself.
func (s *Service) runIsLive(runID string) bool {
	_, live := s.active.Load(runID)
	return live
}
