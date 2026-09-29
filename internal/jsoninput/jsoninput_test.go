package jsoninput

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A request is refused exactly when decoding would put U+FFFD in place of
// what was sent; every other string, including U+FFFD sent on purpose, is
// accepted and decodes to itself.
func TestValidRefusesExactlyTheTextDecodingWouldChange(t *testing.T) {
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"lone high surrogate", `"a\ud800b"`, false},
		{"lone high surrogate at the end", `"a\uD800"`, false},
		{"lone low surrogate", `"a\udc00b"`, false},
		{"high surrogate before an ordinary escape", `"a\ud800\u0041"`, false},
		{"high surrogate before another high one", `"\ud800\ud800\udc00"`, false},
		{"reversed pair", `"\ude00\ud83d"`, false},
		{"high surrogate before a short escape", `"\ud800\u12"`, false},
		{"lone surrogate after other escapes", `"\\\"\n\ud800"`, false},
		{"lone surrogate in a key", `{"\udfff":1}`, false},
		{"byte that is not UTF-8", "\"a\xffb\"", false},
		{"surrogate pair", `"\ud83d\ude00"`, true},
		{"surrogate pair in capitals", `"\uD83D\uDE00"`, true},
		{"U+FFFD escaped", `"\ufffd"`, true},
		{"U+FFFD as UTF-8", "\"\uFFFD\"", true},
		{"multibyte text", `"한글 שלום مرحبا 🙂"`, true},
		{"escaped backslash before u", `"\\ud800"`, true},
		{"ordinary escapes", `"\u0041\n\t\"\\\/"`, true},
		{"surrogate text outside a string", `{"a":"x"} \ud800`, true},
	} {
		if got := Valid([]byte(test.body)); got != test.valid {
			t.Errorf("%s: Valid(%s) = %v", test.name, test.body, got)
		}
		// What the check stands on: encoding/json puts U+FFFD in place of
		// each refused string, and in no accepted one that was not sent it.
		var decoded any
		if json.Unmarshal([]byte(test.body), &decoded) != nil {
			continue
		}
		sent := strings.Contains(test.body, `\ufffd`) || strings.ContainsRune(test.body, 0xFFFD)
		if replaced := strings.ContainsRune(fmt.Sprint(decoded), 0xFFFD); replaced != (!test.valid || sent) {
			t.Errorf("%s: decoded %q", test.name, fmt.Sprint(decoded))
		}
	}
}
