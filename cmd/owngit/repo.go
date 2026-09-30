package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"owngit/internal/apiclient"
)

func repoCommand(arguments []string) error {
	if len(arguments) == 0 {
		printRepoUsage(os.Stderr)
		return cliProblem("invalid_arguments", "repo requires list, show, create, settings, kept-history, or restore.")
	}
	if isHelpArgument(arguments[0]) {
		printRepoUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "list":
		return repoList(arguments[1:])
	case "show":
		return repoShow(arguments[1:])
	case "create":
		return repoCreate(arguments[1:])
	case "settings":
		return repoSettings(arguments[1:])
	case "kept-history":
		return repoKeptHistory(arguments[1:])
	case "restore":
		return repoRestore(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown repo command: "+arguments[0])
	}
}

func repoList(arguments []string) error {
	flags := newCommandFlagSet("repo list")
	remote := addGeneralRemoteFlags(flags, false)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(listRepositories(context.Background(), target))
}

func repoShow(arguments []string) error {
	flags := newCommandFlagSet("repo show")
	remote := addGeneralRemoteFlags(flags, true)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(showRepository(context.Background(), target))
}

func repoCreate(arguments []string) error {
	flags := newCommandFlagSet("repo create")
	remote := addGeneralRemoteFlags(flags, false)
	name := flags.String("name", "", "repository name")
	description := flags.String("description", "", "optional repository description")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *name == "" {
		return cliProblem("invalid_arguments", "repo create requires --name. --description is optional.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(createRepository(context.Background(), target, repositoryInput{Name: *name, Description: *description}))
}

// repoSettings reads and changes a repository's kept history choice and
// default branch protection through the owner API, with the administrator
// password.
func repoSettings(arguments []string) error {
	if len(arguments) == 0 {
		printRepoSettingsUsage(os.Stderr)
		return cliProblem("invalid_arguments", "repo settings requires show or set.")
	}
	if isHelpArgument(arguments[0]) {
		printRepoSettingsUsage(os.Stdout)
		return nil
	}
	command := arguments[0]
	if command != "show" && command != "set" {
		return cliProblem("invalid_arguments", "Unknown repo settings command: "+command)
	}
	flags := newCommandFlagSet("repo settings " + command)
	server := flags.String("server", "", "OwnGit HTTP(S) origin")
	repositoryName := flags.String("repository", "", "repository identifier")
	passwordFile := flags.String("password-file", "", "owner-readable file containing the administrator password")
	acceptInsecureHTTP := flags.Bool("accept-insecure-http", false, "accept unencrypted HTTP for this request")
	keptHistory := flags.String("kept-history", "", "keep overwritten and deleted history: default (follow the server), on or off")
	protect := flags.String("protect-default-branch", "", "refuse pushes that rewrite or delete the default branch: on or off")
	namespaces := flags.String("extra-ref-prefixes", "", "ref namespaces beyond branches and tags that pushes may change, separated by commas, such as refs/notes/; an empty value removes them all")
	if err := parseFlagsWithoutOperands(flags, arguments[1:]); err != nil {
		return err
	}
	given := map[string]bool{}
	flags.Visit(func(option *flag.Flag) { given[option.Name] = true })
	var change map[string]any
	if command == "set" {
		change = map[string]any{}
		if given["kept-history"] {
			change["kept_history"] = *keptHistory
		}
		if given["protect-default-branch"] {
			if *protect != "on" && *protect != "off" {
				return cliProblem("invalid_arguments", "--protect-default-branch takes on or off.")
			}
			change["protect_default_branch"] = *protect == "on"
		}
		if given["extra-ref-prefixes"] {
			prefixes := []string{}
			for _, prefix := range strings.Split(*namespaces, ",") {
				if prefix = strings.TrimSpace(prefix); prefix != "" {
					prefixes = append(prefixes, prefix)
				}
			}
			change["extra_ref_prefixes"] = prefixes
		}
		if len(change) == 0 {
			return cliProblem("invalid_arguments", "Name at least one setting to change, such as --protect-default-branch on.")
		}
	} else if given["kept-history"] || given["protect-default-branch"] || given["extra-ref-prefixes"] {
		return cliProblem("invalid_arguments", "repo settings show takes no settings; use repo settings set.")
	}
	// Inside a clone, the server and the repository default to its origin
	// remote, as for the other repo commands.
	target, err := resolveTarget(context.Background(), *server, *repositoryName, true, *acceptInsecureHTTP, ".")
	if err != nil {
		return err
	}
	if *passwordFile == "" {
		return cliProblem("invalid_arguments", "repo settings requires --password-file with the administrator password.")
	}
	password, err := readServerPassword(*passwordFile, target.server, target.inferredServer, "The administrator password file")
	if err != nil {
		return err
	}
	noteInference(target)
	client := apiclient.NewAdmin(target.server, password)
	path := "/api/v1/repositories/" + url.PathEscape(target.repository) + "/settings"
	method, body := http.MethodGet, any(nil)
	if change != nil {
		method, body = http.MethodPatch, change
	}
	content, err := client.Do(context.Background(), method, path, body)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func printRepoUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit repo <list|show|create|settings|kept-history|restore> [options]")
	fmt.Fprintln(writer, "Lists, shows, and creates repositories with general access and prints JSON. There is no delete or rename.")
	fmt.Fprintln(writer, "Inside a clone of an OwnGit repository, --server (and --repository for show and settings) default to its origin remote.")
	fmt.Fprintln(writer, "repo settings shows and changes one repository's kept history, default branch protection and extra ref namespaces; see owngit repo settings --help.")
	fmt.Fprintln(writer, "repo kept-history lists the history kept from overwritten and deleted branches and tags.")
	fmt.Fprintln(writer, "repo restore previews and restores files from an earlier commit; see owngit repo restore --help.")
}

func printRepoSettingsUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit repo settings <show|set> --password-file PATH [--server URL] [--repository NAME] [options]")
	fmt.Fprintln(writer, "  repo settings show   print the repository's kept history, default branch protection and extra ref namespaces as JSON")
	fmt.Fprintln(writer, "  repo settings set    change them: --kept-history default|on|off, --protect-default-branch on|off, --extra-ref-prefixes refs/notes/,...")
	fmt.Fprintln(writer, "Overwritten or deleted refs in extra ref namespaces have no kept history.")
	fmt.Fprintln(writer, "The password file holds the administrator password. Inside a clone of an OwnGit repository, --server and --repository default to its origin remote.")
	fmt.Fprintln(writer, "A change applies to pushes and imports that start after it is saved.")
}
