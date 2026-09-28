package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// A second name that another program gave a file, put where the serve
// error goes, does not let serve empty or overwrite that file.
func TestServeErrorIsNotRecordedThroughASecondName(t *testing.T) {
	stateDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	noErr(t, os.WriteFile(target, []byte("kept\n"), 0o600))
	noErr(t, os.Link(target, filepath.Join(stateDir, serveErrorFile)))
	recordServeError(stateDir, errors.New("synthetic failure"))
	if content, err := os.ReadFile(target); err != nil || string(content) != "kept\n" {
		t.Fatalf("the file with the second name now holds %q (%v)", content, err)
	}
}

// Nor does such a name at the log's place make serve append to that file.
func TestLogIsNotOpenedThroughASecondName(t *testing.T) {
	target := filepath.Join(t.TempDir(), "elsewhere")
	noErr(t, os.WriteFile(target, []byte("kept\n"), 0o600))
	path := filepath.Join(t.TempDir(), "owngit.log")
	noErr(t, os.Link(target, path))
	previous := log.Writer()
	closeLog, err := writeLogTo(path, true)
	if err == nil {
		closeLog()
		log.SetOutput(previous)
		t.Error("the log opened through a second name")
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "kept\n" {
		t.Fatalf("the file with the second name now holds %q (%v)", content, err)
	}
}
