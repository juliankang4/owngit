package checkapi

import (
	"encoding/json"
	"regexp"
	"runtime"
	"strconv"
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

// One rule keeps a long output's beginning and end around a marker, whether
// the output arrives as one string or in parts. Text under the limit is not
// changed, the result never passes the limit and never splits a character.
func TestClipLogKeepsHeadAndTail(t *testing.T) {
	const limit = 100
	long := strings.Repeat("a", 300) + "FATAL: the reason\n"
	wide := strings.Repeat("가", 300) + "끝"
	tests := []struct {
		name  string
		parts []string
		whole bool
	}{
		{name: "under the limit", parts: []string{"ok 가\n"}, whole: true},
		{name: "exactly the limit", parts: []string{strings.Repeat("b", limit)}, whole: true},
		{name: "one long part", parts: []string{long}},
		{name: "many small parts", parts: strings.SplitAfter(long, "a")},
		{name: "multi-byte text", parts: []string{wide}},
		{name: "multi-byte text in parts", parts: strings.SplitAfter(wide, "가가가")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			joined := strings.Join(test.parts, "")
			buffer := LogBuffer{Limit: limit}
			for _, part := range test.parts {
				buffer.Add(part)
			}
			streamed, streamedCut := buffer.Result()
			clipped, cut := ClipLog(joined, limit, Gap{})
			for name, got := range map[string]string{"ClipLog": clipped, "LogBuffer": streamed} {
				if len(got) > limit || !utf8.ValidString(got) {
					t.Fatalf("%s: len=%d valid=%v", name, len(got), utf8.ValidString(got))
				}
				if test.whole && got != joined {
					t.Fatalf("%s changed text under the limit: %q", name, got)
				}
				if !test.whole {
					start := strings.Index(got, "\n[... ")
					end := strings.Index(got, " bytes omitted ...]\n")
					if start < 0 || end < 0 || !strings.HasPrefix(joined, got[:start]) ||
						!strings.HasSuffix(joined, got[end+len(" bytes omitted ...]\n"):]) {
						t.Fatalf("%s: no head, marker and tail in %q", name, got)
					}
					omitted, _ := strconv.Atoi(strings.TrimPrefix(got[start:end], "\n[... "))
					kept := len(got) - len("\n[...  bytes omitted ...]\n") - len(strconv.Itoa(omitted))
					if omitted != len(joined)-kept {
						t.Fatalf("%s: marker says %d omitted, but %d of %d bytes were kept", name, omitted, kept, len(joined))
					}
				}
			}
			if cut != !test.whole || streamedCut != !test.whole {
				t.Fatalf("cut: ClipLog=%v LogBuffer=%v, want %v", cut, streamedCut, !test.whole)
			}
		})
	}
	// Text that an earlier clip already cut: every later view states how much
	// of the original output is missing, not how much of the clipped text.
	original := strings.Repeat("a", 5000) + "FATAL: the reason\n"
	first := LogBuffer{Limit: 200}
	first.Add(original)
	clipped, gap, _ := first.ResultWithGap()
	if !strings.Contains(clipped[gap.Start:gap.End], "bytes omitted") || gap.Omitted != len(original)-(len(clipped)-(gap.End-gap.Start)) {
		t.Fatalf("first clip: gap %+v, len %d", gap, len(clipped))
	}
	second := LogBuffer{Limit: limit}
	second.Add("[failed] cmd\n")
	second.AddClipped(clipped, gap)
	second.Add("\n")
	viaBuffer, _ := second.Result()
	viaClip, _ := ClipLog(clipped, limit, gap)
	for name, got := range map[string]struct {
		text   string
		source int
	}{"LogBuffer": {viaBuffer, len("[failed] cmd\n") + len(original) + 1}, "ClipLog": {viaClip, len(original)}} {
		start := strings.Index(got.text, "\n[... ")
		end := strings.Index(got.text, "bytes omitted ...]\n") + len("bytes omitted ...]\n")
		omitted, _ := strconv.Atoi(strings.Fields(got.text[start:end])[1])
		if len(got.text) > limit || strings.Count(got.text, "bytes omitted") != 1 || omitted != got.source-(len(got.text)-(end-start)) {
			t.Fatalf("%s: marker says %d omitted of %d, text %q", name, omitted, got.source, got.text)
		}
	}
	// An earlier marker that stays in view keeps its count and is not repeated.
	inner := LogBuffer{Limit: 1000}
	inner.Add(strings.Repeat("i", 10000))
	innerText, innerGap, _ := inner.ResultWithGap()
	outer := LogBuffer{Limit: 8000}
	outer.Add(strings.Repeat("p", 20000))
	outer.AddClipped(innerText, innerGap)
	text, _ := outer.Result()
	counts := regexp.MustCompile(`\n\[\.\.\. (\d+) bytes omitted \.\.\.\]\n`).FindAllStringSubmatch(text, -1)
	if len(counts) != 2 {
		t.Fatalf("want the earlier marker and a new one, got %d markers", len(counts))
	}
	sum := len(text)
	for _, count := range counts {
		n, _ := strconv.Atoi(count[1])
		sum += n - len(count[0])
	}
	if sum != 30000 {
		t.Fatalf("markers and shown bytes add up to %d, want 30000 (original size)", sum)
	}
	if got, _ := ClipLog(long, limit, Gap{}); !strings.HasSuffix(got, "FATAL: the reason\n") {
		t.Fatalf("the final line was lost: %q", got)
	}
	if got, cut := ClipLog("a\xffb", limit, Gap{}); got != "a\uFFFDb" || cut {
		t.Fatalf("invalid bytes: %q, %v", got, cut)
	}
}
