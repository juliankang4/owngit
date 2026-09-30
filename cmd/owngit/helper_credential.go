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
		printHelperCredentialUsage(os.Stderr)
		return cliProblem("invalid_arguments", "helper-credential requires create, list, or revoke.")
	}
	if isHelpArgument(arguments[0]) {
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
	return admin.serverClient()
}

// serverClient is client for a command that --repository only narrows.
func (admin *helperAdminFlags) serverClient() (*apiclient.Client, error) {
	if admin.server == "" || admin.passwordFile == "" {
		return nil, cliProblem("invalid_arguments", "--server and --password-file are required.")
	}
	parsed, err := apiclient.ValidateServer(admin.server, admin.acceptInsecureHTTP)
	if err != nil {
		return nil, err
	}
	password, err := readServerPassword(admin.passwordFile, parsed, false, "The administrator password file")
	if err != nil {
		return nil, err
	}
	return apiclient.NewAdmin(parsed, password), nil
}

func (admin *helperAdminFlags) repositoryPath() string {
	return "/api/v1/repositories/" + url.PathEscape(admin.repository)
}

// helperCredentialCreate issues one credential and delivers its token through
// an exclusively created private file.
func helperCredentialCreate(arguments []string) error {
	flags := newCommandFlagSet("helper-credential create")
	admin := addHelperAdminFlags(flags)
	label := flags.String("label", "", "credential label")
	output := flags.String("output", "", "owner-readable file that receives the token")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	if *output == "" {
		return cliProblem("invalid_arguments", "helper-credential create requires --output. The token is delivered only through that file.")
	}
	client, err := admin.client()
	if err != nil {
		return err
	}
	server, err := url.Parse(admin.server)
	if err != nil {
		return err
	}
	origin := canonicalOrigin(server)
	creationID, err := state.RandomID()
	if err != nil {
		return err
	}
	reserved, err := reservePrivateTokenFile(*output)
	if err != nil {
		return err
	}
	// Each run generates its own creation identity, so a plain retry is new.
	issuance := credentialIssuance{client: client, credentialsPath: admin.repositoryPath() + "/helper-credentials",
		creationID: creationID, command: "owngit helper-credential", idFlag: "--id"}
	fail := func(cause error) error {
		closeErr := reserved.preserve()
		return preservedOutputError(issuance.compensate(cause), closeErr)
	}

	content, err := client.Do(context.Background(), "POST", issuance.credentialsPath,
		checkapi.CreateCredentialInput{Label: *label, CreationID: creationID})
	if err != nil {
		return fail(err)
	}
	var response checkapi.CredentialResponse
	if err := json.Unmarshal(content, &response); err != nil || response.Credential == nil {
		return fail(cliProblem("invalid_response", "The server did not return a helper credential."))
	}
	if response.Credential.RepositoryID != admin.repository || response.Credential.CreationID != creationID {
		return fail(cliProblem("mismatched_response", "The server returned a credential for another repository or creation identity."))
	}
	if response.Token == "" {
		return preservedOutputError(issuance.replayed(response.Credential.ID, response.Credential.RevokedAt != nil), reserved.preserve())
	}
	if reserved.replaced() {
		return fail(outputReplaced())
	}
	// The first line binds the token to the server that issued it, so a
	// command that infers the server from a clone sends it only there.
	if err := reserved.write(credentialOriginPrefix + " " + origin + "\n" + response.Token); err != nil {
		return fail(&apiclient.Error{Code: "token_delivery_failed", Message: "The token could not be written.", Cause: err})
	}
	if reserved.replaced() {
		return fail(outputReplaced())
	}
	if err := reserved.preserve(); err != nil {
		return preservedOutputError(issuance.compensate(
			&apiclient.Error{Code: "token_delivery_failed", Message: "The token file could not be closed.", Cause: err}), err)
	}
	if reserved.replaced() {
		return preservedOutputError(issuance.compensate(outputReplaced()), nil)
	}
	// The token is delivered only through the file, so stdout never carries it.
	response.Token = ""
	return writeJSONValue(struct {
		checkapi.CredentialResponse
		// TokenFileServer is the server named on the token file's first line.
		TokenFileServer string `json:"token_file_server"`
	}{response, origin})
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

// outputReplaced reports a token output path that another writer replaced
// while a credential was issued.
func outputReplaced() error {
	return cliProblem("output_replaced", "The output path was replaced while the credential was created. The replacement was left untouched.")
}

// credentialIssuance is one helper or runner credential issuance, as its
// failure reports need it.
type credentialIssuance struct {
	client          *apiclient.Client
	credentialsPath string // the helper or runner credentials collection
	creationID      string
	// chosenCreationID is set when the caller supplied the creation identity.
	// That identity keeps naming its credential after a revoke, so a retry
	// needs a new one.
	chosenCreationID bool
	command          string // the command that lists and revokes these credentials
	idFlag           string // its revoke option that names a credential
}

// retry tells the owner how to issue again after nothing usable was issued.
func (issuance credentialIssuance) retry() string {
	if issuance.chosenCreationID {
		return "Run the command again with a new --creation-id, or without it."
	}
	return "Retry the command."
}

// findAndRevoke tells the owner how to revoke the credential the creation
// identity may name. No command revokes by creation identity, but the list
// shows each credential's creation_id and revoked_at.
func (issuance credentialIssuance) findAndRevoke() string {
	return "Run " + issuance.command + " list, and if a credential with creation_id " + issuance.creationID +
		" is listed without revoked_at, revoke it with " + issuance.command + " revoke " + issuance.idFlag + " <id>."
}

// replayed reports a creation replay: the creation identity already names a
// credential, and the server does not disclose its token again. The request
// created nothing, so nothing is revoked. An active credential may belong to
// an owner who holds its token from the earlier creation, so it is left for
// the owner to revoke. A revoked one cannot issue again under this identity.
func (issuance credentialIssuance) replayed(credentialID string, revoked bool) error {
	message := "Creation " + issuance.creationID + " already names credential " + credentialID
	if revoked {
		message += ", which is revoked, so it cannot issue a new token. " + issuance.retry()
	} else {
		message += ", and its token is not shown again. If you do not hold that token, revoke the credential with " +
			issuance.command + " revoke " + issuance.idFlag + " " + credentialID + "."
	}
	return cliProblem("token_unavailable", message)
}

// compensate reports a failed issuance and revokes the credential this
// attempt may have created.
//
// Only an attempt that may have created a credential is compensated. An
// OwnGit refusal (a 4xx error object, including a creation conflict) settles
// that nothing was created, so it is reported as received and nothing is
// revoked. This also keeps a reused creation identity from revoking the
// earlier credential it names. The revoke is scoped by the creation identity
// sent with the request, so it never trusts a returned credential identifier.
//
// The result states why creation failed and then what compensation achieved.
// A confirmed revoke keeps the failure's code and tells the owner how to
// retry, since the server did not refuse the request and nothing from it
// remains. An unconfirmed revoke becomes credential_creation_unconfirmed and
// tells the owner how to find and revoke the credential by its non-secret
// creation identity before retrying.
func (issuance credentialIssuance) compensate(cause error) error {
	if definiteRefusal(cause) {
		return cause
	}
	failure := apiclient.Error{Code: "credential_creation_failed", Message: "Credential creation failed."}
	var problem *apiclient.Error
	if errors.As(cause, &problem) {
		failure = *problem
	}
	failure.Cause = cause
	if revokeErr := revokeCredentialByCreation(issuance.client, issuance.credentialsPath, issuance.creationID); revokeErr != nil {
		failure.Code = "credential_creation_unconfirmed"
		failure.Message += " The creation outcome is unconfirmed and the compensating revoke failed. " + issuance.findAndRevoke() + " " + issuance.retry()
		failure.Cause = errors.Join(cause, revokeErr)
		return &failure
	}
	failure.Message += " Any credential this attempt created was revoked. " + issuance.retry()
	return &failure
}

func helperCredentialList(arguments []string) error {
	flags := newCommandFlagSet("helper-credential list")
	admin := addHelperAdminFlags(flags)
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
		return err
	}
	client, err := admin.serverClient()
	if err != nil {
		return err
	}
	// Without --repository it lists every repository's credentials, as
	// the dashboard's Coding tools page does.
	path := "/api/v1/helper-credentials"
	if admin.repository != "" {
		path = admin.repositoryPath() + "/helper-credentials"
	}
	content, err := client.Do(context.Background(), "GET", path, nil)
	if err != nil {
		return err
	}
	return writeJSON(content)
}

func helperCredentialRevoke(arguments []string) error {
	flags := newCommandFlagSet("helper-credential revoke")
	admin := addHelperAdminFlags(flags)
	id := flags.String("id", "", "credential identifier")
	if err := parseFlagsWithoutOperands(flags, arguments); err != nil {
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

func revokeCredentialByCreation(client *apiclient.Client, credentialsPath, creationID string) error {
	_, err := client.Do(context.Background(), "DELETE", credentialsPath+"/by-creation/"+url.PathEscape(creationID), nil)
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
	fmt.Fprintln(writer, "list without --repository lists the credentials of every repository.")
}
