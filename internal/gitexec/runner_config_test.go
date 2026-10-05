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

	"owngit/internal/hostmem"
)

// wantCommandConfig is the configuration every Git command must receive,
// including the packing bounds for this computer's memory.
func wantCommandConfig() [][2]string {
	want := [][2]string{
		{"maintenance.auto", "false"},
		{"gc.auto", "0"},
		{"receive.autogc", "false"},
		{"core.precomposeUnicode", "false"},
	}
	if runtime.GOOS == "windows" {
		want = append(want, [2]string{"core.longpaths", "true"})
	}
	want = append(want, hostmem.PackingConfig(hostmem.Ceiling(), runtime.NumCPU(), hostmem.DefaultPackers(hostmem.Ceiling()))...)
	if threshold := hostmem.BigFileThreshold(hostmem.Ceiling(), hostmem.DefaultPackers(hostmem.Ceiling())); threshold != "" {
		want = append(want, [2]string{"core.bigFileThreshold", threshold})
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

// A large-file threshold changes what a diff or merge shows, so Git commands
// that read text output get none; every other command, archive included,
// gets one where the memory ceiling is known.
func TestLargeFileThresholdSkipsOnlyTextReadingCommands(t *testing.T) {
	runner, err := New("", filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	for _, test := range []struct {
		args []string
		text bool
	}{
		{[]string{"diff", "--numstat"}, true}, {[]string{"--git-dir", ".", "diff-tree", "x"}, true},
		{[]string{"log"}, true}, {[]string{"show", "x"}, true}, {[]string{"blame", "f"}, true},
		{[]string{"format-patch", "x"}, true}, {[]string{"merge-tree", "a", "b"}, true},
		{[]string{"merge-file", "a", "b", "c"}, true}, {[]string{"apply", "p"}, true},
		{[]string{"archive", "HEAD"}, false}, {[]string{"cat-file", "blob", "x"}, false},
		{[]string{"bundle", "create", "x", "--all"}, false}, {[]string{"-C", ".", "repack", "-d"}, false},
		{[]string{"index-pack", "x.pack"}, false},
	} {
		name := commandName(test.args)
		if got := readsTextOutput(name); got != test.text {
			t.Errorf("readsTextOutput(%q) = %v, want %v", name, got, test.text)
		}
		has := false
		for _, setting := range environmentGitConfig(t, runner.environment(readsTextOutput(name))) {
			has = has || setting[0] == "core.bigFileThreshold"
		}
		if want := !test.text && hostmem.Ceiling() > 0; has != want {
			t.Errorf("%s: core.bigFileThreshold set = %v, want %v", name, has, want)
		}
	}
}

// An archive has the same file contents with and without the threshold.
func TestArchiveIsTheSameWithTheLargeFileThreshold(t *testing.T) {
	ctx := context.Background()
	runner, err := New("", filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	dir := filepath.Join(t.TempDir(), "r.git")
	_, err = runner.Run(ctx, "", nil, "init", "--bare", "-q", "--initial-branch=main", dir)
	noErr(t, err)
	blob, err := runner.Run(ctx, dir, strings.NewReader(strings.Repeat("line of text\n", 1000)), "--git-dir", ".", "hash-object", "-w", "--stdin")
	noErr(t, err)
	tree, err := runner.Run(ctx, dir, strings.NewReader("100644 blob "+strings.TrimSpace(string(blob.Stdout))+"\tf.txt\n"), "--git-dir", ".", "mktree")
	noErr(t, err)
	archive := func(extra ...string) string {
		result, err := runner.RunWithEnvironment(ctx, dir, nil, extra, "--git-dir", ".", "archive", "--format=tar", strings.TrimSpace(string(tree.Stdout)))
		noErr(t, err)
		return string(result.Stdout)
	}
	small := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.bigFileThreshold", "GIT_CONFIG_VALUE_0=1024"}
	if archive() != archive(small...) {
		t.Error("archive differs with core.bigFileThreshold set")
	}
}
