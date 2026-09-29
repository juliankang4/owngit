package bidi

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// Every direction control is written as an escape wherever it is in the
// JSON, the value decodes unchanged, and right-to-left letters, Korean and
// other text are left as they are.
func TestEscapeJSONKeepsValuesAndHidesNoLetters(t *testing.T) {
	controls := []rune{0x061C, 0x200E, 0x200F, 0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069}
	var value strings.Builder
	for _, control := range controls {
		value.WriteString("x" + string(control))
	}
	const letters = "שלום مرحبا 한글 \u200d\u2060"
	encoded, err := MarshalJSON(map[string]string{"title": value.String() + letters, "kind": "access"})
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range controls {
		if strings.ContainsRune(string(encoded), control) {
			t.Errorf("U+%04X was written as it is: %s", control, encoded)
		}
	}
	if !strings.Contains(string(encoded), `x\u202e`) || !strings.Contains(string(encoded), letters) || !utf8.Valid(encoded) {
		t.Errorf("encoded=%s", encoded)
	}
	var decoded map[string]string
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded["title"] != value.String()+letters || decoded["kind"] != "access" {
		t.Fatalf("decoded=%q err=%v", decoded, err)
	}
	plain := []byte(`{"title":"한글 שלום"}`)
	if escaped := EscapeJSON(plain); &escaped[0] != &plain[0] {
		t.Error("JSON without a control was copied")
	}
}
