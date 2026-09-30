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

var diskImages = flag.Bool("diskimages", false, "back up and restore onto new exFAT, FAT16 and FAT32 disk images")

// With -args -diskimages, backups are written, verified and removed on
// exFAT, FAT16 and FAT32 disk images that the test makes, mounts and
// detaches again. macOS keeps attributes there in "._" companion files.
// Repositories are never restored there; state is restored where the
// exclusive rename works.
func TestBackupOntoDiskImages(t *testing.T) {
	if !*diskImages {
		t.Skip("run with -args -diskimages to make and mount disk images")
	}
	for _, volume := range []struct{ format, name string }{{"ExFAT", "exfat"}, {"MS-DOS", "msdos"}, {"MS-DOS FAT32", "msdos"}} {
		t.Run(volume.format, func(t *testing.T) {
			image := filepath.Join(t.TempDir(), "volume.dmg")
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
			hdiutil("create", "-size", "256m", "-fs", volume.format, "-volname", "OWNGITTEST", image)
			hdiutil("attach", "-nobrowse", "-mountpoint", mount, image)
			defer hdiutil("detach", "-force", mount)
			destination := filepath.Join(mount, "backups")
			noErr(t, os.Mkdir(destination, 0o700))
			backUpOntoFolder(t, destination)

			// The dashboard says that a restore cannot write there.
			if volume.name == "exfat" {
				if fileSystem, err := RestoreLimit(destination); err != nil || fileSystem != "exfat" {
					t.Fatalf("restore limit of an exFAT folder: %q %v", fileSystem, err)
				}
			}

			// Repositories are refused before any work, naming the file
			// system: on exFAT for the rename, on FAT for the companions.
			backup := newTwoRepositoryBackup(t, t.TempDir())
			stateTarget := filepath.Join(t.TempDir(), "state")
			repositoryTarget := filepath.Join(destination, "repositories")
			err := Restore(context.Background(), backup, stateTarget, repositoryTarget, "")
			if err == nil || !strings.Contains(err.Error(), "("+volume.name+")") {
				t.Fatalf("restore onto %s: %v", volume.format, err)
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

			// State is restored onto FAT, which renames exclusively.
			if volume.name == "msdos" {
				stateTarget = filepath.Join(destination, "state")
				noErr(t, Restore(context.Background(), backup, stateTarget, filepath.Join(t.TempDir(), "repositories"), ""))
				if _, err := os.Stat(filepath.Join(stateTarget, "owngit.sqlite")); err != nil {
					t.Fatalf("restored state: %v", err)
				}
			}
		})
	}
}
