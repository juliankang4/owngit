package checkapi

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The shared renewal cadence must use the small wait when only the small wait
// is left, and must never pick a wait that reaches past a longer lease.
func TestLeaseRenewDelayStaysWithinTheLease(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	expires := func(remaining time.Duration) *time.Time {
		at := now.Add(remaining)
		return &at
	}
	tests := []struct {
		name      string
		expiresAt *time.Time
		want      time.Duration
	}{
		{name: "no deadline", want: DefaultLeaseRenewDelay},
		{name: "expired", expiresAt: expires(-time.Second), want: MinimumLeaseRenewDelay},
		{name: "below the floor", expiresAt: expires(200 * time.Millisecond), want: MinimumLeaseRenewDelay},
		{name: "at the floor", expiresAt: expires(3 * MinimumLeaseRenewDelay), want: MinimumLeaseRenewDelay},
		{name: "above the floor", expiresAt: expires(900 * time.Millisecond), want: 300 * time.Millisecond},
		{name: "at the default", expiresAt: expires(3 * DefaultLeaseRenewDelay), want: DefaultLeaseRenewDelay},
		{name: "above the default", expiresAt: expires(time.Minute), want: DefaultLeaseRenewDelay},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := LeaseRenewDelay(now, test.expiresAt)
			if got != test.want {
				t.Fatalf("LeaseRenewDelay with %v left = %s, want %s", test.expiresAt, got, test.want)
			}
		})
	}
}

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

// ClipText must give what normalizing the whole value and then cutting on a
// rune boundary gives. Every limit puts the cut before, inside and after each
// invalid run and multi-byte character.
func TestClipTextMatchesWholeValueNormalization(t *testing.T) {
	reference := func(value string, limit int) (string, bool) {
		value = strings.ToValidUTF8(value, "\uFFFD")
		limit = max(limit, 0)
		if len(value) <= limit {
			return value, false
		}
		cut := limit
		for cut > 0 && !utf8.RuneStart(value[cut]) {
			cut--
		}
		return value[:cut], true
	}
	values := []string{
		"", "abc", "가나", "\xff", "\xff\xfe\xfd", "ab\xff\xffcd", "\xe0\x80", "가\xea\xb0", "\xea\xb0가",
		"x\xb0\xa1 \xb0\xa1 y", "a\uFFFDb", "\uFFFD\xff\uFFFD", "a\xf0\x9f\x98", "😀\xff😀", "\xff가\xff", "\xed\xa0\x80z",
	}
	for _, value := range values {
		for limit := -1; limit <= 3*len(value)+1; limit++ {
			got, gotCut := ClipText(value, limit)
			want, wantCut := reference(value, limit)
			if got != want || gotCut != wantCut {
				t.Errorf("ClipText(%q, %d) = %q, %v; want %q, %v", value, limit, got, gotCut, want, wantCut)
			}
		}
	}
}

// Clipping a long value must cost what is kept, not what is dropped.
func TestClipTextReadsOnlyWhatItKeeps(t *testing.T) {
	const limit = 64 << 10
	value := strings.Repeat("\xb0\xa1 ", (1<<20)/3)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	clipped, cut := ClipText(value, limit)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 4*limit || !cut || len(clipped) > limit || !utf8.ValidString(clipped) {
		t.Fatalf("allocated %d bytes to keep %d of %d, cut=%v", allocated, len(clipped), len(value), cut)
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
		{name: "full, empty, then more", parts: []string{"abcdef", "", "g"}, want: "abcdef", truncated: true},
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
