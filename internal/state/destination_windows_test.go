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
// cannot rename a held stage, and cannot open it: the stage is private
// from the moment it exists. A folder made by hand beside it is the
// control. win/run.sh creates the second local account; without it the
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
	var renameErr, openErr error
	as(func() {
		renameErr = os.Rename(stage, stage+"-stolen")
		var dir *os.File
		if dir, openErr = os.Open(stage); openErr == nil {
			dir.Close()
		}
	})
	if !errors.Is(renameErr, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("the other account's rename of the held stage: %v, want a sharing violation", renameErr)
	}
	if openErr == nil {
		t.Fatal("the other account opened the stage")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatal(err)
	}
}
