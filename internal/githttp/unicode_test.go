package githttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
)

func TestSmartHTTPAdvertisesDecomposedRefsWithExactBytes(t *testing.T) {
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			manager, runner := newHTTPTestRepository(t)
			_, err := manager.CreateWithOptions(ctx, "exact", "", repository.CreateOptions{ObjectFormat: format})
			noErr(t, err)
			remote, err := manager.Path("exact")
			noErr(t, err)
			runHTTPGit(t, "", "--git-dir", remote, "config", "core.precomposeUnicode", "true")
			handler, err := New(runner, manager, "")
			noErr(t, err)
			handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
			server := httptest.NewServer(handler)
			defer server.Close()
			git := isolatedGit(t, "-c", "core.precomposeUnicode=false")
			work := filepath.Join(t.TempDir(), "work")
			git(t, "", "init", "--bare", "--object-format="+format, work)
			tree := git(t, work, "mktree")
			oid := git(t, work, "commit-tree", tree, "-m", "exact refs")
			branch, tag := "\u1112\u1161\u11ab\u1100\u1173\u11af-follow", "cafe\u0301-tag"
			url := server.URL + "/git/exact.git"
			git(t, work, "push", url, oid+":refs/heads/"+branch, oid+":refs/tags/"+tag)
			for _, protocol := range []string{"0", "2"} {
				listed := git(t, work, "-c", "protocol.version="+protocol, "ls-remote", "--refs", url)
				want := oid + "\trefs/heads/" + branch + "\n" + oid + "\trefs/tags/" + tag
				if listed != want {
					t.Fatalf("protocol %s advertisement=%q, want %q", protocol, listed, want)
				}
				git(t, work, "-c", "protocol.version="+protocol, "fetch", url, "refs/heads/"+branch)
				if got := git(t, work, "rev-parse", "FETCH_HEAD"); got != oid {
					t.Fatalf("fetched ref=%s, want %s", got, oid)
				}
			}
		})
	}
}

func TestSmartHTTPUpdatesAndRetainsDecomposedRefsAfterHookRefresh(t *testing.T) {
	ctx := context.Background()
	manager, runner := newHTTPTestRepository(t)
	remote, err := manager.Path("sample")
	noErr(t, err)
	runHTTPGit(t, "", "--git-dir", remote, "config", "core.precomposeUnicode", "true")
	hookPath := filepath.Join(remote, "hooks", "update")
	hook, err := os.ReadFile(hookPath)
	noErr(t, err)
	// Recreate the previous hook's two Git invocations, which discarded the
	// runner environment and had no command-scope precomposition setting.
	oldHook := strings.ReplaceAll(string(hook), " -c core.precomposeUnicode=false", "")
	noErr(t, os.WriteFile(hookPath, []byte(oldHook), 0o700))
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	server := httptest.NewServer(handler)
	defer server.Close()
	git := isolatedGit(t, "-c", "core.precomposeUnicode=false")
	work := filepath.Join(t.TempDir(), "work")
	git(t, "", "init", "--bare", work)
	tree := git(t, work, "mktree")
	original := git(t, work, "commit-tree", tree, "-m", "original")
	next := git(t, work, "commit-tree", tree, "-p", original, "-m", "next")
	replacement := git(t, work, "commit-tree", tree, "-m", "replacement")
	branch, tag := "refs/heads/\u1112\u1161\u11ab\u1100\u1173\u11af-follow", "refs/tags/cafe\u0301-tag"
	url := server.URL + "/git/sample.git"
	git(t, work, "push", url, original+":"+branch, original+":"+tag)
	configPath := filepath.Join(remote, "config")
	before, err := os.ReadFile(configPath)
	noErr(t, err)

	preparation, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { noErr(t, manager.StopPreparation(ctx)) }()
	noErr(t, manager.StartPreparation(preparation, nil, 10*time.Second, nil))
	if manager.Preparing("sample") {
		t.Fatal("existing repository did not finish hook preparation")
	}
	after, err := os.ReadFile(configPath)
	noErr(t, err)
	if string(before) != string(after) {
		t.Fatal("hook refresh changed repository config")
	}
	git(t, work, "push", url, next+":"+branch)
	git(t, work, "push", "--force", url, replacement+":"+branch, next+":"+tag)
	for ref, want := range map[string]string{branch: replacement, tag: next} {
		if got := git(t, "", "--git-dir", remote, "rev-parse", ref); got != want {
			t.Fatalf("updated %q=%s, want %s", ref, got, want)
		}
	}
	listed := git(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/owngit/provenance")
	for _, ref := range []string{
		"refs/owngit/provenance/heads/" + strings.TrimPrefix(branch, "refs/heads/") + "/" + next,
		"refs/owngit/provenance/tags/" + strings.TrimPrefix(tag, "refs/tags/") + "/" + original,
	} {
		if !strings.Contains(listed, ref+" ") {
			t.Fatalf("exact retained provenance %q absent: %q", ref, listed)
		}
	}
	retained, err := manager.RetainedRefs(ctx, "sample")
	noErr(t, err)
	if len(retained) != 2 {
		t.Fatalf("retained history=%+v", retained)
	}
}

func TestSmartHTTPRefusesDecomposedLookAlikeWithoutOverwritingComposedRef(t *testing.T) {
	ctx := context.Background()
	manager, runner := newHTTPTestRepository(t)
	remote, err := manager.Path("sample")
	noErr(t, err)
	runHTTPGit(t, "", "--git-dir", remote, "config", "core.precomposeUnicode", "true")
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	server := httptest.NewServer(handler)
	defer server.Close()
	work := filepath.Join(t.TempDir(), "work")
	git := isolatedGit(t, "-c", "core.precomposeUnicode=false")
	git(t, "", "init", "--bare", work)
	tree := git(t, work, "mktree")
	original := git(t, work, "commit-tree", tree, "-m", "original")
	replacement := git(t, work, "commit-tree", tree, "-p", original, "-m", "replacement")
	composed, decomposed := "refs/heads/café", "refs/heads/cafe\u0301"
	url := server.URL + "/git/sample.git"
	git(t, work, "push", url, original+":"+composed)
	output, err := httpGitCombined(work, "-c", "core.precomposeUnicode=false", "push", url, replacement+":"+decomposed)
	if err == nil || !strings.Contains(output, "OwnGit refused") || !strings.Contains(output, "[remote rejected]") {
		t.Fatalf("look-alike push was not visibly refused: output=%q err=%v", output, err)
	}
	if got := git(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != composed+" "+original {
		t.Fatalf("refs after refusal=%q", got)
	}
	summary, err := manager.Summary(ctx, "sample")
	noErr(t, err)
	if len(summary.Branches) != 1 || summary.Branches[0].Name != "café" || summary.Branches[0].OID != original {
		t.Fatalf("composed ref changed: %+v", summary)
	}
}
