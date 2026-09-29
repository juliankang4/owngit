package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Exec line keeps every argument whole as the desktop entry
// specification reads it: string escapes first, then the quoting rules,
// and field codes start with a percent sign.
func TestRenderAutostartQuotesEveryArgument(t *testing.T) {
	entry, err := RenderAutostart(`/home/a b/bin/owngit`, `/home/a b/state "x" $HOME \ 100%`)
	if err != nil {
		t.Fatal(err)
	}
	want := `Exec="/home/a b/bin/owngit" "tray" "icon" "--state-dir" "/home/a b/state \\"x\\" \\$HOME \\\\ 100%%"` + "\n"
	if !strings.Contains(entry, want) {
		t.Errorf("entry\n%s\nwant the line\n%s", entry, want)
	}
	for _, line := range []string{"[Desktop Entry]\n", "Type=Application\n", "Terminal=false\n", "X-OwnGit-Icon=true\n"} {
		if !strings.Contains(entry, line) {
			t.Errorf("entry lacks %q", line)
		}
	}
	if _, err := RenderAutostart("/bin/owngit", "/state\nExec=evil"); err == nil {
		t.Error("a state directory with a line break was written")
	}
}

// OwnGit replaces and removes only an entry it wrote.
func TestReadAutostart(t *testing.T) {
	dir := t.TempDir()
	path := AutostartPath(dir)
	if path != filepath.Join(dir, "autostart", "owngit-icon.desktop") {
		t.Fatalf("path %s", path)
	}
	if found, err := ReadAutostart(path); err != nil || found != AutostartAbsent {
		t.Fatalf("absent: %v %v", found, err)
	}
	entry, err := RenderAutostart("/bin/owngit", "/state")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAutostart(path, entry); err != nil {
		t.Fatal(err)
	}
	if found, err := ReadAutostart(path); err != nil || found != AutostartOwn {
		t.Fatalf("own: %v %v", found, err)
	}
	if err := os.WriteFile(path, []byte("[Desktop Entry]\nExec=something-else\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, err := ReadAutostart(path); err != nil || found != AutostartForeign {
		t.Fatalf("foreign: %v %v", found, err)
	}
}
