//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"owngit/internal/service"
)

// tokenInformation reads one class of token information.
func tokenInformation(t *testing.T, token windows.Token, class uint32) unsafe.Pointer {
	t.Helper()
	var size uint32
	_ = windows.GetTokenInformation(token, class, nil, 0, &size)
	if size == 0 {
		t.Fatalf("token information class %d has no size", class)
	}
	buffer := make([]byte, size)
	noErr(t, windows.GetTokenInformation(token, class, &buffer[0], size, &size))
	return unsafe.Pointer(&buffer[0])
}

// The token the server runs with, made from this test's own token, denies
// the administrator groups, has Medium integrity, no privilege except
// SeChangeNotifyPrivilege, and makes the account the owner of new objects
// with access for the account and SYSTEM only.
func TestWithoutAdminTokenDropsAdministratorRights(t *testing.T) {
	restricted, err := withoutAdminToken()
	noErr(t, err)
	defer restricted.Close()
	own := windows.GetCurrentProcessToken()
	user, err := own.GetTokenUser()
	noErr(t, err)

	ownGroups, err := own.GetTokenGroups()
	noErr(t, err)
	held := map[string]bool{}
	for _, group := range ownGroups.AllGroups() {
		held[group.Sid.String()] = true
	}
	groups, err := restricted.GetTokenGroups()
	noErr(t, err)
	denied := 0
	for _, group := range groups.AllGroups() {
		if !slices.Contains(adminGroupSIDs, group.Sid.String()) {
			continue
		}
		if group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 || group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			t.Errorf("%s: attributes %#x, want deny only", group.Sid, group.Attributes)
		}
		denied++
	}
	if held["S-1-5-32-544"] && denied == 0 {
		t.Error("this process is in Administrators, but the restricted token does not list it as deny only")
	}
	t.Logf("administrator groups denied: %d (this process holds Administrators: %v, elevated: %v)", denied, held["S-1-5-32-544"], own.IsElevated())
	// The copy still belongs to an administrator account, so its checkup
	// gives the same repair as the elevated process.
	if service.AdministratorAccount(restricted) != service.AdministratorAccount(own) {
		t.Errorf("administrator account: restricted %v, own %v", service.AdministratorAccount(restricted), service.AdministratorAccount(own))
	}

	label := (*windows.Tokenmandatorylabel)(tokenInformation(t, restricted, windows.TokenIntegrityLevel))
	if got := label.Label.Sid.String(); got != "S-1-16-8192" {
		t.Errorf("integrity %s, want Medium (S-1-16-8192)", got)
	}

	var changeNotify windows.LUID
	name, _ := windows.UTF16PtrFromString("SeChangeNotifyPrivilege")
	noErr(t, windows.LookupPrivilegeValue(nil, name, &changeNotify))
	privileges := (*windows.Tokenprivileges)(tokenInformation(t, restricted, windows.TokenPrivileges))
	for _, privilege := range privileges.AllPrivileges() {
		if privilege.Luid != changeNotify {
			t.Errorf("privilege %v remains", privilege.Luid)
		}
	}

	owner := (*struct{ owner *windows.SID })(tokenInformation(t, restricted, windows.TokenOwner))
	if !owner.owner.Equals(user.User.Sid) {
		t.Errorf("new objects belong to %s, want %s", owner.owner, user.User.Sid)
	}
	dacl := (*struct{ dacl *windows.ACL })(tokenInformation(t, restricted, windows.TokenDefaultDacl)).dacl
	var trustees []string
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		noErr(t, windows.GetAce(dacl, index, &ace))
		trustees = append(trustees, (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String())
	}
	if want := []string{user.User.Sid.String(), "S-1-5-18"}; !slices.Equal(trustees, want) {
		t.Errorf("default access for %q, want %q", trustees, want)
	}
}

// With administrator rights, the ownership repair gives the account what
// the Administrators group owns in the folder, and nothing behind a link,
// nothing with a second name and nothing another owner has.
func TestGiveOwnershipFollowsNoLink(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("changing owners to the Administrators group needs administrator rights")
	}
	sid, err := platformCurrentAccountSID()
	noErr(t, err)
	administrators, err := windows.StringToSid(administratorsSID)
	noErr(t, err)
	system, err := windows.StringToSid("S-1-5-18")
	noErr(t, err)
	setOwner := func(path string, owner *windows.SID) {
		t.Helper()
		noErr(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, owner, nil, nil, nil))
	}
	// The check gets the final path, which Windows gives in its long form,
	// while TEMP may use 8.3 short names such as RUNNER~1.
	temporary, err := windows.UTF16PtrFromString(t.TempDir())
	noErr(t, err)
	long := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(temporary, &long[0], uint32(len(long)))
	noErr(t, err)
	base := windows.UTF16ToString(long[:n])
	root, outside := filepath.Join(base, "state"), filepath.Join(base, "outside")
	for _, dir := range []string{filepath.Join(root, "repositories", "a.git"), outside} {
		noErr(t, os.MkdirAll(dir, 0o700))
	}
	files := map[string]string{
		"head":    filepath.Join(root, "repositories", "a.git", "HEAD"),
		"outside": filepath.Join(outside, "secret"),
		"system":  filepath.Join(root, "system-owned"),
		"linked":  filepath.Join(root, "linked"),
	}
	for _, path := range files {
		noErr(t, os.WriteFile(path, []byte("x"), 0o600))
	}
	noErr(t, os.Link(files["linked"], filepath.Join(outside, "second-name")))
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "junction"), outside).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v\n%s", err, output)
	}
	for _, path := range []string{root, filepath.Join(root, "repositories"), filepath.Join(root, "repositories", "a.git"), files["head"], files["outside"], outside, files["linked"]} {
		setOwner(path, administrators)
	}
	setOwner(files["system"], system)

	checked := ""
	changed, failed, err := platformGiveOwnership(root, sid, func(finalPath, owner string) error {
		checked = finalPath + " " + owner
		return nil
	})
	noErr(t, err)
	if want := root + " " + administratorsSID; !strings.EqualFold(checked, want) {
		t.Errorf("checked %q, want %q", checked, want)
	}
	if changed != 4 || failed != 0 {
		t.Errorf("changed %d, failed %d; want 4 and 0", changed, failed)
	}
	for path, want := range map[string]string{
		root: sid, files["head"]: sid, filepath.Join(root, "repositories", "a.git"): sid,
		files["outside"]: administratorsSID, outside: administratorsSID,
		files["linked"]: administratorsSID, files["system"]: "S-1-5-18",
	} {
		if got, err := platformOwnerOf(path); err != nil || got != want {
			t.Errorf("%s: owner %s (%v), want %s", path, got, err, want)
		}
	}
}

// The step that gives a standard account its folders takes the account
// from the process that started it: here this test, so its own account.
func TestRequestingAccountIsTheParent(t *testing.T) {
	if os.Getenv("OWNGIT_TEST_REQUESTER") == "1" {
		sid, err := platformRequestingAccount()
		if err != nil {
			fmt.Print("error: ", err)
			os.Exit(1)
		}
		fmt.Print(sid)
		os.Exit(0)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRequestingAccountIsTheParent$")
	command.Env = append(os.Environ(), "OWNGIT_TEST_REQUESTER=1")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	own, err := platformCurrentAccountSID()
	noErr(t, err)
	if string(output) != own {
		t.Errorf("requesting account %q, want %q", output, own)
	}
}
