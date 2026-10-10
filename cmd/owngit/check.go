package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/bidi"
	"owngit/internal/checkapi"
	"owngit/internal/checkexec"
	"owngit/internal/checkworkflow"
	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

// checkExit carries the process exit code for a completed check run, for an
// import run that kept refs differing from the source, or for an MCP server
// that could not start. The result or the failure is already written when it
// is returned.
type checkExit struct {
	code int
	err  error
}

func (exit *checkExit) Error() string { return exit.err.Error() }
func (exit *checkExit) Unwrap() error { return exit.err }

func checkCommand(arguments []string) error {
	if len(arguments) == 0 {
		printCheckUsage(os.Stderr)
		return cliProblem("invalid_arguments", "check requires task, cycle, run, status, log, or config.")
	}
	if isHelpArgument(arguments[0]) {
		printCheckUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "task":
		return checkTaskCommand(arguments[1:])
	case "cycle":
		return checkCycleCommand(arguments[1:])
	case "run":
		return checkRun(arguments[1:])
	case "status":
		return checkStatus(arguments[1:])
	case "log":
		return checkLog(arguments[1:])
	case "config":
		return checkConfigCommand(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown check command: "+arguments[0])
	}
}

func checkTaskCommand(arguments []string) error {
	const usage = "Usage: owngit check task <new|list> --server URL --repository ID --credential-file PATH [--title TITLE]"
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return cliProblem("invalid_arguments", "check task requires new or list.")
	}
	if isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, usage)
		return nil
	}
	switch arguments[0] {
	case "new":
		return checkTaskNew(arguments[1:])
	case "list":
		return checkTaskList(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "check task requires new or list.")
	}
}

func checkTaskList(arguments []string) error {
	flags := newCommandFlagSet("check task list")
	remote := addCheckRemoteFlags(flags)
	var query taskListQuery
	flags.IntVar(&query.Limit, "limit", 0, "tasks per page, 1 to 100 (default 50)")
	flags.StringVar(&query.Before, "before", "", "continue below this task position: the \"next\" value of the previous page")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection(".")
	if err != nil {
		return err
	}
	return writeResult(listTasks(context.Background(), target, query))
}

func checkTaskNew(arguments []string) error {
	flags := newCommandFlagSet("check task new")
	remote := addCheckRemoteFlags(flags)
	title := flags.String("title", "", "task title")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection(".")
	if err != nil {
		return err
	}
	return writeResult(createTask(context.Background(), target, *title))
}

// checkCycleCommand reserves one automatic correction round. The round is
// reserved before an agent is asked to correct, counted once whether the
// following check succeeds or fails, and reused by retries inside the round.
func checkCycleCommand(arguments []string) error {
	usage := func(writer io.Writer) {
		fmt.Fprintln(writer, "Usage: owngit check cycle <reserve|list> --task ID --server URL --repository ID --credential-file PATH")
		fmt.Fprintln(writer, "A reserved round is consumed once. The initial check and manual reruns consume none.")
	}
	if len(arguments) == 0 {
		usage(os.Stderr)
		return cliProblem("invalid_arguments", "check cycle requires reserve or list.")
	}
	if isHelpArgument(arguments[0]) {
		usage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "reserve":
		return checkCycleReserve(arguments[1:])
	case "list":
		return checkCycleList(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "check cycle requires reserve or list.")
	}
}

func checkCycleReserve(arguments []string) error {
	flags := newCommandFlagSet("check cycle reserve")
	remote := addCheckRemoteFlags(flags)
	taskID := flags.String("task", "", "stable task identifier")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *taskID == "" {
		return cliProblem("invalid_arguments", "check cycle reserve requires --task.")
	}
	target, err := remote.connection(".")
	if err != nil {
		return err
	}
	return writeResult(reserveCycle(context.Background(), target, *taskID))
}

func checkCycleList(arguments []string) error {
	flags := newCommandFlagSet("check cycle list")
	remote := addCheckRemoteFlags(flags)
	taskID := flags.String("task", "", "stable task identifier")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *taskID == "" {
		return cliProblem("invalid_arguments", "check cycle list requires --task.")
	}
	target, err := remote.connection(".")
	if err != nil {
		return err
	}
	return writeResult(listCycles(context.Background(), target, *taskID))
}

func checkStatus(arguments []string) error {
	flags := newCommandFlagSet("check status")
	remote := addCheckRemoteFlags(flags)
	taskID := flags.String("task", "", "task identifier")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *taskID == "" {
		return cliProblem("invalid_arguments", "check status requires --task.")
	}
	target, err := remote.connection(".")
	if err != nil {
		return err
	}
	return writeResult(showTask(context.Background(), target, *taskID))
}

