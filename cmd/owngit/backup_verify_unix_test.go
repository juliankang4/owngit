//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/recovery"
	"owngit/internal/repository"
	"owngit/internal/state"
)

const interruptChildArgumentsEnv = "OWNGIT_TEST_INTERRUPT_ARGUMENTS"

// TestInterruptHelperProcess is the child of the interrupt test: it runs
// the command it is given and exits as owngit would.
func TestInterruptHelperProcess(t *testing.T) {
	arguments := os.Getenv(interruptChildArgumentsEnv)
	if arguments == "" {
		t.Skip("runs only as the child of TestInterruptedRestoreAndVerifyExit130")
	}
	command := strings.Split(arguments, "\n")
	os.Exit(reportError(os.Stdout, runCommand(command[0], command[1:])))
}

// An interrupt stops backup verify, restore and restore --verify alike:
// Git stops, the rehearsal folder and the restore targets are gone, the
// message says the work was interrupted, and the exit status is 130. A Git
// wrapper holds the work at git fsck until the interrupt comes, so the test
// does not depend on timing.
func TestInterruptedRestoreAndVerifyExit130(t *testing.T) {
	root := t.TempDir()
	backup := newVerifyBackup(t, root)
	realGit, err := exec.LookPath("git")
	noErr(t, err)
	for _, test := range []struct{ name, want string }{
		{"backup verify", "the verification was interrupted"},
		{"restore", "the restore was interrupted: nothing was restored"},
		{"restore --verify", "the verification was interrupted: nothing was restored"},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := t.TempDir()
			marker := filepath.Join(work, "fsck-started")
			wrapper := filepath.Join(work, "git-wrapper")
			script := "#!/bin/sh\nfor argument in \"$@\"; do\n  if [ \"$argument\" = fsck ]; then : > '" + marker + "'; exec sleep 60; fi\ndone\nexec '" + realGit + "' \"$@\"\n"
			noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
			temporary := filepath.Join(work, "temporary")
			noErr(t, os.Mkdir(temporary, 0o700))
			restoredState := filepath.Join(work, "restored", "state")
			restoredRepositories := filepath.Join(work, "restored", "repositories")
			arguments := map[string][]string{
				"backup verify":    {"backup", "verify", backup, "--temp-dir", temporary, "--git", wrapper},
				"restore":          {"restore", "--input", backup, "--state-dir", restoredState, "--repository-root", restoredRepositories, "--git", wrapper},
				"restore --verify": {"restore", "--input", backup, "--state-dir", restoredState, "--repository-root", restoredRepositories, "--verify", "--temp-dir", temporary, "--git", wrapper},
			}[test.name]
			child := exec.Command(os.Args[0], "-test.run=^TestInterruptHelperProcess$")
			child.Env = append(os.Environ(), interruptChildArgumentsEnv+"="+strings.Join(arguments, "\n"))
			var output bytes.Buffer
			child.Stdout, child.Stderr = &output, &output
			noErr(t, child.Start())
			for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = child.Process.Kill()
					_ = child.Wait()
					t.Fatalf("the work never reached git fsck: %s", output.String())
				}
			}
			noErr(t, child.Process.Signal(syscall.SIGINT))
			err := child.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 130 || !strings.Contains(output.String(), test.want) || strings.Contains(output.String(), "context canceled") {
				t.Fatalf("child exit=%v, output:\n%s", err, output.String())
			}
			assertEmptyFolder(t, temporary)
			// The restore creates the targets' missing parent and leaves it
			// empty: no target and no stage.
			if entries, err := os.ReadDir(filepath.Dir(restoredState)); err == nil && len(entries) != 0 || err != nil && !os.IsNotExist(err) {
				t.Fatalf("the interrupted work left %v (%v)", entries, err)
			}
		})
	}
}

// assertEmptyFolder stops the test unless dir holds nothing.
func assertEmptyFolder(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("%s holds %v (%v)", dir, entries, err)
	}
}

// newVerifyBackup backs up a state with one empty repository into
// root/backup.
func newVerifyBackup(t *testing.T, root string) string {
	t.Helper()
	ctx := context.Background()
	stateDir := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, false))
	runner, err := gitexec.New("", filepath.Join(stateDir, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	_, err = manager.Create(ctx, "project", "")
	store.Close()
	noErr(t, err)
	backup := filepath.Join(root, "backup")
	if _, err := captureStdout(func() error { return backupState([]string{"--state-dir", stateDir, "--output", backup}) }); err != nil {
		t.Fatal(err)
	}
	return backup
}

// A rehearsal whose folder cannot be removed is not verified, in the human
// and the JSON result, exits 1, and restore --verify then creates nothing
// at its targets. The fault: once the rehearsal folder exists, the
// temporary folder no longer lets anything be removed from it.
func TestBackupVerifyWhoseFolderStaysIsNotVerified(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("folder modes do not stop root")
	}
	root := t.TempDir()
	backup := newVerifyBackup(t, root)
	restoredState := filepath.Join(root, "missing", "state")
	restoredRepositories := filepath.Join(root, "missing-too", "repositories")
	for _, run := range []struct {
		name  string
		run   func(temporary string) error
		check func(t *testing.T, output string, err error)
	}{
		{"human", func(temporary string) error {
			return backupState([]string{"verify", backup, "--temp-dir", temporary})
		}, func(t *testing.T, output string, err error) {
			var exit *checkExit
			if !errors.As(err, &exit) || exit.code != 1 || !strings.Contains(output, "Not verified: ") || !strings.Contains(output, "remove the rehearsal folder") || strings.Contains(output, "Verified: ") {
				t.Fatalf("output=%s err=%v", output, err)
			}
		}},
		{"json", func(temporary string) error {
			return backupState([]string{"verify", backup, "--temp-dir", temporary, "--json"})
		}, func(t *testing.T, output string, err error) {
			var exit *checkExit
			var result recovery.Verification
			if !errors.As(err, &exit) || exit.code != 1 || json.Unmarshal([]byte(output), &result) != nil || result.Verified || result.CleanupError == "" || result.Error == "" {
				t.Fatalf("output=%s err=%v", output, err)
			}
		}},
		{"restore --verify", func(temporary string) error {
			return restoreState([]string{"--input", backup, "--state-dir", restoredState, "--repository-root", restoredRepositories, "--verify", "--temp-dir", temporary})
		}, func(t *testing.T, output string, err error) {
			if err == nil || !strings.Contains(output, "Not verified: ") {
				t.Fatalf("output=%s err=%v", output, err)
			}
			for _, folder := range []string{filepath.Dir(restoredState), filepath.Dir(restoredRepositories)} {
				if _, err := os.Lstat(folder); !os.IsNotExist(err) {
					t.Fatalf("restore created %s (%v)", folder, err)
				}
			}
		}},
	} {
		t.Run(run.name, func(t *testing.T) {
			temporary := filepath.Join(t.TempDir(), "temporary")
			noErr(t, os.Mkdir(temporary, 0o700))
			done := make(chan struct{})
			go func() {
				defer close(done)
				for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					if entries, _ := os.ReadDir(temporary); len(entries) != 0 {
						_ = os.Chmod(temporary, 0o500)
						return
					}
				}
			}()
			output, err := captureStdout(func() error { return run.run(temporary) })
			<-done
			noErr(t, os.Chmod(temporary, 0o700))
			run.check(t, output, err)
		})
	}
}
