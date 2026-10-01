package main

import (
	"cmp"
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

	"owngit/internal/state"
	"owngit/internal/version"
)

// logFileLimit is the size at which the server log moves to FILE.1,
// replacing the older one, and a new file starts.
const logFileLimit = 10 << 20

// writeLogTo makes the standard logger write to path, kept below
// logFileLimit. A service's log goes only there: a service's own output is
// discarded (a Windows task) or kept in a file that nothing bounds (launchd,
// Homebrew), so it keeps only what the log cannot hold, such as a crash
// report. Otherwise the log goes to the earlier output as well. It returns a
// function that restores the logger and closes the file.
//
// The log opens only when it takes its first lines: a line naming this run,
// and a warning when the older file beside it stays readable by others. The
// older file keeps what it holds either way, so that alone does not keep
// OwnGit from starting. A log that cannot take them, for example because it
// is full and cannot rotate, is not opened: the error says why and what the
// lines said, and serve reports it, as for a log that cannot be opened.
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
		log.SetOutput(file)
	} else {
		log.SetOutput(logAndEarlier{file: file, earlier: previous})
	}
	lines := []string{fmt.Sprintf("OwnGit %s (process %d) starts, logging to this file", version.Version, os.Getpid())}
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

// rotatingFile appends to a file and moves it aside at a size limit.
type rotatingFile struct {
	mu     sync.Mutex
	dir    *os.File // the folder, opened with state.OpenDirectory
	name   string
	limit  int64
	file   *os.File
	size   int64
	broken error // why a failed rotation left no file
}

// openRotatingFile opens the log at path. Its folder, created when missing,
// is opened with state.OpenDirectory and held, and the log file in it with
// state.OpenOwnFile, so a log path in a folder of another account, or a
// link at the log's name, is refused before anything is written. Every
// later open and the rotation work in the held folder, whatever the path
// names by then.
func openRotatingFile(path string, limit int64) (*rotatingFile, error) {
	dir, err := state.OpenDirectory(filepath.Dir(path), true)
	if err != nil {
		return nil, err
	}
	rotating := &rotatingFile{dir: dir, name: filepath.Base(path), limit: limit}
	if err := rotating.open(); err != nil {
		dir.Close()
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
	_, _ = outputs.earlier.Write(line)
	return outputs.file.Write(line)
}

// open opens the log file for appending and makes it private to the owner,
// whether it is new, was left by an earlier run, or was just rotated: a
// file created by a service manager, such as an earlier Homebrew service,
// can be readable by every account. A log that cannot be made private is
// not opened, since it would hold this run's causes where other accounts
// may read them; the error keeps OwnGit from starting and says why.
func (rotating *rotatingFile) open() error {
	file, err := state.OpenOwnFile(rotating.dir, rotating.name, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
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

func (rotating *rotatingFile) Write(data []byte) (int, error) {
	rotating.mu.Lock()
	defer rotating.mu.Unlock()
	if rotating.file == nil {
		return 0, cmp.Or(rotating.broken, errors.New("log file is closed"))
	}
	if rotating.size > 0 && rotating.size+int64(len(data)) > rotating.limit {
		rotating.file.Close()
		rotating.file = nil
		// A rotation that failed leaves no file to write to; every later
		// write reports why.
		if err := state.RenameOwnFile(rotating.dir, rotating.name, rotating.name+".1"); err != nil {
			rotating.broken = fmt.Errorf("rotate the log file: %w", err)
			return 0, rotating.broken
		}
		if err := rotating.open(); err != nil {
			rotating.broken = fmt.Errorf("reopen the log file: %w", err)
			return 0, rotating.broken
		}
	}
	written, err := rotating.file.Write(data)
	rotating.size += int64(written)
	return written, err
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
		err = errors.Join(err, rotating.dir.Close())
		rotating.dir = nil
	}
	return err
}
