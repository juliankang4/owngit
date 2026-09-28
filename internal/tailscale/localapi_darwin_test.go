package tailscale

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The Tailscale app for macOS is reached where the tailscale command finds
// it: the App Store variant through the file its IPNExtension keeps open, as
// lsof lists it, and otherwise the Standalone variant through its port link
// and password file. The files and lsof here are synthetic.
func TestMacAppLocalAPIIsFoundAsTheTailscaleCommandFindsIt(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	dir := t.TempDir()
	lsof := filepath.Join(dir, "lsof")
	setLsof := func(output string) {
		script := "#!/bin/sh\n[ -n \"" + output + "\" ] || exit 1\nprintf '" + output + "'\n"
		if err := os.WriteFile(lsof, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	standalone := filepath.Join(dir, "Tailscale")
	if err := os.Mkdir(standalone, 0o700); err != nil {
		t.Fatal(err)
	}
	savedDir, savedLsof := macStandaloneDir, lsofPath
	macStandaloneDir, lsofPath = standalone, lsof
	t.Cleanup(func() { macStandaloneDir, lsofPath = savedDir, savedLsof })

	dial := func() (string, error) {
		conn, password, err := localAPIDialer(true)(context.Background())
		if err == nil {
			conn.Close()
		}
		return password, err
	}
	setLsof("")
	if _, err := dial(); err == nil {
		t.Fatal("no app: connected")
	}

	setLsof("p42\\nn/private/tmp/synthetic/Group Containers/X.io.tailscale.ipn.macos/sameuserproof-" + port + "-appstoretoken\\n")
	if password, err := dial(); err != nil || password != "appstoretoken" {
		t.Fatalf("App Store: password=%q err=%v", password, err)
	}

	setLsof("")
	if err := os.Symlink(port, filepath.Join(standalone, "ipnport")); err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(standalone, "sameuserproof-"+port)
	if err := os.WriteFile(proof, []byte("standalonetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if password, err := dial(); err != nil || password != "standalonetoken" {
		t.Fatalf("Standalone: password=%q err=%v", password, err)
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(proof, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := dial(); KindOf(err) != KindPermission {
			t.Fatalf("unreadable password file: err=%v", err)
		}
	}
}
