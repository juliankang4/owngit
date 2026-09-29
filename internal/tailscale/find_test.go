package tailscale

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Without an override, Find takes the tailscale command on PATH first. It
// only checks that the file exists and never runs or dials anything.
func TestFindTakesTheCommandOnPathFirst(t *testing.T) {
	dir := t.TempDir()
	name := "tailscale"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	command, err := Find("")
	if err != nil || command.Path != path || command.LocalAPI == nil {
		t.Fatalf("Find = %+v, %v; want %s", command, err, path)
	}
}
