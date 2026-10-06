package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"owngit/internal/state"
	"owngit/internal/version"
)

// logFileLimit is the size at which the server log moves to FILE.1,
// replacing the older one, and a new file starts.
const logFileLimit = 10 << 20

// writeLogTo makes the standard logger write to path, kept near logFileLimit.
// A service's log goes only there: a service's own output is discarded (a
// Windows task) or kept in a file that nothing bounds (launchd, Homebrew), so
// it keeps only what the log cannot hold, such as a crash report and the
// lines that a log without room or a reopen cannot take. Otherwise the log
// goes to the earlier output as well. It returns a function that restores the
// logger and closes the file.
//
// The log opens only when its first lines are taken, by the file or by the
// output that takes what the file cannot: a line naming this run, and a
// warning for each protection that could not be applied. The older file keeps
// what it holds either way, so that alone does not keep OwnGit from starting.
// When nothing takes them the log is not opened: the error says why and what
// the lines said, and serve reports it, as for a log that cannot be opened. A
// log at its size cap, or that cannot rotate, never keeps OwnGit from
// starting (see rotatingFile).
func writeLogTo(path string, service bool) (func(), error) {
	file, err := openRotatingFile(path, logFileLimit)
	if err != nil {
		return nil, fmt.Errorf("open the log file: %w", err)
	}
	previous := log.Writer()
	restore := func() {
		log.SetOutput(previous)
		file.Close()
	}
	if service {
		log.SetOutput(fileOrStderr{file})
	} else {
		log.SetOutput(logAndEarlier{file: file, earlier: previous})
	}
	lines := append([]string{fmt.Sprintf("OwnGit %s (process %d) starts, logging to this file", version.Version, os.Getpid())}, file.warnings...)
	if err := file.protectOlder(); err != nil {
		lines = append(lines, fmt.Sprintf("the older log file could not be made private: %v", err))
	}
	for _, line := range lines {
		if err := log.Output(1, line); err != nil {
			restore()
			return nil, fmt.Errorf("write the log file: %w (it would have said: %s)", err, strings.Join(lines, "; "))
		}
	}
	return restore, nil
}

// logRotationRetry is how long a log that failed to rotate keeps growing
// before it tries again.
const logRotationRetry = time.Minute

// rotatingFile appends to a file and moves it aside at a size limit.
type rotatingFile struct {
	mu         sync.Mutex
	folder     *state.LogFolder
	dir        *os.File // the folder, held by folder
	failure    error    // why the log cannot rotate or reopen, or nil
	name       string
	limit      int64
	file       *os.File
	size       int64
	retryAfter time.Duration
	retryAt    time.Time // no rotation is tried before this time
	reported   bool      // the failure in failure is already said
	warnings   []string  // what opening could not make private, for the first lines
}

