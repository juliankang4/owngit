package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"owngit/internal/state"
)

func TestParseCloneURLAcceptsOnlyOwnGitCloneAddresses(t *testing.T) {
	for _, test := range []struct {
		raw, server, id string
	}{
		{"http://127.0.0.1:7654/git/project.git", "http://127.0.0.1:7654", "project"},
		{"https://OwnGit.Example.test/git/a.b-c_d.git", "https://owngit.example.test", "a.b-c_d"},
		{"https://owngit.example.test:443/git/x.git", "https://owngit.example.test", "x"},
		{"http://owngit.example.test:80/git/x.git", "http://owngit.example.test", "x"},
		{"HTTP://owngit.example.test:8080/git/x.git", "http://owngit.example.test:8080", "x"},
		{"http://[::1]:7654/git/x.git", "http://[::1]:7654", "x"},
		{"http://[::1]/git/x.git", "http://[::1]", "x"},
	} {
		server, id, ok := parseCloneURL(test.raw)
		if !ok || server != test.server || id != test.id {
			t.Errorf("parseCloneURL(%q)=%q,%q,%v, want %q,%q", test.raw, server, id, ok, test.server, test.id)
		}
	}
	for _, raw := range []string{
		"", "git@owngit.example.test:project.git", "ssh://owngit.example.test/git/project.git",
		"file:///C:/repos/project.git", `C:\repos\project.git`, "C:/repos/git/project.git", `\\server\share\git\project.git`,
		"/git/project.git", "../project.git", "http://user:secret@owngit.example.test/git/project.git",
		"http://owngit.example.test/git/project.git?x=1", "http://owngit.example.test/git/project.git?",
		"http://owngit.example.test/git/project.git#top", "http://owngit.example.test/git/project",
		"http://owngit.example.test/git/Project.git", "http://owngit.example.test/prefix/git/project.git",
		"http://owngit.example.test/git/a/b.git", "http://owngit.example.test/git/.git", "http://owngit.example.test/git/project.git/",
		"http://owngit.example.test/git/%70roject.git", "http://owngit.example.test:port/git/project.git",
		"http://owngit.example.test/git/con.git", "http:///git/project.git", "http://owngit.example.test/git/project.git.git",
		"http://owngit.example.test/git/project.git extra",
	} {
		if server, id, ok := parseCloneURL(raw); ok {
			t.Errorf("parseCloneURL(%q) accepted %q,%q", raw, server, id)
		}
	}
}

