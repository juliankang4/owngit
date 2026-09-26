package service

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestGraphicalSessionReadsLogindSessions(t *testing.T) {
	dir := t.TempDir()
	if graphicalSession(filepath.Join(dir, "missing")) {
		t.Error("a missing session directory counts as graphical")
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("3", "# This is private data. Do not parse.\nUID=1000\nUSER=owner\nACTIVE=1\nSTATE=active\nREMOTE=1\nTYPE=tty\nCLASS=user\n")
	if err := syscall.Mkfifo(filepath.Join(dir, "3.ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	if graphicalSession(dir) {
		t.Error("an SSH (tty) session counts as graphical")
	}
	write("1", "UID=1000\nUSER=owner\nACTIVE=1\nSTATE=active\nTYPE=wayland\nCLASS=user\n")
	if !graphicalSession(dir) {
		t.Error("a Wayland session does not count as graphical")
	}
}
