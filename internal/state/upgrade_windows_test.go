//go:build windows

package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// While OpenUpgradeBackupFolder holds the backup folder, neither it nor a
// folder on the way to it can be renamed, so no other folder can take its
// place; after release both can.
func TestWindowsBackupFolderCannotBeRenamedWhileHeld(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	stateDir := filepath.Join(parent, "state")
	noErr(t, os.MkdirAll(stateDir, 0o700))
	folder := UpgradeBackupFolder(stateDir)
	held, release, err := OpenUpgradeBackupFolder(stateDir)
	noErr(t, err)
	noErr(t, requirePrivateFolder(held))
	if err := os.Rename(folder, folder+"-moved"); err == nil {
		release()
		t.Fatal("the held backup folder was renamed")
	}
	if err := os.Rename(parent, parent+"-moved"); err == nil {
		release()
		t.Fatal("a folder on the way to the held backup folder was renamed")
	}
	// Work inside the held folder still works.
	noErr(t, os.Mkdir(filepath.Join(folder, "stage"), 0o700))
	noErr(t, os.Rename(filepath.Join(folder, "stage"), filepath.Join(folder, "published")))
	release()
	noErr(t, os.Rename(folder, folder+"-moved"))
}

// An existing backup folder with an inherited access list is refused and
// left as it is; a new one is made private.
func TestWindowsExistingBackupFolderMustBePrivate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	noErr(t, os.MkdirAll(stateDir, 0o700))
	folder := UpgradeBackupFolder(stateDir)
	noErr(t, os.Mkdir(folder, 0o700))
	before, err := pathDescriptor(folder)
	noErr(t, err)
	_, _, err = OpenUpgradeBackupFolder(stateDir)
	if err == nil || !strings.Contains(err.Error(), "only this account can change") || !strings.Contains(err.Error(), "is not private to this account: the folder must belong to this account") {
		t.Fatalf("err=%v", err)
	}
	after, err := pathDescriptor(folder)
	noErr(t, err)
	if before.String() != after.String() {
		t.Fatalf("the refused folder's access list changed:\n%s\n%s", before, after)
	}
	noErr(t, os.Remove(folder))
	_, release, err := OpenUpgradeBackupFolder(stateDir)
	noErr(t, err)
	release()
}