func TestSecretFileServerLineIsParsedStrictly(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		noErr(t, os.WriteFile(path, []byte(content), 0o600))
		noErr(t, state.ProtectPrivatePath(path, false))
		return path
	}
	for _, test := range []struct {
		name, content, secret, origin string
	}{
		{"legacy", "shared-password\n", "shared-password", ""},
		{"legacy without newline", "shared-password", "shared-password", ""},
		{"bound", "owngit-server: http://127.0.0.1:7654\nshared-password\n", "shared-password", "http://127.0.0.1:7654"},
		{"bound CRLF", "owngit-server: HTTPS://Host.Example:443\r\nshared-password\r\n", "shared-password", "https://host.example"},
	} {
		file, err := readPasswordFile(write(test.name, test.content))
		if err != nil || file.secret != test.secret || file.origin != test.origin {
			t.Errorf("%s: file=%+v err=%v", test.name, file, err)
		}
	}
	for _, test := range []struct{ name, content string }{
		{"no space", "owngit-server:http://127.0.0.1:7654\nshared-password\n"},
		{"two spaces", "owngit-server:  http://127.0.0.1:7654\nshared-password\n"},
		{"trailing junk", "owngit-server: http://127.0.0.1:7654 extra\nshared-password\n"},
		{"two origins", "owngit-server: http://a.example,http://b.example\nshared-password\n"},
		{"path", "owngit-server: http://127.0.0.1:7654/git\nshared-password\n"},
		{"credentials", "owngit-server: http://user@127.0.0.1:7654\nshared-password\n"},
		{"other scheme", "owngit-server: ftp://127.0.0.1\nshared-password\n"},
		{"no secret", "owngit-server: http://127.0.0.1:7654\n"},
		{"server line only", "owngit-server: http://127.0.0.1:7654"},
		{"two secret lines", "owngit-server: http://127.0.0.1:7654\nshared-password\nsecond-line\n"},
		{"byte order mark", "\xef\xbb\xbfowngit-server: http://127.0.0.1:7654\nshared-password\n"},
		{"leading blank line", "\nowngit-server: http://127.0.0.1:7654\nshared-password\n"},
		{"leading space", " owngit-server: http://127.0.0.1:7654\nshared-password\n"},
		{"other case", "Owngit-Server: http://127.0.0.1:7654\nshared-password\n"},
		{"space before colon", "owngit-server : http://127.0.0.1:7654\nshared-password\n"},
	} {
		_, err := readPasswordFile(write(test.name, test.content))
		if got := commandErrorCode(err); got != "invalid_credential_origin" {
			t.Errorf("%s: err=%v code=%q, want invalid_credential_origin", test.name, err, got)
		}
	}
	// A legacy file holds one secret line; anything more is refused.
	for _, content := range []string{"shared-password\nsecond-line\n", "\nshared-password\n", "shared-password\r\nsecond-line"} {
		if _, err := readPasswordFile(write("legacy lines", content)); err == nil {
			t.Errorf("legacy password file %q was accepted", content)
		}
	}
	if _, err := readTokenFile(write("legacy token lines", "synthetic-token\nsecond-line\n")); commandErrorCode(err) != "invalid_credential_file" {
		t.Errorf("legacy token file with two lines err=%v", err)
	}
	if _, err := readTokenFile(write("token BOM", "\xef\xbb\xbfowngit-server: http://127.0.0.1:7654\nsynthetic-token\n")); commandErrorCode(err) != "invalid_credential_origin" {
		t.Errorf("token file with a byte order mark err=%v", err)
	}
	token, err := readTokenFile(write("token", "owngit-server: http://127.0.0.1:7654\nsynthetic-token\n"))
	if err != nil || token.secret != "synthetic-token" || token.origin != "http://127.0.0.1:7654" {
		t.Fatalf("bound token=%+v err=%v", token, err)
	}
	if _, err := readTokenFile(write("token lines", "owngit-server: http://127.0.0.1:7654\nsynthetic-token\nmore\n")); commandErrorCode(err) != "invalid_credential_origin" {
		t.Fatalf("token with two secret lines err=%v", err)
	}
	if _, err := readTokenFile(write("token empty", "owngit-server: http://127.0.0.1:7654\n\n")); commandErrorCode(err) != "invalid_credential_origin" {
		t.Fatalf("bound empty token err=%v", err)
	}
}

// fakeOwnGit answers every request with an empty pull request list and
// records whether any request carried credentials.
type fakeOwnGit struct {
	server      *httptest.Server
	requests    atomic.Int32
	credentials atomic.Int32
}

