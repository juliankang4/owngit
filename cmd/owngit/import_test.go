package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/importsync"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
)

func TestImportCLIListsHelpAndKeepsSecretsOutOfOutput(t *testing.T) {
	output, err := captureStdout(func() error { return importCommand([]string{"help"}) })
	if err != nil || !strings.Contains(output, "import add") || !strings.Contains(output, "import credentials") {
		t.Fatalf("import help output=%q err=%v", output, err)
	}
	usage, err := captureStdout(func() error { return run([]string{"help"}) })
	if err != nil || !strings.Contains(usage, "import") {
		t.Fatalf("top-level help output=%q err=%v", usage, err)
	}
	if err := importCommand([]string{"credentials", "project", "--token", "secret"}); err == nil {
		t.Fatal("token argument was accepted")
	}

	root := t.TempDir()
	tokenPath := filepath.Join(root, "token")
	const token = "cli-import-secret-token"
	noErr(t, os.WriteFile(tokenPath, []byte(token+"\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(tokenPath, false))
	passwordPath := filepath.Join(root, "admin")
	noErr(t, os.WriteFile(passwordPath, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(passwordPath, false))
	fixture := startImportCLIServer(t)
	printed, err := captureStdout(func() error {
		return importCommand([]string{
			"credentials", fixture.repositoryID, "--server", fixture.url, "--accept-insecure-http",
			"--password-file", passwordPath, "--token-file", tokenPath,
		})
	})
	noErr(t, err)
	if strings.Contains(printed, token) || !strings.Contains(printed, "bearer") {
		t.Fatalf("credential output=%q", printed)
	}
}

func TestImportAddSendsTokenFileWithoutPrintingIt(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "token")
	const token = "add-secret-token"
	noErr(t, os.WriteFile(tokenPath, []byte(token+"\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(tokenPath, false))
	passwordPath := filepath.Join(root, "admin")
	noErr(t, os.WriteFile(passwordPath, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(passwordPath, false))
	fixture := startImportCLIServer(t)
	printed, err := captureStdout(func() error {
		return importCommand([]string{
			"add", "private", "https://example.invalid/team/private.git",
			"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath, "--token-file", tokenPath,
		})
	})
	noErr(t, err)
	if fixture.token != token || strings.Contains(printed, token) {
		t.Fatalf("add credential token=%q output=%q", fixture.token, printed)
	}
}

type importCLIServer struct {
	url          string
	repositoryID string
	token        string
	service      *importsync.Service
	store        *state.Store
}

// startImportCLIServer serves one configured repository. An optional
// fetchDelay makes the stub source slow; the run context still ends it.
func startImportCLIServer(t *testing.T, fetchDelay ...time.Duration) *importCLIServer {
	t.Helper()
	var delay time.Duration
	if len(fetchDelay) > 0 {
		delay = fetchDelay[0]
	}
	ctx := context.Background()
	root := t.TempDir()
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, true))
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "project", "")
	noErr(t, err)
	gitHandler, err := githttp.New(runner, manager, "")
	noErr(t, err)
	fixture := &importCLIServer{}
	hosts := server.NewHostPolicy()
	application := &server.App{
		Store: store, Auth: &auth.Manager{Store: store}, Repositories: manager,
		GitHTTP: gitHandler, Hosts: hosts,
		Imports: &importsync.Service{Store: store, Repositories: manager, Fetch: func(ctx context.Context, request importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
			fixture.token = request.Authentication.BearerToken
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &importfetch.Result{Advertisement: &importgit.Advertisement{Empty: true, ObjectFormat: importgit.FormatSHA1}}, nil
		}},
		Network: server.NewLiveNetwork(server.LiveNetworkConfig{Hosts: hosts}),
	}
	// The service lease keeps its marker open; Windows cannot remove the
	// temporary directory until it is released.
	t.Cleanup(func() { _ = application.Imports.Close() })
	if _, err := application.Imports.ConfigureSource(ctx, importsync.ConfigureInput{
		RepositoryID: stored.ID, URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(application.Handler())
	t.Cleanup(httpServer.Close)
	fixture.url = httpServer.URL
	fixture.repositoryID = stored.ID
	fixture.service = application.Imports
	fixture.store = store
	return fixture
}

// A CLI refresh whose server-side run takes longer than an ordinary API
// request limit still completes, and the CLI reports the outcome.
func TestImportRefreshOutlivesTheOrdinaryClientLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("the run must outlast the 35 second ordinary client limit")
	}
	root := t.TempDir()
	passwordPath := filepath.Join(root, "admin")
	noErr(t, os.WriteFile(passwordPath, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(passwordPath, false))
	fixture := startImportCLIServer(t, apiclient.DefaultTimeout+2*time.Second)
	started := time.Now()
	printed, err := captureStdout(func() error {
		return importCommand([]string{
			"refresh", fixture.repositoryID, "--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath,
		})
	})
	elapsed := time.Since(started)
	if err != nil || !strings.Contains(printed, "finished: complete") {
		t.Fatalf("slow refresh after %s output=%q err=%v", elapsed, printed, err)
	}
	if elapsed < apiclient.DefaultTimeout {
		t.Fatalf("refresh finished after %s, before the ordinary limit it must outlast", elapsed)
	}
}

func writePrivateTestFile(t *testing.T, path, content string) string {
	t.Helper()
	noErr(t, os.WriteFile(path, []byte(content), 0o600))
	noErr(t, state.ProtectPrivatePath(path, false))
	return path
}

// The CLI stores a CA-only trust anchor larger than an ordinary API request,
// within the one stored CA bound, and clears credentials, all without
// secrets in arguments.
func TestImportCredentialsCAOnlyAndClear(t *testing.T) {
	root := t.TempDir()
	passwordPath := writePrivateTestFile(t, filepath.Join(root, "admin"), "admin-password\n")
	fixture := startImportCLIServer(t)
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath}
	line := strings.Repeat("A", 64) + "\n"
	caPEM := "-----BEGIN CERTIFICATE-----\n" + strings.Repeat(line, (200<<10)/len(line)) + "-----END CERTIFICATE-----\n"
	caPath := filepath.Join(root, "ca.pem")
	noErr(t, os.WriteFile(caPath, []byte(caPEM), 0o600))
	if _, err := captureStdout(func() error {
		return importCommand(append([]string{"credentials", fixture.repositoryID, "--ca-file", caPath}, remote...))
	}); err != nil {
		t.Fatalf("CA-only credentials: %v", err)
	}
	status, err := fixture.service.Status(context.Background(), fixture.repositoryID)
	if err != nil || !status.CAPresent || status.CredentialForm != "none" {
		t.Fatalf("CA-only status ca=%v form=%q err=%v", status.CAPresent, status.CredentialForm, err)
	}

	oversized := filepath.Join(root, "oversized.pem")
	noErr(t, os.WriteFile(oversized, make([]byte, state.MaxImportCABytes+1), 0o600))
	if err := importCommand(append([]string{"credentials", fixture.repositoryID, "--ca-file", oversized}, remote...)); err == nil {
		t.Fatal("a CA file above the stored bound was accepted")
	}
	if err := importCommand(append([]string{"credentials", fixture.repositoryID, "--clear", "--ca-file", caPath}, remote...)); err == nil {
		t.Fatal("--clear with a credential file was accepted")
	}

	if _, err := captureStdout(func() error {
		return importCommand(append([]string{"credentials", fixture.repositoryID, "--clear"}, remote...))
	}); err != nil {
		t.Fatalf("clear credentials: %v", err)
	}
	status, err = fixture.service.Status(context.Background(), fixture.repositoryID)
	if err != nil || status.CAPresent || status.CredentialBound || (status.CredentialForm != "" && status.CredentialForm != "none") {
		t.Fatalf("cleared status ca=%v bound=%v form=%q err=%v", status.CAPresent, status.CredentialBound, status.CredentialForm, err)
	}
}

// A basic credential file written with CRLF line endings yields the same
// username and password as one written with LF.
func TestBasicCredentialFileAcceptsCRLF(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"lf":   "import-user\nimport-password\n",
		"crlf": "import-user\r\nimport-password\r\n",
	} {
		path := writePrivateTestFile(t, filepath.Join(root, name), content)
		form, username, password, token, err := readImportCredential("", path)
		if err != nil || form != "basic" || username != "import-user" || password != "import-password" || token != "" {
			t.Fatalf("%s basic file form=%q username=%q password length=%d err=%v", name, form, username, len(password), err)
		}
	}
}

// import resolve accepts the repository as found after an unresolved
// publication, through the same owner authentication as the other commands.
func TestImportResolveAcceptsTheDestination(t *testing.T) {
	root := t.TempDir()
	passwordPath := writePrivateTestFile(t, filepath.Join(root, "admin"), "admin-password\n")
	fixture := startImportCLIServer(t)
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath}
	if err := importCommand(append([]string{"resolve", fixture.repositoryID}, remote...)); err == nil || !strings.Contains(err.Error(), "no unresolved publication") {
		t.Fatalf("resolve without an unresolved publication err=%v", err)
	}

	ctx := context.Background()
	now := time.Now().UTC()
	source, exists, err := fixture.store.ImportSource(ctx, fixture.repositoryID)
	if err != nil || !exists {
		t.Fatalf("source exists=%v err=%v", exists, err)
	}
	run := state.ImportRun{
		ID: strings.Repeat("1", 32), RepositoryID: fixture.repositoryID, SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
		Kind: state.ImportKindRefresh, Status: state.ImportRunPublishing, StartedAt: now, CreatedAt: now,
	}
	noErr(t, fixture.store.BeginImportRun(ctx, run))
	desired := strings.Repeat("a", 40)
	intent := state.ImportIntent{
		ID: strings.Repeat("2", 32), RepositoryID: fixture.repositoryID, RunID: run.ID,
		SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision, Status: state.ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": "", state.ImportHeadRef: "symbolic refs/heads/main "},
		Desired:  map[string]string{"refs/heads/main": desired, state.ImportHeadRef: "symbolic refs/heads/main " + desired},
		Observed: map[string]string{"refs/heads/main": desired, state.ImportHeadRef: "symbolic refs/heads/main " + desired},
		Retained: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}
	noErr(t, fixture.store.CreateImportIntent(ctx, intent))
	noErr(t, fixture.store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentUnresolved, "", "", "synthetic mixed outcome", now))
	run.Status, run.ErrorClass, run.FinishedAt = state.ImportRunUnresolved, importsync.CodeUnresolved, now
	noErr(t, fixture.store.FinishImportRun(ctx, run))
	status, err := captureStdout(func() error {
		return importCommand(append([]string{"status", fixture.repositoryID}, remote...))
	})
	if err != nil || !strings.Contains(status, "owngit import resolve "+fixture.repositoryID) {
		t.Fatalf("status output=%q err=%v", status, err)
	}
	printed, err := captureStdout(func() error {
		return importCommand(append([]string{"resolve", fixture.repositoryID}, remote...))
	})
	if err != nil || !strings.Contains(printed, "1 unresolved publication intent") {
		t.Fatalf("resolve output=%q err=%v", printed, err)
	}
	stored, _, err := fixture.store.ImportIntent(ctx, intent.ID)
	if err != nil || stored.Status != state.ImportIntentOwnerResolved {
		t.Fatalf("resolved intent status=%s err=%v", stored.Status, err)
	}
}

// A committed change succeeds even when the server could not read the status
// after it. The CLI then says what was saved, warns, and names the read-only
// status command instead of suggesting that nothing happened.
func TestCommittedImportChangeWarnsWhenStatusIsUnavailable(t *testing.T) {
	const unavailable = `"status_error":{"code":"state_unavailable","message":"import schedule could not be read"}}`
	const warning = "Warning: the import status after the change could not be read (import schedule could not be read). Check it with owngit import status project.\n"
	resolved := `{"ok":true,"resolved":["` + strings.Repeat("2", 32) + `"],`
	resolvedLine := "Accepted the current state of project for 1 unresolved publication intent(s). No ref was changed; the next refresh plans from the repository as it is.\n"
	passwordPath := writePrivateTestFile(t, filepath.Join(t.TempDir(), "admin"), "admin-password\n")
	for _, test := range []struct {
		name, method, path string
		arguments          []string
		response           string
		printed, warning   string
	}{
		{"resolve with status", http.MethodPost, "/resolve", []string{"resolve", "project"},
			resolved + `"status":{"configured":true,"unresolved_intents":0}}`, resolvedLine, ""},
		{"resolve without status", http.MethodPost, "/resolve", []string{"resolve", "project"},
			resolved + `"status":null,` + unavailable, resolvedLine, warning},
		{"credentials with status", http.MethodDelete, "/credentials", []string{"credentials", "project", "--clear"},
			`{"ok":true,"credential_form":"none","credential_bound":false}`, "Credential none, bound: no.\n", ""},
		{"credentials without status", http.MethodDelete, "/credentials", []string{"credentials", "project", "--clear"},
			`{"ok":true,"credential_form":null,"credential_bound":null,` + unavailable, "The credential change for project was saved.\n", warning},
		{"refresh with status", http.MethodPost, "/run", []string{"refresh", "project"},
			`{"ok":true,"run":{"status":"complete","refs_divergent":0},"status":{"refs":[]}}`, "Import for project finished: complete.\n", ""},
		{"refresh without status", http.MethodPost, "/run", []string{"refresh", "project"},
			`{"ok":true,"run":{"status":"complete","refs_divergent":0},"status":null,` + unavailable, "Import for project finished: complete.\n", warning},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != test.method || request.URL.Path != "/api/v1/repositories/project/import"+test.path {
					http.NotFound(writer, request)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.response))
			}))
			defer server.Close()
			var printed string
			warned, err := captureStderr(func() error {
				var runErr error
				printed, runErr = captureStdout(func() error {
					return importCommand(append(test.arguments, "--server", server.URL, "--accept-insecure-http", "--password-file", passwordPath))
				})
				return runErr
			})
			if err != nil || printed != test.printed || warned != test.warning {
				t.Fatalf("output=%q warning=%q err=%v", printed, warned, err)
			}
			// With --json the answer, status_error included, is the result
			// on standard output, and nothing else is printed.
			warned, err = captureStderr(func() error {
				var runErr error
				printed, runErr = captureStdout(func() error {
					return importCommand(append(test.arguments, "--json", "--server", server.URL, "--accept-insecure-http", "--password-file", passwordPath))
				})
				return runErr
			})
			if err != nil || !sameJSON(printed, test.response) || warned != "" {
				t.Fatalf("JSON output=%q warning=%q err=%v", printed, warned, err)
			}
		})
	}
}

