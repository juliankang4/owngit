//go:build !windows

package state

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A supplied secret is read from the handle that was checked. A link at the
// final name is followed and its target checked, as secret volumes that
// publish files through links need; a target of another account (made only
// by root) is refused.
func TestOpenPrivateInputFileChecksTheLinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	noErr(t, os.WriteFile(target, []byte("synthetic secret\n"), 0o600))
	link := filepath.Join(dir, "link")
	noErr(t, os.Symlink(target, link))
	file, err := OpenPrivateInputFile(link)
	noErr(t, err)
	content, err := io.ReadAll(file)
	noErr(t, errors.Join(err, file.Close()))
	if string(content) != "synthetic secret\n" {
		t.Fatalf("linked content = %q", content)
	}
	if os.Geteuid() != 0 {
		t.Log("the other-owner case needs root")
		return
	}
	noErr(t, os.Chown(target, 65534, 65534))
	var owner *PrivateInputOwnerError
	file, err = OpenPrivateInputFile(link)
	if file != nil {
		file.Close()
	}
	if !errors.As(err, &owner) {
		t.Fatalf("linked file of another account: err=%v, want *PrivateInputOwnerError", err)
	}
}
