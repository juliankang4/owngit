package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFolderChooserListingDoesNotTraverseLink(t *testing.T) {
	app, store, _ := newTestApp(t)
	root, target := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "linked")
	noErr(t, os.Symlink(target, link))
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	old := unix.Timespec{Sec: 1}
	reset := func() {
		noErr(t, unix.UtimesNanoAt(unix.AT_FDCWD, link, []unix.Timespec{old, old}, unix.AT_SYMLINK_NOFOLLOW))
	}
	atime := func() unix.Timespec {
		var stat unix.Stat_t
		noErr(t, unix.Lstat(link, &stat))
		return stat.Atim
	}
	// Following a symlink updates its access time on this filesystem.
	// First verify the observation, then reset it before the real requests.
	reset()
	_, err := os.Stat(link)
	noErr(t, err)
	if atime() == old {
		t.Skip("filesystem does not expose symlink access time")
	}
	for _, showHidden := range []bool{false, true} {
		reset()
		result, err := listFolders(context.Background(), root, showHidden, false)
		noErr(t, err)
		if len(result.Folders) != 1 || result.Folders[0].Path != link {
			t.Fatalf("link candidate: %+v", result)
		}
		if got := atime(); got != old {
			t.Fatalf("hidden=%v: listing followed the link: %v", showHidden, got)
		}
		response := folderRequest(t, app, setupFoldersPath, link, "", "owner-session", "owner-csrf", "localhost", "http://localhost")
		if response.Code != http.StatusOK || readFolderResult(t, response).Path != link {
			t.Fatalf("explicit navigation: %d %s", response.Code, response.Body.String())
		}
		if atime() == old {
			t.Fatal("explicit navigation did not resolve the selected link")
		}
	}
}
