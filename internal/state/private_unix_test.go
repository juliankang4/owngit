//go:build !windows

package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A file that its group or other users can access is refused with its mode
// and the chmod command, quoted for a POSIX shell.
func TestUnixNotPrivateNamesTheModeAndTheFix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "it's a secret")
	noErr(t, os.WriteFile(path, []byte("secret\n"), 0o600))
	noErr(t, ValidatePrivateFile(path))
	for mode, problem := range map[os.FileMode]string{
		0o644: "its mode 0644 gives access to its group and all other users",
		0o640: "its mode 0640 gives access to its group",
		0o604: "its mode 0604 gives access to all other users",
		0o610: "its mode 0610 gives access to its group",
	} {
		noErr(t, os.Chmod(path, mode))
		file, err := os.Open(path)
		noErr(t, err)
		for source, err := range map[string]error{"path": ValidatePrivateFile(path), "handle": ValidatePrivateFileHandle(file)} {
			var notPrivate *NotPrivateError
			if !errors.As(err, &notPrivate) {
				t.Fatalf("%04o %s: err=%v, want *NotPrivateError", mode, source, err)
			}
			if notPrivate.Problem != problem || notPrivate.Fix != `chmod 600 '`+filepath.Dir(path)+`/it'\''s a secret'` {
				t.Errorf("%04o %s: problem %q fix %q", mode, source, notPrivate.Problem, notPrivate.Fix)
			}
		}
		noErr(t, file.Close())
	}
}

// The printed fix works for a relative path that starts with "-", which chmod
// would otherwise read as options.
func TestUnixNotPrivateFixKeepsADashPathAnOperand(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	const name = "-Rf secret"
	noErr(t, os.WriteFile(name, []byte("secret\n"), 0o644))
	var notPrivate *NotPrivateError
	if err := ValidatePrivateFile(name); !errors.As(err, &notPrivate) || notPrivate.Fix != `chmod 600 './-Rf secret'` || notPrivate.Shell != "" {
		t.Fatalf("err=%v fix=%+v", err, notPrivate)
	}
	output, err := exec.Command("sh", "-c", notPrivate.Fix).CombinedOutput()
	if err != nil {
		t.Fatalf("the fix failed: %v\n%s", err, output)
	}
	noErr(t, ValidatePrivateFile(name))
}
