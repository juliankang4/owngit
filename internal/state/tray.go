package state

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
// running server, the token its tray status answers to, and the secret with
// which it proves that an answer is its own.
type TrayAccess struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	Proof string `json:"proof"`
}

// trayTokenBytes is the size of the random tray token and of the proof
// secret.
const trayTokenBytes = 32

// The tray status proves its answer: the icon sends a new random nonce in
// TrayNonceHeader with every request, and the server answers with
// TrayProof of that nonce and the exact body in TrayProofHeader. Only the
// server and the icon hold the proof secret, which never crosses the
// connection, so a program that took the address while OwnGit was stopped
// cannot answer as OwnGit, even with the token the icon sent it.
const (
	TrayNonceHeader = "X-OwnGit-Tray-Nonce"
	TrayProofHeader = "X-OwnGit-Tray-Proof"
)

// TrayProof is the proof of a tray status answer: HMAC-SHA256 with the
// proof secret over "owngit tray status", the nonce and the body, each
// ended by a newline, in unpadded base64url.
func TrayProof(secret, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("owngit tray status\n" + nonce + "\n"))
	mac.Write(body)
	mac.Write([]byte("\n"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// NewTrayNonce returns a new random nonce for one status request.
func NewTrayNonce() (string, error) {
	random := make([]byte, trayTokenBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

// ValidTrayNonce reports whether nonce has the form NewTrayNonce makes.
func ValidTrayNonce(nonce string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(nonce)
	return err == nil && len(decoded) == trayTokenBytes
}

// PublishTrayAccess makes a new token and proof secret for a server that
// answers at url and writes them to TrayAccessFile in the held state
// directory. Every start makes new ones, so a token that reached a program listening at the
// address while OwnGit was stopped is worthless once OwnGit runs again; the
// icon reads the file again when its token is refused. The file is written
// under a temporary name, made private to this account before anything is
// written to it, and then renamed, so a reader never sees a partial file.
func PublishTrayAccess(held *os.File, url string) (TrayAccess, error) {
	random := make([]byte, 2*trayTokenBytes+8)
	if _, err := rand.Read(random); err != nil {
		return TrayAccess{}, err
	}
	encode := base64.RawURLEncoding.EncodeToString
	access := TrayAccess{URL: url, Token: encode(random[:trayTokenBytes]), Proof: encode(random[trayTokenBytes : 2*trayTokenBytes])}
	content, err := json.Marshal(access)
	if err != nil {
		return TrayAccess{}, err
	}
	temporary := ".tray-access-" + encode(random[2*trayTokenBytes:])
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