// sameJSON reports whether two JSON texts hold the same value.
func sameJSON(got, want string) bool {
	var gotValue, wantValue any
	return json.Unmarshal([]byte(got), &gotValue) == nil && json.Unmarshal([]byte(want), &wantValue) == nil && reflect.DeepEqual(gotValue, wantValue)
}

// Each import command prints the server's answer with --json, the same facts
// its text states, and a refusal stays a coded error.
func TestImportCommandsPrintJSON(t *testing.T) {
	root := t.TempDir()
	passwordPath := writePrivateTestFile(t, filepath.Join(root, "admin"), "admin-password\n")
	tokenPath := writePrivateTestFile(t, filepath.Join(root, "token"), "json-secret-token\n")
	fixture := startImportCLIServer(t)
	remote := []string{"--json", "--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath}
	runJSON := func(arguments ...string) (string, map[string]any) {
		t.Helper()
		printed, err := captureStdout(func() error { return importCommand(append(arguments, remote...)) })
		var answer map[string]any
		if err != nil || json.Unmarshal([]byte(printed), &answer) != nil || answer["ok"] != true {
			t.Fatalf("%v output=%q err=%v", arguments, printed, err)
		}
		return printed, answer
	}

	_, added := runJSON("add", "private", "https://example.invalid/team/private.git", "--token-file", tokenPath)
	if run, _ := added["run"].(map[string]any); run["status"] != "complete" || added["repository_id"] != "private" {
		t.Fatalf("add answer %v", added)
	}
	_, refreshed := runJSON("refresh", fixture.repositoryID)
	if run, _ := refreshed["run"].(map[string]any); run["status"] != "complete" {
		t.Fatalf("refresh answer %v", refreshed)
	}
	_, history := runJSON("history", fixture.repositoryID)
	if runs, _ := history["runs"].([]any); len(runs) != 1 {
		t.Fatalf("history answer %v", history)
	}
	_, enabled := runJSON("schedule", fixture.repositoryID, "--enable", "--interval", "2h")
	if enabled["enabled"] != true || enabled["interval_seconds"] != float64(7200) {
		t.Fatalf("schedule answer %v", enabled)
	}
	if _, disabled := runJSON("schedule", fixture.repositoryID, "--disable"); disabled["enabled"] != false {
		t.Fatalf("schedule disable answer %v", disabled)
	}
	if _, cancelled := runJSON("cancel", fixture.repositoryID); cancelled["cancelled"] != false {
		t.Fatalf("cancel answer %v", cancelled)
	}
	printed, stored := runJSON("credentials", fixture.repositoryID, "--token-file", tokenPath)
	if stored["credential_form"] != "bearer" || strings.Contains(printed, "json-secret-token") {
		t.Fatalf("credentials answer %q", printed)
	}
	if _, cleared := runJSON("credentials", fixture.repositoryID, "--clear"); cleared["credential_form"] != "none" || cleared["credential_bound"] != false {
		t.Fatalf("credentials clear answer %v", cleared)
	}

	printed, err := captureStdout(func() error { return importCommand(append([]string{"resolve", fixture.repositoryID}, remote...)) })
	var reported strings.Builder
	if printed != "" || reportError(&reported, err) != 1 || !strings.Contains(reported.String(), `"ok":false`) || !strings.Contains(reported.String(), "no unresolved publication") {
		t.Fatalf("refused resolve output=%q reported=%q err=%v", printed, reported.String(), err)
	}
}

// With --json a run still ends with the exit status of its outcome.
func TestImportRunJSONKeepsTheExitStatus(t *testing.T) {
	for _, test := range []struct {
		answer string
		code   int
	}{
		{`{"ok":true,"code":"cancelled","run":{"status":"cancelled"},"status":null}`, importCancelledExit},
		{`{"ok":true,"run":{"status":"complete","refs_divergent":1},"status":{"refs":[{"name":"refs/heads/main","state":"diverged"}]}}`, importDivergedExit},
		{`{"ok":true,"run":{"status":"complete","refs_divergent":0},"status":{"refs":[]}}`, 0},
	} {
		output, err := captureStdout(func() error { return printImportRun("project", []byte(test.answer), true) })
		code := 0
		if err != nil {
			code = reportError(io.Discard, err)
		}
		if code != test.code || !sameJSON(output, test.answer) {
			t.Fatalf("answer %s output=%q exit=%d err=%v", test.answer, output, code, err)
		}
	}
}

// A finished run still exits with the divergence status when the names of
// its differing refs could not all be read: without a status, with a
// truncated ref list, or with local refs that could not be read. The output
// says so instead of describing the rest as refs without a listable name.
func TestImportRunSaysWhenRefNamesCouldNotBeRead(t *testing.T) {
	const head = "Import for project finished: complete.\n2 refs differ from the source and were left unchanged here:\n"
	const unread = " not named here because the ref names could not be read in full. List them with owngit import status project.\n"
	for _, test := range []struct {
		name, status, want string
	}{
		{"no status", `null,"status_error":{"code":"state_unavailable","message":"import schedule could not be read"}`, head + "  2" + unread},
		{"truncated", `{"refs":[{"name":"refs/heads/main","state":"diverged"}],"refs_truncated":true}`, head + "  refs/heads/main\n  1" + unread},
		{"local refs unread", `{"refs":[{"name":"refs/heads/main","state":"unknown_local"},{"name":"refs/tags/v1","state":"diverged"}]}`, head + "  refs/tags/v1\n  1" + unread},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := captureStdout(func() error {
				return printImportRun("project", []byte(`{"ok":true,"run":{"status":"complete","refs_divergent":2},"status":`+test.status+`}`), false)
			})
			var exit *checkExit
			if !errors.As(err, &exit) || exit.code != importDivergedExit || output != test.want {
				t.Fatalf("run output=%q err=%v", output, err)
			}
		})
	}
}

