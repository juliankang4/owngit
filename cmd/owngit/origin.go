package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// This file resolves where a command connects. Inside a clone, a missing
// --server or --repository is read from the clone's origin remote. A secret
// goes to an inferred server only when its file names that server on a first
// "owngit-server: ORIGIN" line, so a clone with a hostile origin never
// receives the password or helper credential. Explicit flags always win.

// originTarget is a server and repository taken from flags or the origin
// remote. The inferred fields say which parts came from the remote.
type originTarget struct {
	server             *url.URL
	repository         string
	inferredServer     bool
	inferredRepository bool
}

// resolveTarget validates an explicit --server and fills a missing --server
// or, when needRepository is set, a missing --repository from the origin
// remote of the clone that contains dir. Plain HTTP needs acceptInsecureHTTP
// either way.
func resolveTarget(ctx context.Context, flagServer, flagRepository string, needRepository, acceptInsecureHTTP bool, dir string) (originTarget, error) {
	target := originTarget{repository: flagRepository}
	if flagServer != "" {
		parsed, err := apiclient.ValidateServer(flagServer, acceptInsecureHTTP)
		if err != nil {
			return originTarget{}, err
		}
		target.server = parsed
		if !needRepository || flagRepository != "" {
			return target, nil
		}
	}
	remote, err := readOriginRemote(ctx, dir)
	if err != nil {
		return originTarget{}, err
	}
	if target.server == nil {
		parsed, err := apiclient.ValidateServer(remote.server, acceptInsecureHTTP)
		if err != nil {
			return originTarget{}, err
		}
		target.server, target.inferredServer = parsed, true
	} else if canonicalOrigin(target.server) != remote.server {
		return originTarget{}, cliProblem("origin_server_mismatch",
			"--server names another server than the origin remote, so the repository cannot be inferred. Pass --repository too.")
	}
	if needRepository && target.repository == "" {
		target.repository, target.inferredRepository = remote.repository, true
	}
	return target, nil
}

// inference describes what resolveTarget read from the origin remote, or
// returns "" when everything came from flags.
func (target originTarget) inference() string {
	switch {
	case target.inferredServer && target.inferredRepository:
		return fmt.Sprintf("owngit: using server %s and repository %s from the origin remote", canonicalOrigin(target.server), target.repository)
	case target.inferredServer:
		return fmt.Sprintf("owngit: using server %s from the origin remote", canonicalOrigin(target.server))
	case target.inferredRepository:
		return fmt.Sprintf("owngit: using repository %s from the origin remote", target.repository)
	default:
		return ""
	}
}

// noteInference tells the caller on stderr what was inferred, so stdout keeps
// only the JSON result.
func noteInference(target originTarget) {
	if line := target.inference(); line != "" {
		fmt.Fprintln(os.Stderr, line)
	}
}

type originRemote struct {
	server     string // canonical origin
	repository string
}

// readOriginRemote reads remote.origin.url from the repository configuration
// of the clone that contains dir. Git runs with empty global and system
// configuration and without inherited Git environment variables, so no user
// or system helper, include, or override takes part.
func readOriginRemote(ctx context.Context, dir string) (originRemote, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "config", "--local", "--no-includes", "--get-all", "remote.origin.url")
	command.Dir = dir
	command.Env = originGitEnvironment()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return originRemote{}, cliProblem("origin_unavailable",
				"This clone has no origin remote to supply the missing --server or --repository. Pass them explicitly.")
		}
		return originRemote{}, &apiclient.Error{Code: "origin_unavailable",
			Message: "No origin remote could be read here to supply the missing --server or --repository. Pass them explicitly, or run the command inside a clone of an OwnGit repository.",
			Cause:   fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))}
	}
	values := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	if len(values) != 1 {
		return originRemote{}, cliProblem("origin_ambiguous",
			"The origin remote has more than one URL, so the server and repository cannot be inferred. Pass --server and --repository.")
	}
	server, id, ok := parseCloneURL(strings.TrimSuffix(values[0], "\r"))
	if !ok {
		// The address is not repeated: it may carry credentials for
		// another host.
		return originRemote{}, cliProblem("origin_unsupported",
			"The origin remote is not an OwnGit clone address of the form http(s)://HOST[:PORT]/git/ID.git. Pass --server and --repository.")
	}
	return originRemote{server: server, repository: id}, nil
}

// originGitEnvironment is the environment for reading the origin remote:
// the program search path and, on Windows, the variables a process needs,
// with empty global and system Git configuration.
func originGitEnvironment() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"LANG=C",
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP"} {
			if value := os.Getenv(key); value != "" {
				env = append(env, key+"="+value)
			}
		}
	}
	return env
}

