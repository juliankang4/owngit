package checkapi

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipTextSurvivesJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		limit     int
		want      string
		truncated bool
	}{
		{name: "fits", value: "ok 가", limit: 16, want: "ok 가"},
		{name: "exact", value: "가나", limit: 6, want: "가나"},
		{name: "split rune", value: "x가나", limit: 5, want: "x가", truncated: true},
		{name: "invalid run becomes one replacement", value: "a\xb0\xa1\xb0\xa1b", limit: 16, want: "a\uFFFDb"},
		{name: "replacement is not split", value: "ab\xff", limit: 4, want: "ab", truncated: true},
		{name: "zero limit", value: "abc", limit: 0, want: "", truncated: true},
		{name: "negative limit", value: "abc", limit: -1, want: "", truncated: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, truncated := ClipText(test.value, test.limit)
			if got != test.want || truncated != test.truncated {
				t.Fatalf("ClipText(%q, %d) = %q, %v; want %q, %v", test.value, test.limit, got, truncated, test.want, test.truncated)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var decoded string
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded != got || !utf8.ValidString(decoded) {
				t.Fatalf("JSON round trip changed %q to %q", got, decoded)
			}
		})
	}

	long := strings.Repeat("\xff가", 10_000)
	got, truncated := ClipText(long, 8<<10)
	if !truncated || len(got) > 8<<10 || !utf8.ValidString(got) {
		t.Fatalf("long mixed text len=%d valid=%v truncated=%v", len(got), utf8.ValidString(got), truncated)
	}
}

func TestSummaryTextIsOneBoundedLine(t *testing.T) {
	got := SummaryText("  first failure\nsecond\r\nthird\x00 \xff  ", 500)
	if got != "first failure second  third \uFFFD" {
		t.Fatalf("SummaryText=%q", got)
	}
	long := SummaryText(strings.Repeat("가", 400), 500)
	if len(long) > 500 || !utf8.ValidString(long) {
		t.Fatalf("long summary len=%d valid=%v", len(long), utf8.ValidString(long))
	}
}

func TestLogBufferReportsEveryDroppedByte(t *testing.T) {
	tests := []struct {
		name      string
		parts     []string
		want      string
		truncated bool
	}{
		{name: "fits", parts: []string{"ab", "cd"}, want: "abcd"},
		{name: "exact fill", parts: []string{"abcdef"}, want: "abcdef"},
		{name: "full then more", parts: []string{"abcdef", "g"}, want: "abcdef", truncated: true},
		{name: "cut inside character, first byte", parts: []string{"abcde가"}, want: "abcde", truncated: true},
		{name: "cut inside character, second byte", parts: []string{"abcd가"}, want: "abcd", truncated: true},
		{name: "cut inside character, later part", parts: []string{"abc", "가나"}, want: "abc가", truncated: true},
		{name: "invalid bytes sanitized", parts: []string{"a\xffb"}, want: "a\uFFFDb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buffer := LogBuffer{Limit: 6}
			for _, part := range test.parts {
				if !buffer.Add(part) {
					break
				}
			}
			got, truncated := buffer.Result()
			if got != test.want || truncated != test.truncated {
				t.Fatalf("got %q, %v; want %q, %v", got, truncated, test.want, test.truncated)
			}
		})
	}
}
