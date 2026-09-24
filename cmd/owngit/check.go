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
	"strings"
	"syscall"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/checkapi"
	"owngit/internal/checkexec"
	"owngit/internal/state"
)

// checkExit carries the process exit code for a completed check run. The JSON
// result is already written to stdout when it is returned.
type checkExit struct {
	code int
	err  error
}

func (exit *checkExit) Error() string { return exit.err.Error() }
func (exit *checkExit) Unwrap() error { return exit.err }

func checkCommand(arguments []string) error {
	if len(arguments) == 0 {
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
	if len(arguments) == 0 || isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, "Usage: owngit check task new --server URL --repository ID --credential-file PATH [--title TITLE]")
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
	client, err := remote.helperClient()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), "POST", remote.repositoryPath()+"/tasks", checkapi.CreateTaskInput{Title: *title})
	if err != nil {
		return err
	}
	return writeJSON(content)
}

// checkCycleCommand reserves one automatic correction round. The round is
// reserved before an agent is asked to correct, counted once whether the
// following check succeeds or fails, and reused by retries inside the round.
func checkCycleCommand(arguments []string) error {
	if len(arguments) == 0 || isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, "Usage: owngit check cycle reserve --task ID --server URL --repository ID --credential-file PATH")
		fmt.Fprintln(os.Stdout, "A reserved round is consumed once. The initial check and manual reruns consume none.")
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
	client, err := remote.helperClient()
	if err != nil {
		return err
	}
	cycleID, err := state.RandomID()
	if err != nil {
		return err
	}
	content, err := postWithRetry(client, remote.repositoryPath()+"/tasks/"+url.PathEscape(*taskID)+"/cycles", checkapi.CreateCycleInput{CycleID: cycleID})
	if err != nil {
		return err
	}
	return writeJSON(content)
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
	client, err := remote.helperClient()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), "GET", remote.repositoryPath()+"/tasks/"+url.PathEscape(*taskID)+"/cycles", nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
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
	client, err := remote.helperClient()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), "GET", remote.repositoryPath()+"/tasks/"+url.PathEscape(*taskID), nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
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
	client, err := remote.helperClient()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), "GET", remote.repositoryPath()+"/check-attempts/"+url.PathEscape(*attemptID)+"/log", nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func checkConfigCommand(arguments []string) error {
	if len(arguments) == 0 || isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, "Usage: owngit check config show --server URL --repository ID --credential-file PATH")
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
	client, err := remote.helperClient()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), "GET", remote.repositoryPath()+"/check-configurations/latest", nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
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
	if *cycleID != "" && !validHexID(*cycleID) {
		return cliProblem("invalid_arguments", "--cycle must be a 32 character lowercase hex identifier.")
	}
	definitions, err := parseCheckDefinitions(checks)
	if err != nil {
		return err
	}
	// A local-only run with explicit checks does not need a server, but
	// reading the recorded configuration and uploading both do.
	var client *apiclient.Client
	if !*noUpload || len(definitions) == 0 {
		client, err = remote.helperClient()
		if err != nil {
			return err
		}
	}
	if len(definitions) == 0 {
		definitions, err = fetchCheckDefinitions(client, remote.repositoryPath())
		if err != nil {
			return err
		}
	}
	revision, worktree, err := inspectWorktree(*workdir)
	if err != nil {
		return err
	}
	// The identity is generated before execution, so the server can issue a
	// repository-wide sequence and a retransmitted registration stays idempotent.
	attemptID, err := state.RandomID()
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	output := checkRunOutput{AttemptID: attemptID, CycleID: *cycleID}
	registration := checkapi.AttemptRegistration{
		AttemptID: attemptID, RevisionOID: revision, WorktreeState: worktree, Checks: checkDefinitionsJSON(definitions),
		CycleID: *cycleID, StartedAt: started, TimeoutMS: timeout.Milliseconds(), OutputLimitBytes: *outputLimit,
	}
	registered := false
	if !*noUpload {
		content, registerErr := postWithRetry(client, remote.repositoryPath()+"/tasks/"+url.PathEscape(*taskID)+"/attempts", registration)
		if registerErr != nil {
			output.UploadError = registerErr.Error()
		} else {
			var response checkapi.TaskResponse
			if err := json.Unmarshal(content, &response); err != nil || !response.OK || response.Attempt == nil {
				output.UploadError = "the server returned an invalid registration response"
			} else {
				registered = true
				output.Registered = true
				output.Task = response.Task
				if response.Task != nil {
					output.CorrectionCyclesRemaining = response.Task.CorrectionCyclesRemaining
				}
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	results, cancelled := checkexec.Run(ctx, definitions, checkexec.Options{
		Dir: *workdir, Timeout: *timeout, OutputLimit: *outputLimit, Redact: []string{remote.token},
	})
	finished := time.Now().UTC()
	status := aggregateCheckStatus(results, cancelled)
	// A check that commits or dirties the tree must not be certified as the
	// revision observed before it ran.
	worktree = confirmWorktree(*workdir, revision, worktree)
	log, logTruncated := buildCheckLog(results)
	output.Results = checkResultsJSON(results)

	if registered {
		completion := checkapi.AttemptCompletion{
			Results: output.Results, Cancelled: cancelled, FinishedAt: finished, WorktreeState: worktree,
			Log: log, LogTruncated: logTruncated,
		}
		content, completeErr := postWithRetry(client, remote.repositoryPath()+"/tasks/"+url.PathEscape(*taskID)+"/attempts/"+url.PathEscape(attemptID)+"/complete", completion)
		if completeErr != nil {
			output.UploadError = completeErr.Error()
		} else {
			var response checkapi.TaskResponse
			if err := json.Unmarshal(content, &response); err != nil || !response.OK || response.Attempt == nil {
				output.UploadError = "the server returned an invalid completion response"
			} else {
				output.OK = true
				output.Uploaded = true
				output.Task = response.Task
				output.Attempt = response.Attempt
				if response.Task != nil {
					output.CorrectionCyclesRemaining = response.Task.CorrectionCyclesRemaining
				}
			}
		}
	} else if *noUpload {
		output.OK = true
		output.Attempt = &checkapi.Attempt{
			ID: attemptID, TaskID: *taskID, RepositoryID: remote.repository, RevisionOID: revision, WorktreeState: worktree,
			Status: status, StartedAt: started, FinishedAt: finished, DurationMS: finished.Sub(started).Milliseconds(),
			Summary: state.AttemptSummary(checkResults(results), worktree, status), Results: output.Results,
			Protection: state.ProtectionUnknown, ExecutionScope: state.ExecutionScopeInherited, CycleID: *cycleID,
			TimeoutMS: timeout.Milliseconds(), OutputLimitBytes: *outputLimit, LogTruncated: logTruncated,
			CleanupFailed: cleanupFailed(results),
		}
	}
	if err := writeJSONValue(output); err != nil {
		return err
	}
	return checkRunOutcomeError(output.OK, status, cancelled)
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

// postWithRetry retries a lost response with the same body. Both the
// registration and the completion are idempotent by identity and payload, so a
// retry cannot create a second attempt or consume a second correction round.
func postWithRetry(client *apiclient.Client, path string, body any) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		content, err := client.Do(context.Background(), "POST", path, body)
		if err == nil {
			return content, nil
		}
		lastErr = err
		var problem *apiclient.Error
		if !errors.As(err, &problem) || (problem.Code != "connection_failed" && problem.Code != "invalid_response") {
			return nil, err
		}
	}
	return nil, lastErr
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

func fetchCheckDefinitions(client *apiclient.Client, repositoryPath string) ([]checkexec.Definition, error) {
	content, err := client.Do(context.Background(), "GET", repositoryPath+"/check-configurations/latest", nil)
	if err != nil {
		return nil, err
	}
	var response checkapi.ConfigurationResponse
	if err := json.Unmarshal(content, &response); err != nil || !response.OK || response.Configuration == nil {
		return nil, cliProblem("invalid_response", "The server returned an invalid check configuration.")
	}
	definitions := make([]checkexec.Definition, 0, len(response.Configuration.Checks))
	for _, check := range response.Configuration.Checks {
		definitions = append(definitions, checkexec.Definition{Name: check.Name, Command: check.Command})
	}
	return definitions, nil
}

func inspectWorktree(directory string) (string, string, error) {
	revision, err := runGit(directory, "rev-parse", "HEAD")
	if err != nil {
		return "", state.WorktreeUnknown, cliProblem("revision_unavailable", "The working directory is not a Git checkout with a commit: "+err.Error())
	}
	status, err := runGit(directory, "status", "--porcelain")
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
func confirmWorktree(directory, revision, before string) string {
	afterRevision, afterState, err := inspectWorktree(directory)
	if err != nil {
		return state.WorktreeUnknown
	}
	if afterRevision != revision || afterState != state.WorktreeClean {
		return state.WorktreeDirty
	}
	return before
}

func runGit(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
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
	token              string
}

func addCheckRemoteFlags(flags *flag.FlagSet) *checkRemoteFlags {
	remote := &checkRemoteFlags{}
	flags.StringVar(&remote.server, "server", "", "OwnGit HTTP(S) origin")
	flags.StringVar(&remote.repository, "repository", "", "repository identifier")
	flags.StringVar(&remote.credentialFile, "credential-file", "", "owner-readable file containing the helper credential")
	flags.BoolVar(&remote.acceptInsecureHTTP, "accept-insecure-http", false, "accept unencrypted HTTP for this request")
	return remote
}

func (remote *checkRemoteFlags) helperClient() (*apiclient.Client, error) {
	if remote.server == "" || remote.repository == "" || remote.credentialFile == "" {
		return nil, cliProblem("invalid_arguments", "--server, --repository, and --credential-file are required.")
	}
	parsed, err := apiclient.ValidateServer(remote.server, remote.acceptInsecureHTTP)
	if err != nil {
		return nil, err
	}
	token, err := readPrivateToken(remote.credentialFile)
	if err != nil {
		return nil, err
	}
	remote.token = token
	client := apiclient.NewBearer(parsed, token)
	client.MaximumRequest = checkapi.MaximumUploadBytes
	return client, nil
}

func (remote *checkRemoteFlags) repositoryPath() string {
	return "/api/v1/repositories/" + url.PathEscape(remote.repository)
}

func readPrivateToken(path string) (string, error) {
	if err := state.ValidatePrivateFile(path); err != nil {
		return "", &apiclient.Error{Code: "invalid_credential_file", Message: "The helper credential file is unavailable or is not private.", Cause: err}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", &apiclient.Error{Code: "invalid_credential_file", Message: "The helper credential file could not be read.", Cause: err}
	}
	token := strings.TrimSpace(string(content))
	if token == "" {
		return "", &apiclient.Error{Code: "invalid_credential_file", Message: "The helper credential file is empty."}
	}
	return token, nil
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
	fmt.Fprintln(writer, "Usage: owngit check <task new|cycle reserve|run|status|log|config show> [options]")
	fmt.Fprintln(writer, "The helper registers an attempt before execution, runs checks in the current environment, and reports revision-bound evidence.")
	fmt.Fprintln(writer, "A reserved correction cycle is consumed once. The initial check and manual reruns consume none.")
}