// parseCloneURL accepts exactly http(s)://HOST[:PORT]/git/ID.git, the address
// OwnGit shows for cloning, and returns the canonical origin and the
// repository ID. Credentials, queries, fragments, other paths, and other
// schemes (including scp-style and local paths such as C:\repo.git) are
// refused.
func parseCloneURL(raw string) (string, string, bool) {
	if raw == "" || strings.ContainsAny(raw, " \t\\") {
		return "", "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", "", false
	}
	name, found := strings.CutPrefix(parsed.Path, "/git/")
	if !found {
		return "", "", false
	}
	id, found := strings.CutSuffix(name, ".git")
	if !found || id == "" || strings.Contains(id, "/") || repository.ValidateID(id) != nil {
		return "", "", false
	}
	if _, err := apiclient.ValidateServer(parsed.Scheme+"://"+parsed.Host, true); err != nil {
		return "", "", false
	}
	return canonicalOrigin(parsed), id, true
}

// canonicalOrigin returns scheme://host[:port] with a lowercase host and
// without the scheme's default port, so equal origins compare equal.
func canonicalOrigin(server *url.URL) string {
	scheme := strings.ToLower(server.Scheme)
	host := strings.ToLower(server.Hostname())
	port := server.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}

// secretFile is a password or credential file. A file may start with the
// line "owngit-server: ORIGIN", which binds its secret to that server; the
// secret is then the last line. A file without that line is the original
// one-secret format. The kind of secret is implied by the flag that named
// the file.
type secretFile struct {
	secret string
	origin string // canonical origin from the server line, or ""
}

const credentialOriginPrefix = "owngit-server:"

// maximumOriginLineBytes bounds the server line of a secret file.
const maximumOriginLineBytes = 2048

// splitOriginLine separates the server line from the rest of a secret file.
// The line is parsed strictly: the exact prefix and one space at the start of
// the file, one origin that the server flags would accept, and nothing else.
// A first line that only resembles a server line (a byte order mark, leading
// blank lines or spaces, or another letter case) is refused instead of being
// read as a legacy secret, so a misspelled binding never sends the file.
func splitOriginLine(content []byte) (string, []byte, error) {
	if !bytes.HasPrefix(content, []byte(credentialOriginPrefix)) {
		if looksLikeOriginLine(content) {
			return "", nil, errOriginLine("The first line must start with \"owngit-server: \" exactly, at the very start of the file, in lowercase.")
		}
		return "", content, nil
	}
	line, rest, found := bytes.Cut(content, []byte("\n"))
	if !found {
		return "", nil, errOriginLine("The file names a server but holds no secret on the next line.")
	}
	value, ok := strings.CutPrefix(strings.TrimSuffix(string(line), "\r"), credentialOriginPrefix+" ")
	if !ok || value == "" || len(line) > maximumOriginLineBytes || strings.ContainsAny(value, " \t\r") {
		return "", nil, errOriginLine("The first line must be \"owngit-server: ORIGIN\" with exactly one HTTP(S) origin.")
	}
	parsed, err := apiclient.ValidateServer(value, true)
	if err != nil {
		return "", nil, errOriginLine("The first line must name an HTTP(S) origin without a path, query, credentials, or fragment.")
	}
	if len(bytes.TrimSpace(rest)) == 0 {
		return "", nil, errOriginLine("The file names a server but holds no secret on the next line.")
	}
	return canonicalOrigin(parsed), rest, nil
}

// looksLikeOriginLine reports whether the first non-blank line, after an
// optional UTF-8 byte order mark, starts with "owngit-server" in any case.
func looksLikeOriginLine(content []byte) bool {
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(content, []byte("\xef\xbb\xbf")), " \t\r\n")
	return len(trimmed) >= len("owngit-server") && bytes.EqualFold(trimmed[:len("owngit-server")], []byte("owngit-server"))
}

func errOriginLine(message string) error {
	return &apiclient.Error{Code: "invalid_credential_origin", Message: message}
}

