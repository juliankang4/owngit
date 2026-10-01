//go:build windows

package repository

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestCreationRollbackPreservesMovedRootJunction(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	path := filepath.Join(manager.RepositoryRoot(), "draft")
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, manager.InitBareRepository(context.Background(), path, CreateOptions{}))
	creation, err := captureEmptyCreation(path)
	noErr(t, err)
	t.Cleanup(func() { _ = creation.parent.Close() })
	moved := path + "-moved"
	noErr(t, os.Rename(path, moved))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "cmd.exe", "/c", "mklink", "/J", path, moved).CombinedOutput()
	if err != nil {
		t.Fatalf("create synthetic junction: %v %s", err, output)
	}
	before := junctionReparseData(t, path)
	if err := creation.rollback(path); err == nil {
		t.Fatal("rollback accepted a junction to the moved original")
	}
	if _, err := os.Stat(filepath.Join(moved, "config")); err != nil {
		t.Fatalf("rollback changed the moved tree: %v", err)
	}
	if after := junctionReparseData(t, path); !bytes.Equal(before, after) {
		t.Fatal("rollback changed the junction's reparse tag or target")
	}
	if creation.preserved != "" {
		t.Fatal("rollback moved the substituted junction")
	}
}

func junctionReparseData(t *testing.T, path string) []byte {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	noErr(t, err)
	handle, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	noErr(t, err)
	defer windows.CloseHandle(handle)
	data := make([]byte, windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)
	var returned uint32
	noErr(t, windows.DeviceIoControl(handle, windows.FSCTL_GET_REPARSE_POINT, nil, 0, &data[0], uint32(len(data)), &returned, nil))
	if returned < 8 || binary.LittleEndian.Uint32(data[:4]) != windows.IO_REPARSE_TAG_MOUNT_POINT {
		t.Fatalf("entry is not a mount-point junction: returned=%d tag=%x", returned, binary.LittleEndian.Uint32(data[:4]))
	}
	return data[:returned]
}