func checkLog(arguments []string) error {
	flags := newCommandFlagSet("check log")
	remote := addCheckRemoteFlags(flags)
	attemptID := flags.String("attempt", "", "check attempt identifier")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *attemptID == "" {
		return cliProblem("invalid_arguments", "check log requires --attempt.")
	}
	target, err := remote.connection(".")
	if err != nil {
		return err
	}
	return writeResult(readAttemptLog(context.Background(), target, *attemptID))
}

func checkConfigCommand(arguments []string) error {
	const usage = "Usage: owngit check config show --server URL --repository ID --credential-file PATH"
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return cliProblem("invalid_arguments", "check config requires show.")
	}
	if isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, usage)
		return nil
	}
	if arguments[0] != "show" {
		return cliProblem("invalid_arguments", "check config requires show.")
	}
	flags := newCommandFlagSet("check config show")
	remote := addCheckRemoteFlags(flags)
	if err := parseFlagsWithoutOperands(flags, arguments[1:]); err != nil {
		return err
	}
	target, err := remote.connection(".")
	if err != nil {
		return err
	}
	return writeResult(latestCheckConfiguration(context.Background(), target))
}

type checkRunOutput struct {
	OK         bool              `json:"ok"`
	Registered bool              `json:"registered"`
	Uploaded   bool              `json:"uploaded"`
	AttemptID  string            `json:"attempt_id"`
	CycleID    string            `json:"cycle_id,omitempty"`
	Task       *checkapi.Task    `json:"task,omitempty"`
	Attempt    *checkapi.Attempt `json:"attempt,omitempty"`

	// WorktreeNote says why the worktree state is unknown, in the words of the
	// recorded log line. It is empty when the state was read.
	WorktreeNote              string            `json:"worktree_note,omitempty"`
	CorrectionCyclesRemaining int               `json:"correction_cycles_remaining"`
	Results                   []checkapi.Result `json:"results"`
	UploadError               string            `json:"upload_error,omitempty"`
}

func checkRun(arguments []string) error {
	flags := newCommandFlagSet("check run")
	remote := addCheckRemoteFlags(flags)
	taskID := flags.String("task", "", "stable task identifier")
	cycleID := flags.String("cycle", "", "reserved correction cycle identifier")
	workdir := flags.String("workdir", ".", "working directory that owns the checks")
	timeout := flags.Duration("timeout", checkexec.DefaultTimeout(), "per-check timeout")
	outputLimit := flags.Int64("output-limit", checkexec.DefaultOutputLimit(), "captured output bytes per check")
	noUpload := flags.Bool("no-upload", false, "run locally without registering the attempt")
	var checks stringList
	flags.Var(&checks, "check", "check as `name=command` (repeatable)")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *taskID == "" {
		return cliProblem("invalid_arguments", "check run requires --task. Create one with owngit check task new.")
	}
	request := checkRunRequest{
		TaskID: *taskID, CycleID: *cycleID, Workdir: *workdir, Timeout: *timeout, OutputLimit: *outputLimit,
		LocalRepository: remote.repository,
	}
	if err := request.validate(); err != nil {
		return err
	}
	definitions, err := parseCheckDefinitions(checks)
	if err != nil {
		return err
	}
	request.Checks = definitions
	// A local-only run does not need a server; uploading does.
	var target *connection
	if !*noUpload {
		resolved, err := remote.connection(*workdir)
		if err != nil {
			return err
		}
		target = &resolved
	}
	// The interrupt covers preparation too. The observations there start Git
	// as an owned process, in its own group on Unix, so a terminal interrupt
	// reaches this process alone and only this process can stop that Git.
	stops := stopRunOnSignal()
	defer stops.release()
	attempt, err := prepareCheckAttempt(stops.run, target, request)
	if signal := stops.stopped(); signal != nil {
		// The owner stopped the run before anything was registered: no check
		// runs, nothing is recorded, and the exit status names the signal.
		return interruptedExit(signal)
	}
	if err != nil {
		return err
	}
	// Registration and completion outlive the signal that stopped the run, so a
	// stopped attempt is still recorded; a second signal ends them at once.
	if err := attempt.register(stops.finish); err != nil {
		return finishInterrupted(stops, err)
	}
	attempt.execute(stops.run)
	output := attempt.complete(stops.finish)
	if err := writeJSONValue(output); err != nil {
		return err
	}
	// The signal is read here, where the exit status is decided: one that arrived
	// while the result was recorded or printed must name that status too, and the
	// second signal that ended the finishing work outranks the upload it made
	// fail.
	attempt.stopSignal = stops.stopped()
	if stops.finish.Err() != nil {
		return interruptedExit(stops.stopped())
	}
	return attempt.outcome()
}

