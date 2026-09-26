package gitexec

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// wantCommandConfig is the configuration every Git command must receive.
func wantCommandConfig() [][2]string {
	want := [][2]string{
		{"maintenance.auto", "false"},
		{"gc.auto", "0"},
		{"receive.autogc", "false"},
	}
	if runtime.GOOS == "windows" {
		want = append(want, [2]string{"core.longpaths", "true"})
	}
	return want
}

func TestRunnerEnvironmentTurnsOffGitAutomaticMaintenance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runner, err := New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	if got, want := environmentGitConfig(t, runner.Environment()), wantCommandConfig(); !reflect.DeepEqual(got, want) {
		t.Fatalf("command config=%v, want %v", got, want)
	}

	// Command-line scope also overrides a repository that asks for automatic
	// maintenance, and a repository that has no OwnGit config yet.
	repositoryPath := filepath.Join(root, "project.git")
	if _, err := runner.Run(ctx, "", nil, "init", "--bare", "--initial-branch=main", repositoryPath); err != nil {
		t.Fatal(err)
	}
	for _, setting := range [][2]string{{"maintenance.auto", "true"}, {"gc.auto", "6700"}, {"receive.autogc", "true"}} {
		if _, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "config", "--local", setting[0], setting[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, setting := range wantCommandConfig() {
		result, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "config", "--get", setting[0])
		if got := strings.TrimSpace(string(result.Stdout)); err != nil || got != setting[1] {
			t.Errorf("effective %s=%q err=%v, want %q", setting[0], got, err, setting[1])
		}
	}
}

func TestRunnerEnvironmentKeepsCallerGitConfig(t *testing.T) {
	ctx := context.Background()
	runner, err := New("", filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	caller := []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=Caller Name",
		"GIT_CONFIG_KEY_1=user.email",
		"GIT_CONFIG_VALUE_1=caller@example.invalid",
		"GIT_NO_REPLACE_OBJECTS=1",
	}
	environment := runner.Environment(caller...)
	want := append(wantCommandConfig(), [2]string{"user.name", "Caller Name"}, [2]string{"user.email", "caller@example.invalid"})
	if got := environmentGitConfig(t, environment); !reflect.DeepEqual(got, want) {
		t.Fatalf("command config=%v, want %v", got, want)
	}
	if !slices.Contains(environment, "GIT_NO_REPLACE_OBJECTS=1") {
		t.Fatalf("caller entry is missing: %v", environment)
	}

	for _, setting := range want {
		result, err := runner.RunWithEnvironment(ctx, "", nil, caller, "config", "--get", setting[0])
		if got := strings.TrimSpace(string(result.Stdout)); err != nil || got != setting[1] {
			t.Errorf("effective %s=%q err=%v, want %q", setting[0], got, err, setting[1])
		}
	}
}

// environmentGitConfig returns the GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n
// pairs in index order. It fails when a variable appears twice, which would
// let one entry hide another, or when the pairs do not match the count.
func environmentGitConfig(t *testing.T, environment []string) [][2]string {
	t.Helper()
	values := map[string]string{}
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "GIT_CONFIG_") || name == "GIT_CONFIG_NOSYSTEM" || name == "GIT_CONFIG_SYSTEM" || name == "GIT_CONFIG_GLOBAL" {
			continue
		}
		if _, seen := values[name]; seen {
			t.Fatalf("%s appears twice in %v", name, environment)
		}
		values[name] = value
	}
	count, err := strconv.Atoi(values["GIT_CONFIG_COUNT"])
	if err != nil {
		t.Fatalf("GIT_CONFIG_COUNT=%q: %v", values["GIT_CONFIG_COUNT"], err)
	}
	if len(values) != 1+2*count {
		t.Fatalf("GIT_CONFIG_COUNT=%d does not match the entries %v", count, values)
	}
	config := make([][2]string, 0, count)
	for i := 0; i < count; i++ {
		key, keyOK := values["GIT_CONFIG_KEY_"+strconv.Itoa(i)]
		value, valueOK := values["GIT_CONFIG_VALUE_"+strconv.Itoa(i)]
		if !keyOK || !valueOK {
			t.Fatalf("entry %d is incomplete in %v", i, values)
		}
		config = append(config, [2]string{key, value})
	}
	return config
}
