package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
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
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	// Only the options given are sent, so the server checks each value,
	// an empty one included, and changes nothing else.
	change := map[string]any{}
	flags.Visit(func(given *flag.Flag) {
		switch given.Name {
		case "session":
			change["session"] = *session
		case "initial-branch":
			change["initial_branch"] = *initialBranch
		}
	})
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
