package state

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
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
	// TrayNotificationsFile holds which desktop notifications the icon of
	// this computer shows (see TrayNotifications). Without it, it shows
	// every kind.
	TrayNotificationsFile = "tray-notifications.json"
	// TrayCursorFile holds the cursor of the event feed: what the icon of
	// this computer has shown or dropped. Hiding the icon removes it, so an
	// icon shown again starts with what happens from then on.
	TrayCursorFile = "tray-cursor"
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
	// A hidden icon shows no notification, including later for what
	// happens while it is hidden.
	if err := RemoveTrayCursor(held); err != nil {
		return fmt.Errorf("hide the tray icon: %w", err)
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

// The kinds of desktop notification. The event feed of the server names
// each notification's kind; TrayNotifications turns each off.
const (
	NotifyPush         = "push"
	NotifyPullRequest  = "pull_request"
	NotifyCheckFailed  = "check_failed"
	NotifyImportFailed = "import_failed"
	NotifyBackupFailed = "backup_failed"
	NotifyUpdate       = "update"
)

// NotifyKinds are all kinds of notification, in the order the settings
// list them.
var NotifyKinds = []string{NotifyPush, NotifyPullRequest, NotifyCheckFailed, NotifyImportFailed, NotifyBackupFailed, NotifyUpdate}

// TrayNotifications is the content of TrayNotificationsFile: the owner's
// choice of desktop notifications on this computer. The zero value, which
// a missing file means, shows every kind.
type TrayNotifications struct {
	// Off turns every notification off.
	Off bool `json:"off"`
	// OnlyOthers drops what came from this computer, where the owner works.
	OnlyOthers bool `json:"only_others"`
	// KindsOff are the kinds turned off one by one.
	KindsOff []string `json:"kinds_off"`
}

// Shows reports whether notifications of kind show.
func (choice TrayNotifications) Shows(kind string) bool {
	return !choice.Off && !slices.Contains(choice.KindsOff, kind)
}

// Kinds are the kinds that show, in NotifyKinds order.
func (choice TrayNotifications) Kinds() []string {
	kinds := []string{}
	for _, kind := range NotifyKinds {
		if choice.Shows(kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// trayNotificationsLimit bounds TrayNotificationsFile.
const trayNotificationsLimit = 4 << 10

// ReadTrayNotifications reads the owner's choice of notifications in the
// held state directory. A file that is not such a choice is an error, never
// taken as a default.
func ReadTrayNotifications(held *os.File) (TrayNotifications, error) {
	content, err := readOwnFile(held, TrayNotificationsFile, trayNotificationsLimit)
	if errors.Is(err, os.ErrNotExist) {
		return TrayNotifications{}, nil
	}
	if err != nil {
		return TrayNotifications{}, fmt.Errorf("read the notification settings: %w", err)
	}
	var choice TrayNotifications
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&choice); err != nil {
		return TrayNotifications{}, fmt.Errorf("read the notification settings: %s is not valid: %w", TrayNotificationsFile, err)
	}
	for _, kind := range choice.KindsOff {
		if !slices.Contains(NotifyKinds, kind) {
			return TrayNotifications{}, fmt.Errorf("read the notification settings: %s names the unknown kind %q", TrayNotificationsFile, kind)
		}
	}
	return choice, nil
}

// WriteTrayNotifications saves the owner's choice of notifications in the
// held state directory.
func WriteTrayNotifications(held *os.File, choice TrayNotifications) error {
	kinds := []string{}
	for _, kind := range NotifyKinds {
		if slices.Contains(choice.KindsOff, kind) {
			kinds = append(kinds, kind)
		}
	}
	for _, kind := range choice.KindsOff {
		if !slices.Contains(NotifyKinds, kind) {
			return fmt.Errorf("save the notification settings: unknown kind %q", kind)
		}
	}
	choice.KindsOff = kinds
	content, err := json.Marshal(choice)
	if err == nil {
		err = replaceOwnFile(held, TrayNotificationsFile, append(content, '\n'))
	}
	if err != nil {
		return fmt.Errorf("save the notification settings: %w", err)
	}
	return nil
}

// TrayCursorLimit bounds a feed cursor.
const TrayCursorLimit = 512

// ValidTrayCursor reports whether cursor has the form of a feed cursor:
// unpadded base64url of at most TrayCursorLimit bytes.
func ValidTrayCursor(cursor string) bool {
	_, err := base64.RawURLEncoding.DecodeString(cursor)
	return cursor != "" && len(cursor) <= TrayCursorLimit && err == nil
}

// ReadTrayCursor returns the feed cursor in the held state directory, or ""
// when there is none. A file that holds no cursor is an error: the icon then
// shows nothing rather than start again from the present.
func ReadTrayCursor(held *os.File) (string, error) {
	content, err := readOwnFile(held, TrayCursorFile, TrayCursorLimit+1)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the notification cursor: %w", err)
	}
	cursor := strings.TrimSuffix(string(content), "\n")
	if !ValidTrayCursor(cursor) {
		return "", fmt.Errorf("read the notification cursor: %s holds no cursor", TrayCursorFile)
	}
	return cursor, nil
}

// WriteTrayCursor saves the feed cursor in the held state directory.
func WriteTrayCursor(held *os.File, cursor string) error {
	if !ValidTrayCursor(cursor) {
		return errors.New("save the notification cursor: not a cursor")
	}
	if err := replaceOwnFile(held, TrayCursorFile, []byte(cursor+"\n")); err != nil {
		return fmt.Errorf("save the notification cursor: %w", err)
	}
	return nil
}

// RemoveTrayCursor removes the feed cursor from the held state directory,
// so the icon starts again with what happens from then on.
func RemoveTrayCursor(held *os.File) error {
	err := removeOwnFile(held, TrayCursorFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the notification cursor: %w", err)
	}
	return nil
}

// readOwnFile reads the file name in the held directory, which must hold at
// most limit bytes.
func readOwnFile(held *os.File, name string, limit int64) ([]byte, error) {
	file, err := OpenOwnFile(held, name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(content)) > limit {
		err = fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return content, err
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
// icon reads the file again when its token is refused.
func PublishTrayAccess(held *os.File, url string) (TrayAccess, error) {
	random := make([]byte, 2*trayTokenBytes)
	if _, err := rand.Read(random); err != nil {
		return TrayAccess{}, err
	}
	encode := base64.RawURLEncoding.EncodeToString
	access := TrayAccess{URL: url, Token: encode(random[:trayTokenBytes]), Proof: encode(random[trayTokenBytes:])}
	content, err := json.Marshal(access)
	if err != nil {
		return TrayAccess{}, err
	}
	if err := replaceOwnFile(held, TrayAccessFile, append(content, '\n')); err != nil {
		return TrayAccess{}, fmt.Errorf("write the tray access file: %w", err)
	}
	return access, nil
}

// replaceOwnFile replaces the file name in the held directory with content.
// The content is written under a temporary name, made private to this
// account before anything is written to it, and then renamed, so a reader
// never sees a partial file.
func replaceOwnFile(held *os.File, name string, content []byte) error {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := "." + name + "-" + base64.RawURLEncoding.EncodeToString(random)
	file, err := OpenOwnFile(held, temporary, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return err
	}
	err = ProtectPrivateHandle(file, false)
	if err == nil {
		_, err = file.Write(content)
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err == nil {
		err = RenameOwnFile(held, temporary, name)
	}
	if err != nil {
		_ = removeOwnFile(held, temporary)
	}
	return err
}
