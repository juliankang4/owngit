package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
)

const (
	ownerFileName = "owner-setup.html"
	lockFileName  = ".owner-setup.lock"
	journalName   = ".owner-setup.issue.json"
)

type Issuer struct {
	Store   *state.Store
	BaseURL string
	Now     func() time.Time
	Publish func(source, destination string) error
}

type issueJournal struct {
	Old state.BootstrapSnapshot `json:"old"`
	New state.BootstrapSnapshot `json:"new"`
}

// AcquireSetupLock serializes setup capability publication and setup
// completion across processes. The returned function releases all resources.
func AcquireSetupLock(ctx context.Context, directory string) (func(), error) {
	lockPath := filepath.Join(directory, lockFileName)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open setup issuance lock: %w", err)
	}
	if err := state.ProtectPrivatePath(lockPath, false); err != nil {
		lock.Close()
		return nil, fmt.Errorf("protect setup issuance lock: %w", err)
	}
	unlockFile, err := lockFile(ctx, lock)
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("lock setup issuance: %w", err)
	}
	return func() {
		unlockFile()
		_ = lock.Close()
	}, nil
}

func RemoveOwnerSetupFiles(directory string) error {
	for _, name := range []string{ownerFileName, journalName} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Issue replaces any previous unredeemed capability and writes it only to an
// owner-readable local file. Issuance is serialized across processes and a
// hash-only recovery journal repairs interruption between SQLite and rename.
func (issuer *Issuer) Issue(ctx context.Context) (string, error) {
	base, err := url.Parse(issuer.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("setup base URL must be an HTTP(S) origin")
	}
	base.Path = "/setup"

	unlock, err := AcquireSetupLock(ctx, issuer.Store.Dir())
	if err != nil {
		return "", err
	}
	defer unlock()
	settings, err := issuer.Store.Settings(ctx)
	if err != nil {
		return "", err
	}
	if settings.Initialized {
		if err := RemoveOwnerSetupFiles(issuer.Store.Dir()); err != nil {
			return "", fmt.Errorf("remove obsolete setup capability file: %w", err)
		}
		return "", state.ErrSetupComplete
	}
	if err := issuer.recoverInterruptedIssue(ctx); err != nil {
		return "", err
	}

	token, err := auth.RandomToken(32)
	if err != nil {
		return "", err
	}
	now := time.Now()
	if issuer.Now != nil {
		now = issuer.Now()
	}
	expires := now.Add(15 * time.Minute)
	content := setupFileContent(base.String(), token)
	path := filepath.Join(issuer.Store.Dir(), ownerFileName)
	temporary, err := os.CreateTemp(issuer.Store.Dir(), ".owner-setup-*")
	if err != nil {
		return "", fmt.Errorf("create private setup file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := state.ProtectPrivatePath(temporaryPath, false); err != nil {
		temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}

	old, err := issuer.Store.BootstrapSnapshot(ctx)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(token))
	newSnapshot := state.BootstrapSnapshot{Present: true, TokenHash: hash[:], ExpiresAt: expires.Unix()}
	journal := issueJournal{Old: old, New: newSnapshot}
	if err := writeJournal(issuer.Store.Dir(), journal); err != nil {
		return "", err
	}
	if err := issuer.Store.PutBootstrap(ctx, token, expires); err != nil {
		_ = os.Remove(filepath.Join(issuer.Store.Dir(), journalName))
		return "", err
	}
	publish := issuer.Publish
	if publish == nil {
		publish = replaceFile
	}
	if err := publish(temporaryPath, path); err != nil {
		if restoreErr := issuer.Store.RestoreBootstrap(ctx, old); restoreErr != nil {
			return "", fmt.Errorf("publish private setup file: %v; restore previous setup capability: %w", err, restoreErr)
		}
		_ = os.Remove(filepath.Join(issuer.Store.Dir(), journalName))
		return "", fmt.Errorf("publish private setup file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if directory, openErr := os.Open(issuer.Store.Dir()); openErr == nil {
			_ = directory.Sync()
			_ = directory.Close()
		}
	}
	if err := os.Remove(filepath.Join(issuer.Store.Dir(), journalName)); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("finish setup issuance: %w", err)
	}
	return path, nil
}

func (issuer *Issuer) recoverInterruptedIssue(ctx context.Context) error {
	journalPath := filepath.Join(issuer.Store.Dir(), journalName)
	content, err := os.ReadFile(journalPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read setup issuance recovery journal: %w", err)
	}
	var journal issueJournal
	if err := json.Unmarshal(content, &journal); err != nil {
		return fmt.Errorf("parse setup issuance recovery journal: %w", err)
	}
	current, err := issuer.Store.BootstrapSnapshot(ctx)
	if err != nil {
		return err
	}
	ownerToken, _ := readOwnerToken(filepath.Join(issuer.Store.Dir(), ownerFileName))
	if sameSnapshot(current, journal.New) && state.BootstrapMatches(journal.New, ownerToken) {
		return os.Remove(journalPath)
	}
	if err := issuer.Store.RestoreBootstrap(ctx, journal.Old); err != nil {
		return fmt.Errorf("recover previous setup capability: %w", err)
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func writeJournal(directory string, journal issueJournal) error {
	content, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".owner-setup-journal-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := state.ProtectPrivatePath(name, false); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFile(name, filepath.Join(directory, journalName))
}

func setupFileContent(target, token string) []byte {
	targetJSON, _ := json.Marshal(target)
	tokenJSON, _ := json.Marshal(token)
	return []byte(fmt.Sprintf(`<!doctype html>
<meta charset="utf-8">
<meta name="robots" content="noindex,nofollow">
<title>Open OwnGit setup</title>
<p>Opening the owner setup page…</p>
<script>
const target = %s;
const capability = %s;
location.replace(target + "#" + capability);
</script>
<noscript>JavaScript is required to redeem this one-time owner setup link.</noscript>
`, targetJSON, tokenJSON))
}

func readOwnerToken(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	const prefix = "const capability = "
	start := bytes.Index(content, []byte(prefix))
	if start < 0 {
		return "", errors.New("setup capability is missing")
	}
	value := content[start+len(prefix):]
	end := bytes.Index(value, []byte(";\n"))
	if end < 0 {
		return "", errors.New("setup capability is malformed")
	}
	var token string
	if err := json.Unmarshal(value[:end], &token); err != nil {
		return "", err
	}
	return token, nil
}

func sameSnapshot(left, right state.BootstrapSnapshot) bool {
	return left.Present == right.Present && left.ExpiresAt == right.ExpiresAt && bytes.Equal(left.TokenHash, right.TokenHash)
}

func Open(path string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("/usr/bin/open", path)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Start(); err != nil {
		return fmt.Errorf("open setup file: %w", err)
	}
	return command.Process.Release()
}
