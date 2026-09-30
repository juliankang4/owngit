package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// Backups that the running server makes: owngit backup now, status, runs,
// schedule, check, download and upload talk to it through the owner API.
// owngit backup --output makes a backup of a stopped OwnGit instead, and
// owngit backup verify checks a backup folder on this computer.

// backupOwnerCommands are the backup subcommands that talk to a server.
var backupOwnerCommands = map[string]func([]string) error{
	"now":      backupNow,
	"status":   backupStatus,
	"runs":     backupRuns,
	"schedule": backupScheduleCommand,
	"check":    backupCheck,
	"download": backupDownload,
	"upload":   backupUpload,
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

// backupRunFlag adds --run, the ID of a recorded backup as owngit backup
// runs lists it.
func backupRunFlag(flags *flag.FlagSet) *string {
	return flags.String("run", "", "ID of the backup, as owngit backup runs lists it")
}

// backupRunPath is the API path of an operation on the backup of run.
func backupRunPath(run, operation string) (string, error) {
	if len(run) != 32 || strings.Trim(run, "0123456789abcdef") != "" {
		return "", cliProblem("invalid_arguments", "--run takes the 32 character ID that owngit backup runs lists.")
	}
	return "/api/v1/backups/runs/" + run + "/" + operation, nil
}

// backupCheck verifies a backup the server made again, as a new backup is
// verified, and prints the verification it started; owngit backup status
// shows its result.
func backupCheck(arguments []string) error {
	flags := newCommandFlagSet("backup check")
	remote := addImportFlags(flags)
	run := backupRunFlag(flags)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	path, err := backupRunPath(*run, "verify")
	if err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

// backupDownload writes a backup the server made into a new tar file.
func backupDownload(arguments []string) error {
	flags := newCommandFlagSet("backup download")
	remote := addImportFlags(flags)
	run := backupRunFlag(flags)
	output := flags.String("output", "", "new file that receives the backup as a tar archive; it holds password hashes and every repository")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	path, err := backupRunPath(*run, "archive")
	if err != nil {
		return err
	}
	if *output == "" {
		return cliProblem("invalid_arguments", "--output is required.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return cliProblem("output_failed", "The backup file could not be created: "+err.Error())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = client.Download(ctx, path, "application/x-tar", file)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = cliProblem("output_failed", "The backup file could not be written: "+closeErr.Error())
	}
	if err != nil {
		// Only this command created the file, so a part of an archive
		// never stays behind to be taken for a whole one.
		_ = os.Remove(*output)
		return err
	}
	info, err := os.Stat(*output)
	if err != nil {
		return err
	}
	return writeJSONValue(struct {
		OK    bool   `json:"ok"`
		Path  string `json:"path"`
		Bytes int64  `json:"bytes"`
	}{true, mustAbs(*output), info.Size()})
}

// backupUpload sends a backup archive to the server, which verifies it and
// keeps it to restore; owngit backup status shows its verification and
// the restore command.
func backupUpload(arguments []string) error {
	flags := newCommandFlagSet("backup upload")
	remote := addImportFlags(flags)
	input := flags.String("input", "", "tar archive of a backup, as owngit backup download writes it")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *input == "" {
		return cliProblem("invalid_arguments", "--input is required.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	file, err := os.Open(*input)
	if err != nil {
		return cliProblem("input_failed", "The backup file could not be opened: "+err.Error())
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return cliProblem("input_failed", "--input must be a regular file.")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	content, err := client.Upload(ctx, "/api/v1/backups/upload", file, info.Size())
	if err != nil {
		return err
	}
	return writeJSON(content)
}
