package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"owngit/internal/state"
)

const (
	argonTime    uint32 = 1
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
)

// A password check has three outcomes besides success. ErrInvalidCredentials
// means the check was completed and the password is wrong. ErrRateLimited
// means the client may not try now. Any other error means the check could
// not be completed, which says nothing about the password.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrRateLimited        = errors.New("too many authentication attempts")
)

// Failed password limit per client address and kind: the fourth wrong
// password within the window refuses that address for the block time.
const (
	maximumFailures = 4
	failureWindow   = 10 * time.Minute
	failureBlock    = 15 * time.Minute
)

// Password length limits, in characters (Unicode code points) rather than
// bytes, so a password in any script meets the rule the interface states.
const (
	MinimumPasswordCharacters = 8
	MaximumPasswordCharacters = 1024
)

var (
	ErrPasswordTooShort = errors.New("password must contain at least 8 characters")
	ErrPasswordTooLong  = errors.New("password must contain at most 1024 characters")
)

type Manager struct {
	Store       *state.Store
	SessionLife time.Duration
	// AdminSessionLife is how long an administrator sign-in keeps the
	// administrator pages open when the confirmation choice remembers no
	// password (Every time, Do not ask). A remembering choice sets its own.
	AdminSessionLife        time.Duration
	MaximumConcurrentChecks int
	Now                     func() time.Time
	checkMu                 sync.Mutex
	checkSlots              chan struct{}
	// clients serializes the checks of one kind and client address, so a
	// failure is recorded before the next check from that address starts.
	clients map[string]*clientTurn
	// remembered holds recent successful shared-password checks.
	remembered rememberedChecks
	// passwordCheck replaces CheckPassword in tests; nil uses CheckPassword.
	passwordCheck func(encoded, password string) bool
}

// clientTurn is a context-aware lock shared by the checks of one kind and
// client address; users counts the holders and waiters.
type clientTurn struct {
	turn  chan struct{}
	users int
}

