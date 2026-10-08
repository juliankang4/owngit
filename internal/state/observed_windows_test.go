//go:build windows

package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsObservedStatePreservesACLs(t *testing.T) {
	ctx := context.Background()
	user, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	for _, test := range []struct {
		name      string
		owner     windows.ACCESS_MASK
		other     windows.ACCESS_MASK
		rootOther windows.ACCESS_MASK
		shortRoot bool
		refused   bool
	}{
		{name: "extra reader", owner: fileAllAccess, other: windows.GENERIC_READ},
		{name: "write attributes", owner: fileAllAccess, other: windows.FILE_WRITE_ATTRIBUTES, refused: true},
		{name: "write extended attributes", owner: fileAllAccess, other: windows.FILE_WRITE_EA, refused: true},
		{name: "read-only owner handle", owner: windows.FILE_GENERIC_READ},
		{name: "temporary root writer", owner: fileAllAccess, rootOther: windows.GENERIC_WRITE, refused: true},
		{name: "temporary root delete-child", owner: fileAllAccess, rootOther: 0x40, refused: true},
		{name: "temporary root attributes", owner: fileAllAccess, rootOther: windows.FILE_WRITE_ATTRIBUTES, refused: true},
		{name: "short temporary root", owner: fileAllAccess, other: windows.GENERIC_READ, shortRoot: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			store, err := Open(ctx, dir)
			noErr(t, err)
			noErr(t, store.Close())
			entries := []windows.EXPLICIT_ACCESS{testEntry(user, windows.GRANT_ACCESS, test.owner)}
			if test.other != 0 {
				entries = append(entries, testEntry(everyone, windows.GRANT_ACCESS, test.other))
			}
			setRawDACL(t, filepath.Join(dir, databaseName), true, entries, false)
			staging := t.TempDir()
			useHooks(t)
			preflightHooks.temporaryRoot = staging
			if test.rootOther != 0 {
				setRawDACL(t, staging, true, []windows.EXPLICIT_ACCESS{
					testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
					testEntry(everyone, windows.GRANT_ACCESS, test.rootOther),
				}, false)
			}
			if test.shortRoot {
				path, err := windows.UTF16PtrFromString(staging)
				noErr(t, err)
				buffer := make([]uint16, 32768)
				length, err := windows.GetShortPathName(path, &buffer[0], uint32(len(buffer)))
				noErr(t, err)
				short := windows.UTF16ToString(buffer[:length])
				if strings.EqualFold(short, staging) {
					t.Skip("8.3 temporary names are not enabled on this volume")
				}
				preflightHooks.temporaryRoot = ""
				t.Setenv("TEMP", short)
				t.Setenv("TMP", short)
			}
			rootBefore, err := LstatIdentity(staging)
			noErr(t, err)
			before := captureSchemaDirectory(t, dir)
			permissions := captureProtectionFingerprints(t, dir, filepath.Join(dir, databaseName), staging)
			var snapshot string
			hookAt(t, pointCapture, func(privateDir string) { snapshot = privateDir })
			observed, err := OpenObserved(ctx, dir)
			if observed != nil {
				defer observed.Close()
			}
			if test.refused {
				if observed != nil || err == nil || !strings.Contains(err.Error(), "another account can change") {
					t.Fatalf("open=%v, error=%v, want refusal", observed, err)
				}
				if test.rootOther != 0 {
					if !strings.Contains(err.Error(), "set "+temporaryEnvironment+" to a writable temporary directory outside the state directory") {
						t.Fatalf("temporary-root refusal has no repair: %v", err)
					}
					rootAfter, err := LstatIdentity(staging)
					noErr(t, err)
					if snapshot != "" || !os.SameFile(rootBefore, rootAfter) || !rootBefore.ModTime().Equal(rootAfter.ModTime()) {
						t.Fatal("unsafe temporary root changed before refusal")
					}
				}
			} else {
				noErr(t, err)
				changes := observed.StateProtectionChanges()
				if len(changes) != 1 || changes[0].Path != databaseName {
					t.Fatalf("planned=%v, want the database", changes)
				}
				_, err = observed.ObserveRunningNetwork(ctx)
				noErr(t, err)
				noErr(t, observed.Close())
			}
			assertSchemaDirectoryUnchanged(t, dir, before)
			assertProtectionFingerprints(t, permissions)
			copies, err := os.ReadDir(staging)
			noErr(t, err)
			if len(copies) != 0 {
				t.Fatalf("temporary snapshot remains: %v", copies)
			}
		})
	}
}
