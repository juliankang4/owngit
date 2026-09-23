package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"

	"owngit/internal/apiclient"
	"owngit/internal/checkapi"
	"owngit/internal/state"
)

func helperCredentialCommand(arguments []string) error {
	if len(arguments) == 0 {
		return cliProblem("invalid_arguments", "helper-credential requires create, list, or revoke.")
	}
	if arguments[0] == "help" || arguments[0] == "-h" || arguments[0] == "--help" {
		printHelperCredentialUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "create":
		return helperCredentialCreate(arguments[1:])
	case "list":
		return helperCredentialList(arguments[1:])
	case "revoke":
		return helperCredentialRevoke(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown helper-credential command: "+arguments[0])
	}
}

type helperAdminFlags struct {
	server             string
	repository         string
	passwordFile       string
	acceptInsecureHTTP bool
}

func addHelperAdminFlags(flags *flag.FlagSet) *helperAdminFlags {
	admin := &helperAdminFlags{}
	flags.StringVar(&admin.server, "server", "", "OwnGit HTTP(S) origin")
	flags.StringVar(&admin.repository, "repository", "", "repository identifier")
	flags.StringVar(&admin.passwordFile, "password-file", "", "owner-readable file containing the administrator password")
	flags.BoolVar(&admin.acceptInsecureHTTP, "accept-insecure-http", false, "accept unencrypted HTTP for this request")
	return admin
}

func (admin *helperAdminFlags) client() (*apiclient.Client, error) {
	if admin.server == "" || admin.repository == "" || admin.passwordFile == "" {
		return nil, cliProblem("invalid_arguments", "--server, --repository, and --password-file are required.")
	}
	parsed, err := apiclient.ValidateServer(admin.server, admin.acceptInsecureHTTP)
	if err != nil {
		return nil, err
	}
	password, err := readPrivatePassword(admin.passwordFile)
	if err != nil {
		return nil, &apiclient.Error{Code: "invalid_password_file", Message: "The administrator password file is unavailable or is not private.", Cause: err}
	}
	return apiclient.NewAdmin(parsed, password), nil
}

func (admin *helperAdminFlags) repositoryPath() string {
	return "/api/v1/repositories/" + url.PathEscape(admin.repository)
}

// helperCredentialCreate issues one credential and delivers its token through
// an exclusively created private file.
func helperCredentialCreate(arguments []string) error {
	flags := newCheckFlagSet("helper-credential create")
	admin := addHelperAdminFlags(flags)
	label := flags.String("label", "", "credential label")
	output := flags.String("output", "", "owner-readable file that receives the token")
	if err := parseCheckFlags(flags, arguments); err != nil {
		return err
	}
	if *output == "" {
		return cliProblem("invalid_arguments", "helper-credential create requires --output. The token is delivered only through that file.")
	}
	client, err := admin.client()
	if err != nil {
		return err
	}
	creationID, err := state.RandomID()
	if err != nil {
		return err
	}
	reserved, err := reservePrivateTokenFile(*output)
	if err != nil {
		return err
	}
	fail := func(code string, cause error, compensate bool) error {
		closeErr := reserved.preserve()
		failure := cause
		if compensate {
			failure = compensateCreation(client, admin.repositoryPath(), creationID, code, cause)
		}
		return preservedOutputError(failure, closeErr)
	}

	content, err := client.Do(context.Background(), "POST", admin.repositoryPath()+"/helper-credentials",
		checkapi.CreateCredentialInput{Label: *label, CreationID: creationID})
	if err != nil {
		// A definite conflicting creation keeps the existing authority.
		return fail("credential_creation_failed", err, !isCreationConflict(err))
	}
	var response checkapi.CredentialResponse
	if err := json.Unmarshal(content, &response); err != nil || response.Credential == nil {
		return fail("credential_creation_failed", cliProblem("invalid_response", "The server did not return a helper credential."), true)
	}
	if response.Credential.RepositoryID != admin.repository || response.Credential.CreationID != creationID {
		return fail("credential_creation_failed",
			cliProblem("mismatched_response", "The server returned a credential for another repository or creation identity."), true)
	}
	if response.Token == "" {
		return fail("credential_creation_failed", cliProblem("token_unavailable", "The server returned an existing credential without its token."), true)
	}
	if reserved.replaced() {
		return fail("output_replaced",
			cliProblem("output_replaced", "The output path was replaced while the credential was created. The replacement was left untouched."), true)
	}
	if err := reserved.write(response.Token); err != nil {
		return fail("token_delivery_failed",
			&apiclient.Error{Code: "token_delivery_failed", Message: "The token could not be written.", Cause: err}, true)
	}
	if reserved.replaced() {
		return fail("output_replaced",
			cliProblem("output_replaced", "The output path was replaced while the credential was created. The replacement was left untouched."), true)
	}
	if err := reserved.preserve(); err != nil {
		return preservedOutputError(compensateCreation(client, admin.repositoryPath(), creationID, "token_delivery_failed",
			&apiclient.Error{Code: "token_delivery_failed", Message: "The token file could not be closed.", Cause: err}), err)
	}
	if reserved.replaced() {
		return preservedOutputError(compensateCreation(client, admin.repositoryPath(), creationID, "output_replaced",
			cliProblem("output_replaced", "The output path was replaced while the credential was created. The replacement was left untouched.")), nil)
	}
	// The token is delivered only through the file, so stdout never carries it.
	response.Token = ""
	return writeJSONValue(response)
}

func preservedOutputError(err, closeErr error) error {
	const instruction = " The reserved output artifact was preserved. Inspect it and remove it manually before retrying."
	var problem *apiclient.Error
	if errors.As(err, &problem) {
		copy := *problem
		copy.Message += instruction
		copy.Cause = errors.Join(problem.Cause, closeErr)
		return &copy
	}
	return &apiclient.Error{
		Code:    "output_preserved",
		Message: "Credential creation failed." + instruction,
		Cause:   errors.Join(err, closeErr),
	}
}

// isCreationConflict reports a definite conflicting creation rejection, which
// preserves the existing operation's authority.
func isCreationConflict(err error) bool {
	var problem *apiclient.Error
	return errors.As(err, &problem) && problem.Code == "creation_conflict"
}

// compensateCreation revokes the credential created by one operation. The
// revoke is scoped by the locally generated creation identity, so it never
// trusts a returned credential identifier, and it is idempotent when nothing
// was created. When the revoke cannot be confirmed, the non-secret creation
// identity is disclosed with an actionable instruction.
func compensateCreation(client *apiclient.Client, repositoryPath, creationID, code string, cause error) error {
	if revokeErr := revokeCredentialByCreation(client, repositoryPath, creationID); revokeErr != nil {
		return &apiclient.Error{
			Code:    "credential_creation_unconfirmed",
			Message: "The creation outcome is unconfirmed and the compensating revoke failed. Revoke creation " + creationID + " before retrying.",
			Cause:   cause,
		}
	}
	return &apiclient.Error{Code: code, Message: "The credential was revoked, so retry.", Cause: cause}
}

func helperCredentialList(arguments []string) error {
	flags := newCheckFlagSet("helper-credential list")
	admin := addHelperAdminFlags(flags)
	if err := parseCheckFlags(flags, arguments); err != nil {
		return err
	}
	client, err := admin.client()
	if err != nil {
		return err
	}
	content, err := client.Do(context.Background(), "GET", admin.repositoryPath()+"/helper-credentials", nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func helperCredentialRevoke(arguments []string) error {
	flags := newCheckFlagSet("helper-credential revoke")
	admin := addHelperAdminFlags(flags)
	id := flags.String("id", "", "credential identifier")
	if err := parseCheckFlags(flags, arguments); err != nil {
		return err
	}
	if *id == "" {
		return cliProblem("invalid_arguments", "helper-credential revoke requires --id.")
	}
	client, err := admin.client()
	if err != nil {
		return err
	}
	if err := revokeCredential(client, admin.repositoryPath(), *id); err != nil {
		return err
	}
	return writeJSONValue(checkapi.OKResponse{OK: true})
}

func revokeCredential(client *apiclient.Client, repositoryPath, id string) error {
	_, err := client.Do(context.Background(), "DELETE", repositoryPath+"/helper-credentials/"+url.PathEscape(id), nil)
	return err
}

func revokeCredentialByCreation(client *apiclient.Client, repositoryPath, creationID string) error {
	_, err := client.Do(context.Background(), "DELETE", repositoryPath+"/helper-credentials/by-creation/"+url.PathEscape(creationID), nil)
	return err
}

// reservedTokenFile keeps the exclusively created delivery file open, so the
// token is written to the reserved file itself and never to whatever the path
// happens to name after a network call.
type reservedTokenFile struct {
	path string
	file *os.File
	info os.FileInfo
}

// reservePrivateTokenFile creates the delivery file exclusively with owner-only
// permissions. An existing file or symlink is reported instead of replaced.
func reservePrivateTokenFile(path string) (*reservedTokenFile, error) {
	return reservePrivateTokenFileWithProtection(path, state.ProtectPrivateHandle)
}

func reservePrivateTokenFileWithProtection(path string, protect func(*os.File, bool) error) (*reservedTokenFile, error) {
	file, err := state.CreatePrivateFile(path)
	if err != nil {
		if os.IsExist(err) {
			return nil, &apiclient.Error{Code: "output_exists", Message: "The output file already exists. Remove it or choose another path."}
		}
		return nil, &apiclient.Error{Code: "output_failed", Message: "The helper token file could not be reserved.", Cause: err}
	}
	if err := protect(file, false); err != nil {
		closeErr := file.Close()
		return nil, &apiclient.Error{
			Code:    "output_failed",
			Message: "The helper token file could not be protected. The reserved artifact was preserved for manual inspection and removal.",
			Cause:   errors.Join(err, closeErr),
		}
	}
	info, err := file.Stat()
	if err != nil {
		closeErr := file.Close()
		return nil, &apiclient.Error{
			Code:    "output_failed",
			Message: "The helper token file could not be inspected. The reserved artifact was preserved for manual inspection and removal.",
			Cause:   errors.Join(err, closeErr),
		}
	}
	return &reservedTokenFile{path: path, file: file, info: info}, nil
}

// write stores the token in the reserved file and flushes it to disk while the
// handle remains open for the post-write identity check.
func (reserved *reservedTokenFile) write(token string) error {
	if _, err := reserved.file.WriteString(token + "\n"); err != nil {
		return err
	}
	return reserved.file.Sync()
}

// replaced reports whether the path now names a different file than the one
// that was reserved.
func (reserved *reservedTokenFile) replaced() bool {
	pathInfo, err := os.Lstat(reserved.path)
	if err != nil {
		return true
	}
	return !os.SameFile(reserved.info, pathInfo)
}

// preserve closes the handle without unlinking a caller-selected pathname.
// Portable filesystems do not provide an atomic identity-checked unlink.
func (reserved *reservedTokenFile) preserve() error {
	if reserved == nil || reserved.file == nil {
		return nil
	}
	file := reserved.file
	reserved.file = nil
	return file.Close()
}

func printHelperCredentialUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit helper-credential <create|list|revoke> [options]")
	fmt.Fprintln(writer, "Credential management requires the current administrator password. The token is delivered only through --output and stored only as a hash.")
}