// openRotatingFile opens the log at path. Its folder, created when missing,
// is opened with state.OpenLogFolder and held, and the log file in it with
// state.OpenLogFile, so a log path in a folder of another account, or a
// link at the log's name, is refused before anything is written. Every
// later open and the rotation work in the held folder, whatever the path
// names by then.
//
// On macOS a folder that OwnGit creates loses an inherited access list, a new
// log file is made in a private staging folder and published only once it
// has no access list entry, and a log file that this version did not publish
// is replaced once by a private copy, so neither a new file nor a handle
// opened earlier lets another account read the lines that follow. A failure
// there is reported in the log's first lines; the file is still protected in
// place as before.
func openRotatingFile(path string, limit int64) (*rotatingFile, error) {
	folder, err := state.OpenLogFolder(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	dir := folder.Dir
	rotating := &rotatingFile{folder: folder, dir: dir, name: filepath.Base(path), limit: limit, retryAfter: logRotationRetry, warnings: folder.Notes}
	for _, name := range []string{rotating.name, rotating.name + ".1"} {
		if err := folder.Replace(name); err != nil {
			rotating.warnings = append(rotating.warnings, fmt.Sprintf("%s could not be replaced by a private copy: %v", name, err))
		}
	}
	if err := rotating.open(); err != nil {
		folder.Close()
		return nil, err
	}
	return rotating, nil
}

// protectOlder makes the older log file beside the log private, when there
// is one.
func (rotating *rotatingFile) protectOlder() error {
	older, err := state.OpenOwnFile(rotating.dir, rotating.name+".1", os.O_RDONLY)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer older.Close()
	return protectLogFile(older)
}

// protectLogFile removes a macOS ACL through the held file handle. Other
// platforms keep the earlier mode-only log behavior of this scoped repair.
// An ACL refusal names the held file and preserves its repair command.
func protectLogFile(file *os.File) error {
	var err error
	if runtime.GOOS == "darwin" {
		err = state.ProtectPrivateHandle(file, false)
	} else {
		err = file.Chmod(0o600)
	}
	return reportLogProtectionError(file.Name(), err)
}

func reportLogProtectionError(path string, err error) error {
	return state.ExplainPrivateFileError(path, err)
}

// logAndEarlier writes each log line to the log file and to the earlier
// output, each whatever the other did. It reports the file's result: the
// file is the log that confirms the error that ends serve (see loggedError),
// and an earlier output that fails has nowhere else to say so.
type logAndEarlier struct {
	file    *rotatingFile
	earlier io.Writer
}

func (outputs logAndEarlier) Write(line []byte) (int, error) {
	_, earlierErr := outputs.earlier.Write(line)
	written, err := outputs.file.Write(line)
	if err != nil && earlierErr == nil {
		return len(line), nil
	}
	return written, err
}

// open opens the log file for appending and makes it private to the owner,
// whether it is new, was left by an earlier run, or was just rotated: a
// file created by a service manager, such as an earlier Homebrew service,
// can be readable by every account. A log that cannot be made private is
// not opened, since it would hold this run's causes where other accounts
// may read them; the error keeps OwnGit from starting and says why.
func (rotating *rotatingFile) open() error {
	file, err := state.OpenLogFile(rotating.dir, rotating.name)
	if err != nil {
		return err
	}
	if err := protectLogFile(file); err != nil {
		file.Close()
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	rotating.file, rotating.size = file, info.Size()
	return nil
}

// Write appends data. When rotation fails, the file keeps growing, up to
// twice its limit, the failure is said once to the log and to standard error,
// and the rotation is tried again after retryAfter, so the log recovers
// without a restart. A line that the file cannot take is returned as an error
// and the caller sends it elsewhere.
func (rotating *rotatingFile) Write(data []byte) (int, error) {
	rotating.mu.Lock()
	defer rotating.mu.Unlock()
	if rotating.dir == nil {
		return 0, errors.New("log file is closed")
	}
	retry := !time.Now().Before(rotating.retryAt)
	switch {
	case rotating.file != nil && rotating.size > 0 && rotating.size+int64(len(data)) > rotating.limit && retry:
		rotating.rotate()
	case rotating.file == nil && retry:
		rotating.reopen()
	}
	if rotating.file == nil || rotating.size+int64(len(data)) > 2*rotating.limit {
		cause := rotating.failure
		if cause == nil {
			cause = errors.New("the line is larger than twice the log file's limit")
		}
		return 0, fmt.Errorf("the log file takes no more lines: %w", cause)
	}
	written, err := rotating.file.Write(data)
	rotating.size += int64(written)
	return written, err
}

// rotate moves the log to its older name and starts a new file. On failure
// it keeps (or reopens) the current file and schedules a later try. The file
// is closed first, which Windows requires before a rename.
func (rotating *rotatingFile) rotate() {
	rotating.file.Close()
	rotating.file = nil
	err := state.RenameOwnFile(rotating.dir, rotating.name, rotating.name+".1")
	if err != nil {
		err = fmt.Errorf("rotate the log file: %w", err)
	}
	rotating.reopen()
	if err != nil {
		rotating.fail(err)
	}
}

// reopen opens the log file again after a rotation or a failed reopen.
func (rotating *rotatingFile) reopen() {
	if err := rotating.open(); err != nil {
		rotating.fail(fmt.Errorf("reopen the log file: %w", err))
		return
	}
	if rotating.failure == nil || rotating.file == nil {
		return
	}
	// A reopen after a failed rename keeps the old file, so only a file
	// under its limit counts as recovered.
	if rotating.size <= rotating.limit {
		rotating.failure, rotating.reported = nil, false
	}
}

// fail schedules the next try and says the failure once, until the log
// works again.
func (rotating *rotatingFile) fail(cause error) {
	rotating.failure = cause
	rotating.retryAt = time.Now().Add(rotating.retryAfter)
	if rotating.reported {
		return
	}
	rotating.reported = true
	rest := "standard error takes the rest"
	if runtime.GOOS == "windows" {
		rest = "the rest is dropped when this runs as a service, whose standard error is discarded"
	}
	notice := fmt.Sprintf("%s: %v; the log takes lines up to twice its limit and %s, and rotation is tried again in %s\n", time.Now().Format("2006/01/02 15:04:05"), cause, rest, rotating.retryAfter)
	if rotating.file != nil {
		rotating.file.WriteString(notice)
	}
	os.Stderr.WriteString(notice)
}

func (rotating *rotatingFile) Close() error {
	rotating.mu.Lock()
	defer rotating.mu.Unlock()
	var err error
	if rotating.file != nil {
		err = rotating.file.Close()
		rotating.file = nil
	}
	if rotating.dir != nil {
		err = errors.Join(err, rotating.folder.Close())
		rotating.dir = nil
	}
	return err
}

// fileOrStderr is a service's log: lines go to the file, and to standard
// error only when the file cannot take them.
type fileOrStderr struct{ file *rotatingFile }

func (output fileOrStderr) Write(line []byte) (int, error) {
	written, err := output.file.Write(line)
	if err != nil {
		return os.Stderr.Write(line)
	}
	return written, nil
}
