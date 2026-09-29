package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"owngit/internal/webui"
)

// settingsCommand reads and changes the server-wide policies of Settings
// through the owner API, with the administrator password.
func settingsCommand(arguments []string) error {
	if len(arguments) == 0 {
		printSettingsUsage(os.Stderr)
		return cliProblem("invalid_arguments", "settings requires show or set.")
	}
	if isHelpArgument(arguments[0]) {
		printSettingsUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "show":
		return settingsShow(arguments[1:])
	case "set":
		return settingsSet(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown settings command: "+arguments[0])
	}
}

func printSettingsUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit settings <show|set> --server URL --password-file PATH [options]")
	fmt.Fprintln(writer, "  settings show   print the server-wide settings as JSON")
	fmt.Fprintln(writer, "  settings set    change the settings named by its options")
	fmt.Fprintln(writer, "The password file holds the administrator password. Each setting applies to what starts after it is saved.")
}

func settingsShow(arguments []string) error {
	flags := newCommandFlagSet("settings show")
	remote := addImportFlags(flags)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodGet, "/api/v1/settings", nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func settingsSet(arguments []string) error {
	flags := newCommandFlagSet("settings set")
	remote := addImportFlags(flags)
	session := flags.String("session", "", "how long a sign-in with the shared password lasts: 1h, 8h, 12h, 1d, 7d or 30d")
	initialBranch := flags.String("initial-branch", "", "the branch new repositories start on, such as main")
	transferSize := flags.String("transfer-size", "", "the most one Git transfer may receive and, separately, send, such as 4GB or 512MB, from 1MB to 64GB")
	transferTime := flags.String("transfer-time", "", "how long one Git transfer may take, such as 30m or 2h, from 1m to 24h")
	checkLogs := flags.String("check-logs", "", "how long raw check logs are kept: 7d, 30d, 90d, 365d or indefinite")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	// Only the options given are sent, so the server checks each value,
	// an empty one included, and changes nothing else.
	given := map[string]bool{}
	flags.Visit(func(option *flag.Flag) { given[option.Name] = true })
	change := map[string]any{}
	if given["session"] {
		change["session"] = *session
	}
	if given["initial-branch"] {
		change["initial_branch"] = *initialBranch
	}
	if given["check-logs"] {
		change["check_logs"] = *checkLogs
	}
	transfer := map[string]int64{}
	if given["transfer-size"] {
		amount := strings.TrimRight(*transferSize, "BKMG")
		bytes, err := webui.ParseLimit(webui.LimitSize, webui.LimitInput{Amount: amount, Unit: strings.TrimPrefix(*transferSize, amount)})
		if err != nil || amount == *transferSize {
			return cliProblem("invalid_arguments", "--transfer-size takes an amount with B, KB, MB or GB, such as 4GB.")
		}
		transfer["maximum_bytes"] = bytes
	}
	if given["transfer-time"] {
		duration, err := time.ParseDuration(*transferTime)
		if err != nil || duration%time.Second != 0 {
			return cliProblem("invalid_arguments", "--transfer-time takes a time in whole seconds, such as 30m or 2h.")
		}
		transfer["operation_seconds"] = int64(duration / time.Second)
	}
	if len(transfer) > 0 {
		change["git_transfer"] = transfer
	}
	if len(change) == 0 {
		return cliProblem("invalid_arguments", "Name at least one setting to change, such as --session 7d.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPatch, "/api/v1/settings", change)
	if err != nil {
		return err
	}
	return writeJSON(content)
}
