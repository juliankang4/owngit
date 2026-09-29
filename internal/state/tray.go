package state

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// The tray icon's files in the state directory. Both belong to this
// computer: backups do not carry them.
const (
	// TrayHiddenFile, when present, hides the tray icon on this computer
	// until it is shown again (see TrayHidden).
	TrayHiddenFile = "tray-hidden"
	// TrayAccessFile holds the address and the credential with which the
	// tray icon of this computer reads the server's status (see
	// PublishTrayAccess). Only this account can read it.
	TrayAccessFile = "tray-access.json"
)

// TrayHidden reports whether the owner hid the tray icon on the computer of
// the held state directory. The icon shows unless it was hidden; whether
// the computer has a desktop to show it on is a separate fact. The choice is
// a file, not a database record, so the icon can read it while the server is
// stopped.
func TrayHidden(held *os.File) (bool, error) {
	file, err := OpenOwnFile(held, TrayHiddenFile, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read whether the tray icon is hidden: %w", err)
	}
	return true, file.Close()
}

// SetTrayHidden hides the tray icon on the computer of the held state
// directory, or shows it again.
func SetTrayHidden(held *os.File, hidden bool) error {
	if !hidden {
		err := removeOwnFile(held, TrayHiddenFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("show the tray icon: %w", err)
		}
		return nil
	}
	file, err := OpenOwnFile(held, TrayHiddenFile, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return fmt.Errorf("hide the tray icon: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		file.Close()
		return fmt.Errorf("hide the tray icon: %w", err)
	}
	_, err = file.WriteString("The OwnGit tray icon is hidden on this computer. Run \"owngit tray on\" to show it again.\n")
	return errors.Join(err, file.Close())
}

// TrayAccess is the content of TrayAccessFile: the loopback address of the
// running server and the token its tray status answers to.
type TrayAccess struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// trayTokenBytes is the size of the random tray token.
const trayTokenBytes = 32

// PublishTrayAccess makes a new token for a server that answers at url and
// writes both to TrayAccessFile in the held state directory. Every start
// makes a new token, so a token that reached a program listening at the
// address while OwnGit was stopped is worthless once OwnGit runs again; the
// icon reads the file again when its token is refused. The file is written
// under a temporary name, made private to this account before anything is
// written to it, and then renamed, so a reader never sees a partial file.
func PublishTrayAccess(held *os.File, url string) (TrayAccess, error) {
	random := make([]byte, trayTokenBytes+8)
	if _, err := rand.Read(random); err != nil {
		return TrayAccess{}, err
	}
	access := TrayAccess{URL: url, Token: base64.RawURLEncoding.EncodeToString(random[:trayTokenBytes])}
	content, err := json.Marshal(access)
	if err != nil {
		return TrayAccess{}, err
	}
	temporary := ".tray-access-" + base64.RawURLEncoding.EncodeToString(random[trayTokenBytes:])
	file, err := OpenOwnFile(held, temporary, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return TrayAccess{}, fmt.Errorf("write the tray access file: %w", err)
	}
	err = ProtectPrivateHandle(file, false)
	if err == nil {
		_, err = file.Write(append(content, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err == nil {
		err = RenameOwnFile(held, temporary, TrayAccessFile)
	}
	if err != nil {
		_ = removeOwnFile(held, temporary)
		return TrayAccess{}, fmt.Errorf("write the tray access file: %w", err)
	}
	return access, nil
}