// A cancelled run used to print its outcome and exit 0, as if it had finished.
func TestCancelledImportRunExitsWithTheCancelledStatus(t *testing.T) {
	output, err := captureStdout(func() error {
		return printImportRun("project", []byte(`{"ok":false,"code":"cancelled","run":{"status":"cancelled"}}`), false)
	})
	var exit *checkExit
	if !errors.As(err, &exit) || exit.code != importCancelledExit || output != "Import for project was cancelled.\n" {
		t.Fatalf("cancelled run output=%q err=%v", output, err)
	}
}

// A cancel that arrives after publication cannot stop the import. The run
// completes, and the output says why the cancellation had no effect.
func TestLateCancellationOfAPublishedImportIsExplained(t *testing.T) {
	output, err := captureStdout(func() error {
		return printImportRun("project", []byte(`{"ok":true,"run":{"status":"complete","refs_divergent":0,"cancel_requested_at":"2026-01-01T00:00:00Z"},"status":{"refs":[]}}`), false)
	})
	if err != nil || output != "Import for project finished: complete.\nThe cancellation arrived after the import was published, so it did not stop it.\n" {
		t.Fatalf("late cancel output=%q err=%v", output, err)
	}
}

// A credential file that cannot be used is reported as a structured error that
// names the problem, like every other import error, instead of a log line.
func TestUnusableImportCredentialFileIsAStructuredError(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing-token")
	_, _, _, _, err := readImportCredential(missing, "")
	var problem *apiclient.Error
	if !errors.As(err, &problem) || problem.Code != "invalid_credential_file" || !strings.Contains(problem.Message, "missing-token") {
		t.Fatalf("missing credential file err=%v", err)
	}
	var written strings.Builder
	if !writeStructuredCommandError(&written, err) || !strings.Contains(written.String(), `"code":"invalid_credential_file"`) {
		t.Fatalf("missing credential file printed %q", written.String())
	}
	if runtime.GOOS == "windows" {
		return
	}
	shared := filepath.Join(directory, "shared-token")
	noErr(t, os.WriteFile(shared, []byte("secret-value\n"), 0o644))
	noErr(t, os.Chmod(shared, 0o644))
	_, _, _, _, err = readImportCredential(shared, "")
	if !errors.As(err, &problem) || problem.Code != "invalid_credential_file" || strings.Contains(problem.Message, "secret-value") {
		t.Fatalf("shared credential file err=%v", err)
	}
}

