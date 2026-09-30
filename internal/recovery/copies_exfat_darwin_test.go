package recovery

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var exfat = flag.Bool("exfat", false, "back up onto a new exFAT disk image, which cannot rename without replacing")

// With -args -exfat, a backup is written, verified and removed on an exFAT
// disk image that the test makes, mounts and detaches again.
func TestBackupOntoExFAT(t *testing.T) {
	if !*exfat {
		t.Skip("run with -args -exfat to make and mount an exFAT disk image")
	}
	image := filepath.Join(t.TempDir(), "exfat.dmg")
	mount := filepath.Join(t.TempDir(), "mount")
	noErr(t, os.Mkdir(mount, 0o700))
	hdiutil := func(arguments ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if output, err := exec.CommandContext(ctx, "hdiutil", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("hdiutil %v: %v\n%s", arguments, err, output)
		}
	}
	hdiutil("create", "-size", "256m", "-fs", "ExFAT", "-volname", "OWNGITTEST", image)
	hdiutil("attach", "-nobrowse", "-mountpoint", mount, image)
	defer hdiutil("detach", "-force", mount)
	destination := filepath.Join(mount, "backups")
	noErr(t, os.Mkdir(destination, 0o700))
	backUpOntoFolder(t, destination)

	// A restore publishes only with the exclusive rename, so a repository
	// folder there is refused before any work, naming the file system.
	backup := newTwoRepositoryBackup(t, t.TempDir())
	stateTarget := filepath.Join(t.TempDir(), "state")
	repositoryTarget := filepath.Join(destination, "repositories")
	err := Restore(context.Background(), backup, stateTarget, repositoryTarget, "")
	if err == nil || !strings.Contains(err.Error(), "(exfat)") {
		t.Fatalf("restore onto exFAT: %v", err)
	}
	for _, target := range []string{stateTarget, repositoryTarget} {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("%s exists after the refusal: %v", target, err)
		}
	}
	entries, err := os.ReadDir(destination)
	noErr(t, err)
	for _, entry := range entries {
		// macOS keeps its own records on a volume it mounted.
		if !strings.HasPrefix(entry.Name(), "._") {
			t.Fatalf("the refused restore left %s", entry.Name())
		}
	}
}
