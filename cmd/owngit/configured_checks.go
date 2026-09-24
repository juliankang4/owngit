package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/checkapi"
	"owngit/internal/checkrunner"
	"owngit/internal/state"
)

func checkPolicyCommand(arguments []string) error {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: owngit check-policy <show|set|enable|disable> [options]")
		return cliProblem("invalid_arguments", "check-policy requires show, set, enable, or disable.")
	}
	if isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, "Usage: owngit check-policy <show|set|enable|disable> [options]")
		return nil
	}
	action, arguments := arguments[0], arguments[1:]
	if !slices.Contains([]string{"show", "set", "enable", "disable"}, action) {
		return cliProblem("invalid_arguments", "Unknown check-policy action.")
	}
	flags := newCheckFlagSet("check-policy " + action)
	admin := addHelperAdminFlags(flags)
	policyFile := flags.String("policy-file", "", "JSON file containing the complete configured-check policy")
	if err := parseCheckFlags(flags, arguments); err != nil {
		return err
	}
	client, err := admin.client()
	if err != nil {
		return err
	}
	path := admin.repositoryPath() + "/check-policy"
	var (
		content []byte
		method  = http.MethodGet
		input   any
	)
	switch action {
	case "show":
	case "set":
		if *policyFile == "" {
			return cliProblem("invalid_arguments", "--policy-file is required for set.")
		}
		policy, err := readPolicyInput(*policyFile)
		if err != nil {
			return err
		}
		method, input = http.MethodPut, policy
	case "enable", "disable":
		method, path = http.MethodPost, path+"/"+action
	default:
		return cliProblem("invalid_arguments", "Unknown check-policy action.")
	}
	content, err = client.Do(context.Background(), method, path, input)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func checkJobCommand(arguments []string) error {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: owngit check-job <list|show|log|cancel|rerun> [options]")
		return cliProblem("invalid_arguments", "check-job requires list, show, log, cancel, or rerun.")
	}
	if isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, "Usage: owngit check-job <list|show|log|cancel|rerun> [options]")
		return nil
	}
	action, arguments := arguments[0], arguments[1:]
	if !slices.Contains([]string{"list", "show", "log", "cancel", "rerun"}, action) {
		return cliProblem("invalid_arguments", "Unknown check-job action.")
	}
	flags := newCheckFlagSet("check-job " + action)
	admin := addHelperAdminFlags(flags)
	jobID := flags.String("job", "", "configured-check job identifier")
	if err := parseCheckFlags(flags, arguments); err != nil {
		return err
	}
	client, err := admin.client()
	if err != nil {
		return err
	}
	path, method := admin.repositoryPath()+"/check-jobs", http.MethodGet
	switch action {
	case "list":
	case "show", "log":
		if !validHexID(*jobID) {
			return cliProblem("invalid_arguments", "--job must be a valid job identifier.")
		}
		path += "/" + *jobID
		if action == "log" {
			path += "/log"
		}
	case "cancel", "rerun":
		if !validHexID(*jobID) {
			return cliProblem("invalid_arguments", "--job must be a valid job identifier.")
		}
		method, path = http.MethodPost, path+"/"+*jobID+"/"+action
	default:
		return cliProblem("invalid_arguments", "Unknown check-job action.")
	}
	content, err := client.Do(context.Background(), method, path, nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func runnerCredentialCommand(arguments []string) error {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: owngit runner-credential <issue|list|revoke> [options]")
		return cliProblem("invalid_arguments", "runner-credential requires issue, list, or revoke.")
	}
	if isHelpArgument(arguments[0]) {
		fmt.Fprintln(os.Stdout, "Usage: owngit runner-credential <issue|list|revoke> [options]")
		return nil
	}
	action, arguments := arguments[0], arguments[1:]
	if !slices.Contains([]string{"issue", "list", "revoke"}, action) {
		return cliProblem("invalid_arguments", "Unknown runner-credential action.")
	}
	flags := newCheckFlagSet("runner-credential " + action)
	admin := addHelperAdminFlags(flags)
	label := flags.String("label", "", "runner credential label")
	creationID := flags.String("creation-id", "", "idempotent credential creation identifier")
	tokenFile := flags.String("token-file", "", "new owner-only token file")
	caFile := flags.String("ca-file", "", "PEM certificate authority file for private HTTPS")
	credentialID := flags.String("credential", "", "runner credential identifier")
	if err := parseCheckFlags(flags, arguments); err != nil {
		return err
	}
	origin, err := apiclient.ValidateServer(admin.server, admin.acceptInsecureHTTP)
	if err != nil {
		return err
	}
	if origin.Scheme == "http" && !runnerLoopbackHost(origin.Hostname()) {
		return cliProblem("invalid_server", "Runner credential operations permit HTTP only for explicitly accepted local loopback fixtures.")
	}
	if origin.Scheme != "https" && *caFile != "" {
		return cliProblem("invalid_arguments", "--ca-file applies only to HTTPS runner connections.")
	}
	client, err := admin.client()
	if err != nil {
		return err
	}
	if *caFile != "" {
		authorities, err := readBoundedFile(*caFile, 1<<20)
		if err != nil {
			return fmt.Errorf("read runner certificate authority file: %w", err)
		}
		if err := client.AddCertificateAuthorities(authorities); err != nil {
			return err
		}
	}
	path := admin.repositoryPath() + "/runner-credentials"
	switch action {
	case "list":
		content, err := client.Do(context.Background(), http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		return writeJSON(content)
	case "revoke":
		if !validHexID(*credentialID) {
			return cliProblem("invalid_arguments", "--credential must be a valid credential identifier.")
		}
		content, err := client.Do(context.Background(), http.MethodDelete, path+"/"+*credentialID, nil)
		if err != nil {
			return err
		}
		return writeJSON(content)
	case "issue":
		if *label == "" || *tokenFile == "" {
			return cliProblem("invalid_arguments", "--label and --token-file are required for issue.")
		}
		if *creationID == "" {
			*creationID, err = state.RandomID()
			if err != nil {
				return err
			}
		}
		reserved, err := reservePrivateTokenFile(*tokenFile)
		if err != nil {
			return err
		}
		fail := func(cause error, compensate bool) error {
			closeErr := reserved.preserve()
			failure := cause
			if compensate {
				failure = compensateRunnerCreation(client, path, *creationID, cause)
			}
			return preservedOutputError(failure, closeErr)
		}
		content, err := client.Do(context.Background(), http.MethodPost, path, checkapi.CreateCredentialInput{Label: *label, CreationID: *creationID})
		if err != nil {
			return fail(err, !isCreationConflict(err))
		}
		var response checkapi.RunnerCredentialResponse
		if err := json.Unmarshal(content, &response); err != nil || response.Credential == nil {
			return fail(errors.New("server did not return a runner credential"), true)
		}
		if response.Credential.RepositoryID != admin.repository || response.Credential.CreationID != *creationID {
			return fail(errors.New("server returned a runner credential for another repository or creation identity"), true)
		}
		if response.Token == "" {
			return fail(errors.New("runner credential issuance did not return a new token"), true)
		}
		if reserved.replaced() {
			return fail(errors.New("runner token path was replaced during issuance; the replacement was left untouched"), true)
		}
		if err := reserved.write(response.Token); err != nil {
			return fail(fmt.Errorf("write runner token file: %w", err), true)
		}
		if reserved.replaced() {
			return fail(errors.New("runner token path was replaced during delivery; the replacement was left untouched"), true)
		}
		if err := reserved.preserve(); err != nil {
			return preservedOutputError(compensateRunnerCreation(client, path, *creationID, fmt.Errorf("close runner token file: %w", err)), err)
		}
		if reserved.replaced() {
			return preservedOutputError(compensateRunnerCreation(client, path, *creationID, errors.New("runner token path was replaced after delivery; the replacement was left untouched")), nil)
		}
		response.Token = ""
		return writeJSONValue(response)
	default:
		return cliProblem("invalid_arguments", "Unknown runner-credential action.")
	}
}

func runnerCommand(arguments []string) error {
	flags := newCheckFlagSet("runner")
	server := flags.String("server", "", "OwnGit HTTP(S) origin")
	repositoryID := flags.String("repository", "", "repository identifier")
	tokenFile := flags.String("token-file", "", "owner-only runner token file")
	caFile := flags.String("ca-file", "", "PEM certificate authority file for private HTTPS")
	workspaceRoot := flags.String("workspace-root", "", "private runner workspace root")
	poll := flags.Duration("poll", 5*time.Second, "idle polling interval")
	once := flags.Bool("once", false, "claim at most one job, then exit")
	insecure := flags.Bool("accept-insecure-http", false, "accept unencrypted HTTP for this runner")
	if err := parseCheckFlags(flags, arguments); err != nil {
		return err
	}
	if *server == "" || *repositoryID == "" || *tokenFile == "" {
		return cliProblem("invalid_arguments", "--server, --repository, and --token-file are required.")
	}
	parsed, err := apiclient.ValidateServer(*server, *insecure)
	if err != nil {
		return err
	}
	if parsed.Scheme == "http" && !runnerLoopbackHost(parsed.Hostname()) {
		return cliProblem("invalid_server", "External runners permit HTTP only for explicitly accepted local loopback fixtures.")
	}
	if parsed.Scheme != "https" && *caFile != "" {
		return cliProblem("invalid_arguments", "--ca-file applies only to HTTPS runner connections.")
	}
	token, err := readPrivateToken(*tokenFile)
	if err != nil {
		return err
	}
	if *workspaceRoot == "" {
		identity := sha256.Sum256([]byte(parsed.String() + "\x00" + *repositoryID))
		*workspaceRoot = filepath.Join(os.TempDir(), fmt.Sprintf("owngit-runner-%x", identity[:8]))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := apiclient.NewBearer(parsed, token)
	if *caFile != "" {
		authorities, err := readBoundedFile(*caFile, 1<<20)
		if err != nil {
			return fmt.Errorf("read runner certificate authority file: %w", err)
		}
		if err := client.AddCertificateAuthorities(authorities); err != nil {
			return err
		}
	}
	client.MaximumRequest = checkapi.MaximumUploadBytes
	client.MaximumResponse = 128 << 20
	runner := &checkrunner.Runner{
		Client: client, RepositoryID: *repositoryID,
		WorkspaceRoot: *workspaceRoot, PollInterval: *poll, Once: *once,
		Logf: func(format string, arguments ...any) { fmt.Fprintf(os.Stderr, format+"\n", arguments...) },
	}
	err = runner.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func runnerLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("file exceeds the supported size")
	}
	return content, nil
}

func compensateRunnerCreation(client *apiclient.Client, path, creationID string, cause error) error {
	_, revokeErr := client.Do(context.Background(), http.MethodDelete, path+"/by-creation/"+creationID, nil)
	if revokeErr != nil {
		return fmt.Errorf("runner credential issuance failed and compensation for creation %s was not confirmed: %w", creationID, errors.Join(cause, revokeErr))
	}
	return fmt.Errorf("runner credential issuance failed and its authority was revoked: %w", cause)
}

func readPolicyInput(path string) (checkapi.PolicyInput, error) {
	file, err := os.Open(path)
	if err != nil {
		return checkapi.PolicyInput{}, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return checkapi.PolicyInput{}, err
	}
	if len(content) > 64<<10 {
		return checkapi.PolicyInput{}, errors.New("configured-check policy file exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var policy checkapi.PolicyInput
	if err := decoder.Decode(&policy); err != nil {
		return checkapi.PolicyInput{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return checkapi.PolicyInput{}, errors.New("configured-check policy file contains trailing JSON")
	}
	return policy, nil
}
