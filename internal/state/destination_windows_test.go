//go:build windows

package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// While a Destination is open, neither its parent nor a folder on the way
// to it can be renamed, and while its stage is held, neither can the
// stage; after ReleaseStage the stage can be published, and after Close
// the parent can be renamed again.
func TestWindowsDestinationHoldsTheWayAndTheStage(t *testing.T) {
	root := t.TempDir()
	above := filepath.Join(root, "above")
	parent := filepath.Join(above, "parent")
	noErr(t, os.MkdirAll(parent, 0o700))
	destination, err := OpenDestination(filepath.Join(parent, "restored"))
	noErr(t, err)
	stage, err := destination.CreateStage("restored.stage")
	if err != nil {
		destination.Close()
		t.Fatal(err)
	}
	for _, path := range []string{stage, parent, above} {
		if err := os.Rename(path, path+"-moved"); err == nil {
			destination.Close()
			t.Fatalf("%s was renamed while the destination was held", path)
		}
	}
	destination.ReleaseStage()
	if err := os.Rename(stage, destination.Path); err != nil {
		destination.Close()
		t.Fatalf("the released stage could not be published: %v", err)
	}
	if err := os.Rename(parent, parent+"-moved"); err == nil {
		destination.Close()
		t.Fatal("the parent was renamed while the destination was held")
	}
	destination.Close()
	noErr(t, os.Rename(parent, parent+"-moved"))
}

// Another account that the parent lets rename and remove what is in it
// cannot take a held stage: the delete access a rename needs, which the
// parent grants it, is refused only because the stage is held, and is
// granted once the stage is released. The stage is also private, so the
// account cannot read it. A folder made by hand beside it is the control.
// The Windows test run creates the second local account; without it the
// test is skipped.
func TestWindowsAnotherAccountCannotExchangeTheStage(t *testing.T) {
	other, as := otherAccount(t)
	user, _, err := processIdentity()
	noErr(t, err)
	parent := filepath.Join(t.TempDir(), "parent")
	noErr(t, os.Mkdir(parent, 0o700))
	const (
		fileAddFile         = 0x2
		fileAddSubdirectory = 0x4
		fileDeleteChild     = 0x40
	)
	changes := windows.ACCESS_MASK(windows.FILE_LIST_DIRECTORY | fileAddFile | fileAddSubdirectory | fileDeleteChild)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{AccessPermissions: fileAllAccess, AccessMode: windows.GRANT_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(user)}},
		{AccessPermissions: changes | windows.DELETE | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE,
			AccessMode: windows.GRANT_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(other)}},
	}, nil)
	noErr(t, err)
	noErr(t, windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))

	control := filepath.Join(parent, "by-hand")
	noErr(t, os.Mkdir(control, 0o700))
	var controlErr error
	as(func() { controlErr = os.Rename(control, control+"-stolen") })
	if controlErr != nil {
		t.Fatalf("the control could not be renamed, so the parent does not let the other account rename: %v", controlErr)
	}

	destination, err := OpenDestination(filepath.Join(parent, "restored"))
	noErr(t, err)
	defer destination.Close()
	stage, err := destination.CreateStage("restored.stage")
	noErr(t, err)
	// Delete access alone, which a rename needs and the parent grants: the
	// overlapped flag keeps CreateFile from also asking for SYNCHRONIZE,
	// which only the stage's own access list could give.
	openForDelete := func() error {
		name, err := windows.UTF16PtrFromString(stage)
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			windows.CloseHandle(handle)
		}
		return err
	}
	var heldErr, readErr error
	as(func() {
		heldErr = openForDelete()
		var dir *os.File
		if dir, readErr = os.Open(stage); readErr == nil {
			dir.Close()
		}
	})
	if !errors.Is(heldErr, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("the other account's delete access to the held stage: %v, want a sharing violation", heldErr)
	}
	if readErr == nil {
		t.Fatal("the other account opened the stage")
	}
	destination.ReleaseStage()
	var releasedErr error
	as(func() { releasedErr = openForDelete() })
	if releasedErr != nil {
		t.Fatalf("the other account had no delete access to the released stage either (%v), so the hold was not what refused it", releasedErr)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatal(err)
	}
}
