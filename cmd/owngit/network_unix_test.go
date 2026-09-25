//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/state"
)

// TestNetworkServeHelperProcess is not a test: it runs serve for
// TestNetworkShowAfterKill9WithAForeignLockHolder in a process that test
// can kill.
func TestNetworkServeHelperProcess(t *testing.T) {
	stateDir := os.Getenv("OWNGIT_NETWORK_SERVE_HELPER_STATE")
	if stateDir == "" {
		t.Skip("helper process only")
	}
	_ = serveWithContext(context.Background(), []string{"--state-dir", stateDir, "--listen", "127.0.0.1:0", "--no-open"},
		func(string) error { return nil }, func(string, ...any) {})
}

// A server killed with SIGKILL leaves its running record. When another
// process then holds the state directory, as an OwnGit 1.0.3 server or an
// offline backup would, "network show" must not report the dead server.
func TestNetworkShowAfterKill9WithAForeignLockHolder(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	child := exec.Command(os.Args[0], "-test.run=^TestNetworkServeHelperProcess$")
	child.Env = append(os.Environ(), "OWNGIT_NETWORK_SERVE_HELPER_STATE="+stateDir)
	noErr(t, child.Start())
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(stateDir, "owngit.sqlite")); err == nil {
			if report := networkJSON(t, stateDir); report.Server == "running" && report.Running.PID == child.Process.Pid {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper server did not report running")
		}
		time.Sleep(50 * time.Millisecond)
	}
	noErr(t, child.Process.Kill())
	_ = child.Wait()
	killed = true

	release, err := state.AcquireOfflineLock(stateDir)
	noErr(t, err)
	defer release()
	report := networkJSON(t, stateDir)
	if report.Server != "unknown" || report.Running != nil || !report.StaleRecord || report.RestartNeeded {
		t.Fatalf("after kill -9 with a foreign lock holder: %+v running=%+v", report, report.Running)
	}
}
