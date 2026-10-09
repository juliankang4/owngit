//go:build windows

package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Between the inspection and SQLite's open by path, no account can put
// another state at the path, by renaming the state folder or a folder
// above it: OpenIn opens the state it inspected or none. The offline lock
// that serve and backup hold in the state folder is a control.
func TestWindowsStateIsNotSwappedAfterItsCheck(t *testing.T) {
	for _, test := range []struct {
		name string
		// swapped is the folder whose name is given to the replacement's.
		swapped     func(state string) string
		offlineLock bool
	}{
		{"state folder", func(state string) string { return state }, false},
		{"folder above it", filepath.Dir, false},
		{"state folder with the offline lock", func(state string) string { return state }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "parent", "state")
			swapped := test.swapped(path)
			replacement := filepath.Join(root, "other", swapped[len(filepath.Join(root, "parent")):])
			createMarkedState(t, path, "checked")
			createMarkedState(t, filepath.Join(root, "other", "state"), "replacement")
			held, err := CreateDirectory(path)
			noErr(t, err)
			defer held.Close()
			if test.offlineLock {
				unlock, err := AcquireOfflineLockIn(held)
				noErr(t, err)
				defer unlock()
			}
			useHooks(t)
			var renamed []error
			preflightHooks.afterRelease = func(string) {
				renamed = append(renamed, os.Rename(swapped, swapped+".checked"))
				renamed = append(renamed, os.Rename(replacement, swapped))
			}
			store, err := OpenIn(context.Background(), held, nil)
			preflightHooks.afterRelease = nil
			t.Logf("renames: %v; open: %v", renamed, err)
			if err != nil {
				return
			}
			defer store.Close()
			for _, phase := range []string{"after open", "after managed protection"} {
				if phase == "after managed protection" {
					noErr(t, ProtectManagedStateFiles(held))
				}
				for _, folder := range []string{path, filepath.Dir(path)} {
					handle, err := openToRename(folder)
					if err == nil {
						windows.CloseHandle(handle)
						t.Fatalf("%s: %s may be renamed while the store is open", phase, folder)
					}
					if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
						t.Fatalf("%s: rename access failed for another reason: %v", phase, err)
					}
				}
			}
			values, err := store.metadataValues(context.Background(), "exchange_marker")
			noErr(t, err)
			if marker := values["exchange_marker"]; marker != "checked" {
				t.Fatalf("OpenIn opened the %q state after the swap", marker)
			}
			noErr(t, store.Close())
			for _, folder := range []string{path, filepath.Dir(path)} {
				handle, err := openToRename(folder)
				noErr(t, err)
				noErr(t, windows.CloseHandle(handle))
			}
		})
	}
}

func TestWindowsFolderSharingFaultKeepsCauseAndPath(t *testing.T) {
	for _, name := range []string{"state", "backup"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "parent", "state")
			held, err := CreateDirectory(path)
			noErr(t, err)
			defer held.Close()
			open := func() error {
				if name == "backup" {
					destination, err := OpenDestination(filepath.Join(path, "backup.tar"))
					if destination != nil {
						destination.Close()
					}
					return err
				}
				store, err := OpenIn(context.Background(), held, nil)
				if store != nil {
					noErr(t, store.Close())
				}
				return err
			}
			requireRenameable := func() {
				t.Helper()
				for _, folder := range []string{root, filepath.Dir(path), path} {
					handle, err := openToRename(folder)
					noErr(t, err)
					noErr(t, windows.CloseHandle(handle))
				}
			}
			parent, err := openToRename(filepath.Dir(path))
			noErr(t, err)
			err = open()
			noErr(t, windows.CloseHandle(parent))
			var pathErr *os.PathError
			if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, ErrInspectionUnstable) || !errors.As(err, &pathErr) {
				t.Fatalf("folder sharing fault lost its cause or path: %v", err)
			}
			got, err := os.Stat(pathErr.Path)
			noErr(t, err)
			want, err := os.Stat(filepath.Dir(path))
			noErr(t, err)
			if !os.SameFile(got, want) {
				t.Fatalf("sharing fault named %s instead of the held parent", pathErr.Path)
			}
			requireRenameable()
			noErr(t, open())
			requireRenameable()
		})
	}
}

// openToRename opens the folder at path with the access that renaming or
// removing it needs, sharing everything, so it fails only while another
// handle to the folder refuses to share delete.
func openToRename(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}