// finishInterrupted reports the error of a registration or a completion that a
// second stop signal ended, and the error itself when it was an answer from the
// server.
func finishInterrupted(stops *runStops, err error) error {
	if stops.finish.Err() != nil {
		return interruptedExit(stops.stopped())
	}
	return err
}

// checkRunRequest describes one check run.
type checkRunRequest struct {
	TaskID      string
	CycleID     string
	Workdir     string
	Timeout     time.Duration
	OutputLimit int64
	// Checks runs exactly these definitions. When it is empty, only the
	// configuration committed in the tested revision may run.
	Checks []checkexec.Definition
	// LocalRepository names the repository in the attempt of a local-only
	// run, which has no connection.
	LocalRepository string
}

func (request checkRunRequest) validate() error {
	if request.TaskID == "" {
		return cliProblem("invalid_arguments", "A check run requires a task identifier.")
	}
	if request.CycleID != "" && !validHexID(request.CycleID) {
		return cliProblem("invalid_arguments", "--cycle must be a 32 character lowercase hex identifier.")
	}
	// Zero or negative limits would run without a bound locally and are
	// refused by the server anyway, so they are refused before anything runs.
	if request.Timeout <= 0 {
		return cliProblem("invalid_arguments", "--timeout must be a positive duration.")
	}
	if request.OutputLimit <= 0 {
		return cliProblem("invalid_arguments", "--output-limit must be a positive number of bytes.")
	}
	return nil
}

// checkAttempt carries one check run through separable steps: prepareCheckAttempt
// inspects the worktree and selects the checks, register records the attempt
// before anything runs, execute runs the checks, and complete records the
// outcome and returns the result. A run without a connection stays local.
//
// Each step honors its own context. Give complete a context that outlives a
// cancellation of execute, so a cancelled run is still recorded.
type checkAttempt struct {
	target              *connection
	request             checkRunRequest
	definitions         []checkexec.Definition
	revision            string
	worktree            string
	started             time.Time
	registration        checkapi.AttemptRegistration
	registered          bool
	resultFactsAccepted bool
	output              checkRunOutput
	results             []checkexec.Result
	cancelled           bool
	status              string
	finished            time.Time
	log                 string
	logTruncated        bool
	// stopSignal is the signal that stopped the run, as 128 plus its number is
	// the exit status. It is nil when no signal stopped the run, and it is read
	// where the exit status is decided, after the result is recorded.
	stopSignal os.Signal
	// worktreeNote explains a worktree state the helper could not read. It is
	// the note of the observation after the checks, or of the observation
	// before them when that one could not read the state either, and it is what
	// the result and the recorded log say.
	worktreeNote string
}

func prepareCheckAttempt(ctx context.Context, target *connection, request checkRunRequest) (*checkAttempt, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	observation, err := inspectWorktree(ctx, request.Workdir)
	if err != nil {
		return nil, err
	}
	revision, worktree := observation.revision, observation.state
	definitions := request.Checks
	if len(definitions) == 0 {
		// Without explicit checks, only the configuration committed in the
		// revision under test may run. A configuration recorded on the server
		// can come from any branch, so it never selects commands here.
		definitions, err = committedCheckDefinitions(ctx, request.Workdir, revision)
		if err != nil {
			return nil, err
		}
	}
	// The identity is generated before execution, so the server can issue a
	// repository-wide sequence and a retransmitted registration stays idempotent.
	attemptID, err := state.RandomID()
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	return &checkAttempt{
		target: target, request: request, definitions: definitions, revision: revision, worktree: worktree, started: started,
		registration: checkapi.AttemptRegistration{
			AttemptID: attemptID, RevisionOID: revision, WorktreeState: worktree, Checks: checkDefinitionsJSON(definitions),
			CycleID: request.CycleID, StartedAt: started, TimeoutMS: request.Timeout.Milliseconds(), OutputLimitBytes: request.OutputLimit,
		},
		output: checkRunOutput{AttemptID: attemptID, CycleID: request.CycleID}, worktreeNote: observation.note,
	}, nil
}

// register records the attempt before execution. It returns an error only
// when the server answered and refused, so nothing was recorded and no check
// may run. An unconfirmed registration is kept as the upload error and the
// checks still run.
func (run *checkAttempt) register(ctx context.Context) error {
	if run.target == nil {
		return nil
	}
	content, err := postWithRetry(ctx, run.target.client(), run.target.taskPath(run.request.TaskID)+"/attempts", run.registration)
	if definiteRefusal(err) {
		return err
	}
	if err != nil {
		run.output.UploadError = err.Error()
		return nil
	}
	var response checkapi.TaskResponse
	if err := json.Unmarshal(content, &response); err != nil || !response.OK || response.Attempt == nil {
		run.output.UploadError = "the server returned an invalid registration response"
		return nil
	}
	run.registered = true
	run.resultFactsAccepted = response.Attempt.AcceptsResultFact(checkapi.OutputLimitExceededFact)
	run.output.Registered = true
	run.output.Task = response.Task
	if response.Task != nil {
		run.output.CorrectionCyclesRemaining = response.Task.CorrectionCyclesRemaining
	}
	return nil
}

