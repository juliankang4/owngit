package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

func repoCommand(arguments []string) error {
	if len(arguments) == 0 {
		printRepoUsage(os.Stderr)
		return cliProblem("invalid_arguments", "repo requires list, show, or create.")
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
	default:
		return cliProblem("invalid_arguments", "Unknown repo command: "+arguments[0])
	}
}

func repoList(arguments []string) error {
	flags := newPRFlagSet("repo list")
	remote := addGeneralRemoteFlags(flags, false)
	if err := parsePRFlags(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(listRepositories(context.Background(), target))
}

func repoShow(arguments []string) error {
	flags := newPRFlagSet("repo show")
	remote := addGeneralRemoteFlags(flags, true)
	if err := parsePRFlags(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(showRepository(context.Background(), target))
}

func repoCreate(arguments []string) error {
	flags := newPRFlagSet("repo create")
	remote := addGeneralRemoteFlags(flags, false)
	name := flags.String("name", "", "repository name")
	description := flags.String("description", "", "optional repository description")
	if err := parsePRFlags(flags, arguments); err != nil {
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

func printRepoUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit repo <list|show|create> [options]")
	fmt.Fprintln(writer, "Lists, shows, and creates repositories with general access and prints JSON. There is no delete or rename.")
	fmt.Fprintln(writer, "Inside a clone of an OwnGit repository, --server (and --repository for show) default to its origin remote.")
}
