package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

// repoShare lists, creates and revokes a repository's share links through
// the owner API, with the administrator password, and prints JSON. The
// link that create prints holds the link's secret; OwnGit shows it only
// then.
func repoShare(arguments []string) error {
	if len(arguments) == 0 {
		printRepoShareUsage(os.Stderr)
		return cliProblem("invalid_arguments", "repo share requires list, create, or revoke.")
	}
	if isHelpArgument(arguments[0]) {
		printRepoShareUsage(os.Stdout)
		return nil
	}
	command := arguments[0]
	flags := newCommandFlagSet("repo share " + command)
	admin := addRepoAdminFlags(flags)
	var label, scope, linkPasswordFile, id *string
	var days *int
	var untilRevoked *bool
	switch command {
	case "list":
	case "create":
		label = flags.String("label", "", "who the link is for, shown only to administrators")
		scope = flags.String("scope", "browse", "browse (pages and raw files) or clone (also a fetch-only Git clone)")
		days = flags.Int("days", 30, "days until the link expires")
		untilRevoked = flags.Bool("until-revoked", false, "the link works until it is revoked, with no expiry")
		linkPasswordFile = flags.String("link-password-file", "", "owner-readable file holding an extra password visitors must type")
	case "revoke":
		id = flags.String("id", "", "the ID of the link to revoke, as repo share list prints it")
	default:
		return cliProblem("invalid_arguments", "Unknown repo share command: "+command)
	}
	if err := parseFlagsWithoutOperands(flags, arguments[1:]); err != nil {
		return err
	}
	given := map[string]bool{}
	flags.Visit(func(option *flag.Flag) { given[option.Name] = true })
	client, path, err := admin.client("repo share "+command, true)
	if err != nil {
		return err
	}
	path += "/share-links"
	method, body := http.MethodGet, any(nil)
	switch command {
	case "create":
		if *label == "" {
			return cliProblem("invalid_arguments", "repo share create requires --label.")
		}
		if *untilRevoked && given["days"] {
			return cliProblem("invalid_arguments", "Use --days or --until-revoked, not both.")
		}
		input := map[string]any{"label": *label, "scope": *scope}
		if *untilRevoked {
			input["until_revoked"] = true
		} else {
			input["expires_in_days"] = *days
		}
		if *linkPasswordFile != "" {
			file, err := readPasswordFile(*linkPasswordFile)
			if err != nil {
				return passwordFileProblem(err, "The link password file")
			}
			input["password"] = file.secret
		}
		method, body = http.MethodPost, input
	case "revoke":
		if *id == "" {
			return cliProblem("invalid_arguments", "repo share revoke requires --id.")
		}
		method, path = http.MethodPost, path+"/"+url.PathEscape(*id)+"/revoke"
	}
	content, err := client.Do(context.Background(), method, path, body)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func printRepoShareUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit repo share <list|create|revoke> --password-file PATH [--server URL] [--repository NAME] [options]")
	fmt.Fprintln(writer, "  repo share list     print the repository's share links as JSON")
	fmt.Fprintln(writer, "  repo share create   --label NAME [--scope browse|clone] [--days N | --until-revoked] [--link-password-file PATH]")
	fmt.Fprintln(writer, "                      creates a link and prints it once, with its secret; OwnGit cannot show it again")
	fmt.Fprintln(writer, "  repo share revoke   --id ID stops a link at once")
	fmt.Fprintln(writer, "A share link lets someone without an account read this one repository; a clone link also allows a fetch-only Git clone.")
	fmt.Fprintln(writer, "The password file holds the administrator password. Inside a clone of an OwnGit repository, --server and --repository default to its origin remote.")
}
