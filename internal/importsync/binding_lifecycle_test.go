package importsync

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"

	"owngit/internal/importfetch"
	"owngit/internal/repository"
	"owngit/internal/state"
)

const testCAPEM = "-----BEGIN CERTIFICATE-----\nc3ludGhldGlj\n-----END CERTIFICATE-----\n"

// Saving one part of the credentials keeps the other stored part. Only an
// explicit clear removes both.
func TestSavingOneCredentialPartKeepsTheOther(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	ctx := context.Background()
	f.mustImport(ImportInput{Credentials: &Credentials{BearerToken: "token-one", RootCAPEM: []byte(testCAPEM)}})

	stored := func() state.ImportCredentials {
		t.Helper()
		credential, exists, err := f.store.LoadImportCredentials(ctx, "project")
		if err != nil || !exists {
			t.Fatalf("stored credential exists=%v err=%v", exists, err)
		}
		return credential
	}

	// A new token keeps the stored CA.
	noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "token-two"}))
	if credential := stored(); credential.BearerToken != "token-two" || string(credential.RootCAPEM) != testCAPEM {
		t.Fatalf("token rotation dropped the CA: token=%q ca=%q", credential.BearerToken, credential.RootCAPEM)
	}
	// A CA alone keeps the stored secret.
	otherCA := strings.Replace(testCAPEM, "c3ludGhldGlj", "b3RoZXI=", 1)
	noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{RootCAPEM: []byte(otherCA)}))
	if credential := stored(); credential.BearerToken != "token-two" || string(credential.RootCAPEM) != otherCA {
		t.Fatalf("CA change dropped the token: token=%q ca=%q", credential.BearerToken, credential.RootCAPEM)
	}
	// A Basic pair replaces the bearer token and keeps the CA.
	noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{Username: "user", Password: "secret"}))
	if credential := stored(); credential.BearerToken != "" || credential.Basic == nil || credential.Basic.Password != "secret" || string(credential.RootCAPEM) != otherCA {
		t.Fatalf("Basic change: %+v", credential)
	}
	status, err := f.service.Status(ctx, "project")
	noErr(t, err)
	if status.CredentialForm != "basic" || !status.CredentialBound || !status.CAPresent {
		t.Fatalf("status after merges=%+v", status)
	}
	// The refresh sends the merged credential.
	_, err = f.refresh()
	noErr(t, err, "refresh")
	last := f.transport.requests[len(f.transport.requests)-1]
	if last.Authentication.Basic == nil || last.Authentication.Basic.Password != "secret" || !bytes.Equal(last.RootCAPEM, []byte(otherCA)) {
		t.Fatalf("refresh request auth=%+v ca=%q", last.Authentication, last.RootCAPEM)
	}

	// Clear still removes everything.
	noErr(t, f.service.SetCredentials(ctx, "project", nil))
	if _, exists, err := f.store.LoadImportCredentials(ctx, "project"); err != nil || exists {
		t.Fatalf("clear left a credential exists=%v err=%v", exists, err)
	}

	// After a source change, material bound to the earlier source is not
	// carried into the new binding.
	noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "token-three", RootCAPEM: []byte(testCAPEM)}))
	if _, err := f.service.ConfigureSource(ctx, ConfigureInput{RepositoryID: "project", URL: "https://example.invalid/other/project.git"}); err != nil {
		t.Fatal(err)
	}
	noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{RootCAPEM: []byte(otherCA)}))
	if credential := stored(); credential.BearerToken != "" || credential.Basic != nil {
		t.Fatalf("a secret bound to the earlier source was carried over: %+v", credential)
	}
}

// A TLS failure is named instead of reported as a generic connection failure.
func TestTLSFailureIsNamed(t *testing.T) {
	connection := &importfetch.Error{Op: "request", Kind: importfetch.ErrConnection}
	tests := []struct {
		cause error
		want  string
	}{
		{x509.UnknownAuthorityError{}, "source TLS certificate could not be verified; if the source uses a private CA"},
		{x509.HostnameError{Host: "example.invalid"}, "source TLS certificate does not match"},
		{errors.New("connection reset by peer"), "source connection or response body failed"},
	}
	for _, test := range tests {
		problem := classifyFetchError(fmt.Errorf("%w: %w", connection, test.cause))
		if problem.Code != CodeNetwork || !strings.HasPrefix(problem.Message, test.want) {
			t.Errorf("cause %T classified as %s %q, want %q", test.cause, problem.Code, problem.Message, test.want)
		}
	}
}

