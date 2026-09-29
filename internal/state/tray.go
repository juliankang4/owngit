package state

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// PublishTrayAccess returns the tray access for a server that answers at
// url, and makes TrayAccessFile in the held state directory say so. The
// token stays the same while the file holds a valid one, so an icon that
// read it keeps working across restarts; a missing or unreadable file gets
// a new token. The file is written under a temporary name, made private to
// this account and then renamed, so a reader never sees a partial file,
// and it is rewritten only when its content changes.
func PublishTrayAccess(held *os.File, url string) (TrayAccess, error) {
	current, err := readTrayAccess(held)
	if err == nil && current.URL == url {
		return current, nil
	}
	access := TrayAccess{URL: url, Token: current.Token}
	if err != nil {
		random := make([]byte, trayTokenBytes)
		if _, err := rand.Read(random); err != nil {
			return TrayAccess{}, err
		}
		access.Token = base64.RawURLEncoding.EncodeToString(random)
	}
	content, err := json.Marshal(access)
	if err != nil {
		return TrayAccess{}, err
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return TrayAccess{}, err
	}
	temporary := ".tray-access-" + base64.RawURLEncoding.EncodeToString(random)
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

// readTrayAccess reads TrayAccessFile and returns it when it holds a token
// PublishTrayAccess could have made.
func readTrayAccess(held *os.File) (TrayAccess, error) {
	file, err := OpenOwnFile(held, TrayAccessFile, os.O_RDONLY)
	if err != nil {
		return TrayAccess{}, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 4<<10))
	if err != nil {
		return TrayAccess{}, err
	}
	var access TrayAccess
	if err := json.Unmarshal(content, &access); err != nil {
		return TrayAccess{}, err
	}
	if token, err := base64.RawURLEncoding.DecodeString(access.Token); err != nil || len(token) != trayTokenBytes {
		return TrayAccess{}, errors.New("the tray access file holds no valid token")
	}
	return access, nil
}
