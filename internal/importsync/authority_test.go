package importsync

import (
	"context"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestRefreshReplacesExactlyOwnedTagAndRetainsTagObject(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("one", "one\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "first upstream annotation", commit)
	firstTag := f.git(f.source, "rev-parse", "refs/tags/v1")
	f.mustImport(ImportInput{})

	f.git(f.source, "tag", "-f", "-a", "v1", "-m", "second upstream annotation", commit)
	secondTag := f.git(f.source, "rev-parse", "refs/tags/v1")
	require(t, secondTag != firstTag, "test did not replace the tag object")
	run, err := f.refresh()
	noErr(t, err, "refresh")
	require(t, run.RefsUpdated == 1 && run.RefsDivergent == 0, "refresh run=%+v", run)
	refs := f.destinationRefs()
	require(t, refs["refs/tags/v1"] == secondTag, "destination tag=%s want %s", refs["refs/tags/v1"], secondTag)
	require(t, refs[repository.RetainedRefName("tags", firstTag)] == firstTag,
		"replaced tag object was not retained: %v", sortedKeys(refs))
	require(t, refs[repository.ProvenanceRefName("tags", "v1", firstTag)] == firstTag,
		"tag provenance was not retained: %v", sortedKeys(refs))
}

func TestRefreshRetainsReplacedTagChainObject(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("one", "one\n")
	f.git(f.source, "tag", "-a", "base", "-m", "base annotation", commit)
	f.git(f.source, "tag", "-a", "release", "-m", "outer annotation", "refs/tags/base")
	firstOuter := f.git(f.source, "rev-parse", "refs/tags/release")
	f.mustImport(ImportInput{})

	f.git(f.source, "tag", "-f", "-a", "release", "-m", "replacement outer annotation", "refs/tags/base")
	replacement := f.git(f.source, "rev-parse", "refs/tags/release")
	require(t, replacement != firstOuter, "test did not replace the outer tag object")
	_, err := f.refresh()
	noErr(t, err, "refresh")
	refs := f.destinationRefs()
	require(t, refs["refs/tags/release"] == replacement &&
		refs[repository.RetainedRefName("tags", firstOuter)] == firstOuter,
		"tag chain replacement was not exact and retained: %v", sortedKeys(refs))
}

func TestNewSourceGenerationCannotInheritBranchAuthorityFromAncestry(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/other/project.git",
	}); err != nil {
		t.Fatal(err)
	}
	second := f.commit("two", "two\n")
	run, err := f.refresh()
	noErr(t, err, "refresh")
	require(t, run.RefsDivergent == 1 && run.RefsUpdated == 0, "new source inherited branch authority: %+v", run)
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == first && got != second, "destination branch=%s first=%s second=%s", got, first, second)
	secondRun, err := f.refresh()
	noErr(t, err, "second refresh")
	require(t, secondRun.RefsDivergent == 1 && secondRun.RefsUpdated == 0 &&
		f.destinationRefs()["refs/heads/main"] == first,
		"recording the new source observation manufactured authority: run=%+v", secondRun)
}

func TestAuthorityChangesCancelPinnedRuns(t *testing.T) {
	tests := []struct {
		name        string
		credentials *Credentials
		mutate      func(*fixture) error
	}{
		{
			name: "mode",
			mutate: func(f *fixture) error {
				_, err := f.service.ConfigureSource(context.Background(), ConfigureInput{RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ModeCoexistence})
				return err
			},
		},
		{
			name: "private network consent",
			mutate: func(f *fixture) error {
				_, err := f.service.ConfigureSource(context.Background(), ConfigureInput{RepositoryID: "project", URL: "https://example.invalid/team/project.git", AllowPrivateNetwork: true})
				return err
			},
		},
		{
			name: "Git-only consent",
			mutate: func(f *fixture) error {
				_, err := f.service.ConfigureSource(context.Background(), ConfigureInput{RepositoryID: "project", URL: "https://example.invalid/team/project.git", GitOnlyConsent: true})
				return err
			},
		},
		{
			name:        "credential replacement",
			credentials: &Credentials{Username: "user", Password: "old-secret"},
			mutate: func(f *fixture) error {
				return f.service.SetCredentials(context.Background(), "project", &Credentials{Username: "user", Password: "replacement-secret"})
			},
		},
		{
			name:        "custom CA replacement",
			credentials: &Credentials{Username: "user", Password: "old-secret"},
			mutate: func(f *fixture) error {
				return f.service.SetCredentials(context.Background(), "project", &Credentials{Username: "user", Password: "old-secret", RootCAPEM: []byte("PRIVATE CA BYTES")})
			},
		},
		{
			name:        "credential revocation",
			credentials: &Credentials{Username: "user", Password: "old-secret"},
			mutate: func(f *fixture) error {
				return f.service.SetCredentials(context.Background(), "project", nil)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			first := f.commit("one", "one\n")
			f.mustImport(ImportInput{Credentials: test.credentials})
			second := f.commit("two", "two\n")
			f.transport.before = func() {
				f.transport.before = nil
				if err := test.mutate(f); err != nil {
					t.Errorf("mutate authority: %v", err)
				}
			}
			run, err := f.refresh()
			require(t, err != nil && problemCode(err) == CodeSuperseded, "refresh err=%v", err)
			require(t, run.Status == state.ImportRunSuperseded && run.CancelRequestedAt != nil, "run=%+v", run)
			got := f.destinationRefs()["refs/heads/main"]
			require(t, got == first && got != second,
				"stale authority published branch=%s first=%s second=%s", got, first, second)
			for _, secret := range []string{"old-secret", "replacement-secret", "PRIVATE CA BYTES"} {
				require(t, !strings.Contains(err.Error(), secret), "error exposed secret material: %v", err)
			}
		})
	}
}

