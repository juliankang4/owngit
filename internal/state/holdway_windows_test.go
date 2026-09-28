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
			store, err := OpenIn(context.Background(), held)
			preflightHooks.afterRelease = nil
			t.Logf("renames: %v; open: %v", renamed, err)
			if err != nil {
				return
			}
			values, err := store.metadataValues(context.Background(), "exchange_marker")
			noErr(t, store.Close())
			noErr(t, err)
			if marker := values["exchange_marker"]; marker != "checked" {
				t.Fatalf("OpenIn opened the %q state after the swap", marker)
			}
		})
	}
}

// A folder on the way that another program holds open to rename or remove
// it is a change in progress, which OpenIn's callers try again, not a
// failure or a crash, and OpenIn leaves no folder on the way held: every
// folder can be opened to rename it again afterwards.
func TestWindowsFolderHeldForRenamingIsAChangeInProgress(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "parent", "state")
	held, err := CreateDirectory(path)
	noErr(t, err)
	defer held.Close()
	openIn := func() (store *Store, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("OpenIn panicked: %v", recovered)
			}
		}()
		return OpenIn(context.Background(), held)
	}
	requireRenameable := func(when string) {
		t.Helper()
		for _, folder := range []string{root, filepath.Dir(path), path} {
			handle, err := openToRename(folder)
			if err != nil {
				t.Fatalf("%s, %s cannot be opened to rename it: %v", when, folder, err)
			}
			noErr(t, windows.CloseHandle(handle))
		}
	}
	parent, err := openToRename(filepath.Dir(path))
	noErr(t, err)
	store, err := openIn()
	noErr(t, windows.CloseHandle(parent))
	if store != nil {
		noErr(t, store.Close())
	}
	if !errors.Is(err, ErrInspectionUnstable) {
		t.Fatalf("OpenIn with a folder on the way held for renaming: %v, want a change in progress", err)
	}
	requireRenameable("after the refused OpenIn")
	store, err = openIn()
	noErr(t, err)
	noErr(t, store.Close())
	requireRenameable("after OpenIn")
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
