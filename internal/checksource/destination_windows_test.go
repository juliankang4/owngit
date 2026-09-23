//go:build windows

package checksource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows ignores the Unix mode bits, so the ACL is the only meaningful check.
//
// Two different properties are asserted, and conflating them produces a false
// failure. The destination root must carry its own protected descriptor, which
// is what stops a permissive parent from leaking in. Its children must grant
// access only to the current user, but they legitimately reach that state by
// inheriting the root's entries, so requiring SE_DACL_PROTECTED on a child
// would reject the correct result.

// destinationIsPrivate reports whether path grants access only to the current
// user. It is the portable predicate used by destination_test.go, so it must
// accept a child that safely inherits owner-only entries from the protected
// root.
func destinationIsPrivate(t *testing.T, path string) bool {
	t.Helper()
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	return grantsOnlyCurrentUser(t, path, user)
}

func currentUserSID() (*windows.SID, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tokenUser.User.Sid.Copy()
}

func readDACL(t *testing.T, path string) (*windows.SECURITY_DESCRIPTOR, *windows.ACL) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read %s security descriptor: %v", path, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read %s DACL: %v", path, err)
	}
	return descriptor, dacl
}

// eachACE visits every access-control entry, failing the test if one cannot be
// read. A NULL DACL grants everyone full access, so it is reported distinctly
// rather than being treated as an empty, harmless list.
func eachACE(t *testing.T, path string, dacl *windows.ACL, visit func(ace *windows.ACCESS_ALLOWED_ACE, sid *windows.SID)) {
	t.Helper()
	if dacl == nil {
		t.Fatalf("%s has a NULL DACL, which grants access to everyone", path)
	}
	if dacl.AceCount == 0 {
		return
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
			t.Fatalf("inspect %s ACE %d: %v", path, index, err)
		}
		visit(ace, (*windows.SID)(unsafe.Pointer(&ace.SidStart)))
	}
}

// grantsOnlyCurrentUser checks the access actually granted, regardless of
// whether the entries are explicit or inherited. A child of the protected
// destination is expected to inherit owner-only entries, so inheritance itself
// is not a failure; a grant to any other SID is.
func grantsOnlyCurrentUser(t *testing.T, path string, user *windows.SID) bool {
	t.Helper()
	_, dacl := readDACL(t, path)
	if dacl == nil {
		t.Logf("%s has a NULL DACL, which grants access to everyone", path)
		return false
	}
	if dacl.AceCount == 0 {
		// An empty DACL denies everyone, including the owner. That is not the
		// intended result for a materialized path.
		t.Logf("%s has an empty DACL and grants nobody access", path)
		return false
	}
	foreign := false
	eachACE(t, path, dacl, func(ace *windows.ACCESS_ALLOWED_ACE, sid *windows.SID) {
		if !sid.Equals(user) {
			t.Logf("%s grants access to %v rather than the current user", path, sid)
			foreign = true
		}
	})
	return !foreign
}

// requireProtectedOwnerOnly asserts the stricter property expected of the
// destination root: its descriptor is protected from parent inheritance and
// every entry belongs to the current user.
func requireProtectedOwnerOnly(t *testing.T, path string, user *windows.SID) {
	t.Helper()
	descriptor, dacl := readDACL(t, path)
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read %s control bits: %v", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s is not protected and still inherits entries from its parent", path)
	}
	if dacl == nil || dacl.AceCount == 0 {
		t.Fatalf("%s has no usable DACL", path)
	}
	eachACE(t, path, dacl, func(ace *windows.ACCESS_ALLOWED_ACE, sid *windows.SID) {
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("%s has an unexpected ACE type %d", path, ace.Header.AceType)
		}
		if !sid.Equals(user) {
			t.Fatalf("%s grants access to %v rather than the current user", path, sid)
		}
	})
}

// requireInheritedOwnerOnly asserts the property expected of a materialized
// child: it grants only the current user, and it reached that state by
// inheriting from the protected destination rather than by chance.
func requireInheritedOwnerOnly(t *testing.T, path string, user *windows.SID) {
	t.Helper()
	_, dacl := readDACL(t, path)
	if dacl == nil || dacl.AceCount == 0 {
		t.Fatalf("%s has no usable DACL", path)
	}
	inherited := false
	eachACE(t, path, dacl, func(ace *windows.ACCESS_ALLOWED_ACE, sid *windows.SID) {
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("%s has an unexpected ACE type %d", path, ace.Header.AceType)
		}
		if !sid.Equals(user) {
			t.Fatalf("%s grants access to %v rather than the current user", path, sid)
		}
		if uint32(ace.Header.AceFlags)&windows.INHERITED_ACE != 0 {
			inherited = true
		}
	})
	if !inherited {
		t.Fatalf("%s has no inherited entry, so it did not receive the destination's protection", path)
	}
}

// everyoneSID is the well-known World SID used to make a parent permissive.
func everyoneSID(t *testing.T) *windows.SID {
	t.Helper()
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	return everyone
}

