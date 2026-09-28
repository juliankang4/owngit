//go:build windows

package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
