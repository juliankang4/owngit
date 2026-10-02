//go:build windows

package server

import (
	"context"
	"os"
	"testing"

	"golang.org/x/sys/windows"
	"owngit/internal/webui"
)

func TestWindowsSetupWarnsForSharedRepositoryFolderAndContinues(t *testing.T) {
	app, store, root := newTestApp(t)
	noErr(t, os.Mkdir(root, 0o700))
	setSetupInheritedWriteDACL(t, root)

	checked := app.CheckRepositoryFolder(root)
	if len(checked.Problems) != 0 || len(checked.Warnings) != 1 || checked.Warnings[0].Code != webui.MsgSetupStorageShared {
		t.Fatalf("folder check=%+v", checked)
	}
	feedback, err := app.CompleteSetup(context.Background(), setupAnswers(root), true)
	if err != nil || len(feedback.Problems) != 0 || len(feedback.Warnings) != 1 {
		t.Fatalf("setup feedback=%+v err=%v", feedback, err)
	}
	settings, err := store.Settings(context.Background())
	noErr(t, err)
	if !settings.Initialized {
		t.Fatal("the storage warning blocked setup")
	}
}

func setSetupInheritedWriteDACL(t *testing.T, path string) {
	t.Helper()
	userToken, err := windows.GetCurrentProcessToken().GetTokenUser()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	entry := func(sid *windows.SID, mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: mask, AccessMode: windows.GRANT_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)},
		}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entry(userToken.User.Sid, windows.GENERIC_ALL), entry(everyone, windows.GENERIC_WRITE),
	}, nil)
	noErr(t, err)
	noErr(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}
