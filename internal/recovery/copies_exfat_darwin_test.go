package recovery

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
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
}
