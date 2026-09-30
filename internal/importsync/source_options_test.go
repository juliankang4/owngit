package importsync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/state"
)

func boolPointer(value bool) *bool       { return &value }
func stringPointer(value string) *string { return &value }

func TestSourceOptionsReachTheTransport(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	_, err := f.service.Import(context.Background(), ImportInput{
		Name: "project", URL: "http://example.invalid/team/project.git",
		Options: OptionsChange{
			AllowPlainHTTP: boolPointer(true), AllowReservedAddresses: boolPointer(true),
			Redirects: stringPointer(state.ImportRedirectApproved), ApprovedRedirectOrigin: stringPointer("HTTPS://Mirror.Example:443"),
			Limits: map[string]int64{"pack_bytes": 64 << 30, "refs": 100_000, "fetch_seconds": 7200, "run_seconds": 3 * 3600, "lfs_objects": 400_000},
		},
	})
	noErr(t, err)
	request := f.transport.requests[0]
	if !request.AllowPlainHTTP || !request.AllowReservedAddresses || request.Redirects != state.ImportRedirectApproved ||
		request.ApprovedRedirectOrigin != "https://mirror.example" {
		t.Fatalf("transport options = %+v", request)
	}
	limits := request.Limits
	if limits.MaxPackBytes != 64<<30 || limits.Advertisement.MaxRefRecords != 100_000 || limits.TotalTimeout != 2*time.Hour {
		t.Fatalf("transport limits = %+v", limits)
	}
	// The total body follows the larger pack instead of the default bound.
	if limits.MaxTotalBodyBytes <= 64<<30 {
		t.Fatalf("total body bound %d does not cover the pack", limits.MaxTotalBodyBytes)
	}
	status, err := f.service.Status(context.Background(), "project")
	noErr(t, err)
	if status.Options == nil || status.Options.Limits.RunSeconds != 3*3600 || status.Options.Limits.VerifySeconds != 600 ||
		strings.Join(status.Options.ChangedLimits, ",") != "pack_bytes,run_seconds,fetch_seconds,refs,lfs_objects" {
		t.Fatalf("status options = %+v", status.Options)
	}
}

func TestPlainHTTPSourceNeedsConsent(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.Import(context.Background(), ImportInput{Name: "project", URL: "http://example.invalid/team/project.git"})
	if problemCode(err) != CodeInvalidSource || !strings.Contains(err.Error(), "plain HTTP") {
		t.Fatalf("plain HTTP import without consent = %v", err)
	}
	if len(f.transport.requests) != 0 {
		t.Fatal("a refused source reached the transport")
	}
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	_, err = f.service.ConfigureSource(context.Background(), ConfigureInput{RepositoryID: "project", URL: "http://example.invalid/team/project.git"})
	if problemCode(err) != CodeInvalidSource {
		t.Fatalf("plain HTTP configuration without consent = %v", err)
	}
}

func TestChangedURLResetsTransportConsentsAndKeepsLimits(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{Options: OptionsChange{
		AllowReservedAddresses: boolPointer(true), Redirects: stringPointer(state.ImportRedirectSameOrigin),
		Limits: map[string]int64{"refs": 10},
	}})
	ctx := context.Background()
	kept, err := f.service.ConfigureSource(ctx, ConfigureInput{RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ModeCoexistence})
	noErr(t, err)
	if !kept.Options.AllowReservedAddresses || kept.Options.Redirects != state.ImportRedirectSameOrigin || kept.Options.Limits.Refs != 10 {
		t.Fatalf("same URL options = %+v", kept.Options)
	}
	moved, err := f.service.ConfigureSource(ctx, ConfigureInput{RepositoryID: "project", URL: "https://example.invalid/other/project.git"})
	noErr(t, err)
	if moved.Options.AllowReservedAddresses || moved.Options.Redirects != state.ImportRedirectRefuse || moved.Options.Limits.Refs != 10 {
		t.Fatalf("changed URL options = %+v", moved.Options)
	}
	// A consent given with the new URL in the same change applies to it.
	withConsent, err := f.service.ConfigureSource(ctx, ConfigureInput{
		RepositoryID: "project", URL: "http://example.invalid/third/project.git",
		Options: OptionsChange{AllowPlainHTTP: boolPointer(true)},
	})
	noErr(t, err)
	if !withConsent.Options.AllowPlainHTTP || withConsent.SourceGeneration != moved.SourceGeneration+1 {
		t.Fatalf("new URL with consent = %+v", withConsent)
	}
}

