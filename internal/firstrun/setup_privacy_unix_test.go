//go:build !windows

package firstrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTerminalFixtureReachable(t *testing.T, path string) {
	t.Helper()
	temporary := filepath.Clean(os.TempDir())
	relative, err := filepath.Rel(temporary, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Skipf("cannot make %s reachable from temporary folder %s", path, temporary)
	}
	for parent := filepath.Dir(path); parent != temporary; parent = filepath.Dir(parent) {
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Skipf("cannot make synthetic parent %s reachable: %v", parent, err)
		}
	}
}

func TestTerminalSetupWarnsForSharedFolderAndContinues(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	h := newHarness(t, "127.0.0.1:7654", Tailscale{State: TailscaleMissing})
	folder := filepath.Join(h.base, "shared-repositories")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	makeTerminalFixtureReachable(t, folder)
	if err := os.Chmod(folder, 0o777); err != nil {
		t.Fatal(err)
	}
	err := h.run("1\r", "1\r", folder+"\r", "1\r", "administrator password\r", "administrator password\r", "\r", "1\r")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, h.out)
	}
	output := h.out.String()
	if !strings.Contains(output, "[!] Setup continued, but other accounts can change the repository folder you") ||
		!strings.Contains(output, filepath.Base(folder)) {
		t.Fatalf("terminal warning is missing:\n%s", output)
	}
	if !h.settings().Initialized || h.started != 1 {
		t.Fatal("the warning blocked terminal setup")
	}
}

func TestTerminalSetupDoesNotWarnForSharedFolderBelowPrivateParent(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{State: TailscaleMissing})
	folder := filepath.Join(h.base, "shared-repositories")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(h.base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0o777); err != nil {
		t.Fatal(err)
	}
	err := h.run("1\r", "1\r", folder+"\r", "1\r", "administrator password\r", "administrator password\r", "\r", "1\r")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, h.out)
	}
	if output := h.out.String(); strings.Contains(output, "[!] Setup continued, but other accounts can change the repository folder you") {
		t.Fatalf("terminal reported a folder below a private parent:\n%s", output)
	}
	if !h.settings().Initialized || h.started != 1 {
		t.Fatal("terminal setup did not finish")
	}
}