// grantEveryone opens the directory to all users and lets that grant inherit,
// so a child that merely inherits its parent's access would be readable by
// others.
func grantEveryone(t *testing.T, path string) {
	t.Helper()
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyoneSID(t)),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatalf("grant Everyone on %s: %v", path, err)
	}
}

// grantsRead reports whether a mask allows reading file data. Windows may keep
// the granted right in generic form on an inherit-only entry and map it to the
// object-specific form on the effective entry, so all three spellings count.
// GENERIC_ALL, GENERIC_READ and FILE_READ_DATA occupy disjoint bits, so testing
// only one of them would miss the others.
func grantsRead(mask windows.ACCESS_MASK) bool {
	return mask&windows.FILE_READ_DATA != 0 ||
		mask&windows.GENERIC_READ != 0 ||
		mask&windows.GENERIC_ALL != 0
}

// requireInheritedEveryoneGrant proves the positive control: an ordinary
// directory created under the permissive parent really did inherit an Everyone
// allow entry carrying read access. Without this the parent fixture could be
// ineffective and the main assertion would pass vacuously.
func requireInheritedEveryoneGrant(t *testing.T, path string) {
	t.Helper()
	everyone := everyoneSID(t)
	_, dacl := readDACL(t, path)
	found := false
	eachACE(t, path, dacl, func(ace *windows.ACCESS_ALLOWED_ACE, sid *windows.SID) {
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(everyone) {
			return
		}
		if uint32(ace.Header.AceFlags)&windows.INHERITED_ACE == 0 {
			return
		}
		if !grantsRead(ace.Mask) {
			return
		}
		found = true
	})
	if !found {
		t.Fatalf("%s did not inherit an Everyone read grant, so the permissive-parent fixture is ineffective", path)
	}
}

// A permissive parent must not leak into the destination. Without an explicit
// ACL the new directory would inherit the parent's Everyone grant, because
// os.Mkdir's mode argument has no effect on Windows.
func TestMaterializeDestinationOverridesAPermissiveParentACL(t *testing.T) {
	parent := t.TempDir()
	grantEveryone(t, parent)

	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: an ordinary directory here must actually inherit the
	// Everyone grant, otherwise the regression below proves nothing.
	control := filepath.Join(parent, "inherited")
	if err := os.Mkdir(control, 0o700); err != nil {
		t.Fatal(err)
	}
	requireInheritedEveryoneGrant(t, control)

	source := newFakeSource(t, map[string]string{"nested/secret.txt": "private"}, nil)
	destination := filepath.Join(parent, "source")
	if _, err := Materialize(context.Background(), source, destination, Options{}); err != nil {
		t.Fatal(err)
	}
	requireProtectedOwnerOnly(t, destination, user)
}

// Files and subdirectories created inside the destination must inherit its
// owner-only ACL rather than the original parent's permissive one.
func TestMaterializeDestinationChildrenInheritOwnerOnlyAccess(t *testing.T) {
	parent := t.TempDir()
	grantEveryone(t, parent)
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}

	source := newFakeSource(t, map[string]string{"nested/deep/secret.txt": "private"}, nil)
	destination := filepath.Join(parent, "source")
	if _, err := Materialize(context.Background(), source, destination, Options{}); err != nil {
		t.Fatal(err)
	}
	requireProtectedOwnerOnly(t, destination, user)
	for _, relative := range []string{"nested", `nested\deep`, `nested\deep\secret.txt`} {
		requireInheritedOwnerOnly(t, filepath.Join(destination, relative), user)
	}
}

// Refusing an existing destination must not touch its security descriptor. The
// portable test compares the Unix mode, which carries no ACL information on
// Windows, so the descriptor is snapshotted and compared here instead.
func TestMaterializeLeavesAnExistingDestinationSecurityDescriptorUnchanged(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "existing")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	// Make the descriptor distinctly permissive so any rewrite is visible.
	grantEveryone(t, destination)

	const securityInformation = windows.OWNER_SECURITY_INFORMATION |
		windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION
	snapshot, err := windows.GetNamedSecurityInfo(destination, windows.SE_FILE_OBJECT, securityInformation)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot.String()
	if before == "" {
		t.Fatal("the existing destination descriptor could not be serialized")
	}

	source := newFakeSource(t, map[string]string{"a.txt": "a"}, nil)
	if _, err := Materialize(context.Background(), source, destination, Options{}); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("err=%v, want existing destination", err)
	}

	current, err := windows.GetNamedSecurityInfo(destination, windows.SE_FILE_OBJECT, securityInformation)
	if err != nil {
		t.Fatal(err)
	}
	if after := current.String(); after != before {
		t.Fatalf("the existing destination descriptor changed:\nbefore %s\nafter  %s", before, after)
	}
	if _, statErr := os.Stat(filepath.Join(destination, "a.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("content was written into the existing destination: %v", statErr)
	}
}
