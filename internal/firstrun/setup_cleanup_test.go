package firstrun

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Setup that is saved while its obsolete owner setup file cannot be removed
// finishes in the terminal, and the server log says why the file remains.
func TestTerminalSetupLogsASetupFileItCouldNotRemove(t *testing.T) {
	h := newHarness(t, "127.0.0.1:7654", Tailscale{State: TailscaleMissing})
	// A nonempty directory at the owner file path makes its removal fail.
	if err := os.MkdirAll(filepath.Join(h.store.Dir(), "owner-setup.html", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	var logs lockedLog
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) })

	if err := h.run("1\r", "1\r", "\r", "1\r", "admin-password-1\r", "admin-password-1\r", "\r", "1\r"); err != nil {
		t.Fatalf("run: %v\n%s", err, h.out)
	}
	if settings := h.settings(); !settings.Initialized || !strings.Contains(h.out.String(), "+- [ok] Setup complete ") {
		t.Fatalf("settings=%+v output:\n%s", settings, h.out)
	}
	logged := logs.String()
	if strings.Count(logged, "\n") != 1 || !strings.HasPrefix(logged, `terminal setup: setup file removal could not be completed: "setup was saved but the owner setup files remain: remove `) {
		t.Fatalf("log=%q, want one line naming the file that remains", logged)
	}
}

// lockedLog is the log output shared with the flow's goroutines.
type lockedLog struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}