// execute runs the checks until they finish or ctx is cancelled, then
// observes the worktree again.
func (run *checkAttempt) execute(ctx context.Context) {
	redact := ""
	if run.target != nil {
		redact = run.target.credential.secret
	}
	run.results, run.cancelled = checkexec.Run(ctx, run.definitions, checkexec.Options{
		Dir: run.request.Workdir, Timeout: run.request.Timeout, OutputLimit: run.request.OutputLimit, Redact: []string{redact},
	})
	run.finished = time.Now().UTC()
	run.status = aggregateCheckStatus(run.results, run.cancelled)
	// A check that commits or dirties the tree must not be certified as the
	// revision observed before it ran, so the worktree is observed again after
	// the checks. That observation is bounded and it stops with the run, so an
	// owner filter that does not finish cannot keep the result. The state is
	// then unknown, and the note says so in the recorded log.
	worktree, note := confirmWorktree(ctx, run.request.Workdir, run.revision, run.worktree)
	run.worktree = worktree
	if note == "" && worktree == state.WorktreeUnknown {
		// Neither observation read the state, and the earlier one says why.
		note = run.worktreeNote
	}
	run.worktreeNote = note
	run.log, run.logTruncated = buildCheckLog(run.results, note)
	run.output.Results = checkResultsJSON(run.results, run.target == nil || run.resultFactsAccepted)
}

// complete records the outcome of a registered attempt, or describes a
// local-only attempt, and returns the result.
func (run *checkAttempt) complete(ctx context.Context) checkRunOutput {
	// The note reaches a reader without a call to check log, and without the
	// line break the recorded log separates its lines with.
	run.output.WorktreeNote = strings.TrimSpace(run.worktreeNote)
	if run.registered {
		completion := checkapi.AttemptCompletion{
			Results: run.output.Results, Cancelled: run.cancelled, FinishedAt: run.finished, WorktreeState: run.worktree,
			Log: run.log, LogTruncated: run.logTruncated,
		}
		content, err := postWithRetry(ctx, run.target.client(), run.target.taskPath(run.request.TaskID)+"/attempts/"+url.PathEscape(run.output.AttemptID)+"/complete", completion)
		if err != nil {
			run.output.UploadError = err.Error()
			return run.output
		}
		var response checkapi.TaskResponse
		if err := json.Unmarshal(content, &response); err != nil || !response.OK || response.Attempt == nil {
			run.output.UploadError = "the server returned an invalid completion response"
			return run.output
		}
		run.output.OK = true
		run.output.Uploaded = true
		run.output.Task = response.Task
		run.output.Attempt = response.Attempt
		if response.Task != nil {
			run.output.CorrectionCyclesRemaining = response.Task.CorrectionCyclesRemaining
		}
	} else if run.target == nil {
		request := run.request
		run.output.OK = true
		run.output.Attempt = &checkapi.Attempt{
			ID: run.output.AttemptID, TaskID: request.TaskID, RepositoryID: request.LocalRepository, RevisionOID: run.revision, WorktreeState: run.worktree,
			Status: run.status, StartedAt: run.started, FinishedAt: run.finished, DurationMS: run.finished.Sub(run.started).Milliseconds(),
			Summary: state.AttemptSummary(checkResults(run.results), run.worktree, run.status), Results: run.output.Results,
			Protection: state.ProtectionUnknown, ExecutionScope: state.ExecutionScopeInherited, CycleID: request.CycleID,
			TimeoutMS: request.Timeout.Milliseconds(), OutputLimitBytes: request.OutputLimit, LogTruncated: run.logTruncated,
			CleanupFailed: cleanupFailed(run.results),
		}
	}
	return run.output
}

// outcome is the conventional exit status of a completed run.
func (run *checkAttempt) outcome() error {
	return checkRunOutcomeError(run.output.OK, run.status, run.cancelled, run.stopSignal)
}

func checkRunOutcomeError(recorded bool, status string, cancelled bool, stop os.Signal) error {
	switch {
	case !recorded:
		return &checkExit{code: 2, err: errors.New("the check attempt was not recorded")}
	case cancelled && status == checkexec.StatusCancelled:
		return interruptedExit(stop)
	case status != checkexec.StatusPassed:
		return &checkExit{code: 1, err: errors.New("one or more checks did not pass")}
	case stop != nil:
		// The checks passed and a signal stopped the run after them, while the
		// worktree was read again: the result is still recorded, and the exit
		// must not claim a run that the owner stopped.
		return interruptedExit(stop)
	default:
		return nil
	}
}

