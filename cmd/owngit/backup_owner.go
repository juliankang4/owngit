package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
)

// Backups that the running server makes: owngit backup now, status, runs
// and schedule talk to it through the owner API. owngit backup --output
// makes a backup of a stopped OwnGit instead.

// backupOwnerCommands are the backup subcommands that talk to a server.
var backupOwnerCommands = map[string]func([]string) error{
	"now":      backupNow,
	"status":   backupStatus,
	"runs":     backupRuns,
	"schedule": backupScheduleCommand,
}

func backupNow(arguments []string) error {
	return backupRequest("backup now", arguments, http.MethodPost, "/api/v1/backups/runs")
}

func backupStatus(arguments []string) error {
	return backupRequest("backup status", arguments, http.MethodGet, "/api/v1/backups")
}

func backupRuns(arguments []string) error {
	return backupRequest("backup runs", arguments, http.MethodGet, "/api/v1/backups/runs")
}

// backupRequest sends one request without a body and prints the answer.
func backupRequest(name string, arguments []string, method, path string) error {
	flags := newCommandFlagSet(name)
	remote := addImportFlags(flags)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), method, path, nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func backupScheduleCommand(arguments []string) error {
	if len(arguments) == 0 {
		printBackupScheduleUsage(os.Stderr)
		return cliProblem("invalid_arguments", "backup schedule requires show, set or off.")
	}
	if isHelpArgument(arguments[0]) {
		printBackupScheduleUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "show":
		return backupRequest("backup schedule show", arguments[1:], http.MethodGet, "/api/v1/backups/schedule")
	case "set":
		return backupScheduleSet(arguments[1:])
	case "off":
		return backupScheduleOff(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown backup schedule command: "+arguments[0])
	}
}

func printBackupScheduleUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit backup schedule <show|set|off> --server URL --password-file PATH [options]")
	fmt.Fprintln(writer, "  backup schedule show   print the schedule as JSON")
	fmt.Fprintln(writer, "  backup schedule set    turn scheduled backups on, changing the options given")
	fmt.Fprintln(writer, "  backup schedule off    stop scheduled backups; back up now still works")
	fmt.Fprintln(writer, "The password file holds the administrator password. The first set needs --destination; the defaults are --interval 1d, --keep 7 and --verify on.")
}

func backupScheduleSet(arguments []string) error {
	flags := newCommandFlagSet("backup schedule set")
	remote := addImportFlags(flags)
	destination := flags.String("destination", "", "absolute path of the folder that receives the backups, created when missing")
	interval := flags.String("interval", "", "time between scheduled backups: 12h, 1d or 7d")
	keep := flags.String("keep", "", "how many of its own backups OwnGit keeps in the folder, from 1 to 1000")
	verify := flags.String("verify", "", "rehearse a restore of each new backup: on or off")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	change := map[string]any{"scheduled": "on"}
	var problem error
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "destination":
			change["destination"] = *destination
		case "interval":
			change["interval"] = *interval
		case "verify":
			change["verify"] = *verify
		case "keep":
			count, err := strconv.Atoi(*keep)
			if err != nil {
				problem = cliProblem("invalid_arguments", "--keep takes a whole number, such as 7.")
			}
			change["keep"] = count
		}
	})
	if problem != nil {
		return problem
	}
	return backupScheduleChange(remote, change)
}

func backupScheduleOff(arguments []string) error {
	flags := newCommandFlagSet("backup schedule off")
	remote := addImportFlags(flags)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	return backupScheduleChange(remote, map[string]any{"scheduled": "off"})
}

func backupScheduleChange(remote *importFlags, change map[string]any) error {
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPatch, "/api/v1/backups/schedule", change)
	if err != nil {
		return err
	}
	return writeJSON(content)
}