// A failed first import leaves no source or secret behind, and a retry with
// the same name does not inherit them. The run history stays.
func TestFailedFirstImportForgetsItsBinding(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	ctx := context.Background()
	f.transport.fail = &importfetch.Error{Op: "request", Kind: importfetch.ErrHTTPStatus, StatusCode: 404}
	result, err := f.importProject(ImportInput{Credentials: &Credentials{BearerToken: "old-token", RootCAPEM: []byte(testCAPEM)}})
	if err == nil {
		t.Fatal("the failing import succeeded")
	}
	if result.Status.LastRun == nil || result.Status.LastRun.Status != state.ImportRunFailed {
		t.Fatalf("the result lost the failed run: %+v", result.Status.LastRun)
	}
	if _, exists, err := f.store.ImportSource(ctx, "project"); err != nil || exists {
		t.Fatalf("the failed import kept its source exists=%v err=%v", exists, err)
	}
	if _, exists, err := f.store.LoadImportCredentials(ctx, "project"); err != nil || exists {
		t.Fatalf("the failed import kept its secret exists=%v err=%v", exists, err)
	}
	if run := f.lastRun(); run.Status != state.ImportRunFailed {
		t.Fatalf("the failed run was not kept as history: %s", run.Status)
	}
	status, err := f.service.Status(ctx, "project")
	noErr(t, err)
	if status.Configured || status.CredentialBound || status.CAPresent {
		t.Fatalf("a later repository with this name would inherit: %+v", status)
	}

	f.transport.fail = nil
	f.mustImport(ImportInput{})
	if request := f.transport.requests[len(f.transport.requests)-1]; request.Authentication.BearerToken != "" || len(request.RootCAPEM) != 0 {
		t.Fatalf("the retry inherited the earlier credential: auth=%+v ca=%q", request.Authentication, request.RootCAPEM)
	}
}

// A binding left behind by an earlier version is not inherited by a new
// import that supplies no credentials.
func TestNewImportDoesNotInheritALeftoverCredential(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	ctx := context.Background()
	url := "https://example.invalid/team/project.git"
	source, err := f.store.ConfigureImportSource(ctx, state.ImportSourceInput{RepositoryID: "project", URL: url, Mode: "standalone", Now: f.now})
	noErr(t, err)
	_, err = f.store.SaveImportCredentials(ctx, state.ImportCredentials{
		RepositoryID: "project", URL: source.URL, SourceGeneration: source.SourceGeneration,
		ExpectedAuthorityRevision: source.AuthorityRevision, BearerToken: "leftover-token",
	}, f.now)
	noErr(t, err)

	f.mustImport(ImportInput{URL: url})
	if request := f.transport.requests[len(f.transport.requests)-1]; request.Authentication.BearerToken != "" {
		t.Fatalf("the new import sent a leftover credential: %+v", request.Authentication)
	}
	if _, exists, err := f.store.LoadImportCredentials(ctx, "project"); err != nil || exists {
		t.Fatalf("the leftover credential stayed exists=%v err=%v", exists, err)
	}
}

// A ref deleted at the source is reported as such, not as tracked.
func TestStatusMarksRefsDeletedAtTheSource(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "tag", "v1")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	f.git(f.source, "tag", "-d", "v1")
	f.git(f.source, "branch", "-D", "dev")
	_, err := f.refresh()
	noErr(t, err, "refresh")

	status, err := f.service.Status(context.Background(), "project")
	noErr(t, err)
	states := map[string]string{}
	for _, ref := range status.Refs {
		states[ref.Name] = ref.State
	}
	want := map[string]string{"refs/heads/main": "tracked", "refs/heads/dev": "deleted_at_source", "refs/tags/v1": "deleted_at_source"}
	for name, state := range want {
		if states[name] != state {
			t.Errorf("%s state=%q, want %q (all=%v)", name, states[name], state, states)
		}
	}
}

// bindOrphan stores a source and a bearer credential for name as a first
// import does before its run, without creating the repository.
func bindOrphan(t *testing.T, f *fixture, name string) state.ImportSource {
	t.Helper()
	ctx := context.Background()
	source, err := f.store.ConfigureImportSource(ctx, state.ImportSourceInput{
		RepositoryID: name, URL: "https://example.invalid/team/" + name + ".git", Mode: "standalone", Now: f.now,
	})
	noErr(t, err)
	source, err = f.store.SaveImportCredentials(ctx, state.ImportCredentials{
		RepositoryID: name, URL: source.URL, SourceGeneration: source.SourceGeneration,
		ExpectedAuthorityRevision: source.AuthorityRevision, BearerToken: "orphan-token", RootCAPEM: []byte(testCAPEM),
	}, f.now)
	noErr(t, err)
	return source
}

