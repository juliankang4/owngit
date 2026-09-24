package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/importsync"
	"owngit/internal/server"
	"owngit/internal/state"
)

func importCommand(arguments []string) error {
	if len(arguments) == 0 || isHelpArgument(arguments[0]) {
		printImportUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "add":
		return importAdd(arguments[1:])
	case "refresh":
		return importRefresh(arguments[1:])
	case "status":
		return importStatus(arguments[1:])
	case "history":
		return importHistory(arguments[1:])
	case "cancel":
		return importCancel(arguments[1:])
	case "schedule":
		return importSchedule(arguments[1:])
	case "credentials":
		return importCredentials(arguments[1:])
	case "resolve":
		return importResolve(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown import command: "+arguments[0])
	}
}

func printImportUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit import <add|refresh|status|history|cancel|schedule|credentials|resolve> [options]")
	fmt.Fprintln(writer, "  import add <name> <url> [--mode standalone|coexistence] [--git-only-consent] [--allow-private-network] [--token-file PATH | --basic-file PATH] [--ca-file PATH]")
	fmt.Fprintln(writer, "  import refresh <name>")
	fmt.Fprintln(writer, "  import status <name>")
	fmt.Fprintln(writer, "  import history <name> [--limit N] [--cursor C]")
	fmt.Fprintln(writer, "  import cancel <name>")
	fmt.Fprintln(writer, "  import schedule <name> --enable --interval 1h | --disable")
	fmt.Fprintln(writer, "  import credentials <name> [--token-file PATH | --basic-file PATH] [--ca-file PATH]")
	fmt.Fprintln(writer, "  import credentials <name> --clear")
	fmt.Fprintln(writer, "  import resolve <name>   accept the repository as it is after an unresolved publication")
	fmt.Fprintln(writer, "Credentials are read from a private file or an interactive prompt, never from arguments or the environment.")
}

type importFlags struct {
	server             string
	passwordFile       string
	acceptInsecureHTTP bool
}

func addImportFlags(flags *flag.FlagSet) *importFlags {
	remote := &importFlags{}
	flags.StringVar(&remote.server, "server", "", "OwnGit HTTP(S) origin")
	flags.StringVar(&remote.passwordFile, "password-file", "", "owner-readable file containing the administrator password")
	flags.BoolVar(&remote.acceptInsecureHTTP, "accept-insecure-http", false, "accept unencrypted HTTP for this request")
	return remote
}

func (remote *importFlags) client() (*apiclient.Client, error) {
	if remote.server == "" || remote.passwordFile == "" {
		return nil, cliProblem("invalid_arguments", "--server and --password-file are required.")
	}
	parsed, err := apiclient.ValidateServer(remote.server, remote.acceptInsecureHTTP)
	if err != nil {
		return nil, err
	}
	password, err := readPrivatePassword(remote.passwordFile)
	if err != nil {
		return nil, &apiclient.Error{Code: "invalid_password_file", Message: "The administrator password file is unavailable or is not private.", Cause: err}
	}
	return apiclient.NewAdmin(parsed, password), nil
}

func importRepositoryPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ToLower(name) != name {
		return "", cliProblem("invalid_arguments", "The repository name must be a lowercase repository identifier.")
	}
	return "/api/v1/repositories/" + name + "/import", nil
}

func importAdd(arguments []string) error {
	flags := newCheckFlagSet("import add")
	remote := addImportFlags(flags)
	mode := flags.String("mode", "standalone", "standalone or coexistence")
	gitOnly := flags.Bool("git-only-consent", false, "accept Git LFS pointers as incomplete content")
	privateNetwork := flags.Bool("allow-private-network", false, "allow a private-network source address")
	tokenFile := flags.String("token-file", "", "private file containing a bearer token")
	basicFile := flags.String("basic-file", "", "private file containing a username and password on separate lines")
	caFile := flags.String("ca-file", "", "PEM file containing source trust anchors")
	if err := parseImportFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return cliProblem("invalid_arguments", "import add requires <name> and <url>.")
	}
	path, err := importRepositoryPath(flags.Arg(0))
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	body := map[string]any{
		"name": flags.Arg(0), "url": flags.Arg(1), "mode": *mode,
		"git_only_consent": *gitOnly, "allow_private_network": *privateNetwork,
	}
	if *tokenFile != "" && *basicFile != "" {
		return cliProblem("invalid_arguments", "Use only one of --token-file or --basic-file.")
	}
	if *tokenFile != "" || *basicFile != "" {
		form, username, password, token, err := readImportCredential(*tokenFile, *basicFile)
		if err != nil {
			return err
		}
		body["credential_form"] = form
		body["username"] = username
		body["password"] = password
		body["token"] = token
	}
	if *caFile != "" {
		caPEM, err := readImportCA(*caFile)
		if err != nil {
			return err
		}
		body["ca_pem"] = caPEM
		if _, exists := body["credential_form"]; !exists {
			body["credential_form"] = "none"
		}
	}
	client.MaximumRequest = server.MaximumImportCredentialRequest
	content, err := runImport(client, path, body)
	if err != nil {
		return err
	}
	return printImportRun(flags.Arg(0), content)
}

