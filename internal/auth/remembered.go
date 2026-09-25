package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"sync"
	"time"
)

// A clone or push sends several Git and API requests with the same shared
// password. rememberedChecks lets a successful check stand for a short time,
// so those requests do not each run Argon2id.
//
// An entry is an HMAC-SHA256, under a random key made for this process, of
// the credential kind, the stored password hash and the presented password.
// The plain password is never kept. The caller reads the stored hash from the
// state on every check, so a changed or reset password, also one changed by
// another process, no longer matches any entry. Only a successful full check
// adds an entry; a wrong password always runs Argon2id and counts toward the
// failure limit.
const (
	rememberedCheckLife  = 5 * time.Minute
	rememberedCheckLimit = 16
)

type rememberedChecks struct {
	mu      sync.Mutex
	key     []byte
	entries []rememberedCheck
}

type rememberedCheck struct {
	digest  []byte
	expires time.Time
}

// digest returns the entry for one presented password, or nil when no key
// could be made, which turns remembering off.
func (r *rememberedChecks) digest(kind, encoded, password string) []byte {
	r.mu.Lock()
	if r.key == nil {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			r.mu.Unlock()
			return nil
		}
		r.key = key
	}
	mac := hmac.New(sha256.New, r.key)
	r.mu.Unlock()
	for _, field := range []string{kind, encoded, password} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		mac.Write(length[:])
		mac.Write([]byte(field))
	}
	return mac.Sum(nil)
}

// contains reports whether digest was remembered and has not expired. It
// compares every entry in constant time.
func (r *rememberedChecks) contains(digest []byte, now time.Time) bool {
	if digest == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	found := 0
	for _, entry := range r.entries {
		if now.Before(entry.expires) {
			found |= subtle.ConstantTimeCompare(entry.digest, digest)
		}
	}
	return found == 1
}

// add remembers digest until the entry life ends. Past the limit it drops
// the entry that expires first.
func (r *rememberedChecks) add(digest []byte, now time.Time) {
	if digest == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.entries[:0]
	for _, entry := range r.entries {
		if now.Before(entry.expires) && subtle.ConstantTimeCompare(entry.digest, digest) == 0 {
			kept = append(kept, entry)
		}
	}
	r.entries = append(kept, rememberedCheck{digest: digest, expires: now.Add(rememberedCheckLife)})
	if len(r.entries) > rememberedCheckLimit {
		first := 0
		for index, entry := range r.entries {
			if entry.expires.Before(r.entries[first].expires) {
				first = index
			}
		}
		r.entries = append(r.entries[:first], r.entries[first+1:]...)
	}
}
