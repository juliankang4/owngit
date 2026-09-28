package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestRotatingLogFileKeepsOneOlderFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "service.log")
	file, err := openRotatingFile(path, 100)
	noErr(t, err)
	line := strings.Repeat("x", 39) + "\n"
	for range 6 {
		if _, err := file.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	noErr(t, file.Close())
	current, err := os.ReadFile(path)
	noErr(t, err)
	older, err := os.ReadFile(path + ".1")
	noErr(t, err)
	// 6 lines of 40 bytes with a limit of 100: two per file, the last two
	// in the current file and two before them in the older one.
	if len(current) != 80 || len(older) != 80 {
		t.Errorf("current %d bytes, older %d bytes", len(current), len(older))
	}
	// Reopening appends.
	file, err = openRotatingFile(path, 1000)
	noErr(t, err)
	_, err = file.Write([]byte(line))
	noErr(t, err)
	noErr(t, file.Close())
	if content, _ := os.ReadFile(path); len(content) != 120 {
		t.Errorf("after reopening: %d bytes", len(content))
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("log file mode %v", info.Mode().Perm())
	}
}

// A service's log goes only to its log file, which is bounded; its standard
// error is discarded or a file nothing bounds. Without --service the log
// goes to both.
func TestServiceLogGoesOnlyToTheLogFile(t *testing.T) {
	for _, service := range []bool{true, false} {
		var earlier bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&earlier)
		path := filepath.Join(t.TempDir(), "owngit.log")
		closeLog, err := writeLogTo(path, service)
		noErr(t, err)
		log.Print("a server line")
		closeLog()
		restored := log.Writer()
		log.SetOutput(previous)
		file, err := os.ReadFile(path)
		noErr(t, err)
		if !strings.Contains(string(file), "a server line") || strings.Contains(earlier.String(), "a server line") == service || restored != &earlier {
			t.Fatalf("service=%v: file %q, earlier output %q", service, file, earlier.String())
		}
	}
}

// A service that keeps failing after its log is open writes each failure
// once, to that log, and nothing to its own output, which nothing bounds.
// When the log cannot be opened, its own output is the only place left.
func TestServiceErrorIsWrittenOnceToItsLog(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	logFile := filepath.Join(t.TempDir(), "owngit.log")
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	serve := func(logFile string) int {
		err := serveWithContext(context.Background(), []string{
			"--state-dir", stateDir, "--listen", "not an address", "--no-open", "--service", "--log-file", logFile,
		}, func(string) error { return nil }, log.Printf)
		if err == nil {
			t.Fatal("serve with an invalid address succeeded")
		}
		return reportError(io.Discard, err)
	}
	for range 10 {
		if code := serve(logFile); code != 1 {
			t.Fatalf("exit code %d", code)
		}
	}
	written, err := os.ReadFile(logFile)
	noErr(t, err)
	if output.Len() != 0 || strings.Count(string(written), "error: ") != 10 {
		t.Fatalf("own output %q; log has %d errors", output.String(), strings.Count(string(written), "error: "))
	}
	blocked := filepath.Join(t.TempDir(), "file")
	noErr(t, os.WriteFile(blocked, nil, 0o600))
	serve(filepath.Join(blocked, "owngit.log"))
	if strings.Count(output.String(), "error: open the log file") != 1 {
		t.Fatalf("an unopenable log was reported as %q", output.String())
	}
}

// The log file is private to its owner whether it is new, was left readable
// by an earlier service manager, or was just rotated, and so is the older
// file beside it, from the moment the log opens.
func TestLogFileIsMadePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are Unix permissions")
	}
	path := filepath.Join(t.TempDir(), "owngit.log")
	for _, name := range []string{path, path + ".1"} {
		noErr(t, os.WriteFile(name, []byte("an earlier line\n"), 0o644))
		noErr(t, os.Chmod(name, 0o644))
	}
	requireMode := func(path string) {
		t.Helper()
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v, %v", path, info, err)
		}
	}
	previous := log.Writer()
	closeLog, err := writeLogTo(path, true)
	noErr(t, err)
	closeLog()
	log.SetOutput(previous)
	requireMode(path)
	requireMode(path + ".1")

	file, err := openRotatingFile(path, 40)
	noErr(t, err)
	_, err = file.Write([]byte(strings.Repeat("x", 39) + "\n"))
	noErr(t, err)
	noErr(t, file.Close())
	requireMode(path)
	requireMode(path + ".1")
}