// stopRunSignals are the signals that stop a run: Ctrl-C, a closed terminal and
// a termination request. A run started under nohup ignores SIGHUP, and its owner
// chose that, so the signal is left alone there. SIGKILL cannot be caught: on
// Unix the owned Git and everything it started outlive this process, because the
// observation bound lives here, and only on Windows does the owned job object
// close with the process and end them.
func stopRunSignals() []os.Signal {
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	return signals
}

// runStops watches the stop signals of one check run. The first signal stops the
// run: the worktree observation and the checks end with it. Registration and
// completion keep going under finish, so the attempt is not left pending, until
// a second signal ends them at once.
//
// The signal is recorded before its context ends, so a run that observed the end
// also observes the signal that caused it.
type runStops struct {
	run       context.Context
	finish    context.Context
	endRun    context.CancelFunc
	endFinish context.CancelFunc
	signals   chan os.Signal
	watcher   chan struct{}
	mutex     sync.Mutex
	last      os.Signal
}

// onStopSignalRecorded, when set, is called with the signal that a run recorded,
// on the watcher goroutine and before the context that the signal ends.
// Production leaves it nil. A test that must know a run recorded a signal before
// it lets the run finish sets it, so the hook must not block.
var onStopSignalRecorded func(os.Signal)

// stopRunOnSignal starts watching the signals that stop a run. release stops the
// watching and leaves no goroutine behind.
func stopRunOnSignal() *runStops {
	stops := &runStops{signals: make(chan os.Signal, 2), watcher: make(chan struct{})}
	stops.run, stops.endRun = context.WithCancel(context.Background())
	stops.finish, stops.endFinish = context.WithCancel(context.Background())
	signal.Notify(stops.signals, stopRunSignals()...)
	go func() {
		defer close(stops.watcher)
		select {
		case arrived := <-stops.signals:
			stops.remember(arrived)
			stops.endRun()
		case <-stops.finish.Done():
			return
		}
		// The second signal ends the registration or the completion that the
		// first one deliberately let finish.
		select {
		case arrived := <-stops.signals:
			stops.remember(arrived)
			stops.endFinish()
		case <-stops.finish.Done():
		}
	}()
	return stops
}

// stopped names the last signal that arrived, or nil when none did.
func (stops *runStops) stopped() os.Signal {
	stops.mutex.Lock()
	defer stops.mutex.Unlock()
	return stops.last
}

func (stops *runStops) remember(arrived os.Signal) {
	stops.mutex.Lock()
	stops.last = arrived
	stops.mutex.Unlock()
	if onStopSignalRecorded != nil {
		onStopSignalRecorded(arrived)
	}
}

func (stops *runStops) release() {
	signal.Stop(stops.signals)
	stops.endRun()
	stops.endFinish()
	<-stops.watcher
}

// interruptedExit is the conventional status of a process stopped by a signal,
// 128 plus the signal number. A cancellation no signal caused keeps the
// interrupt status the command line used before.
func interruptedExit(signal os.Signal) error {
	code := 130
	if value, ok := signal.(syscall.Signal); ok {
		code = 128 + int(value)
	}
	return &checkExit{code: code, err: errors.New("the check run was cancelled")}
}

func parseCheckDefinitions(values []string) ([]checkexec.Definition, error) {
	definitions := make([]checkexec.Definition, 0, len(values))
	for _, value := range values {
		index := strings.Index(value, "=")
		if index <= 0 || index == len(value)-1 {
			return nil, cliProblem("invalid_arguments", "Each --check must use name=command.")
		}
		definitions = append(definitions, checkexec.Definition{Name: value[:index], Command: value[index+1:]})
	}
	return definitions, nil
}

// definiteRefusal reports whether the server answered a request with a client
// error. Such a request was not applied, unlike a lost or failed response.
func definiteRefusal(err error) bool {
	var problem *apiclient.Error
	return errors.As(err, &problem) && problem.Status >= 400 && problem.Status < 500
}

func refusedBeforeOperation(cause error) bool {
	var problem *apiclient.Error
	if !errors.As(cause, &problem) || problem.Status != http.StatusServiceUnavailable || problem.Code != "state_unavailable" {
		return false
	}
	var details pullrequest.OperationErrorDetails
	return json.Unmarshal(problem.Details, &details) == nil && details.OperationStarted != nil && !*details.OperationStarted
}

