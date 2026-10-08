package actions

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMasking(t *testing.T) {
	tests := []struct {
		name      string
		secrets   map[string]string
		input     string
		want      string
		wantError bool
	}{
		{name: "literal and overlapping", secrets: map[string]string{"ONE": "abc", "TWO": "bcde"}, input: "abcde abc", want: "[redacted] [redacted]"},
		{name: "multiline and long lines", secrets: map[string]string{"ONE": "line-eight\n}\nsecond-eight"}, input: "line-eight\n}\nsecond-eight\nline-eight\n}\n", want: "[redacted]\n[redacted]\n}\n"},
		{name: "mask command", input: "::add-mask::hidden-value\nhidden-value\n", want: "[redacted]\n"},
		{name: "command inside line", input: "notice ::add-mask::hidden-value\nhidden-value\n", want: "notice [redacted]\n"},
		{name: "escaped value", input: "::add-mask::first%0D%0Asecond%25value\r\nfirst\r\nsecond%value\n", want: "[redacted]\n"},
		{name: "escape decoded once", input: "::add-mask::value%250A\nvalue%0A", want: "[redacted]"},
		{name: "unfinished command", input: "before\n::add-mask::hidden-value", want: "before\n"},
		{name: "empty mask", input: "::add-mask::\nvisible", want: "visible"},
		{name: "valid UTF-8 across chunks", secrets: map[string]string{"ONE": "비밀값"}, input: "한글 비밀값 end", want: "한글 [redacted] end"},
		{name: "invalid UTF-8", input: "before\xff\xfeafter", want: "before\uFFFDafter"},
		{name: "oversized mask", input: "::add-mask::" + strings.Repeat("x", maxMaskBytes+1) + "\nWITHHELD", wantError: true},
		{name: "oversized unfinished mask", input: "::add-mask::" + strings.Repeat("x", 3*maxMaskBytes+2), wantError: true},
	}
	var commands strings.Builder
	for index := range maxAddedMasks + 1 {
		fmt.Fprintf(&commands, "::add-mask::mask-value-%03d\n", index)
	}
	commands.WriteString("WITHHELD")
	tests = append(tests, struct {
		name      string
		secrets   map[string]string
		input     string
		want      string
		wantError bool
	}{name: "too many values", input: commands.String(), wantError: true})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, chunk := range []int{1, 7, len(test.input) + 1} {
				mask := newMasker(test.secrets)
				stream := newCommandMaskingWriter(mask)
				for index := 0; index < len(test.input); index += chunk {
					_, _ = stream.Write([]byte(test.input[index:min(index+chunk, len(test.input))]))
				}
				output, _, err := stream.finish()
				if test.wantError {
					if err == nil || !mask.stopped || strings.Contains(output, "WITHHELD") {
						t.Fatalf("chunk %d: %q, %v", chunk, output, err)
					}
					continue
				}
				if err != nil || output != test.want || !utf8.ValidString(output) {
					t.Fatalf("chunk %d: got %q, want %q (%v)", chunk, output, test.want, err)
				}
			}
		})
	}
	t.Run("commands parsed before clipping", func(t *testing.T) {
		stream := newCommandMaskingWriter(newMasker(nil))
		fmt.Fprint(stream, strings.Repeat("x", maximumStepOutput), "\n::add-mask::hidden-value\n", strings.Repeat("y", maximumStepOutput), "hidden-value")
		output, gap, err := stream.finish()
		if err != nil || len(output) > maximumStepOutput || gap.Omitted == 0 || strings.Contains(output, "hidden-value") || !strings.HasSuffix(output, "[redacted]") {
			t.Fatalf("bytes=%d gap=%+v err=%v", len(output), gap, err)
		}
	})
}
