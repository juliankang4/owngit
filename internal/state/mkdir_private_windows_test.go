//go:build windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsMkdirPrivateStartsWithOwnerOnlyDACL(t *testing.T) {
	parent, asOther := sharedParent(t)
	child := filepath.Join(parent, "staging")
	noErr(t, MkdirPrivate(child))
	info, err := os.Stat(child)
	noErr(t, err)
	changeable, _, err := OthersCanChange(child, info)
	if err != nil || changeable {
		t.Fatalf("new directory changeable=%v err=%v", changeable, err)
	}

	var writeErr error
	asOther(func() { writeErr = os.WriteFile(filepath.Join(child, "outside"), []byte("outside"), 0o600) })
	if writeErr == nil {
		t.Fatal("another account wrote into the atomically private directory")
	}
}
