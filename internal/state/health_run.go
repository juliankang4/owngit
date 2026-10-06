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

// HealthRunFile, in the state directory, tells "owngit health" which server
// to ask and holds the key with which that server proves its answer. Every
// serve makes a new key and removes the file when it stops. Only the account
// that owns the state directory can read it, and reading it writes nothing,
// so health still works while the state is read-only.
const HealthRunFile = "health-run.json"

// The health check proves its answer: the checker sends a new random nonce
// in HealthNonceHeader, and the server answers with HealthProof of it in
// HealthProofHeader. The key never crosses the connection, so a program
// that took the address cannot answer as OwnGit.
const (
	HealthNonceHeader = "X-OwnGit-Health-Nonce"
	HealthProofHeader = "X-OwnGit-Health-Proof"
)

// HealthRun is the content of HealthRunFile.
type HealthRun struct {
	// Listen is the requested listen address and Address the bound one.
	Listen  string `json:"listen"`
	Address string `json:"address"`
	Key     string `json:"key"`
}

// HealthProof is HMAC-SHA256 with key over "owngit health" and the nonce, in
// unpadded base64url.
func HealthProof(key, nonce string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("owngit health\n" + nonce + "\n"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ValidHealthProof reports whether proof is HealthProof of nonce under key.
func ValidHealthProof(key, nonce, proof string) bool {
	return hmac.Equal([]byte(HealthProof(key, nonce)), []byte(proof))
}

// PublishHealthRun makes a new key for a server bound at address and writes
// HealthRunFile in the held state directory.
func PublishHealthRun(held *os.File, listen, address string) (HealthRun, error) {
	random := make([]byte, trayTokenBytes)
	if _, err := rand.Read(random); err != nil {
		return HealthRun{}, err
	}
	run := HealthRun{Listen: listen, Address: address, Key: base64.RawURLEncoding.EncodeToString(random)}
	content, err := json.Marshal(run)
	if err != nil {
		return HealthRun{}, err
	}
	if err := replaceOwnFile(held, HealthRunFile, append(content, '\n')); err != nil {
		return HealthRun{}, fmt.Errorf("write the health file: %w", err)
	}
	return run, nil
}

// RemoveHealthRun removes HealthRunFile when the server stops.
func RemoveHealthRun(held *os.File) error {
	if err := removeOwnFile(held, HealthRunFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the health file: %w", err)
	}
	return nil
}

// ReadHealthRun reads HealthRunFile of the existing state directory dir. It
// reports false when no server has published one.
func ReadHealthRun(dir string) (HealthRun, bool, error) {
	held, err := OpenStateDirectory(dir)
	if err != nil {
		return HealthRun{}, false, err
	}
	defer held.Close()
	content, err := readOwnFile(held, HealthRunFile, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return HealthRun{}, false, nil
	}
	if err != nil {
		return HealthRun{}, false, fmt.Errorf("read the health file: %w", err)
	}
	var run HealthRun
	if err := json.Unmarshal(content, &run); err != nil || run.Key == "" || run.Address == "" {
		return HealthRun{}, false, fmt.Errorf("read the health file: %s is not valid", HealthRunFile)
	}
	return run, true, nil
}
