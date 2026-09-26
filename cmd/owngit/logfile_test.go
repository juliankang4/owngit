package main

import (
	"os"
	"path/filepath"
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