// committedCheckDefinitions reads the checks from the workflow file committed
// in revision. The working tree copy is ignored, so an uncommitted edit cannot
// change what runs for the recorded revision.
func committedCheckDefinitions(ctx context.Context, directory, revision string) ([]checkexec.Definition, error) {
	readCtx, cancel := context.WithTimeout(ctx, committedCheckReadBound)
	defer cancel()
	unreadable := func(err error) error {
		if ctx.Err() == nil && errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return cliProblem("revision_unavailable", fmt.Sprintf("Git did not read %s in revision %s within %s. In a partial clone the file may be missing and its remote slow. Run git show %s:%s to download it (this waits for the remote), then run the check again.", checkworkflow.Path, revision, committedCheckReadBound, revision, checkworkflow.Path))
		}
		return cliProblem("revision_unavailable", "The committed check configuration could not be read: "+err.Error())
	}
	listing, err := runGit(readCtx, directory, "ls-tree", "-z", "-l", "--full-tree", revision, "--", checkworkflow.Path)
	if err != nil {
		return nil, unreadable(err)
	}
	listing = strings.TrimSuffix(listing, "\x00")
	if listing == "" {
		return nil, cliProblem("checks_not_configured", "Revision "+revision+" has no committed "+checkworkflow.Path+". Commit one, or pass --check name=command.")
	}
	metadata, _, _ := strings.Cut(listing, "\t")
	fields := strings.Fields(metadata)
	if len(fields) != 4 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, cliProblem("invalid_check_configuration", checkworkflow.Path+" in revision "+revision+" is not a regular file.")
	}
	if size, err := strconv.ParseInt(fields[3], 10, 64); err != nil || size > checkworkflow.MaximumBytes {
		return nil, cliProblem("invalid_check_configuration", checkworkflow.Path+" in revision "+revision+" is larger than 64 KiB.")
	}
	content, err := runGit(readCtx, directory, "cat-file", "blob", fields[2])
	if err != nil {
		return nil, unreadable(err)
	}
	document, err := checkworkflow.Parse([]byte(content))
	if err != nil {
		return nil, cliProblem("invalid_check_configuration", checkworkflow.Path+" in revision "+revision+" is invalid: "+err.Error())
	}
	definitions := make([]checkexec.Definition, 0, len(document.Checks))
	for _, check := range document.Checks {
		definitions = append(definitions, checkexec.Definition{Name: check.Name, Command: check.Command})
	}
	return definitions, nil
}

// committedCheckReadBound bounds reading the committed check file as a whole.
// In a partial clone a missing blob is fetched from the promisor remote, which
// a small file needs a few seconds for on a normal connection; when the remote
// is slower than this, the owner downloads the file once with git show. A
// variable only so tests can shorten it.
var committedCheckReadBound = 30 * time.Second

// worktreeObservationBound bounds one Git observation of the working tree,
// before and after a check run. The bound belongs to the observation, not to
// the caller, so a run that nobody stops still ends and records unknown
// instead of no result at all. The value is generous because the owner's
// configured clean filters run here, and a filter that hashes every changed
// file may take long. A warm scan of 100,000 synthetic files took about 150 ms
// on Linux; a very large checkout with a cold cache, above all on Windows
// without a file system monitor, can still reach this bound, and the state is
// then unknown, never a false clean one. It matches the bound the
// configured-check runner gives its own post-run verification. A variable only
// so tests can shorten it.
var worktreeObservationBound = 30 * time.Second

// observationResult is one bounded reading of the working tree. note is the
// recorded log line that explains a state which could not be read.
type observationResult struct {
	revision string
	state    string
	note     string
}

func inspectWorktree(ctx context.Context, directory string) (observationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, worktreeObservationBound)
	defer cancel()
	revision, err := runGit(ctx, directory, "rev-parse", "HEAD")
	if err != nil {
		return observationResult{state: state.WorktreeUnknown, note: observationNote(ctx)},
			cliProblem("revision_unavailable", "The working directory is not a Git checkout with a commit: "+err.Error())
	}
	status, err := runGit(ctx, directory, "status", "--porcelain")
	if err != nil {
		return observationResult{revision: revision, state: state.WorktreeUnknown, note: observationNote(ctx)}, nil
	}
	if strings.TrimSpace(status) != "" {
		return observationResult{revision: revision, state: state.WorktreeDirty}, nil
	}
	return observationResult{revision: revision, state: state.WorktreeClean}, nil
}

// observationNote is the note for a working tree state that could not be read.
// The result carries it as worktree_note and it opens the recorded log, so the
// command line, an MCP caller and the server record all say why.
func observationNote(ctx context.Context) string {
	reason := "Git could not read it"
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		reason = fmt.Sprintf("the Git status scan did not answer within %s", worktreeObservationBound)
	case errors.Is(ctx.Err(), context.Canceled):
		reason = "the run was stopped while the worktree was read"
	}
	return "[OwnGit could not read the worktree: " + reason + ". The worktree state is unknown.]\n"
}

