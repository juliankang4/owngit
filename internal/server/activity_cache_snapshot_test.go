package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// TestWarmDashboardStartsNoGitProcess proves that a dashboard request for
// repositories whose refs have not changed since the previous request starts
// no Git process, and that a push through Smart HTTP and a pull request merge
// in the browser are both on the next dashboard.
func TestWarmDashboardStartsNoGitProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	for _, name := range []string{"second", "third"} {
		addActivityRepository(t, fixture.app, name, 2)
	}
	tracePath := traceGitCommands(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	processes := func() int {
		t.Helper()
		trace, err := os.ReadFile(tracePath)
		noErr(t, err)
		return strings.Count(string(trace), "\n")
	}

	settledGET(t, client, server.URL+"/")
	before := processes()
	body := settledGET(t, client, server.URL+"/")
	if started := processes() - before; started != 0 {
		t.Fatalf("a warm dashboard started %d Git processes, want 0", started)
	}
	if !strings.Contains(body, fixture.targetOID) {
		t.Fatal("the warm dashboard does not show the default branch tip")
	}

	// Push through Smart HTTP.
	noErr(t, os.WriteFile(filepath.Join(fixture.work, "pushed.txt"), []byte("pushed\n"), 0o600))
	apiRunGit(t, fixture.work, "checkout", "main")
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "pushed through Smart HTTP")
	apiRunGit(t, fixture.work, "push", server.URL+"/git/project.git", "HEAD:refs/heads/main")
	pushed := apiGitOutput(t, fixture.work, "rev-parse", "HEAD")
	body = settledGET(t, client, server.URL+"/")
	if !strings.Contains(body, pushed) || !strings.Contains(body, "pushed through Smart HTTP") {
		t.Fatal("the dashboard after a Smart HTTP push does not show the pushed tip")
	}
	before = processes()
	settledGET(t, client, server.URL+"/")
	if started := processes() - before; started != 0 {
		t.Fatalf("the dashboard after the push settled still started %d Git processes", started)
	}

	// Merge a pull request in the browser.
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Snapshot merge", SourceBranch: "feature", TargetBranch: "main",
		ReviewChoice: webui.ReviewChoiceSkip,
	})
	noErr(t, err)
	merge := browserForm(t, client, server.URL+pullRequestURL("project", created.Number)+"/merge", url.Values{
		"csrf":       {cookieValue(t, jar, server.URL, generalCookie)},
		"source_oid": {fixture.sourceOID},
		"target_oid": {pushed},
	}, server.URL)
	if merge.status != http.StatusSeeOther {
		t.Fatalf("merge status=%d", merge.status)
	}
	merged := apiGitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main")
	if merged == pushed {
		t.Fatal("the merge did not move main")
	}
	snapshot, err := fixture.app.Repositories.RefSnapshot(context.Background(), "project")
	noErr(t, err)
	if snapshot.Summary.DefaultOID != merged || snapshot.Head.OID != merged {
		t.Fatalf("snapshot after merge=%+v, want main at %s", snapshot.Summary, merged)
	}
	if body := settledGET(t, client, server.URL+"/"); !strings.Contains(body, merged) {
		t.Fatal("the dashboard after a merge does not show the merged tip")
	}
}

func TestListedRefSnapshotsKeepCatalogueAndStorageFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("creates several synthetic repository and catalogue fixtures")
	}
	for _, test := range []struct {
		name   string
		change func(*testing.T, *App, string)
		want   error
	}{
		{name: "unchanged"},
		{name: "deleted after listing", want: repository.ErrRepositoryNotFound, change: func(t *testing.T, app *App, path string) {
			lock := app.Repositories.Locks.For("listed")
			lock.Lock()
			defer lock.UnlockWithoutRefChanges()
			noErr(t, app.Store.BeginRepositoryDeletion(context.Background(), state.RepositoryDeletion{
				RepositoryID: "listed", Mode: state.RepositoryDeletionKeepFiles, Root: filepath.Dir(path),
				Moved: ".owngit-removed/listed-20270115T080000Z.git", Marker: strings.Repeat("d", 32), CreatedAt: time.Now(),
			}))
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the deleted row must leave its cached storage present: %v", err)
			}
		}},
		{name: "missing storage", want: repository.ErrStorageUnavailable, change: func(t *testing.T, _ *App, path string) {
			noErr(t, os.Rename(path, path+".kept"))
		}},
		{name: "not a directory", want: repository.ErrStorageUnavailable, change: func(t *testing.T, _ *App, path string) {
			noErr(t, os.Rename(path, path+".kept"))
			noErr(t, os.WriteFile(path, nil, 0o600))
		}},
		{name: "replacement root", want: repository.ErrStorageChanged, change: func(t *testing.T, app *App, path string) {
			if runtime.GOOS == "windows" {
				t.Skip("the root replacement fixture requires Unix")
			}
			noErr(t, app.Repositories.ClaimStorage())
			root := filepath.Dir(path)
			noErr(t, os.Rename(root, root+".kept"))
			noErr(t, os.Mkdir(root, 0o700))
		}},
		{name: "incomplete restore", want: repository.ErrStorageUnavailable, change: func(t *testing.T, _ *App, path string) {
			noErr(t, os.WriteFile(filepath.Join(filepath.Dir(path), state.IncompleteRestoreMarkerName), nil, 0o600))
		}},
		{name: "replacement directory", want: repository.ErrStorageChanged, change: func(t *testing.T, _ *App, path string) {
			noErr(t, os.Rename(path, path+".kept"))
			noErr(t, os.Mkdir(path, 0o700))
		}},
		{name: "symlink", want: repository.ErrStorageUnavailable, change: func(t *testing.T, _ *App, path string) {
			if runtime.GOOS == "windows" {
				t.Skip("the symlink fixture requires Unix")
			}
			noErr(t, os.Rename(path, path+".kept"))
			noErr(t, os.Symlink(path+".kept", path))
		}},
		{name: "cancelled", want: context.Canceled},
		{name: "catalogue unavailable", change: func(t *testing.T, app *App, _ string) {
			noErr(t, app.Store.Exec(context.Background(), `ALTER TABLE repositories RENAME TO unavailable_repositories`))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newConfiguredApp(t)
			_, err := app.Repositories.Create(context.Background(), "listed", "")
			noErr(t, err)
			listed, err := app.Store.Repositories(context.Background())
			noErr(t, err)
			cached, err := app.Repositories.RefSnapshot(context.Background(), "listed")
			noErr(t, err)
			path, err := app.Repositories.Path("listed")
			noErr(t, err)
			if test.change != nil {
				test.change(t, app, path)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.want == context.Canceled {
				cancel()
			}
			snapshots, errs := app.Repositories.RefSnapshotsWithin(ctx, listed, repositoryListWait)
			if len(snapshots) != 1 || len(errs) != 1 {
				t.Fatalf("snapshots=%d errors=%d, want one result per listed row", len(snapshots), len(errs))
			}
			if test.name == "unchanged" {
				if errs[0] != nil || snapshots[0].ActivityKey != cached.ActivityKey || snapshots[0].Stale {
					t.Fatalf("unchanged snapshot=%+v error=%v", snapshots[0], errs[0])
				}
			} else if errs[0] == nil || (test.want != nil && !errors.Is(errs[0], test.want)) || snapshots[0].ActivityKey != "" {
				t.Fatalf("failed snapshot=%+v error=%v, want %v and no cached success", snapshots[0], errs[0], test.want)
			}
		})
	}
}
