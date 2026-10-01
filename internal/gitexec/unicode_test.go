package gitexec

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunnerKeepsExactNamesWithoutChangingRepositoryConfig(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runner, err := New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	repositoryPath := filepath.Join(root, "project.git")
	_, err = runner.Run(ctx, "", nil, "init", "--bare", repositoryPath)
	noErr(t, err)
	for _, setting := range [][2]string{{"core.precomposeUnicode", "true"}, {"credential.helper", "owner-helper"}} {
		_, err = runner.Run(ctx, repositoryPath, nil, "config", "--local", setting[0], setting[1])
		noErr(t, err)
	}
	configPath := filepath.Join(repositoryPath, "config")
	before, err := os.ReadFile(configPath)
	noErr(t, err)
	args := []string{"config", "--get", "core.precomposeUnicode"}
	extra := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=Caller"}
	for name, run := range map[string]func() (Result, error){
		"run": func() (Result, error) { return runner.Run(ctx, repositoryPath, nil, args...) },
		"limits": func() (Result, error) {
			return runner.RunWithLimits(ctx, repositoryPath, nil, CommandLimits{Environment: extra}, args...)
		},
		"environment":  func() (Result, error) { return runner.RunWithEnvironment(ctx, repositoryPath, nil, extra, args...) },
		"output limit": func() (Result, error) { return runner.RunWithOutputLimit(ctx, repositoryPath, nil, 1024, args...) },
	} {
		t.Run(name, func(t *testing.T) {
			result, err := run()
			if err != nil || string(result.Stdout) != "false\n" {
				t.Fatalf("effective precomposition=%q err=%v", result.Stdout, err)
			}
		})
	}
	t.Run("stream", func(t *testing.T) {
		var output strings.Builder
		_, err := runner.StreamGit(ctx, repositoryPath, func(reader io.Reader) error {
			_, err := io.Copy(&output, reader)
			return err
		}, args...)
		if err != nil || output.String() != "false\n" {
			t.Fatalf("effective precomposition=%q err=%v", output.String(), err)
		}
	})

	// update-ref takes names on stdin without precomposing them. Its listing
	// still needs the command policy, including for refs created before startup.
	ref := "refs/heads/cafe\u0301"
	result, err := runner.Run(ctx, repositoryPath, nil, "mktree")
	noErr(t, err)
	tree := strings.TrimSpace(string(result.Stdout))
	result, err = runner.RunWithEnvironment(ctx, repositoryPath, strings.NewReader("exact names\n"), []string{
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	}, "commit-tree", tree)
	noErr(t, err)
	oid := strings.TrimSpace(string(result.Stdout))
	_, err = runner.RunPreparedUpdateContext(ctx, repositoryPath, []string{"create " + ref + " " + oid}, CommandLimits{}, func(context.Context) error { return nil })
	noErr(t, err)
	result, err = runner.Run(ctx, repositoryPath, nil, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil || string(result.Stdout) != ref+" "+oid+"\n" {
		t.Fatalf("prepared ref listing=%q err=%v", result.Stdout, err)
	}
	result, err = runner.Run(ctx, repositoryPath, nil, "config", "--get", "credential.helper")
	if err != nil || string(result.Stdout) != "owner-helper\n" {
		t.Fatalf("repository credential helper=%q err=%v", result.Stdout, err)
	}
	after, err := os.ReadFile(configPath)
	noErr(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("command policy changed the repository config")
	}
}