// confirmWorktree re-observes the worktree after execution and keeps the more
// pessimistic of the two observations: dirty, then unknown, then clean. A
// moved revision counts as dirty. An unreadable revision or tree is unknown,
// since it neither shows nor rules out a change, and the returned note names
// the reason for the recorded log.
func confirmWorktree(ctx context.Context, directory, revision, before string) (string, string) {
	after, err := inspectWorktree(ctx, directory)
	switch {
	case err == nil && after.revision != revision, before == state.WorktreeDirty, after.state == state.WorktreeDirty:
		return state.WorktreeDirty, ""
	case before == state.WorktreeUnknown, after.state == state.WorktreeUnknown:
		return state.WorktreeUnknown, after.note
	}
	return state.WorktreeClean, ""
}

// runGit runs Git in the working directory with the user's configuration,
// which decides what counts as a change (for example ignored files and
// filters), so configured clean filters still run. Git is owned like a check
// command: when ctx ends, Git and the filters and other programs it started
// are stopped before runGit returns, so a filter that does not finish cannot
// hold the run. Containment covers Git and every descendant that stays in its
// process group on Unix or its job on Windows, and on Linux also a
// descendant that started a new session, which is found by the mark of the
// run in its environment, and this also holds when Git ends on its own. The
// checks and the owner's Git configuration run with the owner's authority and
// are not a sandbox, so a descendant that leaves both and clears the mark is
// outside what this can stop. Two things that
// do not change the answer are turned off, because a clone's configuration
// could name any program for them:
// core.fsmonitor, which only speeds up the scan, and the optional index write
// of git status, which would run the post-index-change hook.
func runGit(ctx context.Context, directory string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-c", "core.fsmonitor=false"}, arguments...)...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	stdout := &observationOutput{}
	stderr := &observationOutput{limit: observationErrorLimit}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := gitexec.RunOwned(ctx, command, nil, 0); err != nil {
		return "", gitObservationError(arguments, err, stderr.text())
	}
	return strings.TrimSpace(stdout.text()), nil
}

// observationErrorLimit bounds the Git explanation kept for a failed
// observation. Git reports a failure in a few short lines, and the text only
// reaches an error message.
const observationErrorLimit = 64 << 10

// observationOutput collects one stream of a Git observation. The process
// owner may still write after it returns when it could not contain the
// process, so a write and the read are guarded, and limit keeps at most that
// many bytes. A zero limit keeps everything.
type observationOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func (output *observationOutput) Write(p []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	kept := len(p)
	if output.limit > 0 {
		kept = min(kept, max(output.limit-output.buffer.Len(), 0))
	}
	_, _ = output.buffer.Write(p[:kept])
	return len(p), nil
}

func (output *observationOutput) text() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return strings.TrimSpace(output.buffer.String())
}

// gitObservationError names a failed Git observation and its command, with
// Git's own reason when it wrote one.
func gitObservationError(arguments []string, err error, stderr string) error {
	command := "git"
	if len(arguments) > 0 {
		command += " " + arguments[0]
	}
	if stderr == "" {
		return fmt.Errorf("%s: %w", command, err)
	}
	return fmt.Errorf("%s: %w: %s", command, err, stderr)
}

// aggregateCheckStatus uses the server's canonical aggregate so uploaded and
// local-only runs cannot assign different meaning to the same raw results.
func aggregateCheckStatus(results []checkexec.Result, cancelled bool) string {
	return state.AggregateAttemptStatus(checkResults(results), cancelled)
}

// cleanupFailed reports whether any result could not confirm process cleanup.
func cleanupFailed(results []checkexec.Result) bool {
	for _, result := range results {
		if result.CleanupError != "" {
			return true
		}
	}
	return false
}

func checkResultsJSON(results []checkexec.Result, supportsFacts bool) []checkapi.Result {
	output := make([]checkapi.Result, 0, len(results))
	for _, result := range results {
		outputText, gap := result.Output, result.OutputGap
		limit := int64(0)
		if supportsFacts {
			limit = result.ExceededOutputLimit
		} else {
			outputText, gap = result.NotedOutput()
		}
		excerpt, cut := checkapi.ClipLog(outputText, state.MaximumCheckExcerptBytes, gap)
		truncated := result.Truncated || cut
		cleanupError, _ := checkapi.ClipText(result.CleanupError, state.MaximumCleanupErrorBytes)
		output = append(output, checkapi.Result{
			Name: result.Name, Command: result.Command, Status: result.Status, ExitCode: result.ExitCode,
			DurationMS: result.Duration.Milliseconds(), OutputExcerpt: excerpt, Truncated: truncated,
			CleanupError: cleanupError, OutputLimitExceededBytes: limit,
		})
	}
	return output
}

