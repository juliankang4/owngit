package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestImportSourceOptionsAreStoredAndChangeAuthorityOnlyForTransport(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := configureTestImportSource(t, store, "project")
	if source.Options != DefaultImportOptions() {
		t.Fatalf("new source options = %+v", source.Options)
	}
	input := ImportSourceInput{RepositoryID: "project", URL: source.URL, Mode: source.Mode, Now: testImportNow().Add(time.Minute)}

	input.Options = ImportOptions{Redirects: ImportRedirectRefuse, Limits: ImportLimits{PackBytes: 1 << 30, RunSeconds: 7200}}
	limited, err := store.ConfigureImportSource(ctx, input)
	noErr(t, err)
	if limited.AuthorityRevision != source.AuthorityRevision || limited.Options != input.Options {
		t.Fatalf("limits change = %+v", limited)
	}

	input.Options = ImportOptions{AllowPlainHTTP: true, Redirects: ImportRedirectApproved, ApprovedRedirectOrigin: "https://mirror.example",
		AllowReservedAddresses: true, Limits: limited.Options.Limits}
	transport, err := store.ConfigureImportSource(ctx, input)
	noErr(t, err)
	if transport.AuthorityRevision != source.AuthorityRevision+1 {
		t.Fatalf("transport change kept authority %d", transport.AuthorityRevision)
	}
	read, exists, err := store.ImportSource(ctx, "project")
	if err != nil || !exists || read.Options != input.Options {
		t.Fatalf("read back = %+v, %v, %v", read.Options, exists, err)
	}

	// A snapshot of the binding puts the options back exactly.
	snapshot, err := store.ReadImportBinding(ctx, "project")
	noErr(t, err)
	input.Options = DefaultImportOptions()
	_, err = store.ConfigureImportSource(ctx, input)
	noErr(t, err)
	noErr(t, store.RestoreImportBinding(ctx, "project", snapshot))
	if read, _, err = store.ImportSource(ctx, "project"); err != nil || read.Options != snapshot.Source.Options {
		t.Fatalf("restored binding options = %+v, %v", read.Options, err)
	}

	for name, options := range map[string]ImportOptions{
		"origin without its policy": {Redirects: ImportRedirectRefuse, ApprovedRedirectOrigin: "https://mirror.example"},
		"policy without its origin": {Redirects: ImportRedirectApproved},
		"unknown policy":            {Redirects: "follow"},
		"limit out of range":        {Redirects: ImportRedirectRefuse, Limits: ImportLimits{Refs: 1_000_000}},
	} {
		input.Options = options
		if _, err := store.ConfigureImportSource(ctx, input); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
}

func TestUnusableSavedImportOptionsAreNamedPerSource(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := configureTestImportSource(t, store, "project")
	configureTestImportSource(t, store, "other")
	for _, test := range []struct {
		statement string
		setting   string
	}{
		{`UPDATE import_sources SET limits_json='{"refs":0}' WHERE repository_id='project'`, "limits"},
		{`UPDATE import_sources SET limits_json='[]' WHERE repository_id='project'`, "limits"},
		{`UPDATE import_sources SET limits_json='null' WHERE repository_id='project'`, "limits"},
		{`UPDATE import_sources SET limits_json='{"refs":1.5}' WHERE repository_id='project'`, "limits"},
		{`UPDATE import_sources SET redirect_policy='approved' WHERE repository_id='project'`, "redirects"},
	} {
		noErr(t, store.Exec(ctx, `UPDATE import_sources SET limits_json='{}',redirect_policy='refuse',approved_redirect_origin=''`))
		noErr(t, store.Exec(ctx, test.statement))
		read, exists, err := store.ImportSource(ctx, "project")
		var setting *ImportSourceSettingError
		if !errors.As(err, &setting) || setting.RepositoryID != "project" || setting.Setting != test.setting || !exists || read.URL != source.URL {
			t.Fatalf("%s: read = %+v, %v, %v", test.statement, read, exists, err)
		}
		if _, _, err := store.ImportSource(ctx, "other"); err != nil {
			t.Fatalf("another source is affected: %v", err)
		}
		// A change that replaces the options repairs them.
		repaired, err := store.ConfigureImportSource(ctx, ImportSourceInput{RepositoryID: "project", URL: source.URL, Mode: source.Mode, Now: testImportNow()})
		if err != nil || repaired.Options != DefaultImportOptions() {
			t.Fatalf("%s: repair = %+v, %v", test.statement, repaired.Options, err)
		}
	}
}
