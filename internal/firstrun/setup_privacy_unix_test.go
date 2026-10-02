//go:build !windows

package firstrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalSetupWarnsForSharedFolderAndContinues(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{State: TailscaleMissing})
	folder := filepath.Join(h.base, "shared-repositories")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
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
