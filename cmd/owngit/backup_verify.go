package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"owngit/internal/recovery"
)

// verifyBackup rehearses a restore of a backup and reports each check. It
// exits 1 when the backup is not verified or the rehearsal folder could not
// be removed, after the result is written.
func verifyBackup(arguments []string) error {
	flags := flag.NewFlagSet("backup verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	temporary := flags.String("temp-dir", "", "folder for the rehearsal, which needs room for the restored repositories (default: the system's temporary folder)")
	gitPath := flags.String("git", "", "Git executable path")
	asJSON := flags.Bool("json", false, "print JSON")
	operands, err := parseFlagsAndOperands(flags, arguments)
	if err != nil {
		if errors.Is(err, errUsageShown) {
			return err
		}
		return jsonFailure(jsonRequested(arguments), "invalid_arguments", err)
	}
	if len(operands) != 1 {
		return jsonFailure(*asJSON, "invalid_arguments", errors.New("backup verify takes one backup folder"))
	}
	// An interrupt stops the rehearsal, which then removes its folder.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := recovery.Verify(ctx, operands[0], *temporary, *gitPath)
	if *asJSON {
		if writeErr := writeJSONValue(result); writeErr != nil {
			return writeErr
		}
	} else {
		printVerification(os.Stdout, result)
	}
	switch {
	case ctx.Err() != nil:
		return &checkExit{code: 130, err: err}
	case err != nil:
		return &checkExit{code: 1, err: err}
	}
	return nil
}

// printVerification writes the result of a backup verification for a
// person to read.
func printVerification(writer io.Writer, result recovery.Verification) {
	fmt.Fprintf(writer, "Backup %s", result.Backup)
	if result.Version != 0 {
		fmt.Fprintf(writer, " (version %d, made %s)", result.Version, result.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	fmt.Fprintln(writer)
	for _, item := range result.Repositories {
		fmt.Fprintf(writer, "  %-7s  %s (%d refs)", strings.ReplaceAll(item.Status, "_", " "), item.ID, item.Refs)
		if item.Error != "" {
			fmt.Fprintf(writer, ": %s", item.Error)
		}
		fmt.Fprintln(writer)
	}
	fmt.Fprintf(writer, "Database and schema: %s\n", strings.ReplaceAll(result.Database, "_", " "))
	for _, limit := range result.Limits {
		fmt.Fprintf(writer, "Note: %s\n", limit)
	}
	if result.Verified {
		fmt.Fprintln(writer, "Verified: the backup restored in a rehearsal and passed every check.")
	} else {
		fmt.Fprintf(writer, "Not verified: %s\n", strings.ReplaceAll(result.Error, "\n", "\n  "))
	}
	if len(result.Leftovers) != 0 {
		fmt.Fprintf(writer, "Note: rehearsal folders that a verification left when it stopped, or that another one still uses: %s. Remove them once no verification runs.\n", strings.Join(quoted(result.Leftovers), ", "))
	}
	if result.CleanupError != "" {
		fmt.Fprintf(writer, "Warning: %s\n", result.CleanupError)
	}
}

// quoted quotes each path, so a name shows as the characters it holds.
func quoted(paths []string) []string {
	quoted := make([]string, len(paths))
	for index, path := range paths {
		quoted[index] = strconv.Quote(path)
	}
	return quoted
}
