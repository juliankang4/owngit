//go:build darwin || linux

package main

import (
	"context"
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
	ctx := context.Background()
	root := t.TempDir()
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
