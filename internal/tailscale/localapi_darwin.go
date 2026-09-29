package tailscale

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Where the Tailscale app for macOS tells a tailscale command run by a user
// how to reach its LocalAPI: a loopback TCP port and a password. Variables
// only so tests can point them at synthetic files.
var (
	// macStandaloneDir holds, for the Standalone variant, the symbolic
	// link "ipnport" to the port and the file "sameuserproof-PORT" with the
	// password, readable by the admin group.
	macStandaloneDir = "/Library/Tailscale"
	// lsofPath lists open files. The App Store variant keeps its file
	// "sameuserproof-PORT-PASSWORD" open, in a folder only that app may
	// read, so the name is taken from lsof, as the tailscale command does.
	lsofPath = "/usr/sbin/lsof"
)

// localAPIFor returns whether path's Tailscale is the Tailscale app for
// macOS, and how to reach its LocalAPI. Both follow the order of Tailscale's
// own client (client/local defaultDialer and safesocket_darwin.go at
// v1.102.5, commit 5fb2a81b065b), which the tailscale command uses for
// status: the app while it answers for this user, otherwise the open source
// tailscaled's socket. Status and Serve then reach the same Tailscale.
func localAPIFor(path string) (bool, Dialer) {
	return macApp(path), dialDarwin
}

// macApp reports whether path's Tailscale is the app: path is the app's
// bundle executable, or only the app is installed, or both it and the open
// source tailscaled are and the app answers for this user.
func macApp(path string) bool {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if strings.Contains(path, ".app/Contents/") {
		return true
	}
	_, bundleErr := os.Stat(macAppBundle)
	_, socketErr := os.Stat(openSourceSocket)
	switch {
	case bundleErr != nil:
		return false
	case socketErr != nil:
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	_, _, err := macAppCredentials(ctx)
	return err == nil
}

// dialDarwin connects to the app's LocalAPI when the app answers for this
// user, and otherwise to the open source tailscaled's socket. When neither
// answers because this account may not read the Standalone app's password,
// that is the reason given, unless a socket is there that OwnGit does not
// trust (KindUntrustedSocket), which is the more specific reason.
func dialDarwin(ctx context.Context) (net.Conn, string, error) {
	port, password, appErr := macAppCredentials(ctx)
	if appErr == nil {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		return conn, password, err
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	conn, _, err := unixSocket(openSourceSocket)(ctx)
	if err != nil && KindOf(err) != KindUntrustedSocket && !errors.Is(appErr, errNoMacApp) {
		return nil, "", appErr
	}
	return conn, "", err
}

// macAppCredentials finds the port and password of the app: the App Store
// variant, or else the Standalone one, in the order the tailscale command
// tries them.
func macAppCredentials(ctx context.Context) (int, string, error) {
	port, password, err := macAppStoreCredentials(ctx)
	if err != nil {
		port, password, err = macStandaloneCredentials()
	}
	return port, password, err
}

// errNoMacApp means that no Tailscale app for macOS runs for this user.
var errNoMacApp = errors.New("no Tailscale app answers for this user")

// macAppStoreCredentials finds the port and password of the App Store
// variant among the files its IPNExtension process has open.
func macAppStoreCredentials(ctx context.Context) (int, string, error) {
	command := exec.CommandContext(ctx, lsofPath, "-n", "-a", "-u"+strconv.Itoa(os.Getuid()), "-c", "IPNExtension", "-F", "n")
	command.Stdin = nil
	var output limitedBuffer
	command.Stdout = &output
	// lsof fails when no process matches.
	_ = command.Run()
	if ctx.Err() != nil {
		return 0, "", ctx.Err()
	}
	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	for scanner.Scan() {
		_, after, found := strings.Cut(scanner.Text(), ".tailscale.ipn.macos/sameuserproof-")
		if !found {
			continue
		}
		portText, password, _ := strings.Cut(after, "-")
		if port, err := strconv.Atoi(portText); err == nil && password != "" {
			return port, password, nil
		}
	}
	return 0, "", errNoMacApp
}

// macStandaloneCredentials reads the port and password of the Standalone
// variant. Its password file is readable only by the admin group.
func macStandaloneCredentials() (int, string, error) {
	portText, err := os.Readlink(filepath.Join(macStandaloneDir, "ipnport"))
	if err != nil {
		return 0, "", errNoMacApp
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, "", errNoMacApp
	}
	content, err := os.ReadFile(filepath.Join(macStandaloneDir, "sameuserproof-"+portText))
	switch {
	case errors.Is(err, fs.ErrPermission):
		return 0, "", &Error{Kind: KindMacAppAdmin}
	case err != nil:
		return 0, "", errNoMacApp
	}
	password := strings.TrimSpace(string(content))
	if password == "" {
		return 0, "", errNoMacApp
	}
	return port, password, nil
}