func importRefresh(arguments []string) error {
	name, remote, err := parseNamedImport("import refresh", arguments)
	if err != nil {
		return err
	}
	path, err := importRepositoryPath(name)
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := runImport(client, path, map[string]any{})
	if err != nil {
		return err
	}
	return printImportRun(name, content)
}

// importRunClientTimeout bounds one synchronous import run request. It is
// longer than the server's request deadline for that run, so the server, not
// this client, ends a slow run and the client still receives its outcome.
var importRunClientTimeout = server.ImportRunRequestTimeout(importsync.DefaultLimits().RunTimeout) + time.Minute

// runImport posts one synchronous import run. Other import commands keep the
// ordinary API request limit.
func runImport(client *apiclient.Client, path string, body map[string]any) ([]byte, error) {
	client.Timeout = importRunClientTimeout
	return client.Do(context.Background(), http.MethodPost, path+"/run", body)
}

func importStatus(arguments []string) error {
	name, remote, err := parseNamedImport("import status", arguments)
	if err != nil {
		return err
	}
	path, err := importRepositoryPath(name)
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var status struct {
		Configured      bool   `json:"configured"`
		URL             string `json:"url"`
		Mode            string `json:"mode"`
		CredentialForm  string `json:"credential_form"`
		CredentialBound bool   `json:"credential_bound"`
		Unresolved      int    `json:"unresolved_intents"`
		StagingIssues   int    `json:"staging_issues"`
		Content         struct {
			Incomplete bool `json:"incomplete"`
		} `json:"content"`
		Runtime struct {
			SchedulerRunning bool   `json:"scheduler_running"`
			SchedulerFailed  bool   `json:"scheduler_failed"`
			Code             string `json:"code"`
			Reason           string `json:"reason"`
		} `json:"runtime"`
	}
	var envelope struct {
		Status json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil || json.Unmarshal(envelope.Status, &status) != nil {
		return cliProblem("invalid_response", "Import status could not be read.")
	}
	if !status.Configured {
		fmt.Printf("Import for %s is not configured.\n", name)
		return nil
	}
	bound := "no"
	if status.CredentialBound {
		bound = "yes"
	}
	fmt.Printf("Import for %s\nURL: %s\nMode: %s\nCredential: %s, bound: %s\n", name, status.URL, status.Mode, status.CredentialForm, bound)
	if status.Content.Incomplete {
		fmt.Println("Content is incomplete.")
	}
	if status.Unresolved > 0 {
		fmt.Printf("Unresolved publication intents: %d. Refreshes are refused until the owner checks the repository and runs owngit import resolve %s.\n", status.Unresolved, name)
	}
	if status.Runtime.SchedulerRunning {
		fmt.Println("Scheduler: running")
	} else {
		fmt.Println("Scheduler: not running")
	}
	if status.Runtime.Code != "" {
		fmt.Printf("Import runtime problem (%s): %s\n", status.Runtime.Code, status.Runtime.Reason)
	}
	if status.StagingIssues > 0 {
		fmt.Printf("Staging issues: %d\n", status.StagingIssues)
	}
	return nil
}

func importHistory(arguments []string) error {
	flags := newCheckFlagSet("import history")
	remote := addImportFlags(flags)
	limit := flags.Int("limit", 20, "maximum runs to print")
	cursor := flags.Int64("cursor", 0, "return runs older than this row id")
	if err := parseImportFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return cliProblem("invalid_arguments", "import history requires <name>.")
	}
	path, err := importRepositoryPath(flags.Arg(0))
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	target := fmt.Sprintf("%s/history?limit=%d", path, *limit)
	if *cursor > 0 {
		target += fmt.Sprintf("&cursor=%d", *cursor)
	}
	content, err := client.Do(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	var response struct {
		Runs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Kind   string `json:"kind"`
			RowID  int64  `json:"row_id"`
		} `json:"runs"`
		More   bool  `json:"more"`
		Cursor int64 `json:"cursor"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import history could not be read.")
	}
	if len(response.Runs) == 0 {
		fmt.Println("No import runs.")
		return nil
	}
	for _, run := range response.Runs {
		fmt.Printf("%s %s %s row %d\n", run.ID, run.Kind, run.Status, run.RowID)
	}
	if response.More {
		fmt.Printf("More runs follow cursor %d.\n", response.Cursor)
	}
	return nil
}

func importCancel(arguments []string) error {
	name, remote, err := parseNamedImport("import cancel", arguments)
	if err != nil {
		return err
	}
	path, err := importRepositoryPath(name)
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPost, path+"/cancel", map[string]any{})
	if err != nil {
		return err
	}
	var response struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import cancellation could not be read.")
	}
	if response.Cancelled {
		fmt.Printf("Cancellation requested for %s.\n", name)
		return nil
	}
	fmt.Printf("No active import for %s.\n", name)
	return nil
}

// importResolve accepts the repository as it is after an unresolved
// publication. The server writes no ref; it records the accepted state.
func importResolve(arguments []string) error {
	name, remote, err := parseNamedImport("import resolve", arguments)
	if err != nil {
		return err
	}
	path, err := importRepositoryPath(name)
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPost, path+"/resolve", map[string]any{})
	if err != nil {
		return err
	}
	var response struct {
		Resolved []string `json:"resolved"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import resolution could not be read.")
	}
	fmt.Printf("Accepted the current state of %s for %d unresolved publication intent(s). No ref was changed; the next refresh plans from the repository as it is.\n", name, len(response.Resolved))
	return nil
}

