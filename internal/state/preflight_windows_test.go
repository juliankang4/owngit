//go:build windows

package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// applyDistinguishableACL gives a test-owned path a protected DACL that grants
// the current user full control and Everyone read access. That descriptor is
// not owner-only, so a refusal that applied OwnGit's protection would change
// it, and the SDDL comparison in these tests is meaningful regardless of the
// host's inherited defaults.
func applyDistinguishableACL(t *testing.T, path string, directory bool) {
	t.Helper()
	user, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user),
			},
		},
		{
			AccessPermissions: windows.GENERIC_READ,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(everyone),
			},
		},
	}, nil)
	noErr(t, err)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateOwnerOnly(path, user, directory); err == nil {
		t.Fatalf("distinguishable ACL on %s is still owner-only", path)
	}
}

func captureDescriptors(t *testing.T, directory string, names []string) map[string]string {
	t.Helper()
	descriptors := map[string]string{}
	for _, name := range names {
		descriptor, err := protectionFingerprint(filepath.Join(directory, name))
		noErr(t, err)
		descriptors[name] = descriptor
	}
	return descriptors
}

func assertDescriptorsUnchanged(t *testing.T, directory string, want map[string]string) {
	t.Helper()
	for name, before := range want {
		after, err := protectionFingerprint(filepath.Join(directory, name))
		noErr(t, err)
		if after != before {
			t.Fatalf("refusal changed the security descriptor of %s:\nbefore=%s\nafter=%s", name, before, after)
		}
	}
}

