package tailscale

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// No test here reaches this Mac's own Tailscale, such as through Find: the
// app, its files, lsof and the open source socket are looked for where
// nothing is, unless a test points them at synthetic ones.
func init() {
	nowhere := "/nonexistent/owngit-test"
	macAppBundle, openSourceSocket, macStandaloneDir, lsofPath = nowhere, nowhere, nowhere, nowhere
}

// OwnGit reaches Tailscale on macOS in the order of Tailscale's own client:
// the app while it answers for this user (the App Store variant through the
// file its IPNExtension keeps open, as lsof lists it, then the Standalone
// variant through its port link and password file), otherwise the open
// source tailscaled's socket. The Tailscale it reports, app or not, is the
// one it reaches. The files, lsof and both daemons here are synthetic.
func TestMacLocalAPIFollowsTheTailscaleCommandsOrder(t *testing.T) {
	app, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	port := strconv.Itoa(app.Addr().(*net.TCPAddr).Port)
	dir := t.TempDir()
	// A Unix socket path must be short.
	short, err := os.MkdirTemp("/tmp", "ts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
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
	saved := []string{macStandaloneDir, lsofPath, openSourceSocket, macAppBundle}
	macStandaloneDir, lsofPath = standalone, lsof
	openSourceSocket, macAppBundle = filepath.Join(short, "s.sock"), filepath.Join(dir, "Tailscale.app")
	t.Cleanup(func() {
		macStandaloneDir, lsofPath, openSourceSocket, macAppBundle = saved[0], saved[1], saved[2], saved[3]
	})

	check := func(name, wantPassword string, wantMacApp bool) {
		t.Helper()
		conn, password, err := dialDarwin(context.Background())
		if err == nil {
			conn.Close()
		}
		if err != nil || password != wantPassword {
			t.Errorf("%s: password=%q err=%v, want %q", name, password, err, wantPassword)
		}
		if got := macApp("/opt/homebrew/bin/tailscale"); got != wantMacApp {
			t.Errorf("%s: mac app=%v, want %v", name, got, wantMacApp)
		}
	}

	setLsof("")
	if _, _, err := dialDarwin(context.Background()); err == nil || macApp("/opt/homebrew/bin/tailscale") {
		t.Fatalf("nothing installed: err=%v", err)
	}
	if !macApp(filepath.Join(dir, "Tailscale.app/Contents/MacOS/Tailscale")) {
		t.Error("the app's own executable is not the app")
	}
	if err := os.Mkdir(macAppBundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if !macApp("/opt/homebrew/bin/tailscale") {
		t.Error("only the app installed: not the app")
	}
	tailscaled, err := net.Listen("unix", openSourceSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer tailscaled.Close()
	check("both installed, the app not running", "", false)

	setLsof("p42\\nn/private/tmp/synthetic/Group Containers/X.io.tailscale.ipn.macos/sameuserproof-" + port + "-appstoretoken\\n")
	check("App Store app running", "appstoretoken", true)

	setLsof("")
	if err := os.Symlink(port, filepath.Join(standalone, "ipnport")); err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(standalone, "sameuserproof-"+port)
	if err := os.WriteFile(proof, []byte("standalonetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	check("Standalone app running", "standalonetoken", true)

	if os.Geteuid() != 0 {
		// An account outside the admin group falls back to the socket, as
		// the tailscale command does, and without it gets the reason.
		if err := os.Chmod(proof, 0); err != nil {
			t.Fatal(err)
		}
		check("Standalone app unreadable, socket", "", false)
		tailscaled.Close()
		os.Remove(openSourceSocket)
		if _, _, err := dialDarwin(context.Background()); KindOf(err) != KindMacAppAdmin {
			t.Errorf("Standalone app unreadable, no socket: err=%v", err)
		}
	}
}
