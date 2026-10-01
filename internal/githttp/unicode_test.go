package githttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
