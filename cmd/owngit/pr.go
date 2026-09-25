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
		return cliProblem("invalid_arguments", "pr requires create, list, show, diff, review, merge, close, or reopen.")
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
	flags := newPRFlagSet("pr create")
	remote := addGeneralRemoteFlags(flags, true)
	title := flags.String("title", "", "pull request title")
	source := flags.String("source", "", "source branch")
	targetBranch := flags.String("target", "", "target branch")
	review := flags.String("review", "", "optional review choice: request or skip")
	if err := parsePRFlags(flags, arguments); err != nil {
		return err
	}
	if *title == "" || *source == "" || *targetBranch == "" {
		return cliProblem("invalid_arguments", "pr create requires --title, --source, and --target. --review is optional.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(createPullRequest(context.Background(), target, pullrequest.CreateInput{
		Title: *title, SourceBranch: *source, TargetBranch: *targetBranch, ReviewChoice: *review,
	}))
}

func prList(arguments []string) error {
	flags := newPRFlagSet("pr list")
	remote := addGeneralRemoteFlags(flags, true)
	if err := parsePRFlags(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(listPullRequests(context.Background(), target))
}

func prShow(arguments []string) error {
	flags := newPRFlagSet("pr show")
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	if err := parsePRFlags(flags, arguments); err != nil {
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
	flags := newPRFlagSet("pr diff")
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	sourceOID := flags.String("source-oid", "", "pin this exact source commit object ID (with --target-oid)")
	targetOID := flags.String("target-oid", "", "pin this exact target commit object ID (with --source-oid)")
	stat := flags.Bool("stat", false, "print the JSON result without the patch")
	patch := flags.Bool("patch", false, "print only the patch text; the revisions and any cut are noted on standard error")
	if err := parsePRFlags(flags, arguments); err != nil {
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
	flags := newPRFlagSet("pr review " + action)
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	sourceOID := flags.String("source-oid", "", "exact source commit object ID")
	targetOID := flags.String("target-oid", "", "exact target commit object ID")
	decision := flags.String("decision", "", "review result: approved or changes_requested")
	reviewer := flags.String("reviewer", "", "supplied reviewer label")
	if err := parsePRFlags(flags, arguments[1:]); err != nil {
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
		return writeResult(submitPullRequestReview(context.Background(), target, *number, pullrequest.ReviewSubmitInput{
			SourceOID: *sourceOID, TargetOID: *targetOID, Decision: *decision, ReviewerLabel: *reviewer,
		}))
	}
	if *decision != "" || *reviewer != "" {
		return cliProblem("invalid_arguments", "--decision and --reviewer are valid only for pr review submit.")
	}
	return writeResult(markPullRequestReview(context.Background(), target, *number, action, pullrequest.RevisionInput{SourceOID: *sourceOID, TargetOID: *targetOID}))
}

func prMerge(arguments []string) error {
	flags := newPRFlagSet("pr merge")
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	sourceOID := flags.String("source-oid", "", "exact source commit object ID")
	targetOID := flags.String("target-oid", "", "exact target commit object ID")
	if err := parsePRFlags(flags, arguments); err != nil {
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
	flags := newPRFlagSet("pr " + action)
	remote := addGeneralRemoteFlags(flags, true)
	number := flags.Int64("number", 0, "pull request number")
	if err := parsePRFlags(flags, arguments); err != nil {
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

func newPRFlagSet(name string) *flag.FlagSet {
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

func parsePRFlags(flags *flag.FlagSet, arguments []string) error {
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
		password, err := readServerPassword(remote.passwordFile, resolved.server, resolved.inferredServer, "The shared password file is unavailable or is not private.")
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
	fmt.Fprintln(writer, "Usage: owngit pr <create|list|show|diff|review|merge|close|reopen> [options]")
	fmt.Fprintln(writer, "Inside a clone of an OwnGit repository, --server and --repository default to its origin remote. HTTP also requires --accept-insecure-http.")
}
