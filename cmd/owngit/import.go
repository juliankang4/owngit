package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	if len(arguments) == 0 {
		printImportUsage(os.Stderr)
		return cliProblem("invalid_arguments", "import requires add, configure, refresh, status, history, cancel, schedule, credentials, or resolve.")
	}
	if isHelpArgument(arguments[0]) {
		printImportUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "add":
		return importAdd(arguments[1:])
	case "configure":
		return importConfigure(arguments[1:])
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
	fmt.Fprintln(writer, "Usage: owngit import <add|configure|refresh|status|history|cancel|schedule|credentials|resolve> [options]")
	fmt.Fprintln(writer, "  import add <name> <url> [--mode standalone|coexistence] [--git-only-consent] [--allow-private-network] [--token-file PATH | --basic-file PATH] [--ca-file PATH] [source options] [--json]")
	fmt.Fprintln(writer, "  import configure <name> [source options] [--json]   change the connection options, limits and refresh choices of a source")
	fmt.Fprintln(writer, "  import refresh <name> [--json]")
	fmt.Fprintln(writer, "  import status <name> [--json]")
	fmt.Fprintln(writer, "  import history <name> [--limit N] [--cursor ROW] [--json]")
	fmt.Fprintln(writer, "  import cancel <name> [--json]")
	fmt.Fprintln(writer, "  import schedule <name> --enable --interval 1h | --disable [--json]")
	fmt.Fprintln(writer, "  import credentials <name> [--token-file PATH | --basic-file PATH] [--ca-file PATH] [--json]")
	fmt.Fprintln(writer, "  import credentials <name> --clear [--json]")
	fmt.Fprintln(writer, "  import resolve <name> [--json]   accept the repository as it is after an unresolved publication")
	fmt.Fprintln(writer, "With --json a command prints the server's answer as JSON; the exit status is the same as without it.")
	fmt.Fprintln(writer, "Credentials are read from a private file or an interactive prompt, never from arguments or the environment.")
	fmt.Fprintln(writer, "Source options:")
	fmt.Fprintln(writer, "  --allow-plain-http[=false]              use an http:// source; its code and credentials can be read or changed in transit")
	fmt.Fprintln(writer, "  --redirects refuse|same_origin|approved follow no redirect, redirects within the origin, or also to --approved-origin")
	fmt.Fprintln(writer, "  --approved-origin https://HOST[:PORT]    the one other origin approved redirects follow; it never receives the source's credentials")
	fmt.Fprintln(writer, "  --allow-exceptional-destination[=false] reach usable special-purpose addresses, such as documentation or benchmarking ranges; link-local stays refused")
	fmt.Fprintln(writer, "  --limit NAME=VALUE                      set one limit, repeatable; setting a limit to its default value returns it to the default")
	fmt.Fprintln(writer, "    sizes (pack_bytes, advertisement_bytes) take bytes or KiB, MiB, GiB, TiB; times (run_seconds, fetch_seconds,")
	fmt.Fprintln(writer, "    index_seconds, verify_seconds, tls_handshake_seconds, response_header_seconds) take seconds or a duration such")
	fmt.Fprintln(writer, "    as 90m; counts (refs, lfs_objects) take a number. Higher limits let an import use more disk and run longer.")
	fmt.Fprintln(writer, "  --extra-ref-prefixes refs/notes/,...     also import refs in these namespaces; --extra-ref-prefixes= imports only branches and tags")
	fmt.Fprintln(writer, "                                          overwritten or deleted refs in these namespaces have no kept history")
	fmt.Fprintln(writer, "  --overwrite-diverged[=false]            a later refresh replaces a ref changed here since the source was last seen")
	fmt.Fprintln(writer, "  --follow-upstream-deletions[=false]     a later refresh deletes refs the source deleted, except changed ones unless")
	fmt.Fprintln(writer, "                                          overwrite is on, the default branch and symbolic refs")
	fmt.Fprintln(writer, "    Local work may be replaced; upstream deletions will remove these local refs. A new source address turns both off.")
	fmt.Fprintln(writer, "  owngit import status <name> shows every limit in force and the refs the last two choices would change now.")
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
	password, err := readServerPassword(remote.passwordFile, parsed, false, "The administrator password file")
	if err != nil {
		return nil, err
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
	flags := newCommandFlagSet("import add")
	remote := addImportFlags(flags)
	mode := flags.String("mode", "standalone", "standalone or coexistence")
	gitOnly := flags.Bool("git-only-consent", false, "accept Git LFS pointers as incomplete content")
	privateNetwork := flags.Bool("allow-private-network", false, "allow a private-network source address")
	tokenFile := flags.String("token-file", "", "private file containing a bearer token")
	basicFile := flags.String("basic-file", "", "private file containing a username and password on separate lines")
	caFile := flags.String("ca-file", "", "PEM file containing source trust anchors")
	options := addImportOptionFlags(flags)
	asJSON := flags.Bool("json", false, "print JSON")
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
	for key, value := range options.body(flags) {
		body[key] = value
	}
	if *tokenFile != "" && *basicFile != "" {
		return cliProblem("invalid_arguments", "Use only one of --token-file or --basic-file.")
	}
	if *tokenFile != "" || *basicFile != "" {
		form, username, password, token, err := readImportCredential(*tokenFile, *basicFile)
		if err != nil {
			return jsonFailure(*asJSON, "invalid_arguments", err)
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
	return printImportRun(flags.Arg(0), content, *asJSON)
}

func importRefresh(arguments []string) error {
	name, remote, asJSON, err := parseNamedImport("import refresh", arguments)
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
	return printImportRun(name, content, asJSON)
}

// importRunClientTimeout bounds one synchronous import run request. It is
// longer than the server's request deadline for a run with the longest run
// time a source can set, so the server, not this client, ends a slow run and
// the client still receives its outcome.
var importRunClientTimeout = server.ImportRunRequestTimeout(longestImportRunTime()) + time.Minute

func longestImportRunTime() time.Duration {
	for _, field := range state.ImportLimitFields {
		if field.Name == "run_seconds" {
			return time.Duration(field.Max) * time.Second
		}
	}
	return importsync.DefaultLimits().RunTimeout
}

// runImport posts one synchronous import run. Other import commands keep the
// ordinary API request limit.
func runImport(client *apiclient.Client, path string, body map[string]any) ([]byte, error) {
	client.Timeout = importRunClientTimeout
	return client.Do(context.Background(), http.MethodPost, path+"/run", body)
}

func importStatus(arguments []string) error {
	flags := newCommandFlagSet("import status")
	remote := addImportFlags(flags)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := parseImportFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return cliProblem("invalid_arguments", "import status requires <name>.")
	}
	name := flags.Arg(0)
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
	type runSummary struct {
		Kind                string `json:"kind"`
		Status              string `json:"status"`
		RefsDivergent       int64  `json:"refs_divergent"`
		RefsDeletedUpstream int64  `json:"refs_deleted_upstream"`
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
		Refs                  []importRefView            `json:"refs"`
		RefsTruncated         bool                       `json:"refs_truncated"`
		RefreshEffects        []importsync.RefreshEffect `json:"refresh_effects"`
		RefreshEffectsUnknown bool                       `json:"refresh_effects_unknown"`
		LastRun               *runSummary                `json:"last_run"`
		ActiveRun             *runSummary                `json:"active_run"`
		Options               *importsync.OptionsStatus  `json:"options"`
	}
	var envelope struct {
		Status json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil || json.Unmarshal(envelope.Status, &status) != nil {
		return cliProblem("invalid_response", "Import status could not be read.")
	}
	if *asJSON {
		return printIndentedJSON(envelope.Status)
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
	printImportOptions(status.Options)
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
	if status.ActiveRun != nil {
		fmt.Printf("Active run: %s, %s\n", status.ActiveRun.Kind, status.ActiveRun.Status)
	}
	if status.LastRun != nil {
		fmt.Printf("Last run: %s, %s", status.LastRun.Kind, status.LastRun.Status)
		if status.LastRun.RefsDivergent > 0 {
			fmt.Printf(", %d %s from the source", status.LastRun.RefsDivergent, plural(status.LastRun.RefsDivergent, "ref differs", "refs differ"))
		}
		fmt.Println()
	}
	var differing []importRefView
	for _, ref := range status.Refs {
		if ref.State != "tracked" {
			differing = append(differing, ref)
		}
	}
	if len(differing) == 0 {
		if len(status.Refs) > 0 {
			fmt.Println("Every observed branch and tag matches the source.")
		}
	} else {
		fmt.Printf("Refs that do not match the source: %d\n", len(differing))
		for _, ref := range differing {
			fmt.Printf("  %s: %s\n", ref.Name, importRefStateText(ref.State))
		}
	}
	if status.RefsTruncated {
		fmt.Println("The ref list is truncated.")
	}
	if status.RefreshEffectsUnknown {
		fmt.Println("Which refs overwriting diverged refs or following upstream deletions would change could not be worked out now.")
	}
	if len(status.RefreshEffects) > 0 {
		fmt.Println("Refs that overwriting diverged refs or following upstream deletions would change now:")
		for _, effect := range status.RefreshEffects {
			fmt.Printf("  %s: %s\n", effect.Name, refreshEffectText(effect))
		}
	}
	return nil
}

// refreshEffectText describes what a refresh choice would do to a local ref.
func refreshEffectText(effect importsync.RefreshEffect) string {
	if effect.Effect == "refused" {
		return "the protected default branch differs here; with --overwrite-diverged a refresh stops at it and changes nothing until its protection is turned off"
	}
	text := "replaced with the source's, with --overwrite-diverged"
	if effect.Effect == "delete" {
		text = "deleted, with --follow-upstream-deletions"
		if effect.LocalChanged {
			text = "deleted, with --follow-upstream-deletions and --overwrite-diverged, because it changed here"
		}
	}
	if effect.History == "kept" {
		return text + "; kept history keeps the current tip"
	}
	return text + "; no kept history"
}

func importHistory(arguments []string) error {
	flags := newCommandFlagSet("import history")
	remote := addImportFlags(flags)
	limit := flags.Int("limit", 20, "print at most `N` runs")
	cursor := flags.Int64("cursor", 0, "print runs older than this `ROW`, as named by the previous page")
	asJSON := flags.Bool("json", false, "print JSON")
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
	if *asJSON {
		return printIndentedJSON(content)
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
	name, remote, asJSON, err := parseNamedImport("import cancel", arguments)
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
	if asJSON {
		return printIndentedJSON(content)
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
	name, remote, asJSON, err := parseNamedImport("import resolve", arguments)
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
		Resolved    []string           `json:"resolved"`
		StatusError *importStatusError `json:"status_error"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import resolution could not be read.")
	}
	if asJSON {
		return printIndentedJSON(content)
	}
	fmt.Printf("Accepted the current state of %s for %d unresolved publication intent(s). No ref was changed; the next refresh plans from the repository as it is.\n", name, len(response.Resolved))
	response.StatusError.warn(name)
	return nil
}

func importSchedule(arguments []string) error {
	flags := newCommandFlagSet("import schedule")
	remote := addImportFlags(flags)
	enable := flags.Bool("enable", false, "enable scheduled refresh")
	disable := flags.Bool("disable", false, "disable scheduled refresh")
	interval := flags.String("interval", "", "schedule interval, for example 1h")
	asJSON := flags.Bool("json", false, "print JSON")
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
	if *asJSON {
		return printIndentedJSON(content)
	}
	if response.Enabled {
		fmt.Printf("Scheduled refresh enabled for %s, interval %s.\n", flags.Arg(0), time.Duration(response.IntervalSeconds)*time.Second)
		return nil
	}
	fmt.Printf("Scheduled refresh disabled for %s.\n", flags.Arg(0))
	return nil
}

func importCredentials(arguments []string) error {
	flags := newCommandFlagSet("import credentials")
	remote := addImportFlags(flags)
	tokenFile := flags.String("token-file", "", "private file containing a bearer token")
	basicFile := flags.String("basic-file", "", "private file containing a username and password on separate lines")
	caFile := flags.String("ca-file", "", "PEM file containing source trust anchors")
	clear := flags.Bool("clear", false, "remove the stored credential and source CA")
	asJSON := flags.Bool("json", false, "print JSON")
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
				return jsonFailure(*asJSON, "invalid_arguments", err)
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
		CredentialForm  string             `json:"credential_form"`
		CredentialBound bool               `json:"credential_bound"`
		StatusError     *importStatusError `json:"status_error"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import credential state could not be read.")
	}
	if *asJSON {
		return printIndentedJSON(content)
	}
	if response.StatusError != nil {
		fmt.Printf("The credential change for %s was saved.\n", flags.Arg(0))
		response.StatusError.warn(flags.Arg(0))
		return nil
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

// importOptionFlags are the source options of import add and import
// configure. Only the options given on the command line are sent, so the
// others keep their value.
type importOptionFlags struct {
	plainHTTP      *bool
	redirects      *string
	approvedOrigin *string
	reserved       *bool
	limits         importLimitFlags
	extraPrefixes  *string
	overwrite      *bool
	follow         *bool
}

func addImportOptionFlags(flags *flag.FlagSet) *importOptionFlags {
	options := &importOptionFlags{
		plainHTTP:      flags.Bool("allow-plain-http", false, "allow an http:// source for this source"),
		redirects:      flags.String("redirects", "", "refuse, same_origin, or approved"),
		approvedOrigin: flags.String("approved-origin", "", "the one other origin approved redirects follow, such as https://mirror.example"),
		reserved:       flags.Bool("allow-exceptional-destination", false, "reach usable special-purpose addresses for this source"),
		limits:         importLimitFlags{},
		extraPrefixes:  flags.String("extra-ref-prefixes", "", "comma-separated extra ref namespaces, such as refs/notes/; empty imports only branches and tags"),
		overwrite:      flags.Bool("overwrite-diverged", false, "let a later refresh replace refs changed here"),
		follow:         flags.Bool("follow-upstream-deletions", false, "let a later refresh delete refs the source deleted"),
	}
	flags.Var(options.limits, "limit", "set one limit as `NAME=VALUE`; repeatable")
	return options
}

// body is the request fields of the options given on the command line.
func (options *importOptionFlags) body(flags *flag.FlagSet) map[string]any {
	body := map[string]any{}
	flags.Visit(func(given *flag.Flag) {
		switch given.Name {
		case "allow-plain-http":
			body["allow_plain_http"] = *options.plainHTTP
		case "redirects":
			body["redirects"] = *options.redirects
		case "approved-origin":
			body["approved_redirect_origin"] = *options.approvedOrigin
		case "allow-exceptional-destination":
			body["allow_reserved_addresses"] = *options.reserved
		case "extra-ref-prefixes":
			prefixes := []string{}
			for _, prefix := range strings.Split(*options.extraPrefixes, ",") {
				if prefix = strings.TrimSpace(prefix); prefix != "" {
					prefixes = append(prefixes, prefix)
				}
			}
			body["extra_ref_prefixes"] = prefixes
		case "overwrite-diverged":
			body["overwrite_diverged"] = *options.overwrite
		case "follow-upstream-deletions":
			body["follow_upstream_deletions"] = *options.follow
		}
	})
	if len(options.limits) > 0 {
		body["limits"] = map[string]int64(options.limits)
	}
	return body
}

// importLimitFlags collects --limit NAME=VALUE. A size takes bytes or a
// binary unit, a time takes seconds or a Go duration, and a count a number.
// The server checks each value against its range.
type importLimitFlags map[string]int64

func (limits importLimitFlags) String() string { return "" }

func (limits importLimitFlags) Set(argument string) error {
	name, raw, found := strings.Cut(argument, "=")
	name, raw = strings.TrimSpace(name), strings.TrimSpace(raw)
	if !found || name == "" || raw == "" {
		return errors.New("a limit is NAME=VALUE, such as run_seconds=2h")
	}
	known := false
	for _, field := range state.ImportLimitFields {
		known = known || field.Name == name
	}
	if !known {
		return fmt.Errorf("unknown limit %q; see owngit import --help", name)
	}
	if _, repeated := limits[name]; repeated {
		return fmt.Errorf("limit %s is given twice", name)
	}
	value, err := parseImportLimitValue(name, raw)
	if err != nil {
		return fmt.Errorf("limit %s: %w", name, err)
	}
	limits[name] = value
	return nil
}

func parseImportLimitValue(name, raw string) (int64, error) {
	if number, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return number, nil
	}
	switch {
	case strings.HasSuffix(name, "_seconds"):
		duration, err := time.ParseDuration(raw)
		if err != nil || duration%time.Second != 0 {
			return 0, errors.New("use whole seconds or a duration such as 90m")
		}
		return int64(duration / time.Second), nil
	case strings.HasSuffix(name, "_bytes"):
		for _, unit := range []struct {
			suffix string
			factor int64
		}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40}} {
			amount, found := strings.CutSuffix(raw, unit.suffix)
			if !found {
				continue
			}
			number, err := strconv.ParseInt(strings.TrimSpace(amount), 10, 64)
			// Check the amount before multiplying so it cannot overflow.
			if err != nil || number < 0 || number > math.MaxInt64/unit.factor {
				return 0, errors.New("use bytes or a whole number of KiB, MiB, GiB or TiB")
			}
			return number * unit.factor, nil
		}
		return 0, errors.New("use bytes or a whole number of KiB, MiB, GiB or TiB")
	}
	return 0, errors.New("use a whole number")
}

// importConfigure changes the connection options and limits of a source.
// The address, mode and consents stay.
func importConfigure(arguments []string) error {
	flags := newCommandFlagSet("import configure")
	remote := addImportFlags(flags)
	options := addImportOptionFlags(flags)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := parseImportFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return cliProblem("invalid_arguments", "import configure requires <name>.")
	}
	body := options.body(flags)
	if len(body) == 0 {
		return cliProblem("invalid_arguments", "import configure needs at least one source option; see owngit import --help.")
	}
	path, err := importRepositoryPath(flags.Arg(0))
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPatch, path, body)
	if err != nil {
		return err
	}
	var response struct {
		URL     string                    `json:"url"`
		Options *importsync.OptionsStatus `json:"options"`
	}
	if err := json.Unmarshal(content, &response); err != nil || response.Options == nil {
		return cliProblem("invalid_response", "The changed import source could not be read.")
	}
	if *asJSON {
		return printIndentedJSON(content)
	}
	fmt.Printf("Saved the source options of %s. Changes apply from the next run.\n", flags.Arg(0))
	printImportOptions(response.Options)
	if response.Options.OverwriteDiverged || response.Options.FollowUpstreamDeletions {
		fmt.Printf("Local work may be replaced; upstream deletions will remove these local refs. owngit import status %s lists the refs this would change now.\n", flags.Arg(0))
	}
	return nil
}

// printImportOptions describes a source's options and limits in words.
func printImportOptions(options *importsync.OptionsStatus) {
	if options == nil {
		return
	}
	if options.Problem != "" {
		fmt.Println("Problem: " + options.Problem)
	}
	if options.AllowPlainHTTP {
		fmt.Println("Plain HTTP: allowed; the source's code and credentials can be read or changed in transit")
	}
	switch options.Redirects {
	case state.ImportRedirectSameOrigin:
		fmt.Println("Redirects: followed within the source origin")
	case state.ImportRedirectApproved:
		fmt.Printf("Redirects: followed within the source origin and to %s, which never receives the source's credentials\n", options.ApprovedRedirectOrigin)
	default:
		fmt.Println("Redirects: refused")
	}
	if options.AllowReservedAddresses {
		fmt.Println("Exceptional destination: allowed")
	}
	if len(options.ExtraRefPrefixes) > 0 {
		fmt.Println("Extra ref namespaces: " + strings.Join(options.ExtraRefPrefixes, ", ") + "; overwritten or deleted refs in these namespaces have no kept history")
	}
	if options.OverwriteDiverged {
		fmt.Println("Diverged refs: overwritten with the source's")
	}
	if options.FollowUpstreamDeletions {
		fmt.Println("Upstream deletions: followed")
	}
	if len(options.ChangedLimits) == 0 {
		fmt.Println("Limits: defaults")
		return
	}
	var changed []string
	for _, name := range options.ChangedLimits {
		if value, known := options.Limits.Field(name); known {
			changed = append(changed, fmt.Sprintf("%s=%d", name, *value))
		}
	}
	fmt.Println("Changed limits: " + strings.Join(changed, ", "))
}

// printIndentedJSON prints a server answer, which the caller has already
// read, as the command's JSON result.
func printIndentedJSON(content []byte) error {
	var indented bytes.Buffer
	if err := json.Indent(&indented, content, "", "  "); err != nil {
		return cliProblem("invalid_response", "The response could not be read.")
	}
	return writeJSON(indented.Bytes())
}

func parseNamedImport(name string, arguments []string) (string, *importFlags, bool, error) {
	flags := newCommandFlagSet(name)
	remote := addImportFlags(flags)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := parseImportFlags(flags, arguments); err != nil {
		return "", nil, false, err
	}
	if flags.NArg() != 1 {
		return "", nil, false, cliProblem("invalid_arguments", name+" requires <name>.")
	}
	return flags.Arg(0), remote, *asJSON, nil
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
	case "--git-only-consent", "--allow-private-network", "--accept-insecure-http", "--enable", "--disable", "--clear", "-h", "--help",
		"--allow-plain-http", "--allow-exceptional-destination", "--overwrite-diverged", "--follow-upstream-deletions", "--json":
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
		return "", cliProblem("invalid_arguments", "Input could not be hidden on this terminal. Give it in a file instead; see the command's --help.")
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

// importSecretFileMessage describes a refused import credential file. Other
// failures keep their cause, which names the path but never the content.
func importSecretFileMessage(err error) string {
	var notPrivate *state.NotPrivateError
	if errors.As(err, &notPrivate) {
		return secretFileMessage("The credential file", err)
	}
	return "The credential file is unavailable or is not private: " + err.Error()
}

func readPrivateImportSecret(path string) (string, error) {
	if err := state.ValidatePrivateInputFile(path); err != nil {
		return "", &apiclient.Error{Code: "invalid_credential_file", Message: importSecretFileMessage(err), Cause: err}
	}
	content, err := readBoundedFile(path, 1<<20)
	if err != nil {
		return "", &apiclient.Error{Code: "invalid_credential_file", Message: "The credential file could not be read: " + err.Error(), Cause: err}
	}
	return strings.TrimRight(string(content), "\r\n"), nil
}

// importDivergedExit is the exit status of a finished import or refresh that
// kept at least one local ref that differs from the source.
const importDivergedExit = 3

// importCancelledExit is the exit status of an import or refresh that was
// cancelled before it finished, the status check run uses for a cancelled run.
const importCancelledExit = 130

type importRefView struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// importStatusError is why the server could not read the import status after
// a change it committed.
type importStatusError struct {
	Message string `json:"message"`
}

// warn tells the owner that the change was saved but its status is unknown,
// and how to read it. It prints nothing when the status was read.
func (problem *importStatusError) warn(name string) {
	if problem == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "Warning: the import status after the change could not be read (%s). Check it with owngit import status %s.\n", problem.Message, name)
}

// printImportRun prints the result of an import or refresh run, as text or
// with asJSON as the server's answer. Either way a cancelled run, and a
// finished run that kept refs differing from the source, end with their own
// exit status.
func printImportRun(name string, content []byte, asJSON bool) error {
	var response struct {
		Code string `json:"code"`
		Run  struct {
			Status              string     `json:"status"`
			RefsDivergent       int64      `json:"refs_divergent"`
			RefsDeletedUpstream int64      `json:"refs_deleted_upstream"`
			CancelRequestedAt   *time.Time `json:"cancel_requested_at"`
		} `json:"run"`
		Status *struct {
			Refs          []importRefView `json:"refs"`
			RefsTruncated bool            `json:"refs_truncated"`
		} `json:"status"`
		StatusError *importStatusError `json:"status_error"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return cliProblem("invalid_response", "Import run result could not be read.")
	}
	cancelled := response.Code == "cancelled"
	divergent := response.Run.RefsDivergent
	var exit error
	switch {
	case cancelled:
		exit = &checkExit{code: importCancelledExit, err: errors.New("the import was cancelled")}
	case divergent > 0:
		exit = &checkExit{code: importDivergedExit, err: errors.New("refs differ from the import source")}
	}
	if asJSON {
		if err := printIndentedJSON(content); err != nil {
			return err
		}
		return exit
	}
	response.StatusError.warn(name)
	if cancelled {
		fmt.Printf("Import for %s was cancelled.\n", name)
		return exit
	}
	fmt.Printf("Import for %s finished: %s.\n", name, response.Run.Status)
	if response.Run.Status == "complete" && response.Run.CancelRequestedAt != nil {
		fmt.Println("The cancellation arrived after the import was published, so it did not stop it.")
	}
	if deleted := response.Run.RefsDeletedUpstream; deleted > 0 {
		fmt.Printf("%d %s deleted at the source and kept here.\n", deleted, plural(deleted, "ref was", "refs were"))
	}
	if divergent == 0 {
		return nil
	}
	fmt.Printf("%d %s from the source and %s left unchanged here:\n", divergent, plural(divergent, "ref differs", "refs differ"), plural(divergent, "was", "were"))
	// Unlisted refs are HEAD or case-differing names only when the status
	// read every ref name: it was read, not truncated, and saw each local ref.
	var refs []importRefView
	namesRead := false
	if response.Status != nil {
		refs, namesRead = response.Status.Refs, !response.Status.RefsTruncated
	}
	listed := int64(0)
	for _, ref := range refs {
		switch ref.State {
		case "diverged":
			fmt.Printf("  %s\n", ref.Name)
			listed++
		case "unknown_local":
			namesRead = false
		}
	}
	if divergent > listed {
		if namesRead {
			fmt.Printf("  %d more not listed by name, such as HEAD or a name that differs only by case.\n", divergent-listed)
		} else {
			fmt.Printf("  %d not named here because the ref names could not be read in full. List them with owngit import status %s.\n", divergent-listed, name)
		}
	}
	return exit
}

func plural(count int64, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// importRefStateText describes a ref state from import status in words.
func importRefStateText(state string) string {
	switch state {
	case "diverged":
		return "differs from the source"
	case "deleted_at_source":
		return "deleted at the source, kept here"
	case "not_imported":
		return "in a namespace this source no longer imports, kept here"
	case "absent_locally":
		return "missing in OwnGit"
	case "earlier_source":
		return "recorded from an earlier source URL"
	case "unknown_local":
		return "OwnGit side could not be read"
	default:
		return state
	}
}
