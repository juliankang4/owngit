package importsync

import (
	"context"
	"testing"
)

func TestParentFailedCredentialDoesNotBindAfterModeChange(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "synthetic-original"}))
	noErr(t, f.store.Exec(ctx, `CREATE TRIGGER fail_authority_write BEFORE UPDATE OF authority_revision ON import_sources BEGIN SELECT RAISE(FAIL,'synthetic authority write failure'); END`))
	const failedSecret = "synthetic-rejected-replacement"
	if err := f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: failedSecret}); err == nil {
		t.Fatal("credential replacement unexpectedly succeeded")
	}
	noErr(t, f.store.Exec(ctx, `DROP TRIGGER fail_authority_write`))
	source, exists, err := f.store.ImportSource(ctx, "project")
	if err != nil || !exists {
		t.Fatalf("source read: exists=%v err=%v", exists, err)
	}
	changed, err := f.service.ConfigureSource(ctx, ConfigureInput{
		RepositoryID: "project", URL: source.URL, Mode: ModeCoexistence,
		GitOnlyConsent: source.GitOnlyConsent, AllowPrivateNetwork: source.AllowPrivateNetwork,
	})
	noErr(t, err)
	credential, stored, err := f.store.LoadImportCredentials(ctx, "project")
	noErr(t, err)
	if stored && credential.Bound(changed) {
		t.Error("failed credential became bound after unrelated mode change")
	}
	previousRequests := len(f.transport.requests)
	_, _ = f.refresh()
	for _, request := range f.transport.requests[previousRequests:] {
		if request.Authentication.BearerToken == failedSecret {
			t.Error("failed credential entered a later transport request")
		}
	}
}
