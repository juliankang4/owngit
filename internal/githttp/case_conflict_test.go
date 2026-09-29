package githttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// A push creates, changes and deletes no branch or tag whose name differs
// from another only in letter case, whether the other is already in the
// repository, packed or loose, or comes in the same push. Other refs of the
// same push still change. The result is the same on every file system.
func TestPushRefusesRefsThatDifferOnlyInLetterCase(t *testing.T) {
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			manager, runner := newHTTPTestRepository(t)
			_, err := manager.CreateWithOptions(ctx, "cases", "", repository.CreateOptions{ObjectFormat: format})
			noErr(t, err)
			remote, err := manager.Path("cases")
			noErr(t, err)
			handler, err := New(runner, manager, "", 2)
			noErr(t, err)
			handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
			server := httptest.NewServer(handler)
			defer server.Close()

			work := filepath.Join(t.TempDir(), "work")
			runHTTPGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
			runHTTPGit(t, work, "config", "user.name", "Case Test")
			runHTTPGit(t, work, "config", "user.email", "case@example.invalid")
			runHTTPGit(t, work, "remote", "add", "origin", server.URL+"/git/cases.git")
			commit := func(name string) string {
				noErr(t, os.WriteFile(filepath.Join(work, name), []byte(name+"\n"), 0o600))
				runHTTPGit(t, work, "add", name)
				runHTTPGit(t, work, "commit", "-m", name)
				return httpGitOutput(t, work, "rev-parse", "HEAD")
			}
			refs := func() map[string]string {
				listed := map[string]string{}
				output := httpGitOutput(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags")
				for _, line := range strings.Split(output, "\n") {
					if name, oid, found := strings.Cut(line, " "); found {
						listed[name] = oid
					}
				}
				return listed
			}

			first := commit("one")
			runHTTPGit(t, work, "tag", "v1")
			runHTTPGit(t, work, "push", "origin", "main", "v1")
			// A repository copied from a system that tells letter case apart
			// can hold both names; both are packed, as git pack-refs --all
			// leaves them.
			second := commit("two")
			runHTTPGit(t, work, "push", "origin", second+":refs/heads/topic")
			runHTTPGit(t, "", "--git-dir", remote, "pack-refs", "--all")
			runHTTPGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/Main", second)
			runHTTPGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/heads/topic")
			runHTTPGit(t, "", "--git-dir", remote, "pack-refs", "--all")
			before := refs()
			if before["refs/heads/main"] != first || before["refs/heads/Main"] != second {
				t.Fatalf("fixture refs: %v", before)
			}

			third := commit("three")
			for _, refused := range [][]string{
				{"change", "--force", third + ":refs/heads/Main"},
				{"create", third + ":refs/heads/MAIN"},
				{"delete", ":refs/heads/Main"},
				{"create a tag", third + ":refs/tags/V1"},
				{"create two new names together", third + ":refs/heads/fresh", third + ":refs/heads/FRESH"},
			} {
				output, err := httpGitCombined(work, append([]string{"push", "origin"}, refused[1:]...)...)
				if err == nil || !strings.Contains(output, "OwnGit refused changing refs/") || !strings.Contains(output, "treat as the same") {
					t.Fatalf("%s: err=%v output=%s", refused[0], err, output)
				}
				if after := refs(); len(after) != len(before) || after["refs/heads/main"] != first || after["refs/heads/Main"] != second || after["refs/tags/v1"] != before["refs/tags/v1"] {
					t.Fatalf("%s changed refs: %v", refused[0], after)
				}
			}

			// The refusal names only the conflicting ref.
			output, err := httpGitCombined(work, "push", "origin", third+":refs/heads/other", third+":refs/heads/MAIN")
			if err == nil || !strings.Contains(output, "refs/heads/MAIN") {
				t.Fatalf("mixed push: err=%v output=%s", err, output)
			}
			if after := refs(); after["refs/heads/other"] != third || after["refs/heads/main"] != first || after["refs/heads/Main"] != second {
				t.Fatalf("mixed push refs: %v", after)
			}
			// Either spelling of a shared name is refused, and a name that no
			// other ref shares apart from case still changes.
			fourth := commit("four")
			output, err = httpGitCombined(work, "push", "origin", third+":refs/heads/main", fourth+":refs/heads/other")
			if err == nil || !strings.Contains(output, "OwnGit refused changing refs/heads/main ") {
				t.Fatalf("push to main: err=%v output=%s", err, output)
			}
			if after := refs(); after["refs/heads/main"] != first || after["refs/heads/Main"] != second || after["refs/heads/other"] != fourth {
				t.Fatalf("push to main refs: %v", after)
			}
		})
	}
}

