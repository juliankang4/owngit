package gitexec

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A failed Git command is named by its subcommand, never by a global option
// value such as the "." of --git-dir, and its reason is Git's complete
// stderr even when the read's output limit is a few bytes.
func TestRunnerFailureNamesSubcommandAndKeepsReason(t *testing.T) {
	root := t.TempDir()
	runner, err := New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	repository := filepath.Join(root, "sample.git")
	_, err = runner.Run(context.Background(), "", nil, "init", "--bare", repository)
	noErr(t, err)

	missing := strings.Repeat("0", 40)
	_, err = runner.RunWithOutputLimit(context.Background(), repository, nil, 8, "--git-dir", ".", "cat-file", "blob", missing)
	if err == nil {
		t.Fatal("reading a missing blob succeeded")
	}
	message := err.Error()
	if !strings.HasPrefix(message, "git cat-file: ") {
		t.Fatalf("error %q does not name git cat-file", message)
	}
	if code, ok := ExitCode(err); !ok || code != 128 {
		t.Fatalf("exit code=(%d,%v) err=%v, want 128", code, ok, err)
	}
	// Git's reason names the object; cut to the 8-byte output limit it was
	// only "fatal: N".
	if !strings.Contains(message, "fatal: ") || !strings.Contains(message, missing) {
		t.Fatalf("error %q lost Git's reason", message)
	}
}

// commandName follows Git's own grammar: global options come before the
// command, and some take the next argument as their value.
func TestCommandNameIsTheGitSubcommand(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--git-dir", ".", "cat-file", "blob", "HEAD"}, "git cat-file"},
		{[]string{"--no-replace-objects", "--git-dir=.", "-c", "diff.external=", "diff", "a", "b"}, "git diff"},
		{[]string{"-c", "http.extraHeader=Authorization: Basic secret", "--git-dir", ".", "commit-tree", "t"}, "git commit-tree"},
		{[]string{"-C", "/private/path", "--work-tree", "/tree", "--namespace", "n", "status"}, "git status"},
		{[]string{"--config-env", "core.sshCommand=SECRET", "--shallow-file", "/s", "--attr-source", "HEAD", "log"}, "git log"},
		{[]string{"init", "--bare", "/path/to/repository.git"}, "git init"},
		{[]string{"--version"}, "git --version"},
		{[]string{"-c", "core.pager=cat", "--exec-path"}, "git --exec-path"},
		{[]string{"--exec-path=/private/libexec", "--no-pager"}, "git"},
		{[]string{"--git-dir"}, "git"},
		// An option Git does not know, or a later one that takes a value,
		// leaves its value where the command would be. Only a Git command
		// token is ever a name.
		{[]string{"--unknown-global-option", "https://user:token@example.test/r.git", "status"}, "git"},
		{[]string{"--unknown-global-option", "/private/path", "log"}, "git"},
		{[]string{"--unknown-global-option", "user.name=OwnGit", "log"}, "git"},
		{[]string{"."}, "git"},
		{nil, "git"},
	} {
		if got := commandName(test.args); got != test.want {
			t.Errorf("commandName(%q) = %q, want %q", test.args, got, test.want)
		}
	}
}
