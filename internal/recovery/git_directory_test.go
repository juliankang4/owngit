package recovery

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestRecoveryUsesBareRepositoryWorkingDirectoryAtWindowsGitBoundaries(t *testing.T) {
	ctx := context.Background()
	sourceRoot := recoveryRepositoryRootAtLength(t, 221, "project")
	stateDirectory := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(ctx, stateDirectory)
	noErr(t, err)
	defer store.Close()
	adminHash, err := auth.HashPassword("synthetic-admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, sourceRoot, "open", "", adminHash, true))
	runner, err := gitexec.New("", filepath.Join(stateDirectory, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: sourceRoot}
	repositoryPath := filepath.Join(sourceRoot, "project.git")
	if len(repositoryPath) != 221 {
		t.Fatalf("source bare path length=%d want=221", len(repositoryPath))
	}
	noErr(t, os.Mkdir(repositoryPath, 0o700))
	if _, err := runner.Run(ctx, repositoryPath, nil, "init", "--bare", "--initial-branch=main", "."); err != nil {
		t.Fatal(err)
	}
	for _, setting := range [][2]string{{"user.name", "Recovery Boundary Test"}, {"user.email", "recovery@example.invalid"}} {
		if _, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "config", "--local", setting[0], setting[1]); err != nil {
			t.Fatal(err)
		}
	}
	blob, err := runner.Run(ctx, repositoryPath, strings.NewReader("boundary\n"), "--git-dir", ".", "hash-object", "-w", "--stdin")
	noErr(t, err)
	blobOID := strings.TrimSpace(string(blob.Stdout))
	treeInput := "100644 blob " + blobOID + "\tfile.txt\n"
	tree, err := runner.Run(ctx, repositoryPath, strings.NewReader(treeInput), "--git-dir", ".", "mktree")
	noErr(t, err)
	commit, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "commit-tree", strings.TrimSpace(string(tree.Stdout)), "-m", "boundary")
	noErr(t, err)
	commitOID := strings.TrimSpace(string(commit.Stdout))
	if _, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "update-ref", "refs/heads/main", commitOID); err != nil {
		t.Fatal(err)
	}
	noErr(t, store.AddRepository(ctx, state.Repository{ID: "project", Name: "project", Description: "Long bare path fixture", CreatedAt: time.Now().UTC().Truncate(time.Second)}))

	backupRunner := &recordingRecoveryRunner{delegate: runner}
	backup := filepath.Join(t.TempDir(), "backup")
	noErr(t, create(ctx, store, manager, backupRunner, backup))
	assertRecoveryBareCalls(t, backupRunner.calls, repositoryPath)
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if len(manifest.Repositories) != 1 || manifest.Repositories[0].Empty {
		t.Fatalf("manifest repositories=%+v", manifest.Repositories)
	}

	repositoryStage := recoveryRepositoryRootAtLength(t, 250, "project")
	restoreRunner := &recordingRecoveryRunner{delegate: runner}
	noErr(t, restoreRepository(ctx, restoreRunner, backup, repositoryStage, manifest.Repositories[0]))
	restoredPath := filepath.Join(repositoryStage, "project.git")
	if len(restoredPath) != 250 {
		t.Fatalf("restored bare path length=%d want=250", len(restoredPath))
	}
	assertRecoveryBareCalls(t, restoreRunner.calls, restoredPath)
}

type recoveryRunnerCall struct {
	directory string
	arguments []string
}

type recordingRecoveryRunner struct {
	delegate commandRunner
	calls    []recoveryRunnerCall
}

func (runner *recordingRecoveryRunner) Run(ctx context.Context, directory string, stdin io.Reader, arguments ...string) (gitexec.Result, error) {
	runner.calls = append(runner.calls, recoveryRunnerCall{directory: directory, arguments: append([]string(nil), arguments...)})
	return runner.delegate.Run(ctx, directory, stdin, arguments...)
}

func recoveryRepositoryRootAtLength(t *testing.T, target int, repositoryID string) string {
	t.Helper()
	base := t.TempDir()
	suffixLength := len(repositoryID) + len(".git") + 2
	componentLength := target - len(base) - suffixLength
	if componentLength <= 0 || componentLength > 240 {
		t.Fatalf("cannot construct %d-byte repository path below %q", target, base)
	}
	root := filepath.Join(base, strings.Repeat("r", componentLength))
	noErr(t, os.Mkdir(root, 0o700))
	if got := len(filepath.Join(root, repositoryID+".git")); got != target {
		t.Fatalf("constructed repository path length=%d want=%d", got, target)
	}
	return root
}

func assertRecoveryBareCalls(t *testing.T, calls []recoveryRunnerCall, repositoryPath string) {
	t.Helper()
	bareCalls := 0
	bundlePaths := 0
	for _, call := range calls {
		for index, argument := range call.arguments {
			if argument == "--git-dir" {
				bareCalls++
				if index+1 >= len(call.arguments) || call.arguments[index+1] != "." || call.directory != repositoryPath {
					t.Fatalf("bare Git call directory=%q arguments=%q", call.directory, call.arguments)
				}
			}
			if strings.HasSuffix(argument, ".bundle") {
				bundlePaths++
				if !filepath.IsAbs(argument) {
					t.Fatalf("bundle path is not absolute: %q", argument)
				}
			}
		}
	}
	if bareCalls == 0 || bundlePaths == 0 {
		t.Fatalf("calls did not cover bare Git and bundle paths: %+v", calls)
	}
}
