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

	"golang.org/x/crypto/argon2"

	"owngit/internal/state"
)

const (
	argonTime    uint32 = 1
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
)

var ErrRateLimited = errors.New("too many authentication attempts")

type Manager struct {
	Store                   *state.Store
	SessionLife             time.Duration
	AdminSessionLife        time.Duration
	MaximumConcurrentChecks int
	Now                     func() time.Time
	checkMu                 sync.Mutex
	checkSlots              chan struct{}
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
	if err != nil || len(password) > 1024 {
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
	if len(password) < 8 {
		return errors.New("password must contain at least 8 characters")
	}
	if len(password) > 1024 {
		return errors.New("password is too long")
	}
	return nil
}

func (m *Manager) Authenticate(ctx context.Context, kind, password, remoteAddress string) (NewSession, error) {
	if err := m.VerifyCredential(ctx, kind, password, remoteAddress); err != nil {
		return NewSession{}, err
	}
	now := m.now()
	settings, err := m.Store.Settings(ctx)
	if err != nil {
		return NewSession{}, err
	}
	version := settings.AccessSessionVersion
	if kind == "admin" {
		version = settings.AdminSessionVersion
	}
	token, err := RandomToken(32)
	if err != nil {
		return NewSession{}, err
	}
	csrf, err := RandomToken(32)
	if err != nil {
		return NewSession{}, err
	}
	life := m.SessionLife
	if kind == "admin" && m.AdminSessionLife > 0 {
		life = m.AdminSessionLife
	}
	if life <= 0 {
		life = 12 * time.Hour
	}
	expires := now.Add(life)
	if err := m.Store.CreateSession(ctx, token, kind, csrf, version, expires); err != nil {
		return NewSession{}, err
	}
	return NewSession{Token: token, CSRF: csrf, Expires: expires}, nil
}

func (m *Manager) VerifyCredential(ctx context.Context, kind, password, remoteAddress string) error {
	if kind != "general" && kind != "admin" {
		return errors.New("invalid authentication kind")
	}
	now := m.now()
	address := clientAddress(remoteAddress)
	allowed, _, err := m.Store.CheckAttempt(ctx, kind, address, now, 5, 10*time.Minute, 15*time.Minute)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrRateLimited
	}
	encoded, err := m.Store.PasswordHash(ctx, map[string]string{"general": "access", "admin": "admin"}[kind])
	if err != nil {
		return err
	}
	if err := m.acquireCheck(ctx); err != nil {
		return err
	}
	defer func() { <-m.checkSlots }()
	if encoded == "" || !CheckPassword(encoded, password) {
		return errors.New("invalid credentials")
	}
	return m.Store.ClearAttempts(ctx, kind, address)
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
		_ = m.Store.DeleteSession(ctx, token, kind)
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

func RandomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
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