func checkResults(results []checkexec.Result) []state.CheckResult {
	output := make([]state.CheckResult, 0, len(results))
	for _, result := range results {
		output = append(output, state.CheckResult{
			Name: result.Name, Command: result.Command, Status: result.Status, ExitCode: result.ExitCode,
			DurationMS: result.Duration.Milliseconds(), CleanupError: result.CleanupError,
		})
	}
	return output
}

func checkDefinitionsJSON(definitions []checkexec.Definition) []checkapi.CheckDefinition {
	output := make([]checkapi.CheckDefinition, 0, len(definitions))
	for _, definition := range definitions {
		output = append(output, checkapi.CheckDefinition{Name: definition.Name, Command: definition.Command})
	}
	return output
}

// buildCheckLog joins the worktree observation note and the per-check output
// and reports whether the total had to be truncated, so the durable record can
// say so instead of hiding it.
func buildCheckLog(results []checkexec.Result, worktreeNote string) (string, bool) {
	log := checkapi.LogBuffer{Limit: state.MaximumCheckLogBytes}
	log.Add(worktreeNote)
	for _, result := range results {
		log.Add(fmt.Sprintf("== %s: %s (exit %s)\n", result.Name, result.Status, exitCodeText(result.ExitCode)))
		outputText, gap := result.NotedOutput()
		log.AddClipped(outputText, gap)
		if result.Truncated {
			log.Add("\n[output truncated]\n")
		}
	}
	return log.Result()
}

func exitCodeText(code *int) string {
	if code == nil {
		return "n/a"
	}
	return fmt.Sprint(*code)
}

func validHexID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

type checkRemoteFlags struct {
	server             string
	repository         string
	credentialFile     string
	acceptInsecureHTTP bool
}

func addCheckRemoteFlags(flags *flag.FlagSet) *checkRemoteFlags {
	remote := &checkRemoteFlags{}
	flags.StringVar(&remote.server, "server", "", "OwnGit HTTP(S) origin")
	flags.StringVar(&remote.repository, "repository", "", "repository identifier")
	flags.StringVar(&remote.credentialFile, "credential-file", "", "owner-readable file containing the helper credential")
	flags.BoolVar(&remote.acceptInsecureHTTP, "accept-insecure-http", false, "accept unencrypted HTTP for this request")
	return remote
}

// connection resolves the server and repository from the flags or, inside the
// clone that contains dir, from its origin remote, then reads the helper
// credential.
func (remote *checkRemoteFlags) connection(dir string) (connection, error) {
	if remote.credentialFile == "" {
		return connection{}, cliProblem("invalid_arguments", "--credential-file is required.")
	}
	resolved, err := resolveTarget(context.Background(), remote.server, remote.repository, true, remote.acceptInsecureHTTP, dir)
	if err != nil {
		return connection{}, err
	}
	token, err := readServerToken(remote.credentialFile, resolved.server, resolved.inferredServer)
	if err != nil {
		return connection{}, err
	}
	noteInference(resolved)
	return connection{server: resolved.server, repository: resolved.repository, credential: credential{kind: credentialHelperToken, secret: token}}, nil
}

// writeResult prints an operation's JSON result, or returns its error.
func writeResult(content []byte, err error) error {
	if err != nil {
		return err
	}
	return writeJSON(content)
}

// writeJSON prints a JSON result: one from the server as it came, or one the
// command made. Direction controls are escaped, as the server does, so an
// older server's answer cannot reorder the fields around a title either.
func writeJSON(content []byte) error {
	content = bidi.EscapeJSON(content)
	if _, err := os.Stdout.Write(content); err != nil {
		return &apiclient.Error{Code: "output_failed", Message: "The JSON result could not be written.", Cause: err}
	}
	if len(content) == 0 || content[len(content)-1] != '\n' {
		_, _ = fmt.Fprintln(os.Stdout)
	}
	return nil
}

func writeJSONValue(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return &apiclient.Error{Code: "output_failed", Message: "The JSON result could not be encoded.", Cause: err}
	}
	return writeJSON(encoded)
}

func printCheckUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit check <task new|task list|cycle reserve|cycle list|run|status|log|config show> [options]")
	fmt.Fprintln(writer, "The helper registers an attempt before execution, runs checks in the current environment, and reports revision-bound evidence.")
	fmt.Fprintln(writer, "A reserved correction cycle is consumed once. The initial check and manual reruns consume none.")
	fmt.Fprintln(writer, "Inside a clone of an OwnGit repository, --server and --repository default to its origin remote.")
	fmt.Fprintln(writer, "check task list shows the newest tasks first, one page at a time. When older tasks remain, the result has \"next\"; pass it as --before for the following page.")
}
