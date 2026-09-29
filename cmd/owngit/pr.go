package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"owngit/internal/apiclient"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

// generalRemoteFlags are the connection flags of commands that use general
// access: the shared password, or no credential when access is open.
type generalRemoteFlags struct {
	server             string
	repository         string
	passwordFile       string
	acceptInsecureHTTP bool
	withRepository     bool
}

func prCommand(arguments []string) error {
	if len(arguments) == 0 {
		printPRUsage(os.Stderr)
		return cliProblem("invalid_arguments", "pr requires create, list, show, edit, diff, review, merge, close, or reopen.")
	}
	if isHelpArgument(arguments[0]) {
		printPRUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "create":
		return prCreate(arguments[1:])
	case "list":
		return prList(arguments[1:])
	case "show":
		return prShow(arguments[1:])
	case "edit":
		return prEdit(arguments[1:])
	case "diff":
		return prDiff(arguments[1:])
	case "review":
		return prReview(arguments[1:])
	case "merge":
		return prMerge(arguments[1:])
	case "close", "reopen":
		return prSetClosed(arguments[0], arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown pr command: "+arguments[0])
	}
}

func prCreate(arguments []string) error {
	flags := newCommandFlagSet("pr create")
	remote := addGeneralRemoteFlags(flags, true)
	title := flags.String("title", "", "pull request title")
	source := flags.String("source", "", "source branch")
	targetBranch := flags.String("target", "", "target branch")
	review := flags.String("review", "", "optional review choice: request or skip")
	bodyFile := flags.String("body-file", "", "file with the Markdown description, or - for standard input")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *title == "" || *source == "" || *targetBranch == "" {
		return cliProblem("invalid_arguments", "pr create requires --title, --source, and --target. --review and --body-file are optional.")
	}
	body, err := readPullRequestText(*bodyFile, "--body-file")
	if err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(createPullRequest(context.Background(), target, pullrequest.CreateInput{
		Title: *title, Body: body, SourceBranch: *source, TargetBranch: *targetBranch, ReviewChoice: *review,
	}))
}

// prEdit replaces the title, the description, or both. --edit-revision is
// the edit_revision that pr show printed; the edit is refused with stale_edit
// when the pull request was edited since.
func prEdit(arguments []string) error {
	flags := newCommandFlagSet("pr edit")
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	revision := flags.Int64("edit-revision", -1, "edit_revision from pr show")
	title := flags.String("title", "", "new title")
	bodyFile := flags.String("body-file", "", "file with the new Markdown description, or - for standard input; an empty file clears it")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	given := map[string]bool{}
	flags.Visit(func(set *flag.Flag) { given[set.Name] = true })
	if *number <= 0 || *revision < 0 {
		return cliProblem("invalid_arguments", "pr edit requires --number and --edit-revision (edit_revision from pr show).")
	}
	if !given["title"] && !given["body-file"] {
		return cliProblem("invalid_arguments", "pr edit requires --title, --body-file, or both.")
	}
	input := pullrequest.EditInput{EditRevision: revision}
	if given["title"] {
		input.Title = title
	}
	if given["body-file"] {
		body, err := readPullRequestText(*bodyFile, "--body-file")
		if err != nil {
			return err
		}
		input.Body = &body
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(editPullRequest(context.Background(), target, *number, input))
}

// readPullRequestText reads a description or review note from path, or from
// standard input when path is "-". No path is no text. The server applies the
// 64 KiB limit after turning CRLF line ends into LF; a file that could not
// fit even then is refused here without being read to the end.
func readPullRequestText(path, flagName string) (string, error) {
	if path == "" {
		return "", nil
	}
	reader := io.Reader(os.Stdin)
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", &apiclient.Error{Code: "invalid_arguments", Message: flagName + " could not be opened.", Cause: err}
		}
		defer file.Close()
		reader = file
	}
	limit := int64(2 * state.MaximumPullRequestTextBytes)
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", &apiclient.Error{Code: "invalid_arguments", Message: flagName + " could not be read.", Cause: err}
	}
	if int64(len(content)) > limit {
		return "", cliProblem("invalid_arguments", flagName+" holds more than 64 KiB of text.")
	}
	return string(content), nil
}

func prList(arguments []string) error {
	flags := newCommandFlagSet("pr list")
	remote := addGeneralRemoteFlags(flags, true)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(listPullRequests(context.Background(), target))
}

