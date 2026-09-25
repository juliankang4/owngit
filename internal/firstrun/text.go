package firstrun

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// runeRange is an inclusive range of code points.
type runeRange struct{ lo, hi rune }

func inRanges(r rune, ranges []runeRange) bool {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].hi >= r })
	return i < len(ranges) && ranges[i].lo <= r
}

// runeWidth is the number of terminal columns r takes: none for a combining
// mark, two for East Asian Wide and Fullwidth characters such as Hangul
// syllables, and one otherwise.
func runeWidth(r rune) int {
	if inRanges(r, combiningRanges) {
		return 0
	}
	if inRanges(r, wideRanges) {
		return 2
	}
	return 1
}

// textWidth is the display width of plain text without escape sequences.
func textWidth(s string) int {
	width := 0
	for _, r := range s {
		width += runeWidth(r)
	}
	return width
}

// widthSafe reports whether every terminal draws r with the same width:
// printable ASCII and the East Asian Wide scripts OwnGit's text uses (Hangul
// syllables and compatibility jamo, CJK ideographs, kana, fullwidth forms).
// Only such text may sit inside a card with a closed right border.
func widthSafe(r rune) bool {
	return (r >= 0x20 && r < 0x7F) || (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x3131 && r <= 0x318E) ||
		(r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3040 && r <= 0x30FF) || (r >= 0xFF01 && r <= 0xFF60)
}

func textWidthSafe(s string) bool {
	for _, r := range s {
		if !widthSafe(r) {
			return false
		}
	}
	return true
}

// displayValue is the display form of a value that came from outside OwnGit,
// such as a typed path or a host name. Decomposed Hangul (as macOS file names
// often store it) is composed into whole syllables, and control characters
// are shown as visible escapes, so a value can never move the cursor or send
// terminal commands. Direction controls are shown as escapes too, so a
// value is shown in the order it is stored.
func displayValue(value string) string {
	value = composeHangul(strings.ToValidUTF8(value, "\uFFFD"))
	var out strings.Builder
	for _, r := range value {
		switch {
		case r == '\t':
			out.WriteString(`\t`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r < 0x20 || (r >= 0x7F && r <= 0x9F):
			fmt.Fprintf(&out, `\x%02x`, r)
		case directionControl(r):
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// directionControl reports whether r changes the order in which the
// characters around it are shown, so a value could look like another one.
func directionControl(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// unshowable reports whether displayValue shows r as an escape.
func unshowable(r rune) bool {
	return r < 0x20 || (r >= 0x7F && r <= 0x9F) || directionControl(r)
}

// composeHangul applies the Unicode composition of conjoining Hangul jamo
// into precomposed syllables, the part of NFC that matters for display width.
func composeHangul(s string) string {
	const (
		sBase, lBase, vBase, tBase = 0xAC00, 0x1100, 0x1161, 0x11A7
		lCount, vCount, tCount     = 19, 21, 28
	)
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for _, r := range runes {
		if n := len(out); n > 0 {
			last := out[n-1]
			if last >= lBase && last < lBase+lCount && r >= vBase && r < vBase+vCount {
				out[n-1] = sBase + ((last-lBase)*vCount+(r-vBase))*tCount
				continue
			}
			if last >= sBase && last < sBase+lCount*vCount*tCount && (last-sBase)%tCount == 0 && r > tBase && r < tBase+tCount {
				out[n-1] = last + (r - tBase)
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}

// cutPoint is the length in runes of the longest prefix of word that fits in
// width, preferring to end just after '/', '-', '_' or '.'.
func cutPoint(word []rune, width int) int {
	used, n, soft := 0, 0, 0
	for i, r := range word {
		if used+runeWidth(r) > width {
			break
		}
		used += runeWidth(r)
		n = i + 1
		if strings.ContainsRune("/-_.", r) {
			soft = n
		}
	}
	n = max(n, 1)
	if soft >= max(1, n/2) {
		return soft
	}
	return n
}

// breakLong splits a word wider than width into pieces that fit, appending
// all but the last to lines, and returns the last piece.
func breakLong(lines []string, word string, width int) ([]string, string) {
	for textWidth(word) > width {
		runes := []rune(word)
		k := cutPoint(runes, width)
		lines = append(lines, string(runes[:k]))
		word = string(runes[k:])
	}
	return lines, word
}

// wrapText word-wraps text by display width. A word longer than the line,
// such as a path, breaks at a separator where possible.
func wrapText(text string, width int) []string {
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		current := ""
		for _, word := range strings.Split(paragraph, " ") {
			candidate := word
			if current != "" {
				candidate = current + " " + word
			}
			if textWidth(candidate) <= width {
				current = candidate
				continue
			}
			if current != "" {
				lines = append(lines, current)
			}
			lines, current = breakLong(lines, word, width)
		}
		lines = append(lines, current)
	}
	return lines
}

var pathToken = regexp.MustCompile(`[^/]*/|[^/]+$`)

// wrapPath wraps a path by whole directory names, so a name with a space is
// never split at that space. A single name longer than the line breaks at
// '-', '_' or '.'.
func wrapPath(path string, width int) []string {
	var lines []string
	current := ""
	for _, token := range pathToken.FindAllString(path, -1) {
		if textWidth(current+token) <= width {
			current += token
			continue
		}
		if current != "" {
			lines = append(lines, current)
		}
		lines, current = breakLong(lines, token, width)
	}
	if current != "" || len(lines) == 0 {
		lines = append(lines, current)
	}
	return lines
}

// valueLines lays out a displayed value; paths wrap by directory.
func valueLines(value string, width int) []string {
	value = displayValue(value)
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") {
		return wrapPath(value, width)
	}
	return wrapText(value, width)
}

// jamoKeys maps the Hangul jamo produced by the keys of the Korean two-set
// keyboard to the Latin letters of the same keys, so single-letter answers
// work while the Korean input source is active.
var jamoKeys = map[string]string{"ㅛ": "y", "ㅜ": "n", "ㅣ": "l", "ㅅ": "t", "ㅠ": "b", "ㅌ": "x"}

// normalizeKey turns one typed answer into its comparable form.
func normalizeKey(value string) string {
	if mapped, ok := jamoKeys[value]; ok {
		value = mapped
	}
	if utf8.RuneCountInString(value) == 1 {
		return strings.ToLower(value)
	}
	return value
}

// yesNo reads a [y/N] answer; ok is false for an answer it does not know.
func yesNo(value string) (yes, ok bool) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) == 1 {
		value = normalizeKey(value)
	} else {
		value = strings.ToLower(value)
	}
	switch value {
	case "y", "yes", "1", "예":
		return true, true
	case "", "n", "no", "2", "아니요", "아니오":
		return false, true
	}
	return false, false
}
