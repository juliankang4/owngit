package state

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// Openers that create the same new file at once, such as serve and a
// command both taking a lock on a fresh state, all get the file.
func TestOwnFileCreatedByOpenersAtOnce(t *testing.T) {
	dir, err := openFolder(t.TempDir())
	noErr(t, err)
	defer dir.Close()
	for round := range 100 {
		name := "operation-" + strconv.Itoa(round) + ".lock"
		var wait sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				file, err := OpenOwnFile(dir, name, os.O_RDWR|os.O_CREATE)
				if err == nil {
					err = file.Close()
				}
				errs <- err
			}()
		}
		wait.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
}

// A folder where a file is expected is named as such on every platform,
// not reported as denied access, so a log's first lines say the same
// everywhere.
func TestOwnFileRefusesAFolderAsNotAFile(t *testing.T) {
	root := t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(root, "folder"), 0o700))
	dir, err := openFolder(root)
	noErr(t, err)
	defer dir.Close()
	for _, flag := range []int{os.O_RDONLY, os.O_WRONLY | os.O_APPEND | os.O_CREATE, os.O_RDWR | os.O_CREATE} {
		file, err := OpenOwnFile(dir, "folder", flag)
		if err == nil {
			file.Close()
		}
		if want := filepath.Join(dir.Name(), "folder") + " is not a regular file"; err == nil || err.Error() != want {
			t.Errorf("flag %#x: error=%v, want %q", flag, err, want)
		}
	}
}
