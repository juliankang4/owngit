package state

import (
	"os"
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