// A push whose command list OwnGit does not read in full changes no ref.
func TestPushWithACommandListOverTheLimitChangesNothing(t *testing.T) {
	ctx := context.Background()
	manager, runner := newHTTPTestRepository(t)
	_, err := manager.Create(ctx, "cases", "")
	noErr(t, err)
	remote, err := manager.Path("cases")
	noErr(t, err)
	handler, err := New(runner, manager, "", 2)
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	server := httptest.NewServer(handler)
	defer server.Close()
	limit := maximumPushCommands
	maximumPushCommands = 300
	defer func() { maximumPushCommands = limit }()

	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "Case Test")
	runHTTPGit(t, work, "config", "user.email", "case@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file"), []byte("file\n"), 0o600))
	runHTTPGit(t, work, "add", "file")
	runHTTPGit(t, work, "commit", "-m", "file")
	arguments := []string{"push", server.URL + "/git/cases.git"}
	for index := range 8 {
		arguments = append(arguments, fmt.Sprintf("HEAD:refs/heads/branch-%d", index))
	}
	output, err := httpGitCombined(work, arguments...)
	if err == nil || !strings.Contains(output, "OwnGit could not check the pushed ref names") {
		t.Fatalf("err=%v output=%s", err, output)
	}
	if listed := httpGitOutput(t, "", "--git-dir", remote, "for-each-ref"); listed != "" {
		t.Fatalf("refs were created: %s", listed)
	}
}

// A push creates no ref whose name a file system opens as the same file as
// the packed, protected default branch: other Unicode forms, folded
// letters and letters that NTFS upper-cases alike. The client keeps the
// spelling it was given (core.precomposeUnicode=false), as a Linux client
// does. A fast-forward of the default branch itself still works.
func TestPushRefusesNamesThatAFileSystemTreatsAsTheDefaultBranch(t *testing.T) {
	pairs := []struct{ name, defaultBranch, pushed string }{
		{"composed and decomposed", "café", "cafe\u0301"},
		{"Hangul syllables and jamo", "기본", "\u1100\u1175\u1107\u1169\u11ab"},
		{"long s", "master", "ma\u017fter"},
		{"sharp s", "strasse", "straße"},
		{"dotless i", "list", "l\u0131st"},
		{"letter case", "main", "MAIN"},
	}
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			manager, runner := newHTTPTestRepository(t)
			handler, err := New(runner, manager, "", 2)
			noErr(t, err)
			handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
			server := httptest.NewServer(handler)
			defer server.Close()
			work := filepath.Join(t.TempDir(), "work")
			runHTTPGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
			runHTTPGit(t, work, "config", "user.name", "Case Test")
			runHTTPGit(t, work, "config", "user.email", "case@example.invalid")
			commits := 0
			commit := func() string {
				commits++
				name := fmt.Sprintf("file-%d", commits)
				noErr(t, os.WriteFile(filepath.Join(work, name), []byte(name+"\n"), 0o600))
				runHTTPGit(t, work, "add", name)
				runHTTPGit(t, work, "commit", "-m", name)
				return httpGitOutput(t, work, "rev-parse", "HEAD")
			}
			for index, pair := range pairs {
				id := fmt.Sprintf("unicode-%d", index)
				_, err := manager.CreateWithOptions(ctx, id, "", repository.CreateOptions{ObjectFormat: format})
				noErr(t, err)
				remote, err := manager.Path(id)
				noErr(t, err)
				on := true
				_, err = manager.Store.SaveRepositoryRefPolicy(ctx, id, state.RepositoryRefPolicyChange{ProtectDefaultBranch: &on})
				noErr(t, err)
				url := server.URL + "/git/" + id + ".git"
				defaultRef := "refs/heads/" + pair.defaultBranch
				tip := commit()
				runHTTPGit(t, work, "-c", "core.precomposeUnicode=false", "push", url, tip+":"+defaultRef)
				runHTTPGit(t, "", "--git-dir", remote, "symbolic-ref", "HEAD", defaultRef)
				runHTTPGit(t, "", "--git-dir", remote, "pack-refs", "--all")

				runHTTPGit(t, work, "checkout", "-q", "--orphan", id)
				replacement := commit()
				output, err := httpGitCombined(work, "-c", "core.precomposeUnicode=false", "push", url, replacement+":refs/heads/"+pair.pushed)
				if err == nil || !strings.Contains(output, "OwnGit refused changing refs/heads/") {
					t.Fatalf("%s: err=%v output=%s", pair.name, err, output)
				}
				listed := httpGitOutput(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname) %(objectname)")
				if listed != defaultRef+" "+tip {
					t.Fatalf("%s: refs after the refused push:\n%s", pair.name, listed)
				}
				runHTTPGit(t, work, "checkout", "-q", "-B", "next", tip)
				next := commit()
				runHTTPGit(t, work, "-c", "core.precomposeUnicode=false", "push", url, next+":"+defaultRef)
				if got := httpGitOutput(t, "", "--git-dir", remote, "rev-parse", defaultRef); got != next {
					t.Fatalf("%s: fast-forward of the default branch left it at %s", pair.name, got)
				}
			}
		})
	}
}
