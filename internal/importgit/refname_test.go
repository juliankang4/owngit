package importgit

import (
	"os/exec"
	"strings"
	"testing"
)

// CheckRefFormat must give the same answer as `git check-ref-format`, which
// is the definition of a legal ref name.
func TestCheckRefFormatMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	names := []string{
		"refs/heads/main", "refs/heads/a./b", "refs/heads/a/b", "refs/heads/a.b", "refs/heads/@", "refs/heads/a@b",
		"refs/heads/a{b", "refs/heads/@{", "refs/heads/a@{b", "refs/heads/.hidden", "refs/heads/a/.b", "refs/heads/a.lock",
		"refs/heads/a.lock/b", "refs/heads/a/b.lock", "refs/heads/a.locked", "refs/heads/a.", "refs/heads/a/", "refs/heads//a",
		"refs/heads/a//b", "refs/heads/a..b", "refs/heads/a...b", "refs/heads/a b", "refs/heads/a\tb", "refs/heads/a\x7fb",
		"refs/heads/a~b", "refs/heads/a^b", "refs/heads/a:b", "refs/heads/a?b", "refs/heads/a*b", "refs/heads/a[b",
		"refs/heads/a\\b", "refs/heads/é", "refs/heads/-a", "refs/", "refs", "@", "HEAD", "a", "a/b", "/a/b", "refs/tags/v1.0",
		"refs/owngit/pull-requests/1/merge-receipt", "refs/heads/..", "refs/heads/.", "refs/heads/a/..", "refs/.lock",
	}
	for _, name := range names {
		if strings.ContainsRune(name, 0) {
			continue
		}
		legal := exec.Command("git", "check-ref-format", name).Run() == nil
		if got := CheckRefFormat(name) == nil; got != legal {
			t.Errorf("CheckRefFormat(%q) = %v, git check-ref-format says %v", name, got, legal)
		}
	}
}
