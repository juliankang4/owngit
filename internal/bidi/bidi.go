// Package bidi names the Unicode direction controls and keeps them from
// changing how the JSON OwnGit writes looks.
//
// A direction control changes the order in which the characters around it
// are shown. Text from Git and from repository users can hold one, so a title
// could make the fields after it on the same line look different from what
// they are. OwnGit accepts such text and never changes its value; where it
// writes JSON, each control is written as a \uXXXX escape, which decodes to
// the same value and cannot reorder anything.
package bidi

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Control reports whether r is a direction control: an embedding, override
// or isolate, or a directional mark.
func Control(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// EscapeJSON returns JSON text with every direction control written as a
// \uXXXX escape. JSON can hold a control only inside a string, where the
// escape stands for the same character, so the decoded value is unchanged.
// encoded is returned as it is when it holds no control.
func EscapeJSON(encoded []byte) []byte {
	var out []byte
	copied := 0
	for index := 0; index < len(encoded); {
		r, size := utf8.DecodeRune(encoded[index:])
		if Control(r) {
			if out == nil {
				out = make([]byte, 0, len(encoded)+16)
			}
			out = append(out, encoded[copied:index]...)
			out = fmt.Appendf(out, `\u%04x`, r)
			copied = index + size
		}
		index += size
	}
	if out == nil {
		return encoded
	}
	return append(out, encoded[copied:]...)
}

// MarshalJSON is json.Marshal with every direction control escaped. Every
// JSON answer and result OwnGit writes goes through it or EscapeJSON.
func MarshalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return EscapeJSON(encoded), nil
}
