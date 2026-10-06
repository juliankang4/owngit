package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A failed rotation keeps the log taking lines up to twice its limit, says so
// once, and rotation works again once the cause is gone, without a restart.
func TestLogRotationFailureKeepsWritingAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owngit.log")
	obstruction := filepath.Join(path+".1", "keep")
	noErr(t, os.MkdirAll(obstruction, 0o700))
	file, err := openRotatingFile(path, 400)
	noErr(t, err)
	file.retryAfter = 0
	line := strings.Repeat("x", 39) + "\n"
	var refused int
	for range 30 {
		if _, err := file.Write([]byte(line)); err != nil {
			refused++
		}
	}
	content, _ := os.ReadFile(path)
	if refused == 0 || len(content) > 800+len(file.failure.Error())+200 || strings.Count(string(content), "rotate the log file") != 1 || strings.Count(string(content), line) < 10 {
		t.Fatalf("refused %d lines; current log %d bytes: %q", refused, len(content), content)
	}
	noErr(t, os.Remove(obstruction))
	noErr(t, os.Remove(path+".1"))
	if _, err := file.Write([]byte(line)); err != nil {
		t.Fatalf("the log did not recover: %v", err)
	}
	noErr(t, file.Close())
	if older, err := os.ReadFile(path + ".1"); err != nil || len(older) != len(content) {
		t.Errorf("rotation did not recover: older file %d bytes, %v", len(older), err)
	}
	if current, _ := os.ReadFile(path); string(current) != line {
		t.Errorf("current log after recovery: %q", current)
	}
}

// The error that ends serve is marked as logged when the log takes it, and
// says why when the log cannot.
func TestEndingErrorIsRecordedOrExplained(t *testing.T) {
	previous := log.Writer()
	defer log.SetOutput(previous)
	cause := errors.New("synthetic failure")
	log.SetOutput(io.Discard)
	if err := recordEndingError(cause); !errors.As(err, new(loggedError)) {
		t.Errorf("a recorded error was not marked: %v", err)
	}
	log.SetOutput(failingWriter{})
	err := recordEndingError(cause)
	if errors.As(err, new(loggedError)) || !strings.Contains(err.Error(), "the log file did not record this: standard error is closed") {
		t.Errorf("an unrecorded error was not explained: %v", err)
	}
}

// addInheritedACL gives the folder an entry that new files in it inherit.
func addInheritedACL(t *testing.T, folder string) {
	t.Helper()
	output, err := exec.Command("chmod", "+a", "nobody allow read,list,file_inherit,directory_inherit", folder).CombinedOutput()
	if err != nil {
		t.Fatalf("add inherited access entry: %v: %s", err, output)
	}
}

// macLogFolder is a folder, new in the test, that gives its files an
// inherited read entry.
func macLogFolder(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("macOS access lists")
	}
	folder := filepath.Join(t.TempDir(), "logs")
	noErr(t, os.Mkdir(folder, 0o700))
	addInheritedACL(t, folder)
	return folder
}

// On macOS a handle opened before the repair reaches the end of the retired
// file and never sees later lines. A shared folder keeps its access list and
// the log says so; a folder that OwnGit creates loses what it inherited.
func TestMacOSLogRepairRetiresOldHandlesAndOnlyOwnFolders(t *testing.T) {
	shared := macLogFolder(t)
	path := filepath.Join(shared, "owngit.log")
	var handles []*os.File
	for _, name := range []string{path, path + ".1"} {
		noErr(t, os.WriteFile(name, []byte("earlier output\n"), 0o600))
		handle, err := os.Open(name)
		noErr(t, err)
		defer handle.Close()
		if rest, _ := io.ReadAll(handle); string(rest) != "earlier output\n" {
			t.Fatalf("%s: %q", name, rest)
		}
		handles = append(handles, handle)
	}
	file, err := openRotatingFile(path, 1000)
	noErr(t, err)
	_, err = file.Write([]byte("later line\n"))
	noErr(t, err)
	noErr(t, file.Close())
	for _, handle := range handles {
		if rest, _ := io.ReadAll(handle); len(rest) != 0 {
			t.Errorf("a handle opened before the repair reads %s: %q", handle.Name(), rest)
		}
	}
	for _, name := range []string{path, path + ".1"} {
		if content, err := os.ReadFile(name); err != nil || !strings.HasPrefix(string(content), "earlier output\n") {
			t.Errorf("%s lost its content: %q, %v", name, content, err)
		}
	}
	if !macLogHasAllowACL(t, shared) || !strings.Contains(strings.Join(file.warnings, "\n"), "access list of") {
		t.Errorf("a shared folder must keep its access list and be named in the log: %q", file.warnings)
	}

	created := filepath.Join(shared, "own")
	file, err = openRotatingFile(filepath.Join(created, "owngit.log"), 1000)
	noErr(t, err)
	noErr(t, file.Close())
	if macLogHasAllowACL(t, created) {
		t.Errorf("a folder that OwnGit created kept the inherited entry:\n%s", macLogACL(t, created))
	}
}

// A second process on the same log leaves its files alone, so the running
// writer's later lines are not lost.
func TestMacOSSecondStartLeavesTheRunningLogAlone(t *testing.T) {
	folder := macLogFolder(t)
	path := filepath.Join(folder, "owngit.log")
	first, err := openRotatingFile(path, 1000)
	noErr(t, err)
	defer first.Close()
	_, err = first.Write([]byte("first before\n"))
	noErr(t, err)
	second, err := openRotatingFile(path, 1000)
	noErr(t, err)
	noErr(t, second.Close())
	_, err = first.Write([]byte("first after\n"))
	noErr(t, err)
	if content, _ := os.ReadFile(path); string(content) != "first before\nfirst after\n" {
		t.Errorf("the running writer's lines: %q", content)
	}
	if !strings.Contains(strings.Join(second.warnings, "\n"), "holds the lock") {
		t.Errorf("the second start did not say why it left the files: %q", second.warnings)
	}
}

// A log at its size cap whose rotation is blocked does not keep OwnGit from
// starting, as a service or in the foreground: a restart loop would
// otherwise end every record of why.
func TestFullLogWithBlockedRotationStillStarts(t *testing.T) {
	for _, service := range []bool{true, false} {
		dir := resolvedTempDir(t)
		logFile := filepath.Join(dir, "owngit.log")
		noErr(t, os.WriteFile(logFile, nil, 0o600))
		noErr(t, os.Truncate(logFile, 2*logFileLimit))
		noErr(t, os.Mkdir(logFile+".1", 0o700))
		arguments := []string{"--state-dir", filepath.Join(dir, "state"), "--listen", "not an address", "--no-open", "--log-file", logFile}
		if service {
			arguments = append(arguments, "--service")
		}
		previous := log.Writer()
		err := serveWithContext(context.Background(), arguments, func(string) error { return nil }, log.Printf)
		log.SetOutput(previous)
		if err == nil || !strings.Contains(err.Error(), "not an address") || strings.Contains(err.Error(), "log file") {
			t.Errorf("service %v: serve did not reach its own error: %v", service, err)
		}
	}
}
