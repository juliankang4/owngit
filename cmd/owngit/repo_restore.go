package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Kept history and restoring files use general access, as the dashboard's
// restore pages do. A restore is previewed, then applied with the preview's
// expected_head, and is refused when the target branch moved in between.

// restoreSelection is what a restore preview or apply names: the source
// commit, the target branch, and the paths to restore, or none for the
// whole tree.
type restoreSelection struct {
	SourceOID    string   `json:"source_oid"`
	TargetBranch string   `json:"target_branch"`
	Mode         string   `json:"mode"`
	Paths        []string `json:"paths,omitempty"`
}

type restoreApplication struct {
	restoreSelection
	ExpectedHead string `json:"expected_head"`
}

// newRestoreSelection selects the listed paths, or the whole tree when
// there are none.
func newRestoreSelection(source, target string, paths []string) (restoreSelection, error) {
	if source == "" || target == "" {
		return restoreSelection{}, cliProblem("invalid_arguments", "A restore needs the source commit ID and the target branch.")
	}
	selection := restoreSelection{SourceOID: source, TargetBranch: target, Mode: "all"}
	if len(paths) != 0 {
		selection.Mode, selection.Paths = "files", paths
	}
	return selection, nil
}

func listKeptHistory(ctx context.Context, target connection) ([]byte, error) {
	if err := requireIdentifier(target.repository, "repository"); err != nil {
		return nil, err
	}
	return target.client().Do(ctx, http.MethodGet, target.repositoryPath()+"/kept-history", nil)
}

func previewRestore(ctx context.Context, target connection, selection restoreSelection) ([]byte, error) {
	if err := requireIdentifier(target.repository, "repository"); err != nil {
		return nil, err
	}
	return target.client().Do(ctx, http.MethodPost, target.repositoryPath()+"/restore/preview", selection)
}

// applyRestore restores selection onto its target branch if the branch is
// still at expectedHead, the expected_head of the preview.
func applyRestore(ctx context.Context, target connection, selection restoreSelection, expectedHead string) ([]byte, error) {
	if err := requireIdentifier(target.repository, "repository"); err != nil {
		return nil, err
	}
	if expectedHead == "" {
		return nil, cliProblem("invalid_arguments", "Applying a restore needs the expected_head of its preview.")
	}
	return target.client().Do(ctx, http.MethodPost, target.repositoryPath()+"/restore", restoreApplication{selection, expectedHead})
}

func repoKeptHistory(arguments []string) error {
	flags := newCommandFlagSet("repo kept-history")
	remote := addGeneralRemoteFlags(flags, true)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	return writeResult(listKeptHistory(context.Background(), target))
}

func repoRestore(arguments []string) error {
	if len(arguments) == 0 {
		printRepoRestoreUsage(os.Stderr)
		return cliProblem("invalid_arguments", "repo restore requires preview or apply.")
	}
	if isHelpArgument(arguments[0]) {
		printRepoRestoreUsage(os.Stdout)
		return nil
	}
	command := arguments[0]
	if command != "preview" && command != "apply" {
		return cliProblem("invalid_arguments", "Unknown repo restore command: "+command)
	}
	flags := newCommandFlagSet("repo restore " + command)
	remote := addGeneralRemoteFlags(flags, true)
	source := flags.String("source", "", "full `OID` of the commit to restore from")
	targetBranch := flags.String("target", "", "`BRANCH` name to restore onto, such as main; a branch that does not exist is created at the source commit")
	var paths stringList
	flags.Var(&paths, "path", "restore only this `FILE`; repeat for more files (default: the whole tree)")
	var expectedHead *string
	if command == "apply" {
		expectedHead = flags.String("expected-head", "", "expected_head `OID` from the preview; the restore is refused if the branch moved")
	}
	if err := parseFlagsWithoutOperands(flags, arguments[1:]); err != nil {
		return err
	}
	selection, err := newRestoreSelection(*source, *targetBranch, paths)
	if err != nil {
		return err
	}
	if expectedHead != nil && *expectedHead == "" {
		return cliProblem("invalid_arguments", "repo restore apply requires --expected-head with the expected_head of the preview.")
	}
	target, err := remote.connection()
	if err != nil {
		return err
	}
	if expectedHead == nil {
		return writeResult(previewRestore(context.Background(), target, selection))
	}
	return writeResult(applyRestore(context.Background(), target, selection, *expectedHead))
}

func printRepoRestoreUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit repo restore <preview|apply> --source OID --target BRANCH [--path FILE]... [options]")
	fmt.Fprintln(writer, "  repo restore preview   list what restoring would change, as JSON; nothing changes")
	fmt.Fprintln(writer, "  repo restore apply     restore, with --expected-head set to the preview's expected_head")
	fmt.Fprintln(writer, "A restore adds one commit on the target branch, or creates the branch at the source commit when it does not exist. It never rewrites history.")
	fmt.Fprintln(writer, "Apply is refused when the target branch moved after the preview; preview again.")
	fmt.Fprintln(writer, "Inside a clone of an OwnGit repository, --server and --repository default to its origin remote.")
}
