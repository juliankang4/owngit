package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// The log stays in the folder it opened, held: after its folder's name is
// given to a link to another folder, its lines and its rotation still go
// to the folder it opened, and a log file in the other folder is left
// alone.
func TestLogStaysInTheFolderItOpened(t *testing.T) {
	root := t.TempDir()
	folder, other := filepath.Join(root, "logs"), filepath.Join(root, "other")
	noErr(t, os.Mkdir(folder, 0o700))
	noErr(t, os.Mkdir(other, 0o700))
	noErr(t, os.WriteFile(filepath.Join(other, "owngit.log"), []byte("kept\n"), 0o600))
	file, err := openRotatingFile(filepath.Join(folder, "owngit.log"), 40)
	noErr(t, err)
	moved := filepath.Join(root, "moved")
	err = os.Rename(folder, moved)
	if err != nil && runtime.GOOS == "windows" {
		// Windows keeps the name of a folder that holds an open file, so
		// the name cannot be given away while the log is open.
		file.Close()
		t.Skipf("the folder of the open log cannot be renamed: %v", err)
	}
	noErr(t, err)
	linkFolder(t, other, folder)
	for range 3 {
		_, err = file.Write([]byte(strings.Repeat("x", 39) + "\n"))
		noErr(t, err)
	}
	noErr(t, file.Close())
	if content, err := os.ReadFile(filepath.Join(other, "owngit.log")); err != nil || string(content) != "kept\n" {
		t.Fatalf("the other folder's log now holds %q (%v)", content, err)
	}
	if _, err := os.Lstat(filepath.Join(other, "owngit.log.1")); !os.IsNotExist(err) {
		t.Fatalf("the rotation renamed in the other folder: %v", err)
	}
	for _, name := range []string{"owngit.log", "owngit.log.1"} {
		if _, err := os.Stat(filepath.Join(moved, name)); err != nil {
			t.Fatalf("the log's own folder lacks %s: %v", name, err)
		}
	}
}
