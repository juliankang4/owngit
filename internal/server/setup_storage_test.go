package server

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// Each repository folder problem gets its own message, not the one about
// relative paths.
func TestStorageProblemNamesTheCause(t *testing.T) {
	for _, test := range []struct {
		err  error
		want webui.MessageCode
	}{
		{&fs.PathError{Op: "mkdir", Path: "/srv/git", Err: syscall.EROFS}, webui.MsgSetupStorageReadOnly},
		{&fs.PathError{Op: "mkdir", Path: "/srv/git", Err: syscall.EACCES}, webui.MsgSetupStorageDenied},
		{&fs.PathError{Op: "mkdir", Path: "/srv/git", Err: syscall.EPERM}, webui.MsgSetupStorageDenied},
		{errNotDirectory, webui.MsgSetupStorageNotDir},
		{errOverlapsState, webui.MsgSetupStorageOverlap},
		{errRelativeRoot, webui.MsgSetupStorageInvalid},
		{&fs.PathError{Op: "mkdir", Path: "/srv/git", Err: syscall.EIO}, webui.MsgSetupStorageUnusable},
	} {
		if got := storageProblem(test.err); got != test.want {
			t.Errorf("storageProblem(%v) = %s, want %s", test.err, got, test.want)
		}
	}
}

// rootOnlyFolder returns an existing system folder that only root can change
// and this account cannot write, so a missing folder inside it gets the
// command. The tests create nothing there.
func rootOnlyFolder(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder that only root can change and this account cannot write")
	}
	for _, candidate := range []string{"/Library", "/usr", "/"} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && state.InspectFolderWay(candidate).OnlyRoot {
			return candidate
		}
	}
	t.Skip("no folder that only root can change")
	return ""
}

// A folder the account cannot write fails web setup with that reason and
// the command that gives the account the folder.
func TestSetupExplainsAFolderTheAccountCannotWrite(t *testing.T) {
	system := rootOnlyFolder(t)
	app, store, _ := newTestApp(t)
	folder := filepath.Join(system, "owngit-test-"+strconv.FormatInt(time.Now().UnixNano(), 36), "git")
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	browser := newHostBrowser(t, app, "")
	browser.redeem("synthetic-owner-token")
	_, page := browser.finishPage(store, folder, url.Values{})
	account, err := user.Current()
	noErr(t, err)
	group, err := user.LookupGroupId(account.Gid)
	noErr(t, err)
	command := fmt.Sprintf("sudo install -d -o %s -g %s -m 0700 %s", account.Username, group.Name, shellWord(folder))
	if !strings.Contains(page, template.HTMLEscapeString(webui.Text(webui.LangEN, webui.MsgSetupStorageDeniedGive))) || !strings.Contains(page, template.HTMLEscapeString(command)) {
		t.Fatalf("setup page does not explain the folder or give %q:\n%s", command, page)
	}
	if strings.Contains(page, template.HTMLEscapeString(webui.Text(webui.LangEN, webui.MsgSetupStorageInvalid))) {
		t.Fatal("setup page asks for a full path")
	}
}

// A folder below one that nobody may enter, as ProtectHome makes the home
// folders for the owngit account, is read-only for OwnGit, and the command
// that gives the account the folder would not help.
func TestSetupCallsAHiddenFolderReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this account cannot enter")
	}
	hidden := filepath.Join(t.TempDir(), "home")
	noErr(t, os.Mkdir(hidden, 0o000))
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o700) })
	notice := storageNotice(filepath.Join(hidden, "alice", "git"), &fs.PathError{Op: "mkdir", Path: hidden, Err: syscall.EACCES})
	if notice.Code != webui.MsgSetupStorageReadOnly || notice.Detail != "" {
		t.Fatalf("notice = %+v", notice)
	}
}

// An existing folder the account cannot write, such as /opt, never gets a
// command: install -d would hand that folder to the account. The page
// suggests a new folder inside it, and that one gets the command.
func TestSetupNeverOffersToGiveAwayAnExistingFolder(t *testing.T) {
	system := rootOnlyFolder(t)
	denied := &fs.PathError{Op: "open", Path: system, Err: syscall.EACCES}
	notice := storageNotice(system, denied)
	inside := freeSubfolder(system)
	if notice.Code != webui.MsgSetupStorageDeniedExisting || notice.Detail != inside || strings.Contains(notice.Detail, "install") {
		t.Fatalf("existing folder: %+v", notice)
	}
	notice = storageNotice(inside, &fs.PathError{Op: "mkdir", Path: inside, Err: syscall.EACCES})
	if notice.Code != webui.MsgSetupStorageDeniedGive || !strings.HasPrefix(notice.Detail, "sudo install -d ") || !strings.HasSuffix(notice.Detail, " "+shellWord(inside)) {
		t.Fatalf("suggested folder: %+v", notice)
	}
}

// The suggested folder inside an existing one is a name that is not taken.
func TestSuggestedFolderIsNotTaken(t *testing.T) {
	parent := t.TempDir()
	if got := freeSubfolder(parent); got != filepath.Join(parent, "owngit-repos") {
		t.Fatalf("free name: %s", got)
	}
	noErr(t, os.Mkdir(filepath.Join(parent, "owngit-repos"), 0o700))
	if got := freeSubfolder(parent); got != filepath.Join(parent, "owngit-repos-2") {
		t.Fatalf("taken name: %s", got)
	}
}

// Root runs the command, so it is not offered where another account could
// put a link at the folder first and make root give away what the link
// leads to: inside a folder of another account (this test's own folders
// stand for one, since they are not root's), or in a folder every account
// may create entries in. Nor is a new folder suggested there, or inside a
// folder this account cannot look into, because it would not get the
// command either.
func TestSetupOffersNoCommandWhereAnotherAccountCouldPlantALink(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs folders that are not root's")
	}
	other := filepath.Join(t.TempDir(), "home")
	noErr(t, os.Mkdir(other, 0o555))
	t.Cleanup(func() { _ = os.Chmod(other, 0o700) })
	unsearchable := filepath.Join(t.TempDir(), "closed")
	noErr(t, os.Mkdir(unsearchable, 0o600))
	t.Cleanup(func() { _ = os.Chmod(unsearchable, 0o700) })
	shared := filepath.Join(t.TempDir(), "shared")
	noErr(t, os.Mkdir(shared, 0o755))
	noErr(t, os.Chmod(shared, 0o777|os.ModeSticky))
	for _, folder := range []string{
		filepath.Join(other, "git"),
		other,
		filepath.Join(unsearchable, "git"),
		filepath.Join(shared, "git"),
	} {
		notice := storageNotice(folder, &fs.PathError{Op: "mkdir", Path: folder, Err: syscall.EACCES})
		if notice.Code != webui.MsgSetupStorageDenied || notice.Detail != "" {
			t.Errorf("%s: %+v, want the plain refusal", folder, notice)
		}
	}
}
