package checkoutput

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ClipText returns value as valid UTF-8 of at most limit bytes and reports
// whether text was cut. Each run of invalid bytes becomes one U+FFFD, as with
// strings.ToValidUTF8, and the cut never splits a character, so a JSON round
// trip returns exactly the same bytes. A raw byte cut does not have that
// property: encoding/json replaces each invalid byte with a three-byte U+FFFD,
// which can push a value past the bound that the sender just enforced.
//
// It reads and converts value only up to the cut, so a long value costs what
// is kept, not what is dropped. A run of invalid bytes is kept whole once its
// one replacement fits, however long the run is.
func ClipText(value string, limit int) (string, bool) {
	const replacement = "\uFFFD"
	size, end, invalid := 0, 0, false
	for end < len(value) {
		character, width := utf8.DecodeRuneInString(value[end:])
		bytes := width
		if character == utf8.RuneError && width == 1 {
			bytes = len(replacement)
			if invalid {
				bytes = 0
			}
			invalid = true
		} else {
			invalid = false
		}
		if size+bytes > limit {
			break
		}
		size += bytes
		end += width
	}
	return strings.ToValidUTF8(value[:end], replacement), end < len(value)
}

// Gap describes a marker line inside clipped text: text[Start:End] is the
// marker, and it stands for Omitted bytes of the original output. A later clip
// of that text uses it to count against the original output instead of the
// clipped text, and never looks for marker words in the text itself. The zero
// value means nothing was left out.
type Gap struct{ Start, End, Omitted int }

// ClipLog returns value as valid UTF-8 of at most limit bytes, for output
// where the end matters as much as the start: a failing check prints its
// reason last. Text that fits is returned unchanged, apart from the invalid
// byte runs that ClipText also replaces. Longer text keeps its beginning and
// its end around one marker line that counts the bytes left out, and the whole
// result, marker included, stays within limit. The tail gets three quarters of
// the space because the final lines are the ones that explain a failure, while
// the head only has to show how the output began. A cut never splits a
// character. ClipText stays for short fields where only the start is useful.
//
// gap describes a marker that value already holds from an earlier clip (see
// Gap); pass the zero Gap for plain text. When the new cut removes that marker,
// the new marker counts the bytes the earlier clip left out too. A marker that
// stays in view is not counted twice. value must be valid UTF-8 where gap is
// set. The boolean is true when anything was left out, now or earlier.
func ClipLog(value string, limit int, gap Gap) (string, bool) {
	if gap.Omitted == 0 {
		value = strings.ToValidUTF8(value, "\uFFFD")
	}
	if len(value) <= max(limit, 0) {
		return value, gap.Omitted > 0
	}
	text, _ := clipLog(value, value, len(value), limit, gapsOf(gap))
	return text, true
}

func gapsOf(gap Gap) []Gap {
	if gap.Omitted == 0 {
		return nil
	}
	return []Gap{gap}
}

// clipLog joins the first and last parts of a stream of total bytes, where
// gaps are the markers the stream already holds, in stream offsets. head must
// hold at least the first limit/4 bytes and tail at least the last limit
// bytes, or the whole stream where it is shorter. It also returns the new
// marker's position.
func clipLog(head, tail string, total, limit int, gaps []Gap) (string, Gap) {
	marker := func(omitted int) string { return fmt.Sprintf("\n[... %d bytes omitted ...]\n", omitted) }
	whole := total
	for _, gap := range gaps {
		whole += gap.Omitted - (gap.End - gap.Start)
	}
	textBudgetAfterLargestMarker := limit - len(marker(whole))
	if textBudgetAfterLargestMarker < 4 {
		clipped, _ := ClipText(head, limit)
		return clipped, Gap{}
	}
	keepHead := min(textBudgetAfterLargestMarker/4, len(head))
	for cut := keepHead - 1; cut >= 0 && cut >= keepHead-utf8.UTFMax; cut-- {
		if utf8.RuneStart(head[cut]) {
			if !utf8.FullRuneInString(head[cut:keepHead]) {
				keepHead = cut
			}
			break
		}
	}
	tailStart := total - min(textBudgetAfterLargestMarker-textBudgetAfterLargestMarker/4, len(tail))
	for tailStart < total && !utf8.RuneStart(tail[len(tail)-(total-tailStart)]) {
		tailStart++
	}
	// An earlier marker is removed whole or kept whole, never cut through.
	visibleMarkerBytes, visibleOmittedBytes := 0, 0
	for _, gap := range gaps {
		if gap.Start < keepHead && keepHead < gap.End {
			keepHead = gap.Start
		}
		if gap.Start < tailStart && tailStart < gap.End {
			tailStart = gap.End
		}
	}
	for _, gap := range gaps {
		if gap.End <= keepHead || gap.Start >= tailStart {
			visibleMarkerBytes += gap.End - gap.Start
			visibleOmittedBytes += gap.Omitted
		}
	}
	omitted := whole - (keepHead + total - tailStart - visibleMarkerBytes) - visibleOmittedBytes
	text := marker(omitted)
	return head[:keepHead] + text + tail[len(tail)-(total-tailStart):], Gap{Start: keepHead, End: keepHead + len(text), Omitted: omitted}
}