// TestWindowsUnsupportedRefusalPreservesSecurityDescriptors compares the
// owner and DACL of the directory, database and sidecars before and after an
// unsupported refusal on both classification paths.
func TestWindowsUnsupportedRefusalPreservesSecurityDescriptors(t *testing.T) {
	for _, test := range []struct {
		name     string
		fragment string
		prepare  func(t *testing.T, directory string)
	}{
		{name: "sidecar-free schema 5", fragment: "schema 5", prepare: func(t *testing.T, directory string) {
			createNumberedSchemaDatabase(t, directory, 5)
		}},
		{name: "schema 5 committed in WAL", fragment: "schema 5", prepare: func(t *testing.T, directory string) {
			createCrashedWALFixture(t, directory, true, commitBaselineThenChangeVersion("5"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			test.prepare(t, directory)
			before := captureSchemaDirectory(t, directory)
			applyDistinguishableACL(t, directory, true)
			for _, name := range mapKeys(before) {
				applyDistinguishableACL(t, filepath.Join(directory, name), false)
			}
			names := append([]string{"."}, mapKeys(before)...)
			descriptors := captureDescriptors(t, directory, names)
			openRefused(t, directory, test.fragment)
			assertSchemaDirectoryUnchanged(t, directory, before)
			assertDescriptorsUnchanged(t, directory, descriptors)
		})
	}
}

// TestWindowsFreshDirectoryRefusalPreservesSecurityDescriptor covers a
// directory that was empty when inspected. A database or sidecar appearing,
// or the directory being replaced, before acceptance must leave the original
// directory descriptor and the appeared file untouched.
func TestWindowsFreshDirectoryRefusalPreservesSecurityDescriptor(t *testing.T) {
	prepared := filepath.Join(t.TempDir(), "prepared")
	createNumberedSchemaDatabase(t, prepared, 5)
	schemaFive, err := os.ReadFile(filepath.Join(prepared, databaseName))
	noErr(t, err)
	tests := []struct {
		name     string
		fragment string
		disturb  func(t *testing.T, directory string) (appeared string)
	}{
		{name: "schema 5 database appears", fragment: ErrInspectionUnstable.Error(), disturb: func(t *testing.T, directory string) string {
			path := filepath.Join(directory, databaseName)
			noErr(t, os.WriteFile(path, schemaFive, 0o600))
			applyDistinguishableACL(t, path, false)
			return databaseName
		}},
		{name: "WAL appears", fragment: ErrInspectionUnstable.Error(), disturb: func(t *testing.T, directory string) string {
			path := filepath.Join(directory, databaseName+walSuffix)
			noErr(t, os.WriteFile(path, []byte("orphan"), 0o600))
			applyDistinguishableACL(t, path, false)
			return databaseName + walSuffix
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			noErr(t, os.Mkdir(directory, 0o700))
			applyDistinguishableACL(t, directory, true)
			original := captureDescriptors(t, directory, []string{"."})
			var afterDisturbance map[string]string
			hookAt(t, pointAccept, func(string) {
				names := []string{"."}
				if name := test.disturb(t, directory); name != "" {
					names = append(names, name)
				}
				afterDisturbance = captureDescriptors(t, directory, names)
			})
			openRefused(t, directory, test.fragment)
			assertDescriptorsUnchanged(t, directory, afterDisturbance)
			assertDescriptorsUnchanged(t, directory, original)
			if _, err := os.Stat(filepath.Join(directory, databaseName+shmSuffix)); !os.IsNotExist(err) {
				t.Fatalf("refusal created a shared-memory index: %v", err)
			}
		})
	}

	t.Run("directory replaced", func(t *testing.T) {
		root := t.TempDir()
		directory := filepath.Join(root, "state")
		replacement := filepath.Join(root, "replacement")
		noErr(t, os.Mkdir(directory, 0o700))
		noErr(t, os.Mkdir(replacement, 0o700))
		applyDistinguishableACL(t, directory, true)
		applyDistinguishableACL(t, replacement, true)
		originalDescriptor := captureDescriptors(t, directory, []string{"."})
		replacementDescriptor := captureDescriptors(t, replacement, []string{"."})
		moved := directory + ".moved"
		replaceDirectory := func() error {
			if err := os.Rename(directory, moved); err != nil {
				return err
			}
			if err := os.Rename(replacement, directory); err != nil {
				return errors.Join(err, os.Rename(moved, directory))
			}
			return nil
		}
		var replacementErr error
		hookInspectionError(t, pointAccept, func(*inspection, string) error {
			replacementErr = replaceDirectory()
			return replacementErr
		})
		store, err := Open(context.Background(), directory)
		if store != nil {
			_ = store.Close()
			t.Fatal("directory replacement opened the state database")
		}
		if replacementErr != nil {
			if err == nil || !errors.Is(err, replacementErr) {
				t.Fatalf("prevented directory replacement error=%v, want cause %v", err, replacementErr)
			}
			assertDescriptorsUnchanged(t, directory, originalDescriptor)
			assertDescriptorsUnchanged(t, replacement, replacementDescriptor)
			if _, statErr := os.Stat(moved); !os.IsNotExist(statErr) {
				t.Fatalf("prevented replacement left a moved directory: %v", statErr)
			}
			preflightHooks.at = nil
			if err := replaceDirectory(); err != nil {
				t.Fatalf("directory replacement remained blocked after inspection release: %v", err)
			}
		} else if !errors.Is(err, ErrInspectionUnstable) {
			t.Fatalf("completed directory replacement error=%v, want instability", err)
		}
		assertDescriptorsUnchanged(t, directory, replacementDescriptor)
		assertDescriptorsUnchanged(t, moved, originalDescriptor)
		if _, err := os.Stat(filepath.Join(directory, databaseName+shmSuffix)); !os.IsNotExist(err) {
			t.Fatalf("refusal created a shared-memory index: %v", err)
		}
	})
}

func TestWindowsReparseStateEntriesAreRefused(t *testing.T) {
	for _, name := range []string{databaseName, databaseName + walSuffix, databaseName + shmSuffix} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "state")
			noErr(t, os.Mkdir(directory, 0o700))
			if name != databaseName {
				createNumberedSchemaDatabase(t, directory, currentSchemaVersion())
			}
			target := filepath.Join(root, "target")
			noErr(t, os.Mkdir(target, 0o700))
			marker := filepath.Join(target, "marker")
			noErr(t, os.WriteFile(marker, []byte("reparse target"), 0o600))
			beforeInfo, err := os.Stat(marker)
			noErr(t, err)
			protections := captureProtectionFingerprints(t, directory, target, marker)
			junction := filepath.Join(directory, name)
			command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("create junction %s: %v: %s", name, err, output)
			}
			junctionName, err := windows.UTF16PtrFromString(junction)
			noErr(t, err)
			attributes, err := windows.GetFileAttributes(junctionName)
			if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
				t.Fatalf("junction attributes=%#x err=%v", attributes, err)
			}
			openRefused(t, directory, "must be a regular file")
			afterInfo, err := os.Stat(marker)
			noErr(t, err)
			content, err := os.ReadFile(marker)
			if err != nil || string(content) != "reparse target" || !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Size() != afterInfo.Size() || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
				t.Fatalf("reparse target changed: content=%q before=%v after=%v err=%v", content, beforeInfo, afterInfo, err)
			}
			assertProtectionFingerprints(t, protections)
			attributes, err = windows.GetFileAttributes(junctionName)
			if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
				t.Fatalf("junction disappeared or changed: attributes=%#x err=%v", attributes, err)
			}
		})
	}
}

