//go:build windows

package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
				createNumberedSchemaDatabase(t, directory, currentSchemaVersion)
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
