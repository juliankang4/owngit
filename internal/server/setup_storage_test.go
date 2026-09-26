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
	"strings"
	"syscall"
	"testing"
	"time"

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

// A folder the account cannot write fails web setup with that reason and
// the command that gives the account the folder.
func TestSetupExplainsAFolderTheAccountCannotWrite(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this account cannot write")
	}
	app, store, _ := newTestApp(t)
	closed := filepath.Join(t.TempDir(), "closed")
	noErr(t, os.Mkdir(closed, 0o500))
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
	folder := filepath.Join(closed, "git")
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
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this account cannot write")
	}
	system := filepath.Join(t.TempDir(), "opt")
	noErr(t, os.Mkdir(system, 0o555))
	t.Cleanup(func() { _ = os.Chmod(system, 0o700) })
	denied := &fs.PathError{Op: "open", Path: system, Err: syscall.EACCES}
	notice := storageNotice(system, denied)
	if notice.Code != webui.MsgSetupStorageDeniedExisting || notice.Detail != filepath.Join(system, "owngit-repos") || strings.Contains(notice.Detail, "install") {
		t.Fatalf("existing folder: %+v", notice)
	}
	noErr(t, os.Chmod(system, 0o755))
	noErr(t, os.Mkdir(filepath.Join(system, "owngit-repos"), 0o700))
	noErr(t, os.Chmod(system, 0o555))
	if notice := storageNotice(system, denied); notice.Detail != filepath.Join(system, "owngit-repos-2") {
		t.Fatalf("taken name: %+v", notice)
	}
	// A folder this account cannot even look for counts as existing.
	unsearchable := filepath.Join(t.TempDir(), "closed")
	noErr(t, os.Mkdir(unsearchable, 0o600))
	t.Cleanup(func() { _ = os.Chmod(unsearchable, 0o700) })
	unknown := filepath.Join(unsearchable, "git")
	if notice := storageNotice(unknown, &fs.PathError{Op: "mkdir", Path: unknown, Err: syscall.EACCES}); notice.Code != webui.MsgSetupStorageDeniedExisting || strings.Contains(notice.Detail, "install") {
		t.Fatalf("folder that cannot be checked: %+v", notice)
	}
	missing := filepath.Join(system, "owngit-repos-2")
	notice = storageNotice(missing, &fs.PathError{Op: "mkdir", Path: missing, Err: syscall.EACCES})
	if notice.Code != webui.MsgSetupStorageDeniedGive || !strings.HasPrefix(notice.Detail, "sudo install -d ") || !strings.HasSuffix(notice.Detail, " "+shellWord(missing)) {
		t.Fatalf("missing folder: %+v", notice)
	}
}
