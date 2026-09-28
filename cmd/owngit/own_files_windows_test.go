//go:build windows

package main

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A junction at the state directory's name does not lead the serve error
// into the folder it points at.
func TestWindowsServeErrorIsNotRecordedThroughAJunction(t *testing.T) {
	target := t.TempDir()
	junction := filepath.Join(t.TempDir(), "state")
	linkFolder(t, target, junction)
	recordServeError(junction, errors.New("synthetic failure"))
	if _, err := os.Lstat(filepath.Join(target, serveErrorFile)); !os.IsNotExist(err) {
		t.Fatalf("the serve error was written through the junction: %v", err)
	}
}

// Nor does a junction at the log's folder lead the log there.
func TestWindowsLogIsNotOpenedThroughAJunction(t *testing.T) {
	target := t.TempDir()
	junction := filepath.Join(t.TempDir(), "logs")
	linkFolder(t, target, junction)
	previous := log.Writer()
	closeLog, err := writeLogTo(filepath.Join(junction, "owngit.log"), true)
	if err == nil {
		closeLog()
		log.SetOutput(previous)
		t.Error("the log opened through a junction")
	}
	if _, err := os.Lstat(filepath.Join(target, "owngit.log")); !os.IsNotExist(err) {
		t.Fatalf("the log was created through the junction: %v", err)
	}
}

// linkFolder makes link a junction to target.
func linkFolder(t *testing.T, target, link string) {
	t.Helper()
	if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
}