func assertNoBinding(t *testing.T, f *fixture, name string) {
	t.Helper()
	ctx := context.Background()
	if _, exists, err := f.store.ImportSource(ctx, name); err != nil || exists {
		t.Fatalf("%s kept its import source exists=%v err=%v", name, exists, err)
	}
	if _, exists, err := f.store.LoadImportCredentials(ctx, name); err != nil || exists {
		t.Fatalf("%s kept its credential exists=%v err=%v", name, exists, err)
	}
}

// A first import killed during its run leaves its binding and an active run
// row behind. The next start settles the run and removes the binding, so a
// repository created later with that name inherits nothing.
func TestRestartForgetsTheBindingOfAnInterruptedFirstImport(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	ctx := context.Background()
	source := bindOrphan(t, f, "project")
	// The state a killed process leaves: the run row is still active.
	killed := state.ImportRun{
		ID: strings.Repeat("7", 32), RepositoryID: "project", SourceGeneration: source.SourceGeneration,
		AuthorityRevision: source.AuthorityRevision, Kind: state.ImportKindInitial, Status: state.ImportRunFetching,
		StartedAt: f.now, CreatedAt: f.now,
	}
	noErr(t, f.store.BeginImportRun(ctx, killed))
	// A binding left by an earlier version without any run.
	bindOrphan(t, f, "legacy")
	// An existing repository keeps its binding.
	f.mustImport(ImportInput{Name: "kept", URL: "https://example.invalid/team/kept.git", Credentials: &Credentials{BearerToken: "kept-token"}})

	restartService(t, f)
	assertNoBinding(t, f, "project")
	assertNoBinding(t, f, "legacy")
	if _, exists, err := f.store.LoadImportCredentials(ctx, "kept"); err != nil || !exists {
		t.Fatalf("an existing repository lost its credential exists=%v err=%v", exists, err)
	}
	if run, exists, err := f.store.ImportRun(ctx, killed.ID); err != nil || !exists || run.Status != state.ImportRunInterrupted {
		t.Fatalf("the killed run was not kept as interrupted history: %+v exists=%v err=%v", run, exists, err)
	}

	if _, err := f.manager.Create(ctx, "project", ""); err != nil {
		t.Fatal(err)
	}
	status, err := f.service.Status(ctx, "project")
	noErr(t, err)
	if status.Configured || status.CredentialBound || status.CAPresent {
		t.Fatalf("the new repository inherited the interrupted import: %+v", status)
	}
	calls := f.transport.calls
	if _, err := f.refresh(); problemCode(err) != CodeNotConfigured || f.transport.calls != calls {
		t.Fatalf("refresh of the new repository err=%v calls=%d", err, f.transport.calls-calls)
	}
}

// Plain repository creation removes a leftover binding for its name, and
// refuses while an import for that name is still running.
func TestRepositoryCreationDoesNotInheritAnImportBinding(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bindOrphan(t, f, "leftover")
	if _, err := f.manager.Create(ctx, "leftover", ""); err != nil {
		t.Fatal(err)
	}
	assertNoBinding(t, f, "leftover")

	source := bindOrphan(t, f, "running")
	noErr(t, f.store.BeginImportRun(ctx, state.ImportRun{
		ID: strings.Repeat("8", 32), RepositoryID: "running", SourceGeneration: source.SourceGeneration,
		AuthorityRevision: source.AuthorityRevision, Kind: state.ImportKindInitial, Status: state.ImportRunFetching,
		StartedAt: f.now, CreatedAt: f.now,
	}))
	if _, err := f.manager.Create(ctx, "running", ""); !errors.Is(err, repository.ErrImportInProgress) || !errors.Is(err, repository.ErrNameTaken) {
		t.Fatalf("creation beside a running import err=%v", err)
	}
	if _, exists, err := f.store.LoadImportCredentials(ctx, "running"); err != nil || !exists {
		t.Fatalf("a running import lost its credential exists=%v err=%v", exists, err)
	}
}

// A binding for a name without a repository can be cleared on request.
func TestForgetOrphanImportClearsANameWithoutARepository(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if forgotten, err := f.service.ForgetOrphanImport(ctx, "orphan"); err != nil || forgotten {
		t.Fatalf("nothing stored forgotten=%v err=%v", forgotten, err)
	}
	bindOrphan(t, f, "orphan")
	if forgotten, err := f.service.ForgetOrphanImport(ctx, "orphan"); err != nil || !forgotten {
		t.Fatalf("orphan forgotten=%v err=%v", forgotten, err)
	}
	assertNoBinding(t, f, "orphan")
}
