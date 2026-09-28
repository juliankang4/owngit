//go:build unix

package main

import (
	"errors"
	"os"
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

// A link in place of the serve error file is not followed when the error is
// recorded, so whoever can put a link there cannot make serve overwrite the
// file it leads to.
func TestServeErrorIsNotRecordedThroughALink(t *testing.T) {
	stateDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	noErr(t, os.WriteFile(target, []byte("kept\n"), 0o600))
	noErr(t, os.Symlink(target, filepath.Join(stateDir, serveErrorFile)))
	recordServeError(stateDir, errors.New("synthetic failure"))
	if content, err := os.ReadFile(target); err != nil || string(content) != "kept\n" {
		t.Fatalf("the file behind the link now holds %q (%v)", content, err)
	}
}
