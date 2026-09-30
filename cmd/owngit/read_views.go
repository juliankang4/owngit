package main

import (
	"context"
	"net/http"
	"net/url"
)

// This file holds the read commands that print what the dashboard shows:
// owngit activity for the All activity page, and owngit tasks for the
// Checks tab of a repository and the recent tasks of the Coding tools page.
// They use general access, as those pages do.

// activityCommand prints the commits of a year, or of one day, across every
// repository, with the per-day counts: the newest 1,000 commits and whether
// more exist, as the All activity page lists them.
func activityCommand(arguments []string) error {
	flags := newCommandFlagSet("activity")
	remote := addGeneralRemoteFlags(flags, false)
	year := flags.String("year", "", "calendar year to show; the current year by default")
	date := flags.String("date", "", "list the commits of this day (YYYY-MM-DD)")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(readActivity(context.Background(), target, *year, *date))
}

// readActivity reads the activity of year and date, each "" for the
// default. The server checks both values, as it does for the page.
func readActivity(ctx context.Context, target connection, year, date string) ([]byte, error) {
	query := url.Values{}
	if year != "" {
		query.Set("year", year)
	}
	if date != "" {
		query.Set("date", date)
	}
	path := "/api/v1/activity"
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	return target.client().Do(ctx, http.MethodGet, path, nil)
}

// tasksCommand prints check tasks as the dashboard shows them: without
// --repository the most recently updated tasks of every repository, with it
// that repository's tasks with their latest attempts, and with --task too
// that task with its newest attempts.
func tasksCommand(arguments []string) error {
	flags := newCommandFlagSet("tasks")
	remote := addGeneralRemoteFlags(flags, false)
	flags.StringVar(&remote.repository, "repository", "", "list this repository's tasks; every repository's recent tasks without it")
	task := flags.String("task", "", "show this task with its newest attempts; needs --repository")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *task != "" && remote.repository == "" {
		return cliProblem("invalid_arguments", "--task needs --repository.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	path := "/api/v1/tasks"
	if target.repository != "" {
		path += "/" + url.PathEscape(target.repository)
		if *task != "" {
			path += "/" + url.PathEscape(*task)
		}
	}
	return writeResult(target.client().Do(context.Background(), http.MethodGet, path, nil))
}