func TestIdenticalCredentialsDoNotRevokePinnedRun(t *testing.T) {
	f := newFixture(t)
	credential := &Credentials{Username: "user", Password: "same-secret"}
	f.commit("one", "one\n")
	f.mustImport(ImportInput{Credentials: credential})
	before, _, err := f.store.ImportSource(context.Background(), "project")
	noErr(t, err)
	second := f.commit("two", "two\n")
	f.transport.before = func() {
		f.transport.before = nil
		if err := f.service.SetCredentials(context.Background(), "project", credential); err != nil {
			t.Errorf("repeat credentials: %v", err)
		}
	}
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "refresh run=%+v err=%v", run, err)
	after, _, err := f.store.ImportSource(context.Background(), "project")
	require(t, err == nil && after.AuthorityRevision == before.AuthorityRevision &&
		after.CredentialGeneration == before.CredentialGeneration,
		"identical credential changed authority: before=%+v after=%+v err=%v", before, after, err)
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == second, "identical credential blocked refresh: got %s want %s", got, second)
}

// A credential change whose authority write fails must still stop a run that
// is already fetching, for a replacement and a revocation alike, and also when
// recording the cancellation fails too.
func TestCredentialFailureStopsRunWhenDurableCancellationFails(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		replacement *Credentials
		cancelFails bool
	}{
		{"replace", &Credentials{BearerToken: "synthetic-replacement-token"}, false},
		{"revoke", nil, false},
		{"replace without durable cancellation", &Credentials{BearerToken: "synthetic-replacement-token"}, true},
		{"revoke without durable cancellation", nil, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.commit("initial", "initial\n")
			f.mustImport(ImportInput{})
			noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "synthetic-initial-token"}))
			before := f.destinationRefs()["refs/heads/main"]
			f.commit("new source tip", "changed\n")
			started, done, run, runErr := f.gatedRefresh(t)
			<-started
			active, exists, err := f.store.ActiveImportRun(ctx, "project")
			require(t, err == nil && exists, "the gated refresh is not active: exists=%v err=%v", exists, err)
			released := false
			defer func() {
				if !released {
					f.transport.gate <- struct{}{}
					<-done
				}
			}()
			noErr(t, f.store.Exec(ctx,
				`CREATE TRIGGER fail_authority_write BEFORE UPDATE OF authority_revision ON import_sources BEGIN SELECT RAISE(FAIL,'synthetic authority write failure'); END`))
			if testCase.cancelFails {
				noErr(t, f.store.Exec(ctx,
					`CREATE TRIGGER fail_cancel_write BEFORE UPDATE OF cancel_requested_at ON import_runs WHEN NEW.cancel_requested_at IS NOT OLD.cancel_requested_at BEGIN SELECT RAISE(FAIL,'synthetic cancel write failure'); END`))
			}
			err = f.service.SetCredentials(ctx, "project", testCase.replacement)
			require(t, err != nil, "credential mutation unexpectedly succeeded")
			if testCase.cancelFails {
				// The failed change still stops the run in memory, so the run
				// may already have ended: its record is read whatever its status.
				persisted, exists, err := f.store.ImportRun(ctx, active.ID)
				require(t, err == nil && exists && persisted.CancelRequestedAt == nil,
					"cancel persistence failure was not preserved: exists=%v cancel=%v err=%v", exists, persisted.CancelRequestedAt, err)
				noErr(t, f.store.Exec(ctx, `DROP TRIGGER fail_cancel_write`))
			}
			noErr(t, f.store.Exec(ctx, `DROP TRIGGER fail_authority_write`))
			f.transport.gate <- struct{}{}
			<-done
			released = true
			require(t, *runErr != nil && run.Status != state.ImportRunComplete,
				"credential failure allowed stale completion: status=%s err=%v", run.Status, *runErr)
			after := f.destinationRefs()["refs/heads/main"]
			require(t, after == before, "credential failure allowed ref publication: before=%s after=%s", before, after)
		})
	}
}

func TestUnchangedConfigurationDoesNotRevokePinnedRun(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	before, _, err := f.store.ImportSource(context.Background(), "project")
	noErr(t, err)
	second := f.commit("two", "two\n")
	f.transport.before = func() {
		f.transport.before = nil
		after, err := f.service.ConfigureSource(context.Background(), ConfigureInput{RepositoryID: "project", URL: before.URL, Mode: Mode(before.Mode)})
		if err != nil {
			t.Errorf("repeat configuration: %v", err)
		}
		if after.AuthorityRevision != before.AuthorityRevision {
			t.Errorf("authority revision changed from %d to %d", before.AuthorityRevision, after.AuthorityRevision)
		}
	}
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "refresh run=%+v err=%v", run, err)
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == second, "unchanged configuration blocked refresh: got %s want %s", got, second)
}

func TestFinalPublicationRejectsDirectAuthorityChange(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	second := f.commit("two", "two\n")
	f.service.beforeFinalAuthorityCheck = func() {
		f.service.beforeFinalAuthorityCheck = nil
		if _, err := f.store.SetImportTransportConsent(context.Background(), "project", true, f.now.Add(time.Second)); err != nil {
			t.Errorf("change authority at publication boundary: %v", err)
		}
	}
	run, err := f.refresh()
	require(t, err != nil && problemCode(err) == CodeSuperseded, "refresh err=%v", err)
	require(t, run.Status == state.ImportRunSuperseded, "run=%+v", run)
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == first && got != second, "final authority check allowed stale publication: got %s", got)
}
