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
		return cliProblem("invalid_arguments", "pr requires create, list, show, review, merge, close, or reopen.")
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

func (remote *generalRemoteFlags) connection() (connection, error) {
	if !remote.withRepository && remote.server == "" {
		return connection{}, cliProblem("invalid_arguments", "--server is required.")
	}
	if remote.withRepository && (remote.server == "" || remote.repository == "") {
		return connection{}, cliProblem("invalid_arguments", "--server and --repository are required.")
	}
	parsed, err := apiclient.ValidateServer(remote.server, remote.acceptInsecureHTTP)
	if err != nil {
		return connection{}, err
	}
	target := connection{server: parsed, repository: remote.repository, credential: credential{kind: credentialNone}}
	if remote.passwordFile != "" {
		password, err := readPrivatePassword(remote.passwordFile)
		if err != nil {
			return connection{}, &apiclient.Error{Code: "invalid_password_file", Message: "The shared password file is unavailable or is not private.", Cause: err}
		}
		target.credential = credential{kind: credentialSharedPassword, secret: password}
	}
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
	fmt.Fprintln(writer, "Usage: owngit pr <create|list|show|review|merge|close|reopen> [options]")
	fmt.Fprintln(writer, "Every remote command requires --server and --repository. HTTP also requires --accept-insecure-http.")
}
