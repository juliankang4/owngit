package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/checkapi"
	"owngit/internal/checkexec"
	"owngit/internal/checkworkflow"
	"owngit/internal/state"
)

// checkExit carries the process exit code for a completed check run, or for
// an import run that kept refs differing from the source. The result is
// already written to stdout when it is returned.
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
	const usage = "Usage: owngit check task new --server URL --repository ID --credential-file PATH [--title TITLE]"
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return cliProblem("invalid_arguments", "check task requires new.")
	}
	if isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, usage)
		return nil
	}
	if arguments[0] != "new" {
		return cliProblem("invalid_arguments", "check task requires new.")
	}
	flags := newCheckFlagSet("check task new")
	remote := addCheckRemoteFlags(flags)
	title := flags.String("title", "", "task title")
	if err := parseCheckFlags(flags, arguments[1:]); err != nil {
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
	flags := newCheckFlagSet("check cycle reserve")
	remote := addCheckRemoteFlags(flags)
	taskID := flags.String("task", "", "stable task identifier")
	if err := parseCheckFlags(flags, arguments); err != nil {
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
	flags := newCheckFlagSet("check cycle list")
	remote := addCheckRemoteFlags(flags)
	taskID := flags.String("task", "", "stable task identifier")
	if err := parseCheckFlags(flags, arguments); err != nil {
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
	flags := newCheckFlagSet("check status")
	remote := addCheckRemoteFlags(flags)
	taskID := flags.String("task", "", "task identifier")
	if err := parseCheckFlags(flags, arguments); err != nil {
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
	flags := newCheckFlagSet("check log")
	remote := addCheckRemoteFlags(flags)
	attemptID := flags.String("attempt", "", "check attempt identifier")
	if err := parseCheckFlags(flags, arguments); err != nil {
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
	flags := newCheckFlagSet("check config show")
	remote := addCheckRemoteFlags(flags)
	if err := parseCheckFlags(flags, arguments[1:]); err != nil {
		return err
	}
	target, err := remote.connection(".")
	if err != nil {
		return err
	}
	return writeResult(latestCheckConfiguration(context.Background(), target))
}

type checkRunOutput struct {
	OK                        bool              `json:"ok"`
	Registered                bool              `json:"registered"`
	Uploaded                  bool              `json:"uploaded"`
	AttemptID                 string            `json:"attempt_id"`
	CycleID                   string            `json:"cycle_id,omitempty"`
	Task                      *checkapi.Task    `json:"task,omitempty"`
	Attempt                   *checkapi.Attempt `json:"attempt,omitempty"`
	CorrectionCyclesRemaining int               `json:"correction_cycles_remaining"`
	Results                   []checkapi.Result `json:"results"`
	UploadError               string            `json:"upload_error,omitempty"`
}

func checkRun(arguments []string) error {
	flags := newCheckFlagSet("check run")
	remote := addCheckRemoteFlags(flags)
	taskID := flags.String("task", "", "stable task identifier")
	cycleID := flags.String("cycle", "", "reserved correction cycle identifier")
	workdir := flags.String("workdir", ".", "working directory that owns the checks")
	timeout := flags.Duration("timeout", checkexec.DefaultTimeout(), "per-check timeout")
	outputLimit := flags.Int64("output-limit", checkexec.DefaultOutputLimit(), "captured output bytes per check")
	noUpload := flags.Bool("no-upload", false, "run locally without registering the attempt")
	var checks stringList
	flags.Var(&checks, "check", "check as `name=command` (repeatable)")
	if err := parseCheckFlags(flags, arguments); err != nil {
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
	attempt, err := prepareCheckAttempt(context.Background(), target, request)
	if err != nil {
		return err
	}
	if err := attempt.register(context.Background()); err != nil {
		return err
	}
	// Only execution listens for an interrupt, so a cancelled run is still
	// recorded by the completion below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	attempt.execute(ctx)
	output := attempt.complete(context.Background())
	if err := writeJSONValue(output); err != nil {
		return err
	}
	return attempt.outcome()
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
	target       *connection
	request      checkRunRequest
	definitions  []checkexec.Definition
	revision     string
	worktree     string
	started      time.Time
	registration checkapi.AttemptRegistration
	registered   bool
	output       checkRunOutput
	results      []checkexec.Result
	cancelled    bool
	status       string
	finished     time.Time
	log          string
	logTruncated bool
}

func prepareCheckAttempt(ctx context.Context, target *connection, request checkRunRequest) (*checkAttempt, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	revision, worktree, err := inspectWorktree(ctx, request.Workdir)
	if err != nil {
		return nil, err
	}
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
		output: checkRunOutput{AttemptID: attemptID, CycleID: request.CycleID},
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
	// revision observed before it ran. The observation belongs to the record,
	// so it runs even after a cancellation.
	run.worktree = confirmWorktree(context.WithoutCancel(ctx), run.request.Workdir, run.revision, run.worktree)
	run.log, run.logTruncated = buildCheckLog(run.results)
	run.output.Results = checkResultsJSON(run.results)
}

// complete records the outcome of a registered attempt, or describes a
// local-only attempt, and returns the result.
func (run *checkAttempt) complete(ctx context.Context) checkRunOutput {
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
	return checkRunOutcomeError(run.output.OK, run.status, run.cancelled)
}

func checkRunOutcomeError(recorded bool, status string, cancelled bool) error {
	switch {
	case !recorded:
		return &checkExit{code: 2, err: errors.New("the check attempt was not recorded")}
	case cancelled && status == checkexec.StatusCancelled:
		return &checkExit{code: 130, err: errors.New("the check run was cancelled")}
	case status != checkexec.StatusPassed:
		return &checkExit{code: 1, err: errors.New("one or more checks did not pass")}
	default:
		return nil
	}
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

// committedCheckDefinitions reads the checks from the workflow file committed
// in revision. The working tree copy is ignored, so an uncommitted edit cannot
// change what runs for the recorded revision.
func committedCheckDefinitions(ctx context.Context, directory, revision string) ([]checkexec.Definition, error) {
	listing, err := runGit(ctx, directory, "ls-tree", "-z", "-l", "--full-tree", revision, "--", checkworkflow.Path)
	if err != nil {
		return nil, cliProblem("revision_unavailable", "The committed check configuration could not be read: "+err.Error())
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
	command := exec.CommandContext(ctx, "git", "cat-file", "blob", fields[2])
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	content, err := command.Output()
	if err != nil {
		return nil, cliProblem("revision_unavailable", "The committed check configuration could not be read: "+err.Error())
	}
	document, err := checkworkflow.Parse(content)
	if err != nil {
		return nil, cliProblem("invalid_check_configuration", checkworkflow.Path+" in revision "+revision+" is invalid: "+err.Error())
	}
	definitions := make([]checkexec.Definition, 0, len(document.Checks))
	for _, check := range document.Checks {
		definitions = append(definitions, checkexec.Definition{Name: check.Name, Command: check.Command})
	}
	return definitions, nil
}

func inspectWorktree(ctx context.Context, directory string) (string, string, error) {
	revision, err := runGit(ctx, directory, "rev-parse", "HEAD")
	if err != nil {
		return "", state.WorktreeUnknown, cliProblem("revision_unavailable", "The working directory is not a Git checkout with a commit: "+err.Error())
	}
	status, err := runGit(ctx, directory, "status", "--porcelain")
	if err != nil {
		return revision, state.WorktreeUnknown, nil
	}
	if strings.TrimSpace(status) != "" {
		return revision, state.WorktreeDirty, nil
	}
	return revision, state.WorktreeClean, nil
}

// confirmWorktree re-observes the worktree after execution. A changed revision
// or a newly dirty tree downgrades the attempt to dirty, and an unreadable tree
// becomes unknown.
func confirmWorktree(ctx context.Context, directory, revision, before string) string {
	afterRevision, afterState, err := inspectWorktree(ctx, directory)
	if err != nil {
		return state.WorktreeUnknown
	}
	if afterRevision != revision || afterState != state.WorktreeClean {
		return state.WorktreeDirty
	}
	return before
}

func runGit(ctx context.Context, directory string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
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

func checkResultsJSON(results []checkexec.Result) []checkapi.Result {
	output := make([]checkapi.Result, 0, len(results))
	for _, result := range results {
		excerpt, cut := checkapi.ClipText(result.Output, state.MaximumCheckExcerptBytes)
		truncated := result.Truncated || cut
		cleanupError, _ := checkapi.ClipText(result.CleanupError, state.MaximumCleanupErrorBytes)
		output = append(output, checkapi.Result{
			Name: result.Name, Command: result.Command, Status: result.Status, ExitCode: result.ExitCode,
			DurationMS: result.Duration.Milliseconds(), OutputExcerpt: excerpt, Truncated: truncated,
			CleanupError: cleanupError,
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

// buildCheckLog joins the per-check output and reports whether the total had
// to be truncated, so the durable record can say so instead of hiding it.
func buildCheckLog(results []checkexec.Result) (string, bool) {
	log := checkapi.LogBuffer{Limit: state.MaximumCheckLogBytes}
	for _, result := range results {
		part := fmt.Sprintf("== %s: %s (exit %s)\n%s", result.Name, result.Status, exitCodeText(result.ExitCode), result.Output)
		if result.Truncated {
			part += "\n[output truncated]\n"
		}
		if !log.Add(part) {
			break
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

func newCheckFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func parseCheckFlags(flags *flag.FlagSet, arguments []string) error {
	if err := parseFlags(flags, arguments); err != nil {
		if errors.Is(err, errUsageShown) {
			return err
		}
		return cliProblem("invalid_arguments", err.Error())
	}
	if flags.NArg() != 0 {
		return cliProblem("invalid_arguments", "Unexpected positional arguments were supplied.")
	}
	return nil
}

// writeResult prints an operation's JSON result, or returns its error.
func writeResult(content []byte, err error) error {
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func writeJSON(content []byte) error {
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
	fmt.Fprintln(writer, "Usage: owngit check <task new|cycle reserve|cycle list|run|status|log|config show> [options]")
	fmt.Fprintln(writer, "The helper registers an attempt before execution, runs checks in the current environment, and reports revision-bound evidence.")
	fmt.Fprintln(writer, "A reserved correction cycle is consumed once. The initial check and manual reruns consume none.")
	fmt.Fprintln(writer, "Inside a clone of an OwnGit repository, --server and --repository default to its origin remote.")
}
