//go:build darwin || linux

package main

import (
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

const verifyChildArgumentsEnv = "OWNGIT_TEST_VERIFY_ARGUMENTS"

// TestBackupVerifyHelperProcess is the child of the interrupt test: it runs
// backup verify with the arguments it is given and exits as owngit would.
func TestBackupVerifyHelperProcess(t *testing.T) {
	arguments := os.Getenv(verifyChildArgumentsEnv)
	if arguments == "" {
		t.Skip("runs only as the child of TestBackupVerifyInterruptRemovesTheRehearsal")
	}
	os.Exit(reportError(os.Stdout, backupState(append([]string{"verify"}, strings.Split(arguments, "\n")...))))
}

// An interrupt during the rehearsal stops Git, removes the rehearsal folder
// and exits 130. A Git wrapper holds the rehearsal at git fsck until the
// interrupt comes, so the test does not depend on timing.
func TestBackupVerifyInterruptRemovesTheRehearsal(t *testing.T) {
	root := t.TempDir()
	backup := newVerifyBackup(t, root)

	realGit, err := exec.LookPath("git")
	noErr(t, err)
	marker := filepath.Join(root, "fsck-started")
	wrapper := filepath.Join(root, "git-wrapper")
	script := "#!/bin/sh\nfor argument in \"$@\"; do\n  if [ \"$argument\" = fsck ]; then : > '" + marker + "'; exec sleep 60; fi\ndone\nexec '" + realGit + "' \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	temporary := filepath.Join(root, "temporary")
	noErr(t, os.Mkdir(temporary, 0o700))

	child := exec.Command(os.Args[0], "-test.run=^TestBackupVerifyHelperProcess$")
	child.Env = append(os.Environ(), verifyChildArgumentsEnv+"="+strings.Join([]string{backup, "--temp-dir", temporary, "--git", wrapper}, "\n"))
	noErr(t, child.Start())
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = child.Process.Kill()
			_ = child.Wait()
			t.Fatal("the rehearsal never reached git fsck")
		}
		time.Sleep(10 * time.Millisecond)
	}
	noErr(t, child.Process.Signal(syscall.SIGINT))
	err = child.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 130 {
		t.Fatalf("child exit=%v, want 130", err)
	}
	if entries, err := os.ReadDir(temporary); err != nil || len(entries) != 0 {
		t.Fatalf("the interrupted rehearsal left %v (%v)", entries, err)
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
