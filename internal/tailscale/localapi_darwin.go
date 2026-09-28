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

// localAPIDialer reaches the LocalAPI of the open source tailscaled, or of
// the Tailscale app for macOS when macApp is set.
func localAPIDialer(macApp bool) Dialer {
	if !macApp {
		return unixSocket(openSourceSocket)
	}
	return dialMacApp
}

// dialMacApp connects to the LocalAPI of the Tailscale app: the App Store
// variant, or else the Standalone one, in the order the tailscale command
// tries them.
func dialMacApp(ctx context.Context) (net.Conn, string, error) {
	port, password, err := macAppStoreCredentials(ctx)
	if err != nil {
		port, password, err = macStandaloneCredentials()
	}
	if err != nil {
		return nil, "", err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	return conn, password, err
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
		return 0, "", &Error{Kind: KindPermission, Detail: "only members of the admin group can reach the Tailscale app"}
	case err != nil:
		return 0, "", errNoMacApp
	}
	password := strings.TrimSpace(string(content))
	if password == "" {
		return 0, "", errNoMacApp
	}
	return port, password, nil
}
