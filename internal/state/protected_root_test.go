//go:build !windows

package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
