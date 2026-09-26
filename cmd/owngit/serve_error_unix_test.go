//go:build unix

package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe in place of the serve error file is neither read nor waited
// on.
func TestServeErrorIgnoresANamedPipe(t *testing.T) {
	stateDir := t.TempDir()
	noErr(t, syscall.Mkfifo(filepath.Join(stateDir, serveErrorFile), 0o600))
	done := make(chan bool, 1)
	go func() { _, found := serveErrorSince(stateDir, time.Time{}); done <- found }()
	select {
	case found := <-done:
		if found {
			t.Fatal("read a named pipe")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waited on a named pipe")
	}
}
