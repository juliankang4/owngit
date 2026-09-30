package main

import (
	"context"
	"errors"
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
		return cliProblem("invalid_arguments", "repo requires list, show, create, rename, settings, default-branch, delete, kept-history, restore, or share.")
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
	case "rename":
		return repoRename(arguments[1:])
	case "settings":
		return repoSettings(arguments[1:])
	case "default-branch":
		return repoDefaultBranch(arguments[1:])
	case "delete":
		return repoDelete(arguments[1:])
	case "kept-history":
		return repoKeptHistory(arguments[1:])
	case "restore":
		return repoRestore(arguments[1:])
	case "share":
		return repoShare(arguments[1:])
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

// repoRename renames a repository through the owner API, with the
// administrator password, and prints the renamed repository as JSON. The
// repository then answers at the new name, and the old one leads there for
// 90 days.
func repoRename(arguments []string) error {
	flags := newCommandFlagSet("repo rename")
	remote := addImportFlags(flags)
	operands, err := parseFlagsAndOperands(flags, arguments)
	if err != nil {
		if errors.Is(err, errUsageShown) {
			return err
		}
		return cliProblem("invalid_arguments", err.Error())
	}
	if len(operands) != 2 || operands[0] == "" || operands[1] == "" {
		return cliProblem("invalid_arguments", "repo rename takes the repository's current name and its new name.")
	}
	client, err := remote.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPost, "/api/v1/repositories/"+url.PathEscape(operands[0])+"/rename", map[string]string{"name": operands[1]})
	if err != nil {
		return err
	}
	return writeJSON(content)
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
	admin := addRepoAdminFlags(flags)
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
	client, path, err := admin.client("repo settings", true)
	if err != nil {
		return err
	}
	method, body := http.MethodGet, any(nil)
	if change != nil {
		method, body = http.MethodPatch, change
	}
	content, err := client.Do(context.Background(), method, path+"/settings", body)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

// repoDefaultBranch makes an existing branch the repository's default
// branch, as its Settings tab does.
func repoDefaultBranch(arguments []string) error {
	flags := newCommandFlagSet("repo default-branch")
	admin := addRepoAdminFlags(flags)
	branch := flags.String("branch", "", "an existing branch of the repository")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *branch == "" {
		return cliProblem("invalid_arguments", "repo default-branch requires --branch.")
	}
	client, path, err := admin.client("repo default-branch", true)
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPost, path+"/default-branch", map[string]string{"branch": *branch})
	if err != nil {
		return err
	}
	return writeJSON(content)
}

// repoDelete deletes a repository, as its delete page does: --files says
// whether its files are kept in the hidden .owngit-removed folder or
// deleted, and while Settings ask for the name, --confirm-name must repeat
// it. The repository is never taken from the origin remote.
func repoDelete(arguments []string) error {
	flags := newCommandFlagSet("repo delete")
	admin := addRepoAdminFlags(flags)
	files := flags.String("files", "", "keep (move the repository's files to the hidden .owngit-removed folder beside it) or delete (delete them)")
	confirmName := flags.String("confirm-name", "", "the repository name again, needed while Settings ask for it before a deletion")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	modes := map[string]string{"keep": "keep_files", "delete": "delete_files"}
	mode, valid := modes[*files]
	if !valid {
		return cliProblem("invalid_arguments", "repo delete requires --files keep or --files delete.")
	}
	if *admin.repository == "" {
		return cliProblem("invalid_arguments", "repo delete requires --repository; it is never taken from the origin remote.")
	}
	client, path, err := admin.client("repo delete", false)
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), http.MethodPost, path+"/delete", map[string]string{"mode": mode, "confirm_name": *confirmName})
	if err != nil {
		return err
	}
	return writeJSON(content)
}

// repoAdminFlags are the options of the repo commands that need the
// administrator password.
type repoAdminFlags struct {
	server, repository, passwordFile *string
	acceptInsecureHTTP               *bool
}

func addRepoAdminFlags(flags *flag.FlagSet) *repoAdminFlags {
	return &repoAdminFlags{
		server:             flags.String("server", "", "OwnGit HTTP(S) origin"),
		repository:         flags.String("repository", "", "repository identifier"),
		passwordFile:       flags.String("password-file", "", "owner-readable file containing the administrator password"),
		acceptInsecureHTTP: flags.Bool("accept-insecure-http", false, "accept unencrypted HTTP for this request"),
	}
}

// client returns an administrator client and the repository's API path.
// Inside a clone, the server defaults to its origin remote, and so does the
// repository when inferRepository is true.
func (admin *repoAdminFlags) client(command string, inferRepository bool) (*apiclient.Client, string, error) {
	target, err := resolveTarget(context.Background(), *admin.server, *admin.repository, inferRepository, *admin.acceptInsecureHTTP, ".")
	if err != nil {
		return nil, "", err
	}
	if *admin.passwordFile == "" {
		return nil, "", cliProblem("invalid_arguments", command+" requires --password-file with the administrator password.")
	}
	password, err := readServerPassword(*admin.passwordFile, target.server, target.inferredServer, "The administrator password file")
	if err != nil {
		return nil, "", err
	}
	noteInference(target)
	return apiclient.NewAdmin(target.server, password), "/api/v1/repositories/" + url.PathEscape(target.repository), nil
}

func printRepoUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit repo <list|show|create|rename|settings|default-branch|delete|kept-history|restore|share> [options]")
	fmt.Fprintln(writer, "Lists, shows, and creates repositories with general access and prints JSON.")
	fmt.Fprintln(writer, "repo rename NAME NEW-NAME --server URL --password-file PATH renames a repository with the administrator password. The old name leads to the new one for 90 days.")
	fmt.Fprintln(writer, "Inside a clone of an OwnGit repository, --server (and --repository for show, settings and default-branch) default to its origin remote.")
	fmt.Fprintln(writer, "repo settings shows and changes one repository's kept history, default branch protection and extra ref namespaces; see owngit repo settings --help.")
	fmt.Fprintln(writer, "repo default-branch --branch NAME makes an existing branch the default branch, with the administrator password.")
	fmt.Fprintln(writer, "repo delete --repository NAME --files keep|delete [--confirm-name NAME] deletes a repository, with the administrator password.")
	fmt.Fprintln(writer, "repo kept-history lists the history kept from overwritten and deleted branches and tags.")
	fmt.Fprintln(writer, "repo restore previews and restores files from an earlier commit; see owngit repo restore --help.")
	fmt.Fprintln(writer, "repo share lists, creates and revokes links that let someone without an account read one repository; see owngit repo share --help.")
}

func printRepoSettingsUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit repo settings <show|set> --password-file PATH [--server URL] [--repository NAME] [options]")
	fmt.Fprintln(writer, "  repo settings show   print the repository's kept history, default branch protection and extra ref namespaces as JSON")
	fmt.Fprintln(writer, "  repo settings set    change them: --kept-history default|on|off, --protect-default-branch on|off, --extra-ref-prefixes refs/notes/,...")
	fmt.Fprintln(writer, "Overwritten or deleted refs in extra ref namespaces have no kept history.")
	fmt.Fprintln(writer, "The password file holds the administrator password. Inside a clone of an OwnGit repository, --server and --repository default to its origin remote.")
	fmt.Fprintln(writer, "A change applies to pushes and imports that start after it is saved.")
}
