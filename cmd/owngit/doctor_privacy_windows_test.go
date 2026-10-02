//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"owngit/internal/doctor"
	"owngit/internal/webui"
)

func TestWindowsDoctorUsesCompleteDACLReplacement(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "shared.git")
	noErr(t, os.Mkdir(repository, 0o700))
	setDoctorEveryoneWrite(t, root)
	setDoctorEveryoneWrite(t, repository)
	findings := repositoryPrivacyFindings(doctorSubject{
		server: doctor.ServerRunning, setupComplete: true, repositories: root, repositoryIDs: []string{"shared"},
	})
	if len(findings) != 2 || findings[0].Code != webui.MsgDoctorRepositoryRootShared || findings[1].Code != webui.MsgDoctorRepositoriesShared {
		t.Fatalf("findings=%+v", findings)
	}
	for _, finding := range findings {
		if !strings.Contains(finding.Repair, "SetSecurityDescriptorSddlForm") || strings.Contains(finding.Repair, "icacls") || strings.Contains(finding.Repair, "-Recurse") {
			t.Errorf("repair does not use the complete no-reparse rule: %q", finding.Repair)
		}
	}
	if !strings.Contains(findings[1].Repair, "Get-ChildItem") || !strings.Contains(findings[1].Repair, "SetOwner") || !strings.Contains(findings[1].Repair, "ReparsePoint") {
		t.Fatalf("repository repair is not recursively owner-only: %q", findings[1].Repair)
	}
}

func setDoctorEveryoneWrite(t *testing.T, path string) {
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
