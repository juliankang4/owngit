//go:build linux

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Owners and modes on network and FUSE filesystems come from a server or a
// program, not from this kernel.
func TestOwnershipIsEnforcedOnlyOnLocalFilesystems(t *testing.T) {
	for _, test := range []struct {
		name     string
		magic    uint64
		enforced bool
	}{
		{"ext4", unix.EXT4_SUPER_MAGIC, true},
		{"xfs", unix.XFS_SUPER_MAGIC, true},
		{"btrfs", unix.BTRFS_SUPER_MAGIC, true},
		{"tmpfs", unix.TMPFS_MAGIC, true},
		{"NFS", unix.NFS_SUPER_MAGIC, false},
		{"SMB", unix.SMB2_SUPER_MAGIC, false},
		{"CIFS", unix.CIFS_SUPER_MAGIC, false},
		{"9P", unix.V9FS_MAGIC, false},
		{"Ceph", unix.CEPH_SUPER_MAGIC, false},
		{"FUSE", unix.FUSE_SUPER_MAGIC, false},
	} {
		if got := ownershipEnforcedOn(test.magic); got != test.enforced {
			t.Errorf("%s: enforced=%t, want %t", test.name, got, test.enforced)
		}
	}
}

// A folder on a FUSE mount that this computer has, such as LXCFS in a
// container, shows owners that a program reports: the create-folder command
// is not offered there, and setup points to the mount's own settings.
func TestFolderOnAFUSEMountIsNotRootOnly(t *testing.T) {
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	noErr(t, err)
	folder := ""
	for _, line := range strings.Split(string(mountinfo), "\n") {
		fields := strings.Fields(line)
		_, after, _ := strings.Cut(line, " - ")
		filesystem, _, _ := strings.Cut(after, " ")
		if len(fields) < 5 || filesystem != "fuse" && filesystem != "fuseblk" && !strings.HasPrefix(filesystem, "fuse.") {
			continue
		}
		if info, err := os.Stat(fields[4]); err == nil && info.IsDir() {
			folder = fields[4]
			break
		}
	}
	if folder == "" {
		t.Skip("no FUSE folder is mounted here")
	}
	missing := filepath.Join(folder, "owngit-test-missing")
	way := InspectFolderWay(missing)
	if way.OnlyRoot {
		t.Errorf("%s on a FUSE mount counts as root's", missing)
	}
	if !way.Shared {
		t.Errorf("%s on a FUSE mount is not reported as shared", missing)
	}
}
