package testfixture

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGitEnvironmentAppendsAfterExistingEntries(t *testing.T) {
	env := []string{"HOME=/h", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.longpaths", "GIT_CONFIG_VALUE_0=true"}
	got := GitEnvironment(env)
	want := []string{"HOME=/h", "GIT_CONFIG_KEY_0=core.longpaths", "GIT_CONFIG_VALUE_0=true",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=gc.auto", "GIT_CONFIG_VALUE_2=0", "GIT_CONFIG_COUNT=3"}
	if !slices.Equal(got, want) {
		t.Fatalf("GitEnvironment=%q, want %q", got, want)
	}
	if env[1] != "GIT_CONFIG_COUNT=1" {
		t.Fatalf("GitEnvironment changed its input: %q", env)
	}
	if got := GitEnvironment(nil); !slices.Equal(got, []string{"GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=gc.auto", "GIT_CONFIG_VALUE_1=0", "GIT_CONFIG_COUNT=2"}) {
		t.Fatalf("GitEnvironment(nil)=%q", got)
	}
}

// Git itself reads the settings, next to an entry that was already there.
func TestGitEnvironmentTurnsOffAutomaticMaintenanceInGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	env := GitEnvironment(append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=test.kept", "GIT_CONFIG_VALUE_0=yes"))
	for name, want := range map[string]string{"maintenance.auto": "false", "gc.auto": "0", "test.kept": "yes"} {
		command := exec.Command("git", "config", "--get", name)
		command.Env = env
		output, err := command.Output()
		if err != nil || strings.TrimSpace(string(output)) != want {
			t.Fatalf("git config --get %s = %q, %v; want %q", name, output, err, want)
		}
	}
}

// A commit with the settings starts no automatic maintenance. The control
// commit without them shows that this Git starts it; it runs in the
// foreground there, so nothing outlives the test.
func TestGitEnvironmentKeepsACommitFromStartingMaintenance(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	config := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	base := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+config, "HOME="+root,
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	repository := filepath.Join(root, "repository")
	run := func(env []string, arguments ...string) {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = root
		command.Env = env
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
	}
	run(base, "init", "-q", repository)
	commitStartsMaintenance := func(env []string, arguments ...string) bool {
		t.Helper()
		trace := filepath.Join(root, "trace-"+strings.Join(arguments, ""))
		run(append(env, "GIT_TRACE2_EVENT="+trace), append(append([]string{"-C", repository}, arguments...), "commit", "-q", "--allow-empty", "-m", "commit")...)
		content, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(content), `"maintenance","run","--auto"`)
	}
	if !commitStartsMaintenance(base, "-c", "maintenance.autoDetach=false") {
		t.Skip("this Git does not start automatic maintenance after a commit")
	}
	if commitStartsMaintenance(GitEnvironment(base)) {
		t.Fatal("a commit with GitEnvironment started automatic maintenance")
	}
}
