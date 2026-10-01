package importsync

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDurableRefIdentityIsStableAndDetectsReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "HEAD.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	noErr(t, err)
	first, err := durableFileID(file)
	if err != nil {
		noErr(t, file.Close())
		if runtime.GOOS == "windows" {
			t.Fatal(err)
		}
		t.Skipf("known local filesystem required: %v", err)
	}
	noErr(t, file.Close())
	prefix := "unix:"
	if runtime.GOOS == "windows" {
		prefix = "windows:"
	}
	if !strings.HasPrefix(first, prefix) || directoryFileID(directory) == "" {
		t.Fatal("local volume did not provide durable identity")
	}
	file, err = os.Open(path)
	noErr(t, err)
	second, err := durableFileID(file)
	noErr(t, err)
	noErr(t, file.Close())
	if first != second {
		t.Fatal("reopening changed the file identity")
	}
	noErr(t, os.Rename(path, path+".original"))
	file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	noErr(t, err)
	replacement, err := durableFileID(file)
	noErr(t, err)
	noErr(t, file.Close())
	if replacement == first {
		t.Fatal("replacement reused the still-existing original file identity")
	}
}