func importSchedule(arguments []string) error {
	flags := newCheckFlagSet("import schedule")
	remote := addImportFlags(flags)
	enable := flags.Bool("enable", false, "enable scheduled refresh")
	disable := flags.Bool("disable", false, "disable scheduled refresh")
	interval := flags.String("interval", "", "schedule interval, for example 1h")
	if err := parseImportFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 || *enable == *disable {
		return cliProblem("invalid_arguments", "import schedule requires <name> and exactly one of --enable or --disable.")
	}
	if *enable && *interval == "" {
		return cliProblem("invalid_arguments", "import schedule --enable requires --interval.")
	}
	path, err := importRepositoryPath(flags.Arg(0))
	if err != nil {
		return err
	}
	chosen := *interval
	if chosen == "" {
		chosen = "1h"
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPut, path+"/schedule", map[string]any{
		"enabled": *enable, "interval": chosen,
	})
	if err != nil {
		return err
	}
	var response struct {
		Enabled         bool  `json:"enabled"`
		IntervalSeconds int64 `json:"interval_seconds"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import schedule could not be read.")
	}
	if response.Enabled {
		fmt.Printf("Scheduled refresh enabled for %s, interval %s.\n", flags.Arg(0), time.Duration(response.IntervalSeconds)*time.Second)
		return nil
	}
	fmt.Printf("Scheduled refresh disabled for %s.\n", flags.Arg(0))
	return nil
}

func importCredentials(arguments []string) error {
	flags := newCheckFlagSet("import credentials")
	remote := addImportFlags(flags)
	tokenFile := flags.String("token-file", "", "private file containing a bearer token")
	basicFile := flags.String("basic-file", "", "private file containing a username and password on separate lines")
	caFile := flags.String("ca-file", "", "PEM file containing source trust anchors")
	clear := flags.Bool("clear", false, "remove the stored credential and source CA")
	if err := parseImportFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return cliProblem("invalid_arguments", "import credentials requires <name>.")
	}
	if *tokenFile != "" && *basicFile != "" {
		return cliProblem("invalid_arguments", "Use only one of --token-file or --basic-file.")
	}
	if *clear && (*tokenFile != "" || *basicFile != "" || *caFile != "") {
		return cliProblem("invalid_arguments", "--clear cannot be combined with credential files.")
	}
	var method string
	var body map[string]any
	if *clear {
		method = http.MethodDelete
	} else {
		body = map[string]any{"form": "none"}
		// A CA file alone stores only a trust anchor. Otherwise the secret
		// comes from a private file or the interactive prompt.
		if *tokenFile != "" || *basicFile != "" || *caFile == "" {
			form, username, password, token, err := readImportCredential(*tokenFile, *basicFile)
			if err != nil {
				return err
			}
			body = map[string]any{"form": form, "username": username, "password": password, "token": token}
		}
		if *caFile != "" {
			caPEM, err := readImportCA(*caFile)
			if err != nil {
				return err
			}
			body["ca_pem"] = caPEM
		}
		method = http.MethodPut
	}
	path, err := importRepositoryPath(flags.Arg(0))
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	client.MaximumRequest = server.MaximumImportCredentialRequest
	var input any
	if body != nil {
		input = body
	}
	content, err := client.Do(context.Background(), method, path+"/credentials", input)
	if err != nil {
		return err
	}
	var response struct {
		CredentialForm  string `json:"credential_form"`
		CredentialBound bool   `json:"credential_bound"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import credential state could not be read.")
	}
	bound := "no"
	if response.CredentialBound {
		bound = "yes"
	}
	fmt.Printf("Credential %s, bound: %s.\n", response.CredentialForm, bound)
	return nil
}