// A finished run that kept refs differing from the source names them and
// exits with the distinct divergence status; import status lists them too.
func TestImportOutputReportsRefsThatDifferFromTheSource(t *testing.T) {
	runResult := `{"ok":true,"run":{"status":"complete","refs_divergent":2,"refs_deleted_upstream":1},
		"status":{"refs":[{"name":"refs/heads/main","state":"diverged"},{"name":"refs/tags/v1","state":"deleted_at_source"},{"name":"refs/tags/v2","state":"tracked"}]}}`
	output, err := captureStdout(func() error { return printImportRun("project", []byte(runResult), false) })
	var exit *checkExit
	if !errors.As(err, &exit) || exit.code != importDivergedExit {
		t.Fatalf("divergent run err=%v", err)
	}
	for _, want := range []string{"finished: complete", "1 ref was deleted at the source", "2 refs differ from the source", "  refs/heads/main\n", "1 more not listed by name"} {
		if !strings.Contains(output, want) {
			t.Errorf("run output lacks %q: %q", want, output)
		}
	}
	clean := `{"ok":true,"run":{"status":"complete","refs_divergent":0},"status":{"refs":[]}}`
	if output, err := captureStdout(func() error { return printImportRun("project", []byte(clean), false) }); err != nil || output != "Import for project finished: complete.\n" {
		t.Fatalf("clean run output=%q err=%v", output, err)
	}

	passwordPath := writePrivateTestFile(t, filepath.Join(t.TempDir(), "admin"), "admin-password\n")
	for _, test := range []struct {
		name, status string
		want, absent []string
	}{
		{"configured", `{"configured":true,"url":"https://example.invalid/team/project.git","mode":"standalone","credential_form":"none",
			"last_run":{"kind":"refresh","status":"complete","refs_divergent":1},"active_run":{"kind":"refresh","status":"fetching"},
			"refs":[{"name":"refs/heads/main","state":"diverged"},{"name":"refs/tags/v1","state":"deleted_at_source"},{"name":"refs/tags/v2","state":"tracked"}]}`,
			[]string{"Active run: refresh, fetching", "Last run: refresh, complete, 1 ref differs from the source",
				"Refs that do not match the source: 2", "refs/heads/main: differs from the source", "refs/tags/v1: deleted at the source"},
			[]string{"refs/tags/v2"}},
		{"cancelled without source", `{"configured":false,"last_run":{"kind":"initial","status":"cancelled"},"staging_issues":1}`,
			[]string{"Import for project is not configured.\n", "Scheduler: not running\n", "Staging issues: 1\n", "Last run: initial, cancelled\n"},
			[]string{"URL:", "Mode:", "Credential:", "Active run:"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"ok":true,"status":` + test.status + `}`))
			}))
			defer server.Close()
			output, err := captureStdout(func() error {
				return importCommand([]string{"status", "project", "--server", server.URL, "--accept-insecure-http", "--password-file", passwordPath})
			})
			noErr(t, err)
			for _, want := range test.want {
				if !strings.Contains(output, want) {
					t.Errorf("status output lacks %q: %q", want, output)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(output, absent) {
					t.Errorf("status output contains %q: %q", absent, output)
				}
			}
		})
	}
}
