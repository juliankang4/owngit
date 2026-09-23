package importsync

import (
	"os"
	"testing"
	"time"
)

// fakeFileInfo drives the mode-level guards without a filesystem fixture. It
// covers only the mode logic; the platform attribute check is exercised by
// the native Unix and Windows tests.
type fakeFileInfo struct {
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

func TestDirectPathGuardsRejectIndirectModes(t *testing.T) {
	if directRegularFile("", nil) || directDirectory("", nil) {
		t.Fatal("nil file info was accepted")
	}
	for _, mode := range []os.FileMode{os.ModeSymlink, os.ModeDir | os.ModeSymlink, os.ModeIrregular, os.ModeDir, os.ModeNamedPipe} {
		if directRegularFile("", fakeFileInfo{mode: mode}) {
			t.Fatalf("mode %v accepted as a direct regular file", mode)
		}
	}
	for _, mode := range []os.FileMode{os.ModeSymlink, os.ModeDir | os.ModeSymlink, os.ModeIrregular, 0} {
		if directDirectory("", fakeFileInfo{mode: mode}) {
			t.Fatalf("mode %v accepted as a direct directory", mode)
		}
	}
}
