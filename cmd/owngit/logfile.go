package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// logFileLimit is the size at which the server log moves to FILE.1,
// replacing the older one, and a new file starts.
const logFileLimit = 10 << 20

// writeLogTo makes the standard logger write to path as well, for a
// service whose output nobody keeps. It returns a function that restores
// the logger and closes the file.
func writeLogTo(path string) (func(), error) {
	file, err := openRotatingFile(path, logFileLimit)
	if err != nil {
		return nil, fmt.Errorf("open the log file: %w", err)
	}
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(previous, file))
	return func() {
		log.SetOutput(previous)
		file.Close()
	}, nil
}

// rotatingFile appends to a file and moves it aside at a size limit.
type rotatingFile struct {
	mu    sync.Mutex
	path  string
	limit int64
	file  *os.File
	size  int64
}

func openRotatingFile(path string, limit int64) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	rotating := &rotatingFile{path: path, limit: limit}
	return rotating, rotating.open()
}

func (rotating *rotatingFile) open() error {
	file, err := os.OpenFile(rotating.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
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
		return 0, errors.New("log file is closed")
	}
	if rotating.size > 0 && rotating.size+int64(len(data)) > rotating.limit {
		rotating.file.Close()
		rotating.file = nil
		if err := os.Rename(rotating.path, rotating.path+".1"); err != nil {
			return 0, err
		}
		if err := rotating.open(); err != nil {
			return 0, err
		}
	}
	written, err := rotating.file.Write(data)
	rotating.size += int64(written)
	return written, err
}

func (rotating *rotatingFile) Close() error {
	rotating.mu.Lock()
	defer rotating.mu.Unlock()
	if rotating.file == nil {
		return nil
	}
	err := rotating.file.Close()
	rotating.file = nil
	return err
}
