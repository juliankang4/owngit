package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	gitHandler, err := githttp.New(runner, manager, "", 1)
	noErr(t, err)
	fixture := &importCLIServer{}
	application := &server.App{
		Store: store, Auth: &auth.Manager{Store: store, SessionLife: time.Hour}, Repositories: manager,
		GitHTTP: gitHandler, Hosts: server.NewHostPolicy(),
		Imports: &importsync.Service{Store: store, Repositories: manager, Fetch: func(ctx context.Context, request importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
			fixture.token = request.Authentication.BearerToken
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &importfetch.Result{Advertisement: &importgit.Advertisement{Empty: true, ObjectFormat: importgit.FormatSHA1}}, nil
		}},
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
