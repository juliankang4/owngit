//go:build !windows

package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A folder that root creates for another account is safe only where no
// other account can put a link first: every existing folder on the way
// belongs to root and nobody else may write to it.
func TestOnlyRootCanChangeTheWay(t *testing.T) {
	missing := filepath.Join(string(filepath.Separator), "owngit-test-missing-folder", "git")
	if !InspectFolderWay(missing).OnlyRoot {
		t.Fatalf("%s is under folders only root can change", missing)
	}
	if own := filepath.Join(t.TempDir(), "git"); os.Geteuid() != 0 && InspectFolderWay(own).OnlyRoot {
		t.Fatalf("%s is inside a folder of this account", own)
	}
	shared := filepath.Join(t.TempDir(), "shared")
	noErr(t, os.Mkdir(shared, 0o755))
	noErr(t, os.Chmod(shared, 0o777|os.ModeSticky))
	if InspectFolderWay(filepath.Join(shared, "git")).OnlyRoot {
		t.Fatal("a folder every account can create entries in counts as root's")
	}
}

// When root opens state in a folder that another account owns, the state
// would be that account's, so the refusal names the account, and nothing is
// created in its folder.
func TestRootIsToldToRunAsTheFolderOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	const nobody = 65534
	home := filepath.Join(resolveTestPath(t, t.TempDir()), "home")
	noErr(t, os.Mkdir(home, 0o755))
	noErr(t, os.Chown(home, nobody, nobody))
	store, err := Open(context.Background(), filepath.Join(home, ".config", "owngit"))
	if store != nil {
		_ = store.Close()
	}
	var other *OtherAccountError
	if !errors.As(err, &other) || other.Path != home || other.Account != accountName(nobody) {
		t.Fatalf("Open error=%v, want %s named as the account %s's", err, home, accountName(nobody))
	}
	if _, err := os.Lstat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Fatalf("the refused Open created a folder in the account's home: %v", err)
	}
}

// When the state directory itself belongs to another account, root is told
// to run the command as that account before anything uses the directory,
// such as serve's lock file.
func TestRootIsToldToRunAsTheStateDirectoryOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	const nobody = 65534
	directory := filepath.Join(resolveTestPath(t, t.TempDir()), "state")
	noErr(t, os.Mkdir(directory, 0o700))
	noErr(t, os.Chown(directory, nobody, nobody))
	held, err := CreateDirectory(directory)
	if held != nil {
		held.Close()
	}
	var other *OtherAccountError
	if !errors.As(err, &other) || other.Path != directory || other.Account != accountName(nobody) {
		t.Fatalf("CreateDirectory error=%v, want %s named as the account %s's", err, directory, accountName(nobody))
	}
}

// Another account can take a missing name in a sticky folder that every
// account may write with a link before root looks at it, for example to a
// folder of root's. Root must refuse the link, not create or use state
// where it leads. macOS follows such a link for root; Linux may not.
func TestRootRefusesALinkTakenInAStickyFolder(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	const nobody = 65534
	root := resolveTestPath(t, t.TempDir())
	noErr(t, os.Chmod(root, 0o755))
	sticky, target := filepath.Join(root, "shared"), filepath.Join(root, "roots")
	noErr(t, os.Mkdir(sticky, 0o755))
	noErr(t, os.Chmod(sticky, 0o777|os.ModeSticky))
	noErr(t, os.Mkdir(target, 0o755))
	link := filepath.Join(sticky, "state")
	noErr(t, os.Symlink(target, link))
	noErr(t, os.Lchown(link, nobody, nobody))
	held, err := CreateDirectory(link)
	var other *OtherAccountError
	if !errors.As(err, &other) || other.Path != link {
		name := ""
		if held != nil {
			name = held.Name()
			held.Close()
		}
		t.Fatalf("CreateDirectory returned %q, error=%v, want the link refused as another account's", name, err)
	}
	store, err := Open(context.Background(), link)
	if store != nil {
		_ = store.Close()
	}
	if !errors.As(err, &other) {
		t.Fatalf("Open error=%v, want the link refused as another account's", err)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("root's folder behind the link changed: %v %v", entries, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("root's folder behind the link changed: %v %v", info.Mode(), err)
	}
}