// LogBuffer joins log parts and keeps the beginning and the end of the joined
// text within Limit bytes, as ClipLog does for one string. It holds the first
// Limit/4 bytes and up to about twice Limit of the latest bytes, however much
// is added. Parts are sanitized like ClipLog input. Result reports whether
// anything was left out.
type LogBuffer struct {
	Limit int
	total int
	head  strings.Builder
	tail  []byte
	gaps  []Gap
}

// Add appends part. Nothing is refused: the end of the log must survive, so
// callers keep adding after the limit is passed.
func (buffer *LogBuffer) Add(part string) { buffer.AddClipped(part, Gap{}) }

// AddClipped appends part, which holds the marker described by gap from an
// earlier clip, so a later cut counts the bytes that marker stands for. part
// must be valid UTF-8 when gap is set.
func (buffer *LogBuffer) AddClipped(part string, gap Gap) {
	if gap.Omitted > 0 {
		buffer.gaps = append(buffer.gaps, Gap{buffer.total + gap.Start, buffer.total + gap.End, gap.Omitted})
	} else {
		part = strings.ToValidUTF8(part, "\uFFFD")
	}
	buffer.total += len(part)
	if room := buffer.Limit/4 - buffer.head.Len(); room > 0 {
		kept := min(room, len(part))
		buffer.head.WriteString(part[:kept])
		part = part[kept:]
	}
	keep := buffer.Limit - buffer.Limit/4
	buffer.tail = append(buffer.tail, part[max(0, len(part)-keep):]...)
	// The tail may grow to twice what the result can use before it is cut, so
	// trimming costs little per added byte.
	if len(buffer.tail) > 2*keep {
		buffer.tail = append(buffer.tail[:0], buffer.tail[len(buffer.tail)-keep:]...)
	}
}

// Result returns the joined text and whether anything was left out, now or by
// an earlier clip.
func (buffer *LogBuffer) Result() (string, bool) {
	text, _, cut := buffer.ResultWithGap()
	return text, cut
}

// ResultWithGap is Result plus the position of the marker it added, or the
// zero Gap when the text fits whole.
func (buffer *LogBuffer) ResultWithGap() (string, Gap, bool) {
	if buffer.total <= buffer.Limit {
		return buffer.head.String() + string(buffer.tail), Gap{}, len(buffer.gaps) > 0
	}
	text, gap := clipLog(buffer.head.String(), string(buffer.tail), buffer.total, buffer.Limit, buffer.gaps)
	return text, gap, true
}

// SummaryText returns value as one trimmed line of valid UTF-8 within limit
// bytes. Stored summaries refuse line breaks and NUL, so a multi-line error,
// such as one built with errors.Join, is joined with spaces instead of making
// the server refuse the report.
func SummaryText(value string, limit int) string {
	value = strings.Map(func(character rune) rune {
		switch character {
		case '\r', '\n':
			return ' '
		case 0:
			return -1
		}
		return character
	}, value)
	value, _ = ClipText(strings.TrimSpace(value), limit)
	return strings.TrimSpace(value)
}