func newFakeOwnGit(t *testing.T) *fakeOwnGit {
	t.Helper()
	fake := &fakeOwnGit{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.requests.Add(1)
		if request.Header.Get("Authorization") != "" {
			fake.credentials.Add(1)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true,"pull_requests":[]}`))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// newClone makes a Git working tree whose origin remote is each of origins.
func newClone(t *testing.T, origins ...string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "clone")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	for _, origin := range origins {
		runPRGit(t, work, "config", "--add", "remote.origin.url", origin)
	}
	return work
}

func writePrivate(t *testing.T, path, content string) string {
	t.Helper()
	noErr(t, os.WriteFile(path, []byte(content), 0o600))
	noErr(t, state.ProtectPrivatePath(path, false))
	return path
}

func TestOriginInferenceFindsTheServerAndRepository(t *testing.T) {
	serverURL, _ := startRepositoryCLIServer(t, "")
	_, err := captureStdout(func() error {
		return repoCommand([]string{"create", "--name", "project", "--server", serverURL, "--accept-insecure-http"})
	})
	noErr(t, err)
	t.Chdir(newClone(t, serverURL+"/git/project.git"))

	var stdout string
	stderr, err := captureStderr(func() error {
		var err error
		stdout, err = captureStdout(func() error { return prCommand([]string{"list", "--accept-insecure-http"}) })
		return err
	})
	noErr(t, err)
	if stdout != "{\"ok\":true,\"pull_requests\":[]}\n" {
		t.Fatalf("inferred pr list stdout=%q", stdout)
	}
	if want := "owngit: using server " + serverURL + " and repository project from the origin remote\n"; stderr != want {
		t.Fatalf("inference note=%q, want %q", stderr, want)
	}
	var shown struct {
		Repository repositoryCLIItem `json:"repository"`
	}
	runRepoCommandJSON(t, []string{"show", "--accept-insecure-http"}, &shown)
	if shown.Repository.ID != "project" {
		t.Fatalf("inferred repo show=%+v", shown)
	}
	// Plain HTTP still needs the explicit acknowledgement.
	if err := prCommand([]string{"list"}); commandErrorCode(err) != "insecure_http_confirmation_required" {
		t.Fatalf("inferred HTTP without acknowledgement err=%v", err)
	}
	// An explicit repository wins over the inferred one, and the server is
	// still inferred.
	if err := prCommand([]string{"list", "--accept-insecure-http", "--repository", "missing"}); commandErrorCode(err) != "repository_not_found" {
		t.Fatalf("explicit repository err=%v", err)
	}
	// --server naming the same origin in another spelling infers only the
	// repository; another server cannot pick a repository.
	parsed, err := url.Parse(serverURL)
	noErr(t, err)
	sameServer := "HTTP://" + parsed.Host
	if _, err := captureStdout(func() error { return prCommand([]string{"list", "--accept-insecure-http", "--server", sameServer}) }); err != nil {
		t.Fatalf("same server in another spelling err=%v", err)
	}
	if err := prCommand([]string{"list", "--accept-insecure-http", "--server", "http://other.example.test"}); commandErrorCode(err) != "origin_server_mismatch" {
		t.Fatalf("other server without repository err=%v", err)
	}
}

func TestOriginInferenceRefusesMissingAmbiguousAndForeignRemotes(t *testing.T) {
	for _, test := range []struct {
		name    string
		origins []string
		code    string
	}{
		{"no origin", nil, "origin_unavailable"},
		{"two origin URLs", []string{"http://127.0.0.1:7654/git/a.git", "http://127.0.0.1:7654/git/b.git"}, "origin_ambiguous"},
		{"another host shape", []string{"https://example.test/owner/project.git"}, "origin_unsupported"},
		{"scp style", []string{"git@example.test:owner/project.git"}, "origin_unsupported"},
		{"Windows drive path", []string{`C:\Users\dev\repos\project.git`}, "origin_unsupported"},
		{"Windows forward-slash path", []string{"C:/Users/dev/git/project.git"}, "origin_unsupported"},
		{"Windows file URL", []string{"file:///C:/Users/dev/git/project.git"}, "origin_unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(newClone(t, test.origins...))
			for _, run := range []func() error{
				func() error { return prCommand([]string{"list", "--accept-insecure-http"}) },
				func() error { return repoCommand([]string{"show", "--accept-insecure-http"}) },
			} {
				if err := run(); commandErrorCode(err) != test.code {
					t.Fatalf("err=%v code=%q, want %s", err, commandErrorCode(err), test.code)
				}
			}
		})
	}
	t.Run("outside a clone", func(t *testing.T) {
		dir := t.TempDir()
		if exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil {
			t.Skip("the temporary directory is inside a Git working tree")
		}
		t.Chdir(dir)
		if err := prCommand([]string{"list"}); commandErrorCode(err) != "origin_unavailable" {
			t.Fatalf("err=%v", err)
		}
	})
}

// Inherited Git environment does not redirect the origin lookup to another
// repository.
func TestOriginInferenceIgnoresInheritedGitEnvironment(t *testing.T) {
	fake := newFakeOwnGit(t)
	other := newClone(t, "http://other.example.test/git/elsewhere.git")
	t.Chdir(newClone(t, fake.server.URL+"/git/project.git"))
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	stderr, err := captureStderr(func() error {
		_, err := captureStdout(func() error { return prCommand([]string{"list", "--accept-insecure-http"}) })
		return err
	})
	noErr(t, err)
	if !strings.Contains(stderr, "repository project ") || fake.requests.Load() != 1 {
		t.Fatalf("stderr=%q requests=%d", stderr, fake.requests.Load())
	}
}

// A clone whose origin names a server the owner never approved does not
// receive the shared password or the helper credential.
func TestCredentialsDoNotFollowAnUntrustedOrigin(t *testing.T) {
	hostile := newFakeOwnGit(t)
	dir := t.TempDir()
	legacyPassword := writePrivate(t, filepath.Join(dir, "legacy-password"), "shared-password\n")
	boundPassword := writePrivate(t, filepath.Join(dir, "bound-password"), "owngit-server: https://owngit.example.test\nshared-password\n")
	legacyToken := writePrivate(t, filepath.Join(dir, "legacy-token"), "synthetic-token\n")
	boundToken := writePrivate(t, filepath.Join(dir, "bound-token"), "owngit-server: https://owngit.example.test\nsynthetic-token\n")
	clone := newClone(t, hostile.server.URL+"/git/project.git")
	t.Chdir(clone)

	for _, test := range []struct {
		name string
		run  func() error
		code string
	}{
		{"pr with a legacy password", func() error {
			return prCommand([]string{"list", "--accept-insecure-http", "--password-file", legacyPassword})
		}, "credential_origin_required"},
		{"pr with a password bound elsewhere", func() error {
			return prCommand([]string{"list", "--accept-insecure-http", "--password-file", boundPassword})
		}, "credential_origin_mismatch"},
		{"repo list with a legacy password", func() error {
			return repoCommand([]string{"list", "--accept-insecure-http", "--password-file", legacyPassword})
		}, "credential_origin_required"},
		{"check status with a legacy token", func() error {
			return checkCommand([]string{"status", "--task", "task", "--accept-insecure-http", "--credential-file", legacyToken})
		}, "credential_origin_required"},
		{"check run with a token bound elsewhere", func() error {
			return checkCommand([]string{"run", "--task", "task", "--workdir", clone, "--check", "marker=echo ran > ran-marker", "--accept-insecure-http", "--credential-file", boundToken})
		}, "credential_origin_mismatch"},
		// An explicit --server keeps a bound file to its own server.
		{"explicit server with a password bound elsewhere", func() error {
			return prCommand([]string{"list", "--server", hostile.server.URL, "--repository", "project", "--accept-insecure-http", "--password-file", boundPassword})
		}, "credential_origin_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); commandErrorCode(err) != test.code {
				t.Fatalf("err=%v code=%q, want %s", err, commandErrorCode(err), test.code)
			}
		})
	}
	if hostile.requests.Load() != 0 {
		t.Fatalf("the hostile origin received %d requests", hostile.requests.Load())
	}
	if markerExists(t, clone, "ran-marker") {
		t.Fatal("a check ran although the credential was refused")
	}
	// An explicit --server with a legacy file keeps the earlier behavior.
	if _, err := captureStdout(func() error {
		return prCommand([]string{"list", "--server", hostile.server.URL, "--repository", "project", "--accept-insecure-http", "--password-file", legacyPassword})
	}); err != nil || hostile.credentials.Load() != 1 {
		t.Fatalf("explicit server err=%v credentialed requests=%d", err, hostile.credentials.Load())
	}
}

// A helper credential file written by helper-credential create names the
// server that issued it, so inference inside a clone of that server uses it,
// and a bound shared password file works the same way.
func TestBoundCredentialFilesWorkWithInference(t *testing.T) {
	remoteFlags, taskID, work := startCheckCLIServer(t)
	serverURL := remoteFlags[1]
	passwordFile := writePrivate(t, filepath.Join(t.TempDir(), "admin-password"), "admin-password\n")
	issued := filepath.Join(t.TempDir(), "issued-token")
	output, err := captureStdout(func() error {
		return helperCredentialCommand([]string{"create", "--label", "bound", "--output", issued,
			"--server", serverURL, "--accept-insecure-http", "--repository", "project", "--password-file", passwordFile})
	})
	noErr(t, err)
	var created struct {
		OK              bool   `json:"ok"`
		Token           string `json:"token"`
		TokenFileServer string `json:"token_file_server"`
	}
	noErr(t, json.Unmarshal([]byte(output), &created))
	if !created.OK || created.Token != "" || created.TokenFileServer != serverURL {
		t.Fatalf("create output=%s", output)
	}
	content, err := os.ReadFile(issued)
	noErr(t, err)
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != 2 || lines[0] != "owngit-server: "+serverURL || lines[1] == "" {
		t.Fatalf("issued file has %d lines, first=%q", len(lines), lines[0])
	}

	runPRGit(t, work, "remote", "add", "origin", serverURL+"/git/project.git")
	t.Chdir(work)
	status, err := captureStdout(func() error {
		return checkCommand([]string{"status", "--task", taskID, "--accept-insecure-http", "--credential-file", issued})
	})
	noErr(t, err)
	if !strings.Contains(status, `"ok":true`) {
		t.Fatalf("inferred check status=%s", status)
	}
	run, err := captureStdout(func() error {
		return checkCommand([]string{"run", "--task", taskID, "--check", "pass=exit 0", "--accept-insecure-http", "--credential-file", issued})
	})
	noErr(t, err)
	var result checkRunOutput
	noErr(t, json.Unmarshal([]byte(run), &result))
	if !result.OK || !result.Uploaded {
		t.Fatalf("inferred check run=%s", run)
	}
	// The same file with an explicit --server for that server keeps working.
	if _, err := captureStdout(func() error {
		return checkCommand([]string{"status", "--task", taskID, "--server", serverURL, "--repository", "project", "--accept-insecure-http", "--credential-file", issued})
	}); err != nil {
		t.Fatalf("explicit server with the bound file err=%v", err)
	}
}

func TestBoundSharedPasswordWorksWithInference(t *testing.T) {
	serverURL, _ := startRepositoryCLIServer(t, "shared-password")
	bound := writePrivate(t, filepath.Join(t.TempDir(), "password"), "owngit-server: "+serverURL+"\nshared-password\n")
	_, err := captureStdout(func() error {
		return repoCommand([]string{"create", "--name", "project", "--server", serverURL, "--accept-insecure-http", "--password-file", bound})
	})
	noErr(t, err)
	t.Chdir(newClone(t, serverURL+"/git/project.git"))
	output, err := captureStdout(func() error {
		return prCommand([]string{"list", "--accept-insecure-http", "--password-file", bound})
	})
	noErr(t, err)
	if output != "{\"ok\":true,\"pull_requests\":[]}\n" {
		t.Fatalf("inferred pr list with a bound password=%q", output)
	}
	if _, err := resolveTarget(context.Background(), "", "", true, true, "."); err != nil {
		t.Fatalf("resolve inside the clone: %v", err)
	}
}

// A file whose first line only resembles a server line, or a legacy file with
// more than one line, is refused before any request, even with an explicit
// --server naming another server.
func TestNearMissServerLinesSendNothing(t *testing.T) {
	fake := newFakeOwnGit(t)
	dir := t.TempDir()
	files := map[string]string{
		"byte order mark":    "\xef\xbb\xbfowngit-server: http://127.0.0.1:7811\nsynthetic-password-1\n",
		"leading blank line": "\nowngit-server: http://127.0.0.1:7811\nsynthetic-password-1\n",
		"other case":         "Owngit-Server: http://127.0.0.1:7811\nsynthetic-password-1\n",
		"legacy two lines":   "synthetic-password-1\nsecond-line\n",
	}
	for name, content := range files {
		path := writePrivate(t, filepath.Join(dir, strings.ReplaceAll(name, " ", "-")), content)
		for _, run := range []func() error{
			func() error {
				return prCommand([]string{"list", "--server", fake.server.URL, "--repository", "demo", "--accept-insecure-http", "--password-file", path})
			},
			func() error {
				return helperCredentialCommand([]string{"list", "--server", fake.server.URL, "--repository", "demo", "--accept-insecure-http", "--password-file", path})
			},
			func() error {
				return checkCommand([]string{"status", "--task", "task", "--server", fake.server.URL, "--repository", "demo", "--accept-insecure-http", "--credential-file", path})
			},
		} {
			if _, err := captureStdout(run); err == nil {
				t.Errorf("%s: a command accepted the file", name)
			}
		}
	}
	if got := fake.requests.Load(); got != 0 {
		t.Fatalf("the other server received %d requests", got)
	}
}