type NewSession struct {
	Token   string
	CSRF    string
	Expires time.Time
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory,
		argonTime,
		argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func CheckPassword(encoded, password string) bool {
	parameters, salt, expected, err := parseHash(encoded)
	if err != nil || !withinMaximum(password) {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, parameters.time, parameters.memory, parameters.threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func ValidatePasswordHash(encoded string) error {
	_, _, _, err := parseHash(encoded)
	return err
}

func ValidatePassword(password string) error {
	if !withinMaximum(password) {
		return ErrPasswordTooLong
	}
	if utf8.RuneCountInString(password) < MinimumPasswordCharacters {
		return ErrPasswordTooShort
	}
	return nil
}

// withinMaximum applies the character limit. A character is at most four
// bytes, so a longer string is refused before it is counted.
func withinMaximum(password string) bool {
	return len(password) <= utf8.UTFMax*MaximumPasswordCharacters && utf8.RuneCountInString(password) <= MaximumPasswordCharacters
}

func (m *Manager) Authenticate(ctx context.Context, kind, password, remoteAddress string) (NewSession, error) {
	if err := m.VerifyCredential(ctx, kind, password, remoteAddress); err != nil {
		return NewSession{}, err
	}
	if kind == "admin" {
		return m.StartAdminSession(ctx)
	}
	life := m.SessionLife
	if life <= 0 {
		life = 12 * time.Hour
	}
	return m.startSession(ctx, kind, life)
}

// StartAdminSession starts an administrator session for this browser after
// the caller verified the administrator password. Under a confirmation
// choice that remembers the password it lasts that window; otherwise it
// lasts AdminSessionLife and only opens the administrator pages, while
// every change still asks for the password.
func (m *Manager) StartAdminSession(ctx context.Context) (NewSession, error) {
	choice, _, err := m.Store.AdminConfirmation(ctx)
	if err != nil {
		return NewSession{}, err
	}
	return m.startSession(ctx, "admin", m.adminLife(choice))
}

// SetAdminConfirmation saves choice. A stricter choice shortens the
// administrator sessions browsers hold to the new time counted from when
// their password was typed; a looser one extends none.
func (m *Manager) SetAdminConfirmation(ctx context.Context, choice state.AdminConfirmation) error {
	return m.Store.SetAdminConfirmation(ctx, choice, m.adminLife)
}

// adminLife is how long an administrator session started under choice
// lasts.
func (m *Manager) adminLife(choice state.AdminConfirmation) time.Duration {
	if window := choice.Window(); window > 0 {
		return window
	}
	if m.AdminSessionLife > 0 {
		return m.AdminSessionLife
	}
	return 15 * time.Minute
}

func (m *Manager) startSession(ctx context.Context, kind string, life time.Duration) (NewSession, error) {
	settings, err := m.Store.Settings(ctx)
	if err != nil {
		return NewSession{}, err
	}
	version := settings.AccessSessionVersion
	if kind == "admin" {
		version = settings.AdminSessionVersion
	}
	token, csrf := RandomToken(32), RandomToken(32)
	expires := m.now().Add(life)
	if err := m.Store.CreateSession(ctx, token, kind, csrf, version, expires); err != nil {
		return NewSession{}, err
	}
	return NewSession{Token: token, CSRF: csrf, Expires: expires}, nil
}

func (m *Manager) VerifyCredential(ctx context.Context, kind, password, remoteAddress string) error {
	if kind != "general" && kind != "admin" {
		return errors.New("invalid authentication kind")
	}
	address := clientAddress(remoteAddress)
	// Only wrong passwords count toward the limit. Checks from one address
	// take turns, so parallel guesses cannot pass the limit before their
	// failures are recorded, while parallel correct requests are never
	// refused for being parallel.
	release, err := m.takeTurn(ctx, kind+"\x00"+address)
	if err != nil {
		return err
	}
	defer release()
	blocked, err := m.Store.AttemptBlocked(ctx, kind, address, m.now())
	if err != nil {
		return err
	}
	if blocked {
		return ErrRateLimited
	}
	encoded, err := m.Store.PasswordHash(ctx, map[string]string{"general": "access", "admin": "admin"}[kind])
	if err != nil {
		return err
	}
	// A missing or damaged stored password cannot tell a right password
	// from a wrong one.
	if err := ValidatePasswordHash(encoded); err != nil {
		return fmt.Errorf("stored %s password: %w", kind, err)
	}
	// Only the shared access password is remembered. Administrator
	// confirmations are rare and typed by a person, so they keep the full
	// check and no fast digest of that password stays in memory.
	var remembered []byte
	if kind == "general" && withinMaximum(password) {
		remembered = m.remembered.digest(kind, encoded, password)
		if m.remembered.contains(remembered, m.now()) {
			return m.Store.ClearAttempts(ctx, kind, address)
		}
	}
	if err := m.acquireCheck(ctx); err != nil {
		return err
	}
	defer func() { <-m.checkSlots }()
	check := m.passwordCheck
	if check == nil {
		check = CheckPassword
	}
	if !check(encoded, password) {
		// A client that leaves after its guess was checked still counts. A
		// guess that could not be counted is not reported as checked.
		if err := m.Store.RecordFailedAttempt(context.WithoutCancel(ctx), kind, address, m.now(), maximumFailures, failureWindow, failureBlock); err != nil {
			return err
		}
		return ErrInvalidCredentials
	}
	if err := m.Store.ClearAttempts(ctx, kind, address); err != nil {
		return err
	}
	m.remembered.add(remembered, m.now())
	return nil
}

// takeTurn waits until no other check of key runs and returns the release
// function.
func (m *Manager) takeTurn(ctx context.Context, key string) (func(), error) {
	m.checkMu.Lock()
	if m.clients == nil {
		m.clients = make(map[string]*clientTurn)
	}
	client := m.clients[key]
	if client == nil {
		client = &clientTurn{turn: make(chan struct{}, 1)}
		m.clients[key] = client
	}
	client.users++
	m.checkMu.Unlock()
	leave := func() {
		m.checkMu.Lock()
		client.users--
		if client.users == 0 {
			delete(m.clients, key)
		}
		m.checkMu.Unlock()
	}
	select {
	case client.turn <- struct{}{}:
		return func() {
			<-client.turn
			leave()
		}, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

func (m *Manager) ValidateSession(ctx context.Context, token, kind string) (state.Session, bool, error) {
	if token == "" {
		return state.Session{}, false, nil
	}
	session, ok, err := m.Store.Session(ctx, token, kind, m.now())
	if err != nil || !ok {
		return state.Session{}, ok, err
	}
	settings, err := m.Store.Settings(ctx)
	if err != nil {
		return state.Session{}, false, err
	}
	expectedVersion := settings.AccessSessionVersion
	if kind == "admin" {
		expectedVersion = settings.AdminSessionVersion
	}
	if session.Version != expectedVersion {
		if err := m.Store.DeleteSession(ctx, token, kind); err != nil {
			return state.Session{}, false, fmt.Errorf("%w: %w", state.ErrEndedSessionKept, err)
		}
		return state.Session{}, false, nil
	}
	return session, true, nil
}

func (m *Manager) acquireCheck(ctx context.Context) error {
	m.checkMu.Lock()
	if m.checkSlots == nil {
		maximum := m.MaximumConcurrentChecks
		if maximum <= 0 {
			maximum = 4
		}
		m.checkSlots = make(chan struct{}, maximum)
	}
	slots := m.checkSlots
	m.checkMu.Unlock()
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// RandomToken returns bytes random bytes as unpadded URL-safe base64. It
// cannot fail: crypto/rand.Read never returns an error and crashes the
// program instead (Go 1.24).
func RandomToken(bytes int) string {
	value := make([]byte, bytes)
	rand.Read(value)
	return base64.RawURLEncoding.EncodeToString(value)
}

type hashParameters struct {
	time    uint32
	memory  uint32
	threads uint8
}

func parseHash(encoded string) (hashParameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return hashParameters{}, nil, nil, errors.New("invalid password hash")
	}
	var parameters hashParameters
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &parameters.memory, &parameters.time, &parameters.threads); err != nil {
		return hashParameters{}, nil, nil, errors.New("invalid password hash parameters")
	}
	if parameters.memory < 8*1024 || parameters.memory > 128*1024 || parameters.time < 1 || parameters.time > 5 || parameters.threads < 1 || parameters.threads > 16 {
		return hashParameters{}, nil, nil, errors.New("password hash parameters are outside limits")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return hashParameters{}, nil, nil, errors.New("invalid password hash salt")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return hashParameters{}, nil, nil, errors.New("invalid password hash key")
	}
	return parameters, salt, key, nil
}

func clientAddress(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil {
		return host
	}
	return remoteAddress
}
