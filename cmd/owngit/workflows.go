package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"owngit/internal/state"
	"owngit/internal/workflows"
)

func workflowCommand(arguments []string) error {
	action, rest, err := workflowAction("workflow", arguments, []string{"list", "show", "dispatch"})
	if err != nil || action == "" {
		return err
	}
	flags := newCommandFlagSet("workflow " + action)
	remote := addGeneralRemoteFlags(flags, true)
	flags.Bool("json", false, "print JSON")
	ref := flags.String("ref", "", "branch name (default: the default branch)")
	path, expected := new(string), new(string)
	var inputs stringList
	if action != "list" {
		flags.StringVar(path, "path", "", "top-level .github/workflows file path")
	}
	if action == "dispatch" {
		flags.StringVar(expected, "expected-oid", "", "refuse if the branch moved from this commit")
		flags.Var(&inputs, "input", "declared input as name=value (repeatable)")
	}
	if err := parseFlagsWithoutOperands(flags, rest); err != nil {
		return err
	}
	if action != "list" && *path == "" {
		return cliProblem("invalid_arguments", "--path is required.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	endpoint := target.repositoryPath() + "/workflows"
	method := http.MethodGet
	var body any
	if action == "dispatch" {
		values := map[string]any{}
		for _, input := range inputs {
			name, value, ok := strings.Cut(input, "=")
			if !ok || name == "" {
				return cliProblem("invalid_arguments", "Each --input must be name=value.")
			}
			if _, exists := values[name]; exists {
				return cliProblem("invalid_arguments", "An input was supplied more than once.")
			}
			values[name] = value
		}
		if *expected != "" && !workflowCommitOID(*expected) {
			return cliProblem("invalid_arguments", "--expected-oid must be a commit object ID.")
		}
		method, endpoint, body = http.MethodPost, endpoint+"/dispatch", workflows.DispatchInput{Path: *path, Ref: *ref, ExpectedOID: *expected, Inputs: values}
	} else {
		query := url.Values{}
		if *ref != "" {
			query.Set("ref", *ref)
		}
		if action == "show" {
			query.Set("path", *path)
		}
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
	}
	return writeResult(target.client().Do(context.Background(), method, endpoint, body))
}

func workflowRunCommand(arguments []string) error {
	action, rest, err := workflowAction("workflow-run", arguments, []string{"list", "show", "log", "cancel", "rerun"})
	if err != nil || action == "" {
		return err
	}
	flags := newCommandFlagSet("workflow-run " + action)
	remote := addGeneralRemoteFlags(flags, true)
	flags.Bool("json", false, "print JSON")
	run, job := new(string), new(string)
	if action != "list" {
		flags.StringVar(run, "run", "", "workflow run identifier")
	}
	if action == "log" || action == "show" {
		flags.StringVar(job, "job", "", "job identifier within the workflow run")
	}
	limit := new(int)
	if action == "list" {
		flags.IntVar(limit, "limit", 50, "maximum runs from 1 to 999; truncated reports older runs")
	}
	if err := parseFlagsWithoutOperands(flags, rest); err != nil {
		return err
	}
	if action != "list" && !validHexID(*run) {
		return cliProblem("invalid_arguments", "--run must be a workflow run identifier.")
	}
	if (action == "log" || *job != "") && !validHexID(*job) {
		return cliProblem("invalid_arguments", "--job must be a job identifier.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	endpoint, method := target.repositoryPath()+"/workflow-runs", http.MethodGet
	var body any
	switch action {
	case "list":
		if *limit < 1 || *limit > 999 {
			return cliProblem("invalid_arguments", "--limit must be from 1 to 999.")
		}
		endpoint += fmt.Sprintf("?limit=%d", *limit)
	case "show":
		endpoint += "/" + *run
		if *job != "" {
			endpoint += "/jobs/" + *job
		}
	case "log":
		endpoint += "/" + *run + "/jobs/" + *job + "/log"
	case "cancel", "rerun":
		method, endpoint, body = http.MethodPost, endpoint+"/"+*run+"/"+action, struct{}{}
	}
	return writeResult(target.client().Do(context.Background(), method, endpoint, body))
}

func workflowSecretCommand(arguments []string) error {
	action, rest, err := workflowAction("workflow-secret", arguments, []string{"list", "set", "remove"})
	if err != nil || action == "" {
		return err
	}
	flags := newCommandFlagSet("workflow-secret " + action)
	admin := addHelperAdminFlags(flags)
	flags.Bool("json", false, "print JSON without secret values")
	name, valueFile := new(string), new(string)
	valueStdin := new(bool)
	if action != "list" {
		flags.StringVar(name, "name", "", "repository secret name")
	}
	if action == "set" {
		flags.StringVar(valueFile, "value-file", "", "owner-only file containing the exact UTF-8 secret value")
		flags.BoolVar(valueStdin, "value-stdin", false, "read the exact secret value from standard input")
	}
	if err := parseFlagsWithoutOperands(flags, rest); err != nil {
		return err
	}
	if action != "list" {
		if err := state.ValidateWorkflowSecretName(*name); err != nil {
			return cliProblem("invalid_arguments", err.Error())
		}
	}
	var body any
	if action == "set" {
		if (*valueFile != "") == *valueStdin {
			return cliProblem("invalid_arguments", "Choose exactly one of --value-file or --value-stdin.")
		}
		var reader io.Reader = os.Stdin
		if *valueFile != "" {
			file, err := state.OpenPrivateInputFile(*valueFile)
			if err != nil {
				return cliProblem("workflow.secret_input", "The secret value file must be an owner-only regular file.")
			}
			defer file.Close()
			reader = file
		}
		value, err := io.ReadAll(io.LimitReader(reader, state.MaxWorkflowSecretValueBytes+1))
		if err != nil {
			return cliProblem("workflow.secret_input", "The secret value could not be read.")
		}
		if err := state.ValidateWorkflowSecretInput(*name, string(value)); err != nil {
			return cliProblem("workflow.secret_input", err.Error())
		}
		body = struct {
			Value string `json:"value"`
		}{string(value)}
	}
	client, err := admin.client()
	if err != nil {
		return err
	}
	endpoint, method := admin.repositoryPath()+"/workflow-secrets", http.MethodGet
	switch action {
	case "set":
		method, endpoint = http.MethodPut, endpoint+"/"+url.PathEscape(*name)
	case "remove":
		method, endpoint, body = http.MethodDelete, endpoint+"/"+url.PathEscape(*name), struct{}{}
	}
	return writeResult(client.Do(context.Background(), method, endpoint, body))
}

func workflowCommitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func workflowAction(command string, arguments, actions []string) (string, []string, error) {
	usage := "Usage: owngit " + command + " <" + strings.Join(actions, "|") + "> [options]"
	if len(arguments) > 0 && isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, usage)
		return "", nil, nil
	}
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return "", nil, cliProblem("invalid_arguments", command+" requires an action.")
	}
	if !slices.Contains(actions, arguments[0]) {
		return "", nil, cliProblem("invalid_arguments", usage)
	}
	return arguments[0], arguments[1:], nil
}
