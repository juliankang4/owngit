//go:build linux

package state

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// ownershipEnforced reports whether this computer's kernel keeps the owners
// and modes of the filesystem that holds the open directory. On a network
// filesystem the server decides them, and on a FUSE filesystem the program
// behind it does; either can let another account change what they show as
// root's folder.
func ownershipEnforced(dir *os.File) (bool, error) {
	var stat unix.Statfs_t
	err := unix.Fstatfs(int(dir.Fd()), &stat)
	runtime.KeepAlive(dir)
	if err != nil {
		return false, err
	}
	return ownershipEnforcedOn(uint64(stat.Type)), nil
}

// ownershipEnforcedOn reports it for the filesystem type magic. It is a
// list of the filesystem types known to take owners and modes from
// elsewhere: network and cluster filesystems, whose server or other nodes
// decide them, FUSE, whose program does (virtiofs, used for Docker Desktop
// bind mounts, and GlusterFS report FUSE too), 9P, which WSL uses for
// Windows drives, and the shared folders of virtual machines. Any other
// type counts as local, so a network filesystem missing here is trusted.
func ownershipEnforcedOn(magic uint64) bool {
	switch magic {
	case uint64(unix.NFS_SUPER_MAGIC), uint64(unix.CIFS_SUPER_MAGIC), uint64(unix.SMB_SUPER_MAGIC),
		uint64(unix.SMB2_SUPER_MAGIC), uint64(unix.V9FS_MAGIC), uint64(unix.CEPH_SUPER_MAGIC),
		uint64(unix.AFS_SUPER_MAGIC), uint64(unix.AFS_FS_MAGIC), uint64(unix.CODA_SUPER_MAGIC),
		uint64(unix.NCP_SUPER_MAGIC), uint64(unix.FUSE_SUPER_MAGIC), uint64(unix.OCFS2_SUPER_MAGIC),
		acfsMagic, beegfsMagic, gfs2Magic, gpfsMagic, ibrixMagic, lustreMagic,
		orangefsMagic, panfsMagic, parallelsMagic, stornextMagic, virtualBoxMagic, vmwareMagic:
		return false
	}
	return true
}

// Filesystem type magics that golang.org/x/sys/unix does not name.
const (
	acfsMagic       = 0x61636673
	beegfsMagic     = 0x19830326
	gfs2Magic       = 0x01161970
	gpfsMagic       = 0x47504653
	ibrixMagic      = 0x013111a8
	lustreMagic     = 0x0bd00bd0
	orangefsMagic   = 0x20030528
	panfsMagic      = 0xaad7aaea
	parallelsMagic  = 0x7c7c6673 // prl_fs
	stornextMagic   = 0xbeefdead
	virtualBoxMagic = 0x786f4256 // vboxsf
	vmwareMagic     = 0xbacbacbc // vmhgfs
)
