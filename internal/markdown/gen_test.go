package markdown

import (
	"math/rand"
	"strings"
)

// generate builds a Markdown document of about size bytes from seed. It
// mixes the line kinds that change how goldmark groups lines (quotes, list
// items including empty ones, fences at every indent, headings, tables, HTML
// blocks, definitions, thematic breaks and blank lines) with lines dense in
// the characters that make goldmark slow. A few line templates ("motifs") are
// repeated, because a slow document repeats one costly shape many times.
func generate(seed int64, size int) string {
	r := rand.New(rand.NewSource(seed))
	pick := func(options ...string) string { return options[r.Intn(len(options))] }
	inline := func() string {
		var b strings.Builder
		for n := r.Intn(12) + 1; n > 0; n-- {
			b.WriteString(pick(
				"*a_", "_a*", "~a*", "***a___", "a*b_", "*", "_", "~~", "**",
				"[a](", "[a](b \"", "![a](", "[a](<", "[a]", "[a][", "[", "]", "(", ")",
				"<!--", "<?", "<!X", "<![CDATA[", "<a href=\"", "<b>", "</b>", "<", ">", "-->",
				"`", "``", "```", "a `b` c", "\\*", "&amp;", "&#",
				"www.a.b", "http://a.b/c_(d", "a@b.c", "<http://a", "|", " ", "word ",
			))
		}
		return b.String()
	}
	prefix := func() string {
		var b strings.Builder
		for n := r.Intn(4); n > 0; n-- {
			b.WriteString(pick("> ", ">", "- ", "* ", "+ ", "1. ", "2) ", "  ", "    ", "\t", "   > "))
		}
		return b.String()
	}
	structural := func() string {
		indent := pick("", " ", "  ", "   ", "    ", "     ", "\t", " \t")
		switch r.Intn(16) {
		case 0:
			return ""
		case 1:
			return indent + pick("```", "~~~", "````", "```go", "~~~ x", "``` `")
		case 2:
			return indent + pick("* ", "+ ", "- ", "1. ", "*", "-", "+")
		case 3:
			return indent + pick("# ", "## a", "###### ", "#")
		case 4:
			return indent + pick("| a | b |", "|---|---|", "| - |", "a | b")
		case 5:
			return indent + pick("<table>", "<div>", "</div>", "<p align=\"center\">", "<script>", "</script>", "<!--", "-->", "<?", "?>", "<!X", "<![CDATA[", "]]>", "<pre>", "<custom>", "<TABLE>", "<table\r")
		case 6:
			return indent + pick("[a]: /b", "[a]: <", "[a]: /b \"t", "[a]:")
		case 7:
			return indent + pick("---", "***", "===", "___", "- - -")
		case 8:
			return indent + prefix()
		default:
			return prefix() + inline()
		}
	}
	motifs := make([]string, r.Intn(3)+1)
	for i := range motifs {
		switch r.Intn(3) {
		case 0:
			motifs[i] = prefix() + inline() + inline() + inline()
		case 1:
			motifs[i] = structural()
		default:
			// One costly token, repeated.
			motifs[i] = prefix() + strings.Repeat(inline(), r.Intn(200)+5)
		}
	}
	separators := make([]string, r.Intn(3)+1)
	for i := range separators {
		separators[i] = structural()
	}
	var b strings.Builder
	for b.Len() < size {
		switch n := r.Intn(10); {
		case n < 6:
			b.WriteString(motifs[r.Intn(len(motifs))])
		case n < 9:
			b.WriteString(separators[r.Intn(len(separators))])
		default:
			b.WriteString(structural())
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// largestAccepted returns the longest prefix of source, cut at a line end,
// whose estimated cost is within maxCost. It puts generated documents at the
// edge of the budget, where a gap in the estimate would show.
func largestAccepted(source string) string {
	if len(source) > MaxSource {
		source = source[:MaxSource]
	}
	low, high := 0, len(source)
	for low < high {
		middle := (low + high + 1) / 2
		cut := strings.LastIndexByte(source[:middle], '\n') + 1
		if estimateCost([]byte(source[:cut])) <= maxCost {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return source[:strings.LastIndexByte(source[:low], '\n')+1]
}

// fillTo repeats unit, each followed by separator, up to MaxSource.
func fillTo(unit, separator string) string {
	var b strings.Builder
	for b.Len()+len(unit)+len(separator) <= MaxSource {
		b.WriteString(unit)
		b.WriteString(separator)
	}
	return b.String()
}

// wrap breaks s into lines of width characters.
func wrap(s string, width int) string {
	var b strings.Builder
	for len(s) > width {
		b.WriteString(s[:width])
		b.WriteByte('\n')
		s = s[width:]
	}
	b.WriteString(s)
	return b.String()
}
