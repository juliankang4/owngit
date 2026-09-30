package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"owngit/internal/webui"
)

// settingsCommand reads and changes the server-wide policies of Settings
// through the owner API, with the administrator password.
func settingsCommand(arguments []string) error {
	if len(arguments) == 0 {
		printSettingsUsage(os.Stderr)
		return cliProblem("invalid_arguments", "settings requires show, set, access, admin-password or confirmation.")
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
	case "access":
		return settingsAccess(arguments[1:])
	case "admin-password":
		return settingsAdminPassword(arguments[1:])
	case "confirmation":
		return settingsConfirmation(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown settings command: "+arguments[0])
	}
}

func printSettingsUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit settings <show|set|access|admin-password|confirmation> --server URL --password-file PATH [options]")
	fmt.Fprintln(writer, "  settings show             print the server-wide settings as JSON")
	fmt.Fprintln(writer, "  settings set              change the settings named by its options")
	fmt.Fprintln(writer, "  settings access           turn the shared password on, change it, or turn it off")
	fmt.Fprintln(writer, "  settings admin-password   change the administrator password")
	fmt.Fprintln(writer, "  settings confirmation     choose how long a browser remembers the administrator password")
	fmt.Fprintln(writer, "The password file holds the administrator password. Each setting applies to what starts after it is saved.")
	fmt.Fprintln(writer, "A new password is read from an owner-only file or typed at a hidden prompt, never given as an argument.")
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
	transferPerRepository := flags.Int("transfer-per-repository", 0, "how many Git transfers of one repository run at once, from 1 to 32")
	transferExtraSlots := flags.Int("transfer-extra-slots", 0, "extra transfer slots that only a repository with no transfer running may take, from 0 to 32")
	transferIdle := flags.String("transfer-idle", "", "stop a Git transfer whose client moves no data for this long, such as 1m, from 10s to 1h")
	transferQueue := flags.String("transfer-queue", "", "how long a Git transfer waits for a free slot, such as 90s, from 5s to 10m")
	checkLogs := flags.String("check-logs", "", "how long raw check logs are kept: 7d, 30d, 90d, 365d or indefinite")
	keptHistory := flags.String("kept-history", "", "whether repositories that follow the server keep overwritten and deleted history: on or off")
	deleteName := flags.String("delete-requires-name", "", "whether deleting a repository asks for its typed name: on or off")
	loginAttempts := flags.Int("login-attempts", 0, "how many wrong passwords from one address within the login window pause it, from 1 to 100")
	loginWindow := flags.String("login-window", "", "how long wrong passwords are counted together, such as 10m, from 1m to 24h")
	loginPause := flags.String("login-pause", "", "how long an address that reached the attempts is paused, such as 15m, from 1m to 24h")
	browse := map[string]*string{}
	for _, option := range browseOptions {
		browse[option.flag] = flags.String(option.flag, "", option.usage)
	}
	ceilings := map[string]*string{}
	for _, option := range ceilingOptions {
		ceilings[option.flag] = flags.String(option.flag, "", option.usage)
	}
	maintenance := flags.String("maintenance", "", "whether OwnGit maintains repositories: on or off")
	maintenanceWindow := flags.String("maintenance-window", "", "the daily consolidation window in local whole hours, START-END such as 3-5; it may pass midnight, such as 22-6")
	maintenanceIdle := flags.String("maintenance-idle", "", "how long a repository goes unused before maintenance, such as 5m, from 1m to 24h")
	maintenanceStep := flags.String("maintenance-step-time", "", "how long each ordinary maintenance step may take, such as 30m, from 1m to 24h")
	maintenanceFull := flags.String("maintenance-consolidation-time", "", "how long consolidating a repository's packs may take, such as 2h, from 1m to 24h")
	maintenancePacks := flags.Int("maintenance-packs", 0, "the pack count above which the daily window consolidates a repository, from 2 to 1000")
	cleanup := flags.String("unused-object-cleanup", "", "whether nightly maintenance removes objects no ref reaches that are older than the grace period: on or off")
	cleanupGrace := flags.Int("cleanup-grace-days", 0, "the unused object cleanup grace period in days, from 2 to 365")
	crossSite := flags.String("cross-site-links", "", "whether a link from another site keeps the shared sign-in: strict (open it again from OwnGit) or lax (keep the sign-in)")
	updateCheck := flags.String("update-check", "", "whether OwnGit asks GitHub once a day for a newer release: on or off")
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
	if given["kept-history"] {
		change["kept_history"] = *keptHistory
	}
	if given["delete-requires-name"] {
		change["delete_requires_name"] = *deleteName
	}
	if given["cross-site-links"] {
		change["cross_site_links"] = *crossSite
	}
	if given["update-check"] {
		change["update_check"] = *updateCheck
	}
	login := map[string]int64{}
	if given["login-attempts"] {
		login["attempts"] = int64(*loginAttempts)
	}
	for _, option := range []struct{ flag, field, value string }{{"login-window", "window_seconds", *loginWindow}, {"login-pause", "pause_seconds", *loginPause}} {
		if !given[option.flag] {
			continue
		}
		seconds, err := wholeSeconds(option.flag, option.value)
		if err != nil {
			return err
		}
		login[option.field] = seconds
	}
	if len(login) > 0 {
		change["login_limits"] = login
	}
	transfer := map[string]int64{}
	if given["transfer-size"] {
		bytes, err := byteSize("transfer-size", *transferSize)
		if err != nil {
			return err
		}
		transfer["maximum_bytes"] = bytes
	}
	for _, option := range []struct{ flag, field, value string }{
		{"transfer-time", "operation_seconds", *transferTime}, {"transfer-idle", "idle_seconds", *transferIdle}, {"transfer-queue", "queue_seconds", *transferQueue},
	} {
		if !given[option.flag] {
			continue
		}
		seconds, err := wholeSeconds(option.flag, option.value)
		if err != nil {
			return err
		}
		transfer[option.field] = seconds
	}
	if given["transfer-per-repository"] {
		transfer["per_repository"] = int64(*transferPerRepository)
	}
	if given["transfer-extra-slots"] {
		transfer["extra_slots"] = int64(*transferExtraSlots)
	}
	if len(transfer) > 0 {
		change["git_transfer"] = transfer
	}
	browseChange := map[string]int64{}
	for _, option := range browseOptions {
		if !given[option.flag] {
			continue
		}
		var value int64
		var err error
		if option.field == "compare_seconds" {
			value, err = wholeSeconds(option.flag, *browse[option.flag])
		} else {
			value, err = byteSize(option.flag, *browse[option.flag])
		}
		if err != nil {
			return err
		}
		browseChange[option.field] = value
	}
	if len(browseChange) > 0 {
		change["browse_limits"] = browseChange
	}
	ceilingChange := map[string]int64{}
	for _, option := range ceilingOptions {
		if !given[option.flag] {
			continue
		}
		value, err := option.parse(option.flag, *ceilings[option.flag])
		if err != nil {
			return err
		}
		ceilingChange[option.field] = value
	}
	if len(ceilingChange) > 0 {
		change["check_ceilings"] = ceilingChange
	}
	maintenanceChange := map[string]any{}
	if given["maintenance"] {
		maintenanceChange["enabled"] = *maintenance == "on"
		if *maintenance != "on" && *maintenance != "off" {
			return cliProblem("invalid_arguments", "--maintenance takes on or off.")
		}
	}
	if given["maintenance-window"] {
		var start, end int
		if _, err := fmt.Sscanf(*maintenanceWindow, "%d-%d", &start, &end); err != nil || fmt.Sprintf("%d-%d", start, end) != *maintenanceWindow {
			return cliProblem("invalid_arguments", "--maintenance-window takes START-END in whole hours, such as 3-5.")
		}
		maintenanceChange["window_start_hour"], maintenanceChange["window_end_hour"] = start, end
	}
	for _, option := range []struct{ flag, field, value string }{
		{"maintenance-idle", "idle_seconds", *maintenanceIdle}, {"maintenance-step-time", "command_seconds", *maintenanceStep},
		{"maintenance-consolidation-time", "full_repack_seconds", *maintenanceFull},
	} {
		if !given[option.flag] {
			continue
		}
		seconds, err := wholeSeconds(option.flag, option.value)
		if err != nil {
			return err
		}
		maintenanceChange[option.field] = seconds
	}
	if given["maintenance-packs"] {
		maintenanceChange["pack_threshold"] = *maintenancePacks
	}
	if len(maintenanceChange) > 0 {
		change["maintenance"] = maintenanceChange
	}
	cleanupChange := map[string]any{}
	if given["unused-object-cleanup"] {
		if *cleanup != "on" && *cleanup != "off" {
			return cliProblem("invalid_arguments", "--unused-object-cleanup takes on or off.")
		}
		cleanupChange["enabled"] = *cleanup == "on"
	}
	if given["cleanup-grace-days"] {
		cleanupChange["grace_days"] = *cleanupGrace
	}
	if len(cleanupChange) > 0 {
		change["unused_object_cleanup"] = cleanupChange
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

// wholeSeconds reads the time an option names, such as 10m or 90s, as a
// whole number of seconds.
func wholeSeconds(option, value string) (int64, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration%time.Second != 0 {
		return 0, cliProblem("invalid_arguments", "--"+option+" takes a time in whole seconds, such as 90s, 10m or 2h.")
	}
	return int64(duration / time.Second), nil
}

// browseOptions are the options of the browsing limits and their fields in
// the settings API.
var browseOptions = []struct{ flag, field, usage string }{
	{"browse-raw", "raw_bytes", "the largest raw file download, such as 10MB, from 1MB to 256MB"},
	{"browse-file", "file_bytes", "how much of one file the file view shows, such as 2MB, from 64KB to 64MB"},
	{"browse-commit-diff", "commit_patch_bytes", "how much diff a commit page reads, such as 2MB, from 64KB to 64MB"},
	{"browse-file-diff", "file_patch_bytes", "how much diff the page of one changed file reads, such as 8MB, from 64KB to 64MB"},
	{"browse-commit-file", "commit_file_bytes", "the largest diff of one file shown within a commit page, such as 256KB, from 16KB to 16MB"},
	{"browse-compare", "compare_bytes", "how much diff a pull request page reads, such as 8MB, from 64KB to 64MB"},
	{"browse-compare-time", "compare_seconds", "how long reading a pull request comparison may take, such as 20s, from 5s to 1m"},
}

// ceilingOptions are the options of the check ceilings and their fields in
// the settings API.
var ceilingOptions = []struct {
	flag, field, usage string
	parse              func(option, value string) (int64, error)
}{
	{"check-time", "timeout_seconds", "the longest time a check policy may give one check, such as 48h, from 1s to 168h", wholeSeconds},
	{"check-output", "output_bytes", "the most output a check policy may keep for one check, such as 64MB, from 1KB to 1024MB", byteSize},
	{"check-queue", "queue_limit", "the most checks a check policy may queue per repository, from 1 to 10000", wholeNumber},
	{"check-active", "active_jobs", "the most checks a check policy may run at once per repository, from 1 to 1000", wholeNumber},
	{"check-cpus", "container_cpu_millis", "the most CPUs a check policy may give a container, such as 64 or 0.5, from 0.1 to 1024", cores},
	{"check-memory", "container_memory_bytes", "the most memory a check policy may give a container, such as 64GB, from 64MB to 1024GB", byteSize},
	{"check-processes", "container_pids", "the most processes a check policy may allow in a container, from 16 to 65536", wholeNumber},
	{"check-scratch", "container_scratch_bytes", "the most scratch space a check policy may give a container, such as 16GB, from 1MB to 1024GB", byteSize},
	{"check-source", "source_total_bytes", "the most source a check policy may copy for one check, such as 4GB, up to 1024GB", byteSize},
}

// wholeNumber reads the whole number an option names.
func wholeNumber(option, value string) (int64, error) {
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, cliProblem("invalid_arguments", "--"+option+" takes a whole number.")
	}
	return number, nil
}

// cores reads a number of CPUs, such as 2 or 0.5, in thousandths.
func cores(option, value string) (int64, error) {
	millis, err := webui.ParseLimit(webui.LimitCores, webui.LimitInput{Amount: value, Unit: webui.UnitCores})
	if err != nil {
		return 0, cliProblem("invalid_arguments", "--"+option+" takes a number of CPUs, such as 64 or 0.5.")
	}
	return millis, nil
}

// byteSize reads the amount an option names with its unit, such as 4GB or
// 256KB, in bytes.
func byteSize(option, value string) (int64, error) {
	amount := strings.TrimRight(value, "BKMG")
	bytes, err := webui.ParseLimit(webui.LimitSize, webui.LimitInput{Amount: amount, Unit: strings.TrimPrefix(value, amount)})
	if err != nil || amount == value {
		return 0, cliProblem("invalid_arguments", "--"+option+" takes an amount with B, KB, MB or GB, such as 4GB.")
	}
	return bytes, nil
}

// settingsAccess turns the shared password on or replaces it (--mode
// password), or turns it off (--mode open), as the Access group of Settings
// does. The shared password comes from a file or a hidden prompt.
func settingsAccess(arguments []string) error {
	flags := newCommandFlagSet("settings access")
	remote := addImportFlags(flags)
	mode := flags.String("mode", "", "open (no shared password) or password")
	accessFile := flags.String("access-password-file", "", "owner-only file with the new shared password; without it, the password is asked for")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	body := map[string]any{"mode": *mode}
	switch {
	case *mode == "open" && *accessFile != "":
		return cliProblem("invalid_arguments", "--mode open takes no shared password.")
	case *mode == "password":
		password, err := readNewPassword(*accessFile, "The new shared password", "--access-password-file")
		if err != nil {
			return err
		}
		body["password"] = password
	case *mode != "open" && *mode != "password":
		return cliProblem("invalid_arguments", "--mode takes open or password.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPut, "/api/v1/settings/access", body)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

// settingsAdminPassword replaces the administrator password that
// --password-file holds with a new one from a file or a hidden prompt.
func settingsAdminPassword(arguments []string) error {
	flags := newCommandFlagSet("settings admin-password")
	remote := addImportFlags(flags)
	newFile := flags.String("new-password-file", "", "owner-only file with the new administrator password; without it, the password is asked for")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	password, err := readNewPassword(*newFile, "The new administrator password", "--new-password-file")
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPut, "/api/v1/settings/admin-password", map[string]string{"password": password})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Put the new administrator password in the password files your commands use.")
	return writeJSON(content)
}

// settingsConfirmation chooses how long a browser remembers the
// administrator password, as Settings does. Do not ask ("never") needs
// --acknowledge-no-ask.
func settingsConfirmation(arguments []string) error {
	flags := newCommandFlagSet("settings confirmation")
	remote := addImportFlags(flags)
	choice := flags.String("choice", "", "every, 30m, 1h, 8h, 1d, 7d, 30d or never (Do not ask)")
	acknowledge := flags.Bool("acknowledge-no-ask", false, "with --choice never, confirm that anyone who can open the dashboard can then make administrator changes")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *choice == "" {
		return cliProblem("invalid_arguments", "--choice is required.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPut, "/api/v1/settings/admin-confirmation",
		map[string]any{"admin_confirmation": *choice, "acknowledge_no_ask": *acknowledge})
	if err != nil {
		return err
	}
	return writeJSON(content)
}

// readNewPassword reads a new password, named what in messages, from the
// owner-only file at path, or without a file from two hidden prompts on the
// terminal. fileFlag names the option that gives the file.
func readNewPassword(path, what, fileFlag string) (string, error) {
	if path != "" {
		file, err := readPasswordFile(path)
		if err != nil {
			return "", passwordFileProblem(err, what+" file")
		}
		return file.secret, nil
	}
	if info, err := os.Stdin.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "", cliProblem("invalid_arguments", "Give "+fileFlag+", or run from an interactive terminal.")
	}
	reader := bufio.NewReader(os.Stdin)
	first, err := readHiddenLine(reader, what+": ")
	if err != nil {
		return "", err
	}
	second, err := readHiddenLine(reader, "Type it again: ")
	if err != nil {
		return "", err
	}
	first, second = strings.TrimRight(first, "\r\n"), strings.TrimRight(second, "\r\n")
	if first != second {
		return "", cliProblem("invalid_arguments", "The two passwords differ. Nothing was changed.")
	}
	return first, nil
}
