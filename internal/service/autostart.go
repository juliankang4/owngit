package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The OwnGit icon on a Linux desktop starts at sign-in from an entry in the
// account's XDG autostart folder, which GNOME, KDE Plasma, Omarchy and
// other desktops read. It runs "owngit tray icon" as the account; nothing
// is installed for other accounts or with root.

// AutostartName is the file name of the icon's autostart entry.
const AutostartName = "owngit-icon.desktop"

// autostartMarker marks an entry that OwnGit wrote, so OwnGit replaces or
// removes only its own.
const autostartMarker = "X-OwnGit-Icon=true"

// AutostartPath is the icon's autostart entry in the account's
// configuration folder (XDG_CONFIG_HOME, usually ~/.config).
func AutostartPath(configDir string) string {
	return filepath.Join(configDir, "autostart", AutostartName)
}

// RenderAutostart is the autostart entry that runs the icon of the server
// of stateDir with executable.
func RenderAutostart(executable, stateDir string) (string, error) {
	command := []string{}
	for _, argument := range []string{executable, "tray", "icon", "--state-dir", stateDir} {
		quoted, err := desktopQuote(argument)
		if err != nil {
			return "", err
		}
		command = append(command, quoted)
	}
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=OwnGit icon\n" +
		"Name[ko]=OwnGit 아이콘\n" +
		"Comment=Shows whether OwnGit runs, its clone address and the latest pushes. OwnGit runs without it.\n" +
		"Comment[ko]=OwnGit 실행 상태, 클론 주소, 최근 푸시를 보여 줍니다. 이 아이콘 없이도 OwnGit은 실행됩니다.\n" +
		"Exec=" + strings.Join(command, " ") + "\n" +
		"Terminal=false\n" +
		"NoDisplay=true\n" +
		"X-GNOME-Autostart-enabled=true\n" +
		autostartMarker + "\n", nil
}

// desktopQuote quotes one argument of an Exec line: in double quotes with
// the characters the specification reserves escaped, then written as a
// desktop entry string, where a backslash is itself escaped, and with a
// percent sign doubled, since field codes start with it.
func desktopQuote(argument string) (string, error) {
	if strings.ContainsFunc(argument, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", fmt.Errorf("%q cannot be written in an autostart entry", argument)
	}
	var quoted strings.Builder
	for _, r := range argument {
		switch r {
		case '"', '`', '$', '\\':
			quoted.WriteString(`\\`)
			if r == '\\' {
				quoted.WriteString(`\\`)
				continue
			}
		case '%':
			quoted.WriteRune('%')
		}
		quoted.WriteRune(r)
	}
	return `"` + quoted.String() + `"`, nil
}

// AutostartState is what the icon's autostart entry path holds.
type AutostartState int

const (
	// AutostartAbsent means there is no entry.
	AutostartAbsent AutostartState = iota
	// AutostartOwn means the entry is one OwnGit wrote.
	AutostartOwn
	// AutostartForeign means a file of that name that OwnGit did not
	// write; OwnGit leaves it alone.
	AutostartForeign
)

// ReadAutostart reports what path holds.
func ReadAutostart(path string) (AutostartState, error) {
	content, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return AutostartAbsent, nil
	case err != nil:
		return AutostartAbsent, err
	case strings.Contains("\n"+string(content), "\n"+autostartMarker+"\n"):
		return AutostartOwn, nil
	}
	return AutostartForeign, nil
}

// WriteAutostart writes the entry in one step.
func WriteAutostart(path, entry string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, []byte(entry), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