// pendingDeletion is a deletion that a process holding the entry open has
// requested, as SQLite does to its sidecars on close while another opener
// inspects them: the name stays, and opening it by name fails, until the
// last handle closes. Until then the holder can cancel it.
type pendingDeletion struct {
	handle windows.Handle
	// release lets go, so the deletion finishes; cancelAndRelease cancels
	// it first, so the entry stays. Only the first of them acts.
	release, cancelAndRelease func()
}

// markDeletePending requests the deletion of path through a handle that the
// returned pendingDeletion holds.
func markDeletePending(t *testing.T, path string) *pendingDeletion {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	noErr(t, err)
	handle, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	noErr(t, err)
	pending := &pendingDeletion{handle: handle}
	var once sync.Once
	pending.release = func() { once.Do(func() { _ = windows.CloseHandle(handle) }) }
	pending.cancelAndRelease = func() {
		once.Do(func() {
			pending.set(t, false)
			_ = windows.CloseHandle(handle)
		})
	}
	t.Cleanup(pending.release)
	pending.set(t, true)
	if _, err := LstatIdentity(path); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("%s is not waiting for deletion: lookup error %v, want access denied", filepath.Base(path), err)
	}
	return pending
}

// set requests (true) or cancels (false) the deletion with the legacy
// FileDispositionInfo class, which allows both.
func (pending *pendingDeletion) set(t *testing.T, deleteFile bool) {
	t.Helper()
	disposition := struct{ DeleteFile bool }{DeleteFile: deleteFile} // FILE_DISPOSITION_INFO
	noErr(t, windows.SetFileInformationByHandle(pending.handle, windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&disposition)), uint32(unsafe.Sizeof(disposition))))
}

// A sidecar that a closing opener deleted while another process holds it
// open cannot be opened by name until that process lets go, and the deletion
// could still be cancelled. The inspection reports it as a change in
// progress, whenever it meets it, and never fails with another error because
// of it. Once the deletion has finished, the next Open succeeds.
func TestWindowsSidecarsWhoseDeletionIsPendingAreAChangeInProgress(t *testing.T) {
	ctx := context.Background()
	sidecars := []string{databaseName + walSuffix, databaseName + shmSuffix}
	prepare := func(t *testing.T) string {
		directory := filepath.Join(t.TempDir(), "state")
		store, err := Open(ctx, directory)
		noErr(t, err)
		noErr(t, store.Close())
		for _, name := range sidecars {
			noErr(t, os.WriteFile(filepath.Join(directory, name), nil, 0o600))
		}
		return directory
	}
	markSidecars := func(t *testing.T, directory string) func() {
		var pending []*pendingDeletion
		for _, name := range sidecars {
			pending = append(pending, markDeletePending(t, filepath.Join(directory, name)))
		}
		return func() {
			for _, deletion := range pending {
				deletion.release()
			}
		}
	}
	for _, point := range []string{"", pointListed, pointAccept, pointProtect} {
		name := "before the inspection"
		if point != "" {
			name = "at " + point
		}
		t.Run(name, func(t *testing.T) {
			directory := prepare(t)
			release := func() {}
			if point == "" {
				release = markSidecars(t, directory)
			} else {
				hookAt(t, point, func(string) { release = markSidecars(t, directory) })
			}
			store, err := Open(ctx, directory)
			if store != nil {
				noErr(t, store.Close())
			}
			if !errors.Is(err, ErrInspectionUnstable) {
				t.Fatalf("Open with sidecars waiting for deletion: %v, want a retryable change", err)
			}
			// The other process lets go, so the deletion finishes.
			release()
			preflightHooks.at = nil
			store, err = Open(ctx, directory)
			noErr(t, err)
			noErr(t, store.Close())
		})
	}
}

