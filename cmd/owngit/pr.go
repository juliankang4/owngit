package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"owngit/internal/apiclient"
	"owngit/internal/pullrequest"
)

type prRemoteFlags struct {
	server             string
	repository         string
	passwordFile       string
	acceptInsecureHTTP bool
}

func prCommand(arguments []string) error {
	if len(arguments) == 0 {
		printPRUsage(os.Stderr)
		return cliProblem("invalid_arguments", "pr requires create, list, show, review, or merge.")
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
	default:
		return cliProblem("invalid_arguments", "Unknown pr command: "+arguments[0])
	}
}

func prCreate(arguments []string) error {
	flags := newPRFlagSet("pr create")
	remote := addPRRemoteFlags(flags)
	title := flags.String("title", "", "pull request title")
	source := flags.String("source", "", "source branch")
	target := flags.String("target", "", "target branch")
	review := flags.String("review", "", "optional review choice: request or skip")
	if err := parsePRFlags(flags, arguments); err != nil {
		return err
	}
	if *title == "" || *source == "" || *target == "" {
		return cliProblem("invalid_arguments", "pr create requires --title, --source, and --target. --review is optional.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	return executePRRequest(client, http.MethodPost, remote.collectionPath(), pullrequest.CreateInput{
		Title: *title, SourceBranch: *source, TargetBranch: *target, ReviewChoice: *review,
	})
}

func prList(arguments []string) error {
	flags := newPRFlagSet("pr list")
	remote := addPRRemoteFlags(flags)
	if err := parsePRFlags(flags, arguments); err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	return executePRRequest(client, http.MethodGet, remote.collectionPath(), nil)
}

func prShow(arguments []string) error {
	flags := newPRFlagSet("pr show")
	remote := addPRRemoteFlags(flags)
	number := flags.Int64("number", 0, "pull request number")
	if err := parsePRFlags(flags, arguments); err != nil {
		return err
	}
	if *number <= 0 {
		return cliProblem("invalid_arguments", "pr show requires a positive --number.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	return executePRRequest(client, http.MethodGet, remote.itemPath(*number), nil)
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
	remote := addPRRemoteFlags(flags)
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
	client, err := remote.client()
	if err != nil {
		return err
	}
	path := remote.itemPath(*number) + "/review/" + action
	if action == "submit" {
		if *decision == "" || *reviewer == "" {
			return cliProblem("invalid_arguments", "pr review submit requires --decision and --reviewer.")
		}
		return executePRRequest(client, http.MethodPost, path, pullrequest.ReviewSubmitInput{
			SourceOID: *sourceOID, TargetOID: *targetOID, Decision: *decision, ReviewerLabel: *reviewer,
		})
	}
	if *decision != "" || *reviewer != "" {
		return cliProblem("invalid_arguments", "--decision and --reviewer are valid only for pr review submit.")
	}
	return executePRRequest(client, http.MethodPost, path, pullrequest.RevisionInput{SourceOID: *sourceOID, TargetOID: *targetOID})
}

func prMerge(arguments []string) error {
	flags := newPRFlagSet("pr merge")
	remote := addPRRemoteFlags(flags)
	number := flags.Int64("number", 0, "pull request number")
	sourceOID := flags.String("source-oid", "", "exact source commit object ID")
	targetOID := flags.String("target-oid", "", "exact target commit object ID")
	if err := parsePRFlags(flags, arguments); err != nil {
		return err
	}
	if *number <= 0 || *sourceOID == "" || *targetOID == "" {
		return cliProblem("invalid_arguments", "pr merge requires --number, --source-oid, and --target-oid.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	return executePRRequest(client, http.MethodPost, remote.itemPath(*number)+"/merge", pullrequest.RevisionInput{SourceOID: *sourceOID, TargetOID: *targetOID})
}

func newPRFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func addPRRemoteFlags(flags *flag.FlagSet) *prRemoteFlags {
	remote := &prRemoteFlags{}
	flags.StringVar(&remote.server, "server", "", "OwnGit HTTP(S) origin")
	flags.StringVar(&remote.repository, "repository", "", "repository identifier")
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

func (remote *prRemoteFlags) client() (*apiclient.Client, error) {
	if remote.server == "" || remote.repository == "" {
		return nil, cliProblem("invalid_arguments", "--server and --repository are required.")
	}
	parsed, err := apiclient.ValidateServer(remote.server, remote.acceptInsecureHTTP)
	if err != nil {
		return nil, err
	}
	password := ""
	if remote.passwordFile != "" {
		password, err = readPrivatePassword(remote.passwordFile)
		if err != nil {
			return nil, &apiclient.Error{Code: "invalid_password_file", Message: "The shared password file is unavailable or is not private.", Cause: err}
		}
	}
	return apiclient.New(parsed, password), nil
}

func (remote *prRemoteFlags) collectionPath() string {
	return "/api/v1/repositories/" + url.PathEscape(remote.repository) + "/pull-requests"
}

func (remote *prRemoteFlags) itemPath(number int64) string {
	return remote.collectionPath() + "/" + strconv.FormatInt(number, 10)
}

func executePRRequest(client *apiclient.Client, method, path string, input any) error {
	content, err := client.Do(context.Background(), method, path, input)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(content); err != nil {
		return &apiclient.Error{Code: "output_failed", Message: "The JSON result could not be written.", Cause: err}
	}
	if len(content) == 0 || content[len(content)-1] != '\n' {
		_, _ = fmt.Fprintln(os.Stdout)
	}
	return nil
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
	fmt.Fprintln(writer, "Usage: owngit pr <create|list|show|review|merge> [options]")
	fmt.Fprintln(writer, "Every remote command requires --server and --repository. HTTP also requires --accept-insecure-http.")
}