func prShow(arguments []string) error {
	flags := newCommandFlagSet("pr show")
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *number <= 0 {
		return cliProblem("invalid_arguments", "pr show requires a positive --number.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(showPullRequest(context.Background(), target, *number))
}

func prDiff(arguments []string) error {
	flags := newCommandFlagSet("pr diff")
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	sourceOID := flags.String("source-oid", "", "pin this exact source commit object ID (with --target-oid)")
	targetOID := flags.String("target-oid", "", "pin this exact target commit object ID (with --source-oid)")
	stat := flags.Bool("stat", false, "print the JSON result without the patch")
	patch := flags.Bool("patch", false, "print only the patch text; the revisions and any cut are noted on standard error")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *number <= 0 {
		return cliProblem("invalid_arguments", "pr diff requires a positive --number.")
	}
	if (*sourceOID == "") != (*targetOID == "") {
		return cliProblem("invalid_arguments", "pr diff requires both --source-oid and --target-oid, or neither.")
	}
	if *stat && *patch {
		return cliProblem("invalid_arguments", "Use --stat or --patch, not both.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	content, err := pullRequestDiff(context.Background(), target, *number, pullrequest.RevisionInput{SourceOID: *sourceOID, TargetOID: *targetOID})
	switch {
	case err != nil:
		return err
	case *stat:
		return writeResult(diffStat(content))
	case *patch:
		return writeDiffPatch(content, os.Stdout, os.Stderr)
	}
	return writeJSON(content)
}

// diffStat returns a diff result without its patch.
func diffStat(content []byte) ([]byte, error) {
	diff, err := decodeDiff(content)
	if err != nil {
		return nil, err
	}
	return encodeDiffStat(diff)
}

func decodeDiff(content []byte) (pullrequest.Diff, error) {
	var diff pullrequest.Diff
	if err := json.Unmarshal(content, &diff); err != nil {
		return pullrequest.Diff{}, &apiclient.Error{Code: "invalid_response", Message: "The OwnGit API returned an invalid diff.", Cause: err}
	}
	return diff, nil
}

// encodeDiffStat encodes diff without its patch.
func encodeDiffStat(diff pullrequest.Diff) ([]byte, error) {
	encoded, err := json.Marshal(struct {
		pullrequest.Diff
		// A field at a shallower depth hides the embedded one of the same
		// JSON name, and a nil pointer with omitempty is left out.
		Patch *struct{} `json:"patch,omitempty"`
	}{Diff: diff})
	if err != nil {
		return nil, &apiclient.Error{Code: "output_failed", Message: "The JSON result could not be encoded.", Cause: err}
	}
	return encoded, nil
}

// writeDiffPatch prints only the patch text on output, and on notes one line
// with the diffed revisions plus a line for each condition a reader of the
// bare patch would otherwise miss.
func writeDiffPatch(content []byte, output, notes io.Writer) error {
	var diff pullrequest.Diff
	if err := json.Unmarshal(content, &diff); err != nil {
		return &apiclient.Error{Code: "invalid_response", Message: "The OwnGit API returned an invalid diff.", Cause: err}
	}
	fmt.Fprintf(notes, "owngit: source %s, target %s, merge base %s\n", diff.Source.OID, diff.Target.OID, valueOr(diff.MergeBase, "none"))
	if diff.Moved && diff.Current != nil {
		fmt.Fprintf(notes, "owngit: the branches moved; the current source is %s and the current target is %s\n",
			valueOr(diff.Current.Source.OID, diff.Current.Source.Status), valueOr(diff.Current.Target.OID, diff.Current.Target.Status))
	}
	if diff.Unavailable != "" {
		fmt.Fprintf(notes, "owngit: no patch: %s\n", diff.Unavailable)
	}
	if diff.Truncated {
		scope := "the patch leaves out some files"
		if diff.Incomplete {
			scope = "the patch and the file list leave out some files"
		}
		fmt.Fprintf(notes, "owngit: %s (%s)\n", scope, diff.Reason)
	}
	if _, err := io.WriteString(output, diff.Patch); err != nil {
		return &apiclient.Error{Code: "output_failed", Message: "The patch could not be written.", Cause: err}
	}
	return nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func prReview(arguments []string) error {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: owngit pr review <request|submit|skip> [options]")
		return cliProblem("invalid_arguments", "pr review requires request, submit, or skip.")
	}
	if isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, "Usage: owngit pr review <request|submit|skip> [options]")
		return nil
	}
	action := arguments[0]
	if action != "request" && action != "submit" && action != "skip" {
		return cliProblem("invalid_arguments", "pr review requires request, submit, or skip.")
	}
	flags := newCommandFlagSet("pr review " + action)
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	sourceOID := flags.String("source-oid", "", "exact source commit object ID")
	targetOID := flags.String("target-oid", "", "exact target commit object ID")
	decision := flags.String("decision", "", "review result: approved or changes_requested")
	reviewer := flags.String("reviewer", "", "supplied reviewer label")
	noteFile := flags.String("note-file", "", "file with a Markdown review note, or - for standard input")
	if err := parseFlagsWithoutOperands(flags, arguments[1:]); err != nil {
		return err
	}
	if *number <= 0 || *sourceOID == "" || *targetOID == "" {
		return cliProblem("invalid_arguments", "pr review requires --number, --source-oid, and --target-oid.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	if action == "submit" {
		if *decision == "" || *reviewer == "" {
			return cliProblem("invalid_arguments", "pr review submit requires --decision and --reviewer.")
		}
		note, err := readPullRequestText(*noteFile, "--note-file")
		if err != nil {
			return err
		}
		return writeResult(submitPullRequestReview(context.Background(), target, *number, pullrequest.ReviewSubmitInput{
			SourceOID: *sourceOID, TargetOID: *targetOID, Decision: *decision, ReviewerLabel: *reviewer, Note: note,
		}))
	}
	if *decision != "" || *reviewer != "" || *noteFile != "" {
		return cliProblem("invalid_arguments", "--decision, --reviewer and --note-file are valid only for pr review submit.")
	}
	return writeResult(markPullRequestReview(context.Background(), target, *number, action, pullrequest.RevisionInput{SourceOID: *sourceOID, TargetOID: *targetOID}))
}

func prMerge(arguments []string) error {
	flags := newCommandFlagSet("pr merge")
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	sourceOID := flags.String("source-oid", "", "exact source commit object ID")
	targetOID := flags.String("target-oid", "", "exact target commit object ID")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *number <= 0 || *sourceOID == "" || *targetOID == "" {
		return cliProblem("invalid_arguments", "pr merge requires --number, --source-oid, and --target-oid.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(mergePullRequest(context.Background(), target, *number, pullrequest.RevisionInput{SourceOID: *sourceOID, TargetOID: *targetOID}))
}

// prSetClosed closes or reopens a pull request. Neither changes a branch, so
// no object IDs are needed.
func prSetClosed(action string, arguments []string) error {
	flags := newCommandFlagSet("pr " + action)
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *number <= 0 {
		return cliProblem("invalid_arguments", "pr "+action+" requires a positive --number.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(setPullRequestClosed(context.Background(), target, *number, action == "close"))
}

// newCommandFlagSet returns an empty flag set for a client command. The flag
// package's own error output is discarded because the command reports a
// structured error instead.
func newCommandFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

// addGeneralRemoteFlags adds the connection flags. withRepository adds
// --repository for commands that address one repository.
func addGeneralRemoteFlags(flags *flag.FlagSet, withRepository bool) *generalRemoteFlags {
	remote := &generalRemoteFlags{withRepository: withRepository}
	flags.StringVar(&remote.server, "server", "", "OwnGit HTTP(S) origin")
	if withRepository {
		flags.StringVar(&remote.repository, "repository", "", "repository identifier")
	}
	flags.StringVar(&remote.passwordFile, "password-file", "", "owner-readable file containing the shared general-access password")
	flags.BoolVar(&remote.acceptInsecureHTTP, "accept-insecure-http", false, "accept unencrypted HTTP for this request")
	return remote
}

// parseFlagsWithoutOperands parses a client command that takes options only.
// A flag error or any positional argument becomes an invalid_arguments
// problem, and -h or --help still ends the command through errUsageShown.
func parseFlagsWithoutOperands(flags *flag.FlagSet, arguments []string) error {
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

// connection resolves the server and repository from the flags or, inside a
// clone, from its origin remote, then reads the shared password if a file is
// given.
func (remote *generalRemoteFlags) connection() (connection, error) {
	resolved, err := resolveTarget(context.Background(), remote.server, remote.repository, remote.withRepository, remote.acceptInsecureHTTP, ".")
	if err != nil {
		return connection{}, err
	}
	target := connection{server: resolved.server, repository: resolved.repository, credential: credential{kind: credentialNone}}
	if remote.passwordFile != "" {
		password, err := readServerPassword(remote.passwordFile, resolved.server, resolved.inferredServer, "The shared password file")
		if err != nil {
			return connection{}, err
		}
		target.credential = credential{kind: credentialSharedPassword, secret: password}
	}
	noteInference(resolved)
	return target, nil
}

func cliProblem(code, message string) error {
	return &apiclient.Error{Code: code, Message: message}
}

func writeStructuredCommandError(writer io.Writer, err error) bool {
	var coded interface {
		error
		ErrorCode() string
		ErrorDetails() json.RawMessage
	}
	if !errors.As(err, &coded) {
		return false
	}
	description := pullrequest.ErrorDescription{Code: coded.ErrorCode(), Message: coded.Error()}
	description.Details = coded.ErrorDetails()
	_ = json.NewEncoder(writer).Encode(pullrequest.ErrorEnvelope{OK: false, Error: description})
	return true
}

func printPRUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit pr <create|list|show|edit|diff|review|merge|close|reopen> [options]")
	fmt.Fprintln(writer, "Inside a clone of an OwnGit repository, --server and --repository default to its origin remote. HTTP also requires --accept-insecure-http.")
}