func TestOptionChangesAreCheckedAndStoredCanonically(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	before, _, err := f.store.ImportSource(ctx, "project")
	noErr(t, err)

	// A limits change applies to the next run and keeps the execution
	// authority of a run in progress. A limit equal to its default is not
	// stored.
	limited, err := f.service.ChangeOptions(ctx, "project", OptionsChange{Limits: map[string]int64{"pack_bytes": 1 << 30, "verify_seconds": 600}})
	noErr(t, err)
	if limited.AuthorityRevision != before.AuthorityRevision || limited.Options.Limits != (state.ImportLimits{PackBytes: 1 << 30}) {
		t.Fatalf("limits change = %+v", limited)
	}
	approved, err := f.service.ChangeOptions(ctx, "project", OptionsChange{
		Redirects: stringPointer(state.ImportRedirectApproved), ApprovedRedirectOrigin: stringPointer("https://Mirror.Example/"),
	})
	noErr(t, err)
	if approved.AuthorityRevision != before.AuthorityRevision+1 || approved.Options.ApprovedRedirectOrigin != "https://mirror.example" || approved.Options.Limits.PackBytes != 1<<30 {
		t.Fatalf("redirect change = %+v", approved)
	}
	refused, err := f.service.ChangeOptions(ctx, "project", OptionsChange{Redirects: stringPointer(state.ImportRedirectRefuse)})
	noErr(t, err)
	if refused.Options.ApprovedRedirectOrigin != "" {
		t.Fatalf("an unused origin was kept: %+v", refused.Options)
	}

	for name, change := range map[string]OptionsChange{
		"fetch longer than run":   {Limits: map[string]int64{"fetch_seconds": 2 * 3600}},
		"index longer than fetch": {Limits: map[string]int64{"index_seconds": 45 * 60}},
		"out of range":            {Limits: map[string]int64{"refs": 0}},
		"unknown limit":           {Limits: map[string]int64{"packets": 1}},
		"approved without origin": {Redirects: stringPointer(state.ImportRedirectApproved)},
		"plain HTTP origin":       {Redirects: stringPointer(state.ImportRedirectApproved), ApprovedRedirectOrigin: stringPointer("http://mirror.example")},
		"unknown policy":          {Redirects: stringPointer("follow")},
	} {
		if _, err := f.service.ChangeOptions(ctx, "project", change); problemCode(err) != CodeInvalidSource {
			t.Errorf("%s = %v", name, err)
		}
	}
	after, _, err := f.store.ImportSource(ctx, "project")
	noErr(t, err)
	if after.Options != refused.Options {
		t.Fatalf("refused changes altered the options: %+v", after.Options)
	}
	// Raising the run time with the fetch time is coherent.
	if _, err := f.service.ChangeOptions(ctx, "project", OptionsChange{Limits: map[string]int64{"fetch_seconds": 2 * 3600, "run_seconds": 3 * 3600}}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceLimitsBoundTheRun(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.commit("two", "two\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	_, err := f.service.ChangeOptions(ctx, "project", OptionsChange{Limits: map[string]int64{"run_seconds": 120, "fetch_seconds": 60, "index_seconds": 60, "verify_seconds": 60}})
	noErr(t, err)
	var deadline time.Time
	previous := f.service.Fetch
	f.service.Fetch = func(ctx context.Context, request importfetch.Request, consume importfetch.PackConsumer) (*importfetch.Result, error) {
		deadline, _ = ctx.Deadline()
		return previous(ctx, request, consume)
	}
	started := time.Now()
	if _, err := f.refresh(); err != nil {
		t.Fatal(err)
	}
	if deadline.IsZero() || deadline.Sub(started) > 121*time.Second {
		t.Fatalf("run deadline %v after start, want the source's 120 s", deadline.Sub(started))
	}
	// A caller's own run time wins, so a server keeps its request bound.
	if _, err := f.service.Refresh(ctx, "project", Limits{RunTimeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if deadline.Sub(started) < 59*time.Minute {
		t.Fatalf("caller run time was not used: %v", deadline.Sub(started))
	}
}

func TestUnreadableSavedLimitsAreNamedAndRepaired(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	for _, stored := range []string{`{"pack_bytes":"big"}`, `{"packets":1}`, `{"refs":0}`, `{"refs":-5}`, `{"run_seconds":99999999}`} {
		noErr(t, f.store.Exec(ctx, `UPDATE import_sources SET limits_json=? WHERE repository_id='project'`, stored))
		_, err := f.refresh()
		var setting *state.ImportSourceSettingError
		if !errors.As(err, &setting) || !strings.Contains(err.Error(), "owngit import configure --limit") {
			t.Fatalf("refresh with limits %s = %v", stored, err)
		}
		status, err := f.service.Status(ctx, "project")
		if err != nil || status.Options == nil || !strings.Contains(status.Options.Problem, "import limits cannot be read") {
			t.Fatalf("status with limits %s = %+v, %v", stored, status.Options, err)
		}
		// A change that leaves the limits alone keeps the refusal.
		if _, err := f.service.ChangeOptions(ctx, "project", OptionsChange{AllowReservedAddresses: boolPointer(true)}); !errors.As(err, &setting) {
			t.Fatalf("unrelated change with unreadable limits = %v", err)
		}
		repaired, err := f.service.ChangeOptions(ctx, "project", OptionsChange{Limits: map[string]int64{"refs": 1000}})
		if err != nil || repaired.Options.Limits != (state.ImportLimits{Refs: 1000}) {
			t.Fatalf("repair = %+v, %v", repaired.Options, err)
		}
	}
}
