package checksource

import (
	"bytes"
	"strconv"
	"strings"
)

// Nonempty serialized Git LFS pointer recognition follows the specification at
// https://github.com/git-lfs/git-lfs/blob/main/docs/spec.md and the corresponding
// forms accepted by the reference decoder in that project's lfs/pointer.go.
// Its EmptyPointer normalization is intentionally excluded: an empty blob is
// copied as an ordinary empty file. Attributes are not evaluated, so this
// recognition does not establish the presence or absence of all LFS usage.
//
// This is validation, not availability. A validated pointer means the blob is
// a well-formed pointer whose bytes were preserved exactly. It never means the
// referenced large object exists, was fetched, or matches its recorded digest.

// lfsPointerSizeCutoff is the reference decoder's BlobSizeCutoff. The
// specification requires a pointer to be less than 1024 bytes, so a blob of
// exactly 1024 bytes is not a pointer.
const lfsPointerSizeCutoff = 1024

// MaxLFSPointerBytes is the largest serialized blob the parser can recognize.
const MaxLFSPointerBytes = lfsPointerSizeCutoff - 1

// lfsVersionAliases are the version URLs the reference decoder accepts. The
// first is the current one; the others are the pre-release and alpha spellings
// that existing repositories still contain.
var lfsVersionAliases = []string{
	"https://git-lfs.github.com/spec/v1",
	"https://hawser.github.com/spec/v1",
	"http://git-media.io/v/2",
}

// lfsMarkers mirror the reference decoder's precheck. Content without one of
// them is never parsed as a pointer.
var lfsMarkers = [][]byte{[]byte("git-lfs"), []byte("hawser"), []byte("git-media")}

// lfsOIDPrefix is the only hash method the specification defines.
const lfsOIDPrefix = "sha256:"

// LFSPointer is a validated Git LFS pointer. Values come from the pointer
// text; the large object itself is never read.
type LFSPointer struct {
	// Version is the pointer spec URL exactly as written.
	Version string
	// OID is the lowercase hexadecimal SHA-256 of the referenced object.
	OID string
	// Size is the referenced object size in bytes, as recorded in the pointer.
	Size int64
	// Extensions lists any ext-<priority>-<name> keys in order of appearance.
	// A pointer with extensions describes transformed content, so its OID does
	// not directly identify the working-tree bytes.
	Extensions []string
}

// ParseLFSPointer validates blob bytes against the Git LFS pointer grammar and
// returns the decoded pointer, or nil when the bytes are not a pointer.
//
// Import and materialization callers share this parser so every accepted form
// has the same meaning and the original pointer bytes remain untouched.
func ParseLFSPointer(content []byte) *LFSPointer {
	return parseLFSPointer(content)
}

// parseLFSPointer implements recognition for nonempty serialized pointers. It
// accepts forms slightly wider than the canonical encoding: CRLF line endings,
// blank lines, and a missing
// trailing newline are tolerated, because real repositories contain those and
// git-lfs still treats them as pointers. Every field is validated strictly:
// an unrecognized version, a malformed SHA-256, a non-numeric or negative
// size, a wrong key order, an unknown key, a duplicate extension priority, or
// any trailing content all mean "not a pointer".
func parseLFSPointer(content []byte) *LFSPointer {
	if len(content) == 0 || len(content) >= lfsPointerSizeCutoff {
		return nil
	}
	if !hasLFSMarker(content) {
		return nil
	}
	// The required keys must appear in this order. Extension lines may be
	// interleaved and do not advance the position.
	required := [...]string{"version", "oid", "size"}
	position := 0
	values := make(map[string]string, len(required))
	pointer := &LFSPointer{}
	priorities := make(map[int]struct{})

	for _, rawLine := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if line == "" {
			continue
		}
		key, value, separated := strings.Cut(line, " ")
		if !separated {
			return nil
		}
		if position >= len(required) {
			return nil
		}
		if key != required[position] {
			priority, ok := lfsExtensionPriority(key)
			if !ok {
				return nil
			}
			if _, duplicate := priorities[priority]; duplicate {
				return nil
			}
			if _, ok := lfsObjectID(value); !ok {
				return nil
			}
			priorities[priority] = struct{}{}
			pointer.Extensions = append(pointer.Extensions, key)
			continue
		}
		values[key] = value
		position++
	}

	version, ok := values["version"]
	if !ok || !isLFSVersion(version) {
		return nil
	}
	oid, ok := lfsObjectID(values["oid"])
	if !ok {
		return nil
	}
	size, err := strconv.ParseInt(values["size"], 10, 64)
	if err != nil || size < 0 {
		return nil
	}
	pointer.Version = version
	pointer.OID = oid
	pointer.Size = size
	return pointer
}

func hasLFSMarker(content []byte) bool {
	for _, marker := range lfsMarkers {
		if bytes.Contains(content, marker) {
			return true
		}
	}
	return false
}

func isLFSVersion(value string) bool {
	for _, alias := range lfsVersionAliases {
		if value == alias {
			return true
		}
	}
	return false
}

// lfsObjectID accepts only "sha256:" followed by 64 lowercase hexadecimal
// digits, which is the one form the specification defines.
func lfsObjectID(value string) (string, bool) {
	hexadecimal, found := strings.CutPrefix(value, lfsOIDPrefix)
	if !found || len(hexadecimal) != 64 {
		return "", false
	}
	for index := 0; index < len(hexadecimal); index++ {
		character := hexadecimal[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", false
		}
	}
	return hexadecimal, true
}

// lfsExtensionPriority validates an ext-<priority>-<name> key. The reference
// decoder allows exactly one priority digit and requires a non-empty name of
// ASCII letters, digits, or underscores, optionally followed by more key text.
func lfsExtensionPriority(key string) (int, bool) {
	rest, found := strings.CutPrefix(key, "ext-")
	if !found || len(rest) < 3 || rest[0] < '0' || rest[0] > '9' || rest[1] != '-' {
		return 0, false
	}
	name := rest[2:]
	if !isLFSExtensionNameStart(name[0]) {
		return 0, false
	}
	return int(rest[0] - '0'), true
}

func isLFSExtensionNameStart(character byte) bool {
	return character >= '0' && character <= '9' ||
		character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character == '_'
}