// An older log file that cannot be made private keeps what it holds either
// way, so the log still opens and says why in its first line.
func TestOlderLogThatStaysReadableIsLogged(t *testing.T) {
	chflags, err := exec.LookPath("chflags")
	if err != nil {
		t.Skip("no chflags to make a file unchangeable without privileges")
	}
	path := filepath.Join(t.TempDir(), "owngit.log")
	noErr(t, os.WriteFile(path+".1", []byte("an earlier line\n"), 0o644))
	noErr(t, os.Chmod(path+".1", 0o644))
	if output, err := exec.Command(chflags, "uchg", path+".1").CombinedOutput(); err != nil {
		t.Skipf("chflags uchg: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command(chflags, "nouchg", path+".1").Run() })
	previous := log.Writer()
	closeLog, err := writeLogTo(path, true)
	if err != nil {
		t.Fatalf("the log did not open: %v", err)
	}
	closeLog()
	log.SetOutput(previous)
	written, err := os.ReadFile(path)
	noErr(t, err)
	if !strings.Contains(string(written), " starts, logging to this file") || !strings.Contains(string(written), "the older log file could not be made private: chmod "+path+".1: ") {
		t.Fatalf("log: %q", written)
	}
}

// A log opens only when it takes its first lines. A full log whose rotation
// fails, because the older file cannot be replaced, is not opened; serve
// reports why and what the lines said to its own output.
func TestLogThatCannotTakeItsFirstLinesIsNotOpened(t *testing.T) {
	chflags, err := exec.LookPath("chflags")
	if err != nil {
		t.Skip("no chflags to make a file unchangeable without privileges")
	}
	dir := t.TempDir()
	logFile := filepath.Join(dir, "owngit.log")
	noErr(t, os.WriteFile(logFile, nil, 0o600))
	noErr(t, os.Truncate(logFile, logFileLimit))
	noErr(t, os.WriteFile(logFile+".1", []byte("an earlier line\n"), 0o644))
	noErr(t, os.Chmod(logFile+".1", 0o644))
	if output, err := exec.Command(chflags, "uchg", logFile+".1").CombinedOutput(); err != nil {
		t.Skipf("chflags uchg: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command(chflags, "nouchg", logFile+".1").Run() })
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	err = serveWithContext(context.Background(), []string{
		"--state-dir", filepath.Join(dir, "state"), "--listen", "not an address", "--no-open", "--service", "--log-file", logFile,
	}, func(string) error { return nil }, log.Printf)
	if err == nil {
		t.Fatal("serve started with a log that cannot be written")
	}
	if log.Writer() != &output {
		t.Fatal("the logger was left writing to the unopened log")
	}
	reportError(io.Discard, err)
	for _, want := range []string{"error: write the log file: rotate the log file: ", "the older log file could not be made private: chmod " + logFile + ".1: "} {
		if strings.Count(output.String(), want) != 1 {
			t.Fatalf("output lacks %q: %q", want, output.String())
		}
	}
}

// In a run in the foreground the log goes to the file and to the earlier
// output, each whatever the other did: an earlier output that fails does
// not keep the file from its lines or from confirming the ending error.
func TestLogFileIsWrittenWhenTheEarlierOutputFails(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "owngit.log")
	previous := log.Writer()
	log.SetOutput(failingWriter{})
	defer log.SetOutput(previous)
	err := serveWithContext(context.Background(), []string{
		"--state-dir", filepath.Join(dir, "state"), "--listen", "not an address", "--no-open", "--log-file", logFile,
	}, func(string) error { return nil }, log.Printf)
	var logged loggedError
	if err == nil || !errors.As(err, &logged) {
		t.Fatalf("the ending error was not confirmed by the log file: %v", err)
	}
	written, readErr := os.ReadFile(logFile)
	noErr(t, readErr)
	if !strings.Contains(string(written), "error: "+logged.error.Error()) {
		t.Fatalf("log: %q", written)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("standard error is closed") }

// When the log that serve opened cannot take the error that ends serve, the
// error is not lost: main writes it to its own output, with why the log
// could not.
func TestServiceErrorTheLogCouldNotTakeIsWrittenToItsOutput(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "owngit.log")
	// A log with room for its first line only, whose rotation then fails:
	// the older file's place is taken.
	noErr(t, os.WriteFile(logFile, nil, 0o600))
	noErr(t, os.Truncate(logFile, logFileLimit-100))
	noErr(t, os.Mkdir(logFile+".1", 0o700))
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	err := serveWithContext(context.Background(), []string{
		"--state-dir", filepath.Join(dir, "state"), "--listen", "not an address", "--no-open", "--service", "--log-file", logFile,
	}, func(string) error { return nil }, log.Printf)
	if err == nil {
		t.Fatal("serve with an invalid address succeeded")
	}
	if code := reportError(io.Discard, err); code != 1 || strings.Count(output.String(), "error: ") != 1 ||
		!strings.Contains(output.String(), "not an address") || !strings.Contains(output.String(), "the log file did not record this: rotate the log file") {
		t.Fatalf("exit %d, output %q", code, output.String())
	}
}

// An error the log confirmed is written nowhere else, even one that a
// command would otherwise print as JSON.
func TestLoggedErrorIsNotPrintedAgain(t *testing.T) {
	var stdout, output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	if code := reportError(&stdout, loggedError{codedError{}}); code != 1 || stdout.Len() != 0 || output.Len() != 0 {
		t.Fatalf("exit %d, stdout %q, output %q", code, stdout.String(), output.String())
	}
	if reportError(&stdout, codedError{}); !strings.Contains(stdout.String(), `"code":"synthetic"`) || output.Len() != 0 {
		t.Fatalf("a coded error was reported as stdout %q, output %q", stdout.String(), output.String())
	}
}

type codedError struct{}

func (codedError) Error() string                 { return "synthetic failure" }
func (codedError) ErrorCode() string             { return "synthetic" }
func (codedError) ErrorDetails() json.RawMessage { return nil }