// Folders that OwnGit creates on the way to the state and for the backups
// give another account no handle, even when their parent lets that account
// add, list and remove names in the folders it holds: another account
// that keeps opening the new folder's name while it is created gets
// nothing, and afterwards it can neither add a backup name nor remove the
// temporary copy or a published backup. The Windows test run creates the
// second local account; without it the test is skipped.
func TestWindowsNewFoldersGiveAnotherAccountNoHandle(t *testing.T) {
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
		{AccessPermissions: changes | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE,
			AccessMode: windows.GRANT_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(other)}},
	}, nil)
	noErr(t, err)
	noErr(t, windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))

	// grab keeps opening path as the other account, with the rights the
	// parent passes on, while create runs and briefly after, and returns
	// what it opened.
	grab := func(path string, create func()) []windows.Handle {
		name, err := windows.UTF16PtrFromString(path)
		noErr(t, err)
		var handles []windows.Handle
		started, stop, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(finished)
			as(func() {
				for tries := 0; ; tries++ {
					handle, err := windows.CreateFile(name, uint32(changes)|windows.SYNCHRONIZE,
						windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
						windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
					if err == nil {
						handles = append(handles, handle)
					}
					if tries == 0 {
						close(started)
					}
					select {
					case <-stop:
						return
					default:
					}
				}
			})
		}()
		select {
		case <-started:
		case <-finished:
			t.Fatal("the other account could not start")
		}
		func() {
			// Stopped even when create fails the test.
			defer func() {
				close(stop)
				<-finished
			}()
			create()
			time.Sleep(200 * time.Millisecond)
		}()
		t.Cleanup(func() {
			for _, handle := range handles {
				windows.CloseHandle(handle)
			}
		})
		return handles
	}

	// The control: a folder made by hand inherits the parent's entries, so
	// the other account gets a handle to it.
	control := filepath.Join(parent, "by-hand")
	if handles := grab(control, func() { noErr(t, os.Mkdir(control, 0o700)) }); len(handles) == 0 {
		t.Fatal("the control got no handle, so the parent does not let the other account in")
	}

	stateDir := filepath.Join(parent, "state")
	if handles := grab(stateDir, func() {
		held, err := CreateDirectory(stateDir)
		noErr(t, err)
		noErr(t, requirePrivateFolder(held))
		held.Close()
	}); len(handles) != 0 {
		t.Fatalf("the other account opened the new state directory %d times", len(handles))
	}

	folder := UpgradeBackupFolder(stateDir)
	var release func()
	if handles := grab(folder, func() {
		_, release, err = OpenUpgradeBackupFolder(stateDir)
		noErr(t, err)
	}); len(handles) != 0 {
		release()
		t.Fatalf("the other account opened the new backup folder %d times", len(handles))
	}
	defer release()
	copyDir, err := os.MkdirTemp(folder, ".owngit-upgrade-copy-*")
	noErr(t, err)
	backup := filepath.Join(folder, "pre-1.1.3-20260101T000000Z")
	noErr(t, os.Mkdir(backup, 0o700))
	noErr(t, os.WriteFile(filepath.Join(backup, "manifest.json"), []byte("{}"), 0o600))
	var changed []string
	as(func() {
		if os.Mkdir(backup+"-2", 0o700) == nil {
			changed = append(changed, "added a backup name")
		}
		for _, path := range []string{copyDir, filepath.Join(backup, "manifest.json"), backup} {
			if os.Remove(path) == nil {
				changed = append(changed, "removed "+path)
			}
		}
		if os.Rename(backup, backup+"-moved") == nil {
			changed = append(changed, "renamed the backup")
		}
	})
	if len(changed) != 0 {
		t.Fatalf("the other account changed the backup folder: %v", changed)
	}
	for _, path := range []string{copyDir, filepath.Join(backup, "manifest.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}

// otherAccountCredentials reads the second local account once per test
// process and removes its password from the environment, so no process the
// tests start sees it.
var otherAccountCredentials = sync.OnceValues(func() (string, string) {
	name, password := os.Getenv("OWNGIT_TEST_OTHER_ACCOUNT"), os.Getenv("OWNGIT_TEST_OTHER_PASSWORD")
	os.Unsetenv("OWNGIT_TEST_OTHER_PASSWORD")
	return name, password
})

// otherAccount logs on the second local account that the Windows test run
// creates, named by OWNGIT_TEST_OTHER_ACCOUNT with its password in
// OWNGIT_TEST_OTHER_PASSWORD, and returns its SID and a function that runs
// f on a thread that acts as that account.
func otherAccount(t *testing.T) (*windows.SID, func(f func())) {
	sid, _, as := otherAccountWithToken(t)
	return sid, as
}

// otherAccountWithToken also returns the primary logon token for a test that
// must start the product's printed command as the standard account itself.
func otherAccountWithToken(t *testing.T) (*windows.SID, windows.Token, func(f func())) {
	name, password := otherAccountCredentials()
	if name == "" || password == "" {
		t.Skip("needs a second local account: OWNGIT_TEST_OTHER_ACCOUNT and OWNGIT_TEST_OTHER_PASSWORD")
	}
	sid, _, _, err := windows.LookupSID("", name)
	noErr(t, err)
	advapi := windows.NewLazySystemDLL("advapi32.dll")
	logon, impersonate := advapi.NewProc("LogonUserW"), advapi.NewProc("ImpersonateLoggedOnUser")
	utf16 := func(s string) uintptr {
		p, err := windows.UTF16PtrFromString(s)
		noErr(t, err)
		return uintptr(unsafe.Pointer(p))
	}
	var token windows.Token
	var logonErr error
	// Network, then interactive, then batch: whichever logon the host allows.
	for _, logonType := range []uintptr{3, 2, 4} {
		ok, _, err := logon.Call(utf16(name), utf16("."), utf16(password), logonType, 0, uintptr(unsafe.Pointer(&token)))
		if ok != 0 {
			logonErr = nil
			break
		}
		logonErr = err
	}
	if logonErr != nil {
		t.Fatalf("log on %s: %v", name, logonErr)
	}
	var primary windows.Token
	noErr(t, windows.DuplicateTokenEx(token, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary))
	t.Cleanup(func() {
		primary.Close()
		token.Close()
	})
	return sid, primary, func(f func()) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			// Never unlocked: the thread ends with this goroutine, so no
			// other goroutine runs as the other account.
			runtime.LockOSThread()
			if ok, _, err := impersonate.Call(uintptr(token)); ok == 0 {
				t.Errorf("act as %s: %v", name, err)
				return
			}
			f()
		}()
		<-done
	}
}