// A state database whose deletion is pending is not an empty state
// directory: the holder may cancel the deletion after the inspection, and the
// restored entry, possibly a link, would then be used without having been
// inspected. Open reports the change, and the next Open inspects whatever is
// there.
func TestWindowsCancelledDeletionIsInspectedBeforeUse(t *testing.T) {
	ctx := context.Background()
	openCancelledLater := func(t *testing.T, directory string) {
		t.Helper()
		pending := markDeletePending(t, filepath.Join(directory, databaseName))
		useHooks(t)
		// If the inspection took the database as absent, the holder
		// cancels the deletion before SQLite opens the name.
		preflightHooks.afterRelease = func(string) { pending.cancelAndRelease() }
		store, err := Open(ctx, directory)
		if store != nil {
			noErr(t, store.Close())
		}
		if !errors.Is(err, ErrInspectionUnstable) {
			t.Fatalf("Open with the database waiting for deletion: %v, want a retryable change", err)
		}
		pending.cancelAndRelease()
		preflightHooks.afterRelease = nil
	}
	t.Run("regular database", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "state")
		createMarkedState(t, directory, "kept")
		openCancelledLater(t, directory)
		if marker, _ := openStateMarker(t, directory); marker != "kept" {
			t.Fatalf("the next Open found marker %q", marker)
		}
	})
	reparse := func(t *testing.T, link func(t *testing.T, root, name string)) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		createMarkedState(t, target, "elsewhere")
		before := captureSchemaDirectory(t, target)
		directory := filepath.Join(root, "state")
		noErr(t, os.Mkdir(directory, 0o700))
		link(t, root, filepath.Join(directory, databaseName))
		openCancelledLater(t, directory)
		openRefused(t, directory, "must be a regular file")
		assertSchemaDirectoryUnchanged(t, target, before)
	}
	t.Run("junction", func(t *testing.T) {
		reparse(t, func(t *testing.T, root, name string) {
			if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", name, filepath.Join(root, "target")).CombinedOutput(); err != nil {
				t.Fatalf("create junction: %v: %s", err, output)
			}
		})
	})
	t.Run("file symbolic link", func(t *testing.T) {
		reparse(t, func(t *testing.T, root, name string) {
			if err := os.Symlink(filepath.Join(root, "target", databaseName), name); err != nil {
				t.Skipf("this account may not create symbolic links: %v", err)
			}
		})
	})
}

// A pending deletion is a change in progress, and every other failure to
// open a state entry keeps the error that CreateFile would report, so an
// entry this account may not open is still refused with its cause.
func TestWindowsEntryOpenStatusKeepsItsMeaning(t *testing.T) {
	for _, test := range []struct {
		status                       windows.NTStatus
		changing, absent, permission bool
	}{
		{windows.STATUS_DELETE_PENDING, true, false, false},
		{windows.STATUS_OBJECT_NAME_NOT_FOUND, false, true, false},
		{windows.STATUS_ACCESS_DENIED, false, false, true},
		{windows.STATUS_SHARING_VIOLATION, false, false, false},
	} {
		err := entryOpenError(test.status)
		changing, absent, permission := errors.Is(err, ErrInspectionUnstable), errors.Is(err, os.ErrNotExist), errors.Is(err, os.ErrPermission)
		if changing != test.changing || absent != test.absent || permission != test.permission {
			t.Errorf("status %#x gave %v: changing=%t absent=%t permission=%t, want %t, %t and %t", uint32(test.status), err,
				changing, absent, permission, test.changing, test.absent, test.permission)
		}
	}
}

// Protecting a directory that is already private writes nothing. Setting a
// directory's access list rewrites the inherited entries of the files inside
// it from an earlier reading, which could undo the protection that another
// process has just given one of them: a concurrent start then failed with
// "private file ACL must not inherit access entries".
func TestWindowsProtectingAPrivateDirectoryAgainLeavesItsFilesAlone(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	noErr(t, os.Mkdir(directory, 0o700))
	noErr(t, ProtectPrivatePath(directory, true))
	child := filepath.Join(directory, "child")
	noErr(t, os.WriteFile(child, nil, 0o600))
	// Give the file an access list without the entry its directory passes
	// on, written without inheritance processing, so any rewrite of the
	// inherited entries shows.
	user, _, err := processIdentity()
	noErr(t, err)
	acl, err := ownerOnlyACL(user, false)
	noErr(t, err)
	descriptor, err := windows.NewSecurityDescriptor()
	noErr(t, err)
	noErr(t, descriptor.SetDACL(acl, true, false))
	name, err := windows.UTF16PtrFromString(child)
	noErr(t, err)
	handle, err := windows.CreateFile(name, windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	noErr(t, err)
	err = windows.SetKernelObjectSecurity(handle, windows.DACL_SECURITY_INFORMATION, descriptor)
	noErr(t, errors.Join(err, windows.CloseHandle(handle)))
	before, err := protectionFingerprint(child)
	noErr(t, err)
	noErr(t, ProtectPrivatePath(directory, true))
	after, err := protectionFingerprint(child)
	noErr(t, err)
	if after != before {
		t.Fatalf("protecting the private directory again rewrote the file inside it:\nbefore=%s\nafter=%s", before, after)
	}
}
