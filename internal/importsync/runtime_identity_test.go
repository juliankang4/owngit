package importsync

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReplacedRuntimeDirectoryEndsOwnership replaces <state>/runtime with a new
// directory that holds the original import-staging directory, so the lock
// file, the marker and the staging root are all unchanged and only the runtime
// directory identity reveals the swap. That identity must describe the
// directory Prepare inspected, not whatever the path names at the first
// revalidation.
func TestReplacedRuntimeDirectoryEndsOwnership(t *testing.T) {
	f := newFixture(t)
	f.prepareRuntime(t)
	runtimeRoot := f.service.runtimeRootPath()
	moved := runtimeRoot + "-moved"
	if err := os.Rename(runtimeRoot, moved); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows refuses to move the runtime directory while the lease handles inside it are open: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(moved, filepath.Base(f.service.stagingRootPath())), f.service.stagingRootPath()); err != nil {
		t.Fatal(err)
	}

	root, prepared := f.service.preparedRuntime()
	if !prepared {
		t.Fatal("runtime was not prepared")
	}
	owned, err := root.stillOwned()
	if owned || err == nil || !strings.Contains(err.Error(), "import runtime directory was replaced") {
		t.Fatalf("stillOwned after the runtime directory was replaced: owned=%v err=%v", owned, err)
	}
}