// readImportCA reads a source CA bundle within the bound the server stores.
func readImportCA(path string) (string, error) {
	content, err := readBoundedFile(path, state.MaxImportCABytes)
	if err != nil {
		return "", cliProblem("invalid_arguments", fmt.Sprintf("The CA file could not be read within %d bytes: %v", state.MaxImportCABytes, err))
	}
	return string(content), nil
}

func parseNamedImport(name string, arguments []string) (string, *importFlags, error) {
	flags := newCheckFlagSet(name)
	remote := addImportFlags(flags)
	if err := parseImportFlags(flags, arguments); err != nil {
		return "", nil, err
	}
	if flags.NArg() != 1 {
		return "", nil, cliProblem("invalid_arguments", name+" requires <name>.")
	}
	return flags.Arg(0), remote, nil
}

func parseImportFlags(flags *flag.FlagSet, arguments []string) error {
	positionals, flagArgs := splitImportArgs(arguments)
	if err := parseFlags(flags, append(flagArgs, positionals...)); err != nil {
		if errors.Is(err, errUsageShown) {
			return err
		}
		return cliProblem("invalid_arguments", err.Error())
	}
	return nil
}

func splitImportArgs(arguments []string) (positionals, flagArgs []string) {
	for index := 0; index < len(arguments); index++ {
		arg := arguments[index]
		if arg == "--" {
			positionals = append(positionals, arguments[index+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		if isImportBoolFlag(arg) || strings.Contains(arg, "=") {
			continue
		}
		if index+1 < len(arguments) && !strings.HasPrefix(arguments[index+1], "-") {
			index++
			flagArgs = append(flagArgs, arguments[index])
		}
	}
	return positionals, flagArgs
}

func isImportBoolFlag(arg string) bool {
	switch arg {
	case "--git-only-consent", "--allow-private-network", "--accept-insecure-http", "--enable", "--disable", "--clear", "-h", "--help":
		return true
	default:
		return false
	}
}

func readImportCredential(tokenFile, basicFile string) (form, username, password, token string, err error) {
	if tokenFile != "" {
		token, err = readPrivateImportSecret(tokenFile)
		if err != nil {
			return "", "", "", "", err
		}
		return "bearer", "", "", token, nil
	}
	if basicFile != "" {
		content, err := readPrivateImportSecret(basicFile)
		if err != nil {
			return "", "", "", "", err
		}
		username, password, ok := strings.Cut(content, "\n")
		// Files written on Windows end lines with CRLF; the CR is not part
		// of either value.
		username = strings.TrimSuffix(username, "\r")
		password = strings.TrimRight(password, "\r\n")
		if !ok || username == "" || password == "" {
			return "", "", "", "", cliProblem("invalid_arguments", "The basic credential file must contain a username and password on separate lines.")
		}
		return "basic", username, password, "", nil
	}
	info, statErr := os.Stdin.Stat()
	if statErr != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "", "", "", "", cliProblem("invalid_arguments", "Provide --token-file or --basic-file, or run from an interactive terminal.")
	}
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, "Credential form (bearer or basic): ")
	form, err = reader.ReadString('\n')
	if err != nil {
		return "", "", "", "", err
	}
	form = strings.TrimSpace(form)
	switch form {
	case "bearer":
		token, err = readHiddenLine(reader, "Token: ")
		if err != nil {
			return "", "", "", "", err
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return "", "", "", "", cliProblem("invalid_arguments", "A token is required.")
		}
		return "bearer", "", "", token, nil
	case "basic":
		fmt.Fprint(os.Stderr, "Username: ")
		username, err = reader.ReadString('\n')
		if err != nil {
			return "", "", "", "", err
		}
		password, err = readHiddenLine(reader, "Password: ")
		if err != nil {
			return "", "", "", "", err
		}
		username = strings.TrimSpace(username)
		password = strings.TrimRight(password, "\r\n")
		if username == "" || password == "" {
			return "", "", "", "", cliProblem("invalid_arguments", "A username and password are required.")
		}
		return "basic", username, password, "", nil
	default:
		return "", "", "", "", cliProblem("invalid_arguments", "Credential form must be bearer or basic.")
	}
}

// disableEcho turns off stdin echo and returns the restore function. Tests
// replace it to observe restoration without a terminal.
var disableEcho = disableStdinEcho

// readHiddenLine prompts and reads one line from the stdin terminal without
// echoing it. Echo is off before the prompt appears, so fast typing is hidden
// too. The signals that can end the process are caught before echo goes off,
// and every exit path restores echo, so the terminal is never left without it.
// Input that cannot be hidden is refused instead of being shown.
func readHiddenLine(reader *bufio.Reader, prompt string) (string, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, hiddenInputSignals...)
	restore, err := disableEcho()
	if err != nil {
		signal.Stop(signals)
		return "", cliProblem("invalid_arguments", "Input could not be hidden on this terminal. Use --token-file or --basic-file.")
	}
	var restoreOnce sync.Once
	var restoreErr error
	restoreEcho := func() { restoreOnce.Do(func() { restoreErr = restore() }) }
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		if received, ok := <-signals; ok {
			restoreEcho()
			fmt.Fprintln(os.Stderr)
			os.Exit(128 + signalNumber(received))
		}
	}()
	fmt.Fprint(os.Stderr, prompt)
	line, readErr := reader.ReadString('\n')
	// Restore echo while the signals are still caught, so no signal can end
	// the process between the read and the restore.
	restoreEcho()
	// After Stop no signal is sent, so closing the channel lets the watcher
	// act on a signal that arrived meanwhile and otherwise finish.
	signal.Stop(signals)
	close(signals)
	<-handled
	// The Enter key was not echoed either, so end the prompt line here.
	fmt.Fprintln(os.Stderr)
	if readErr != nil {
		return "", readErr
	}
	if restoreErr != nil {
		return "", fmt.Errorf("restore terminal echo: %w", restoreErr)
	}
	return line, nil
}

// signalNumber is the conventional exit status offset of a signal.
func signalNumber(received os.Signal) int {
	if number, ok := received.(syscall.Signal); ok {
		return int(number)
	}
	return 1
}

func readPrivateImportSecret(path string) (string, error) {
	if err := state.ValidatePrivateFile(path); err != nil {
		return "", fmt.Errorf("inspect credential file: %w", err)
	}
	content, err := readBoundedFile(path, 1<<20)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(content), "\r\n"), nil
}

func printImportRun(name string, content []byte) error {
	var response struct {
		Code string `json:"code"`
		Run  struct {
			Status string `json:"status"`
		} `json:"run"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import run result could not be read.")
	}
	if response.Code == "cancelled" {
		fmt.Printf("Import for %s was cancelled.\n", name)
		return nil
	}
	fmt.Printf("Import for %s finished: %s.\n", name, response.Run.Status)
	return nil
}