// readPasswordFile reads a password file with owner-only permissions and
// the optional server line.
func readPasswordFile(path string) (secretFile, error) {
	if err := state.ValidatePrivateInputFile(path); err != nil {
		return secretFile{}, fmt.Errorf("inspect password file: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return secretFile{}, err
	}
	defer file.Close()
	// The longest accepted password plus an optional CRLF line ending.
	const maximumPasswordBytes = utf8.UTFMax*auth.MaximumPasswordCharacters + 2
	content, err := io.ReadAll(io.LimitReader(file, maximumOriginLineBytes+1+maximumPasswordBytes+1))
	if err != nil {
		return secretFile{}, err
	}
	origin, content, err := splitOriginLine(content)
	if err != nil {
		return secretFile{}, err
	}
	if len(content) > maximumPasswordBytes {
		return secretFile{}, errors.New("password file is too large")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	// A file holds exactly one secret line, so extra lines can never travel
	// with the password.
	if strings.ContainsAny(password, "\r\n") {
		if origin != "" {
			return secretFile{}, errOriginLine("After the server line, the file must hold only the password line.")
		}
		return secretFile{}, errors.New("password file must hold the password on one line")
	}
	if err := auth.ValidatePassword(password); err != nil {
		return secretFile{}, err
	}
	return secretFile{secret: password, origin: origin}, nil
}

// readTokenFile reads a helper or runner credential file with owner-only
// permissions and the optional server line.
func readTokenFile(path string) (secretFile, error) {
	if err := state.ValidatePrivateInputFile(path); err != nil {
		return secretFile{}, &apiclient.Error{Code: "invalid_credential_file", Message: secretFileMessage("The helper credential file", err), Cause: err}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return secretFile{}, &apiclient.Error{Code: "invalid_credential_file", Message: "The helper credential file could not be read.", Cause: err}
	}
	origin, content, err := splitOriginLine(content)
	if err != nil {
		return secretFile{}, err
	}
	token := strings.TrimSpace(string(content))
	if strings.ContainsAny(token, "\r\n") {
		if origin != "" {
			return secretFile{}, errOriginLine("After the server line, the file must hold only the credential line.")
		}
		return secretFile{}, &apiclient.Error{Code: "invalid_credential_file", Message: "The helper credential file must hold the credential on one line."}
	}
	if token == "" {
		return secretFile{}, &apiclient.Error{Code: "invalid_credential_file", Message: "The helper credential file is empty."}
	}
	return secretFile{secret: token, origin: origin}, nil
}

// secretFor returns the secret for a request to server. A file bound to
// another server is refused. When the server was inferred from the origin
// remote, the file must name that server, because the clone's configuration
// alone does not show that its owner trusts it.
func (file secretFile) secretFor(server *url.URL, inferred bool) (string, error) {
	origin := canonicalOrigin(server)
	if file.origin != "" && file.origin != origin {
		return "", cliProblem("credential_origin_mismatch",
			"The credential file is bound to "+file.origin+", so it was not sent to "+origin+".")
	}
	if inferred && file.origin == "" {
		return "", cliProblem("credential_origin_required",
			"The server "+origin+" was inferred from the origin remote, and the credential file does not name a server, so it was not sent. Pass --server, or add the line \"owngit-server: "+origin+"\" at the top of the file if you trust this server.")
	}
	return file.secret, nil
}

// privateFileGuide names the documentation that shows how to make a private
// password or token file.
const privateFileGuide = `"Password and token files" in docs/OPERATIONS.md`

// secretFileMessage describes a refused password or credential file named
// what, such as "The shared password file". A file that is not private gets
// what is wrong and a one-line fix; any other failure gets the general
// sentence. Neither includes the file's content.
func secretFileMessage(what string, err error) string {
	var notPrivate *state.NotPrivateError
	if errors.As(err, &notPrivate) {
		run := "To fix it, run: "
		if notPrivate.Shell != "" {
			run = "In " + notPrivate.Shell + ", run: "
		}
		return what + " is not private: " + notPrivate.Problem + ". " + run + notPrivate.Fix + " (see " + privateFileGuide + ")."
	}
	return what + " is unavailable or is not private."
}

// passwordFileProblem reports an unreadable password file named what, and
// keeps a malformed server line's own code.
func passwordFileProblem(err error, what string) error {
	var problem *apiclient.Error
	if errors.As(err, &problem) {
		return problem
	}
	return &apiclient.Error{Code: "invalid_password_file", Message: secretFileMessage(what, err), Cause: err}
}

// readServerPassword reads a password file named what, such as "The shared
// password file", for a request to server.
func readServerPassword(path string, server *url.URL, inferred bool, what string) (string, error) {
	file, err := readPasswordFile(path)
	if err != nil {
		return "", passwordFileProblem(err, what)
	}
	return file.secretFor(server, inferred)
}

// readServerToken reads a helper or runner credential file for a request to
// server.
func readServerToken(path string, server *url.URL, inferred bool) (string, error) {
	file, err := readTokenFile(path)
	if err != nil {
		return "", err
	}
	return file.secretFor(server, inferred)
}
