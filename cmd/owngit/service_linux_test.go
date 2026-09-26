package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/service"
)

// Root uses the account's state directory only when it is a real directory
// owned by the account, reached through no link that someone other than
// root made; the account could otherwise point root at any file.
func TestAccountStateDirectoryRefusesWhatTheAccountCouldRedirect(t *testing.T) {
	uid := os.Getuid()
	home := t.TempDir()
	stateDir := filepath.Join(home, "state")
	noErr(t, os.Mkdir(stateDir, 0o700))
	noErr(t, accountStateDirectory(stateDir, uid))

	if err := accountStateDirectory(stateDir, uid+1); err == nil {
		t.Fatal("accepted a directory of another account")
	}
	link := filepath.Join(home, "link")
	noErr(t, os.Symlink(stateDir, link))
	if os.Geteuid() == 0 {
		// Root runs this test; the link must look made by another account.
		noErr(t, os.Lchown(link, 65534, 65534))
	}
	if err := accountStateDirectory(link, uid); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("accepted a state directory that is a link: %v", err)
	}
	noErr(t, os.Mkdir(filepath.Join(stateDir, "inner"), 0o700))
	if err := accountStateDirectory(filepath.Join(link, "inner"), uid); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("accepted a path through a link: %v", err)
	}
	file := filepath.Join(home, "file")
	noErr(t, os.WriteFile(file, nil, 0o600))
	if err := accountStateDirectory(file, uid); err == nil {
		t.Fatal("accepted a file")
	}
}

// A root-installed service runs only a binary that root alone can change.
func TestRootControlledExecutable(t *testing.T) {
	if info, err := os.Stat("/bin/sh"); err == nil {
		if uid, _, _ := service.FileOwner(info); uid == 0 {
			noErr(t, rootControlledExecutable("/bin/sh"))
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("root owns every file it makes")
	}
	binary := filepath.Join(t.TempDir(), "owngit")
	noErr(t, os.WriteFile(binary, nil, 0o755))
	if err := rootControlledExecutable(binary); err == nil || !strings.Contains(err.Error(), "does not belong to root") {
		t.Fatalf("accepted a binary another account can change: %v", err)
	}
}
