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
	if secondTag == firstTag {
		t.Fatal("test did not replace the tag object")
	}
	run, err := f.refresh()
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if run.RefsUpdated != 1 || run.RefsDivergent != 0 {
		t.Fatalf("refresh run=%+v", run)
	}
	refs := f.destinationRefs()
	if refs["refs/tags/v1"] != secondTag {
		t.Fatalf("destination tag=%s want %s", refs["refs/tags/v1"], secondTag)
	}
	if refs[repository.RetainedRefName("tags", firstTag)] != firstTag {
		t.Fatalf("replaced tag object was not retained: %v", sortedKeys(refs))
	}
	if refs[repository.ProvenanceRefName("tags", "v1", firstTag)] != firstTag {
		t.Fatalf("tag provenance was not retained: %v", sortedKeys(refs))
	}
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
	if replacement == firstOuter {
		t.Fatal("test did not replace the outer tag object")
	}
	if _, err := f.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	refs := f.destinationRefs()
	if refs["refs/tags/release"] != replacement || refs[repository.RetainedRefName("tags", firstOuter)] != firstOuter {
		t.Fatalf("tag chain replacement was not exact and retained: %v", sortedKeys(refs))
	}
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
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if run.RefsDivergent != 1 || run.RefsUpdated != 0 {
		t.Fatalf("new source inherited branch authority: %+v", run)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != first || got == second {
		t.Fatalf("destination branch=%s first=%s second=%s", got, first, second)
	}
	secondRun, err := f.refresh()
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if secondRun.RefsDivergent != 1 || secondRun.RefsUpdated != 0 || f.destinationRefs()["refs/heads/main"] != first {
		t.Fatalf("recording the new source observation manufactured authority: run=%+v", secondRun)
	}
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
			if err == nil || problemCode(err) != CodeSuperseded {
				t.Fatalf("refresh err=%v", err)
			}
			if run.Status != state.ImportRunSuperseded || run.CancelRequestedAt == nil {
				t.Fatalf("run=%+v", run)
			}
			if got := f.destinationRefs()["refs/heads/main"]; got != first || got == second {
				t.Fatalf("stale authority published branch=%s first=%s second=%s", got, first, second)
			}
			for _, secret := range []string{"old-secret", "replacement-secret", "PRIVATE CA BYTES"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposed secret material: %v", err)
				}
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
	if err != nil {
		t.Fatal(err)
	}
	second := f.commit("two", "two\n")
	f.transport.before = func() {
		f.transport.before = nil
		if err := f.service.SetCredentials(context.Background(), "project", credential); err != nil {
			t.Errorf("repeat credentials: %v", err)
		}
	}
	run, err := f.refresh()
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("refresh run=%+v err=%v", run, err)
	}
	after, _, err := f.store.ImportSource(context.Background(), "project")
	if err != nil || after.AuthorityRevision != before.AuthorityRevision || after.CredentialGeneration != before.CredentialGeneration {
		t.Fatalf("identical credential changed authority: before=%+v after=%+v err=%v", before, after, err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != second {
		t.Fatalf("identical credential blocked refresh: got %s want %s", got, second)
	}
}

func TestCredentialFailureStopsRunWhenDurableCancellationFails(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	if err := f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "synthetic-initial-token"}); err != nil {
		t.Fatal(err)
	}
	before := f.destinationRefs()["refs/heads/main"]
	f.commit("new source tip", "changed\n")
	started, done, run, runErr := f.gatedRefresh(t)
	<-started
	released := false
	defer func() {
		if !released {
			f.transport.gate <- struct{}{}
			<-done
		}
	}()
	if err := f.store.Exec(ctx, `CREATE TRIGGER fail_authority_write BEFORE UPDATE OF authority_revision ON import_sources BEGIN SELECT RAISE(FAIL,'synthetic authority write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Exec(ctx, `CREATE TRIGGER fail_cancel_write BEFORE UPDATE OF cancel_requested_at ON import_runs WHEN NEW.cancel_requested_at IS NOT OLD.cancel_requested_at BEGIN SELECT RAISE(FAIL,'synthetic cancel write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "synthetic-replacement-token"}); err == nil {
		t.Fatal("credential mutation unexpectedly succeeded")
	}
	persisted, exists, err := f.store.ActiveImportRun(ctx, "project")
	if err != nil || !exists || persisted.CancelRequestedAt != nil {
		t.Fatalf("cancel persistence failure was not preserved: exists=%v cancel=%v err=%v", exists, persisted.CancelRequestedAt, err)
	}
	if err := f.store.Exec(ctx, `DROP TRIGGER fail_cancel_write; DROP TRIGGER fail_authority_write`); err != nil {
		t.Fatal(err)
	}
	f.transport.gate <- struct{}{}
	<-done
	released = true
	if *runErr == nil || run.Status == state.ImportRunComplete {
		t.Fatalf("local invalidation allowed stale completion: status=%s err=%v", run.Status, *runErr)
	}
	if after := f.destinationRefs()["refs/heads/main"]; after != before {
		t.Fatalf("local invalidation allowed ref publication: before=%s after=%s", before, after)
	}
}

func TestUnchangedConfigurationDoesNotRevokePinnedRun(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	before, _, err := f.store.ImportSource(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("refresh run=%+v err=%v", run, err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != second {
		t.Fatalf("unchanged configuration blocked refresh: got %s want %s", got, second)
	}
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
	if err == nil || problemCode(err) != CodeSuperseded {
		t.Fatalf("refresh err=%v", err)
	}
	if run.Status != state.ImportRunSuperseded {
		t.Fatalf("run=%+v", run)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != first || got == second {
		t.Fatalf("final authority check allowed stale publication: got %s", got)
	}
}
