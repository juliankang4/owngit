package firstrun

import (
	"io"
	"strconv"
	"strings"
)

// span is a piece of text with one color role.
type span struct {
	text string
	role role
	bold bool
}

func plain(text string) span           { return span{text: text} }
func colored(text string, r role) span { return span{text: text, role: r} }
func strong(text string, r role) span  { return span{text: text, role: r, bold: true} }
func spaces(n int) span                { return span{text: strings.Repeat(" ", max(n, 0))} }
func spansText(parts []span) (out string) {
	for _, part := range parts {
		out += part.text
	}
	return out
}

// Card content items.
type itemKind int

const (
	itemBlank itemKind = iota
	itemText
	itemTag
	itemOption
	itemPairs
	itemCode
)

type pair struct {
	label, value string
	role         role
}

type item struct {
	kind  itemKind
	text  string // text, tag text, option label, or code
	role  role   // text role
	tag   string // tag kind for itemTag: ok, err, warn, note
	key   string // option key
	help  string // option help
	pairs []pair
}

func blankItem() item                   { return item{kind: itemBlank} }
func textItem(text string, r role) item { return item{kind: itemText, text: text, role: r} }
func tagItem(kind, text string) item    { return item{kind: itemTag, tag: kind, text: text} }
func optionItem(key, label, help string) item {
	return item{kind: itemOption, key: key, text: label, help: help}
}
func pairsItem(pairs []pair) item { return item{kind: itemPairs, pairs: pairs} }
func codeItem(code string) item   { return item{kind: itemCode, text: code} }

// tags are the text markers that carry each status without color.
var tags = map[string]struct {
	marker   string
	markRole role
	textRole role
}{
	"ok":   {"[ok]", roleOK, rolePlain},
	"err":  {"[x]", roleErr, roleErr},
	"warn": {"[!]", roleWarn, roleWarn},
	"note": {"[i]", roleBrand, rolePlain},
}

// wordmark is the ASCII OwnGit logo shown once when setup starts.
var wordmark = []string{
	"  ___                   ___  _  _",
	" / _ \\ __ __ __  _ _   / __|(_)| |_",
	"| (_) |\\ V  V / | ' \\ | (_ || ||  _|",
	" \\___/  \\_/\\_/  |_||_| \\___||_| \\__|",
}

// screen draws the setup cards. It writes whole lines and never moves the
// cursor, so the setup stays in the scrollback together with the server log.
type screen struct {
	out     io.Writer
	painter painter
	// columns reports the terminal width, or false when it is unknown. It is
	// asked again for every card, so a resized window is followed.
	columns func() (int, bool)
	// flush writes held server log lines. It runs before each card and
	// while waiting without a prompt, never inside a prompt.
	flush func()
}

func (s *screen) flushLogs() {
	if s.flush != nil {
		s.flush()
	}
}

func (s *screen) write(text string) { _, _ = io.WriteString(s.out, text) }

func (s *screen) paint(parts []span) string {
	var out strings.Builder
	for _, part := range parts {
		out.WriteString(s.painter.paint(part.role, part.text, part.bold))
	}
	return out.String()
}

func (s *screen) emit(parts ...span) { s.write(s.paint(parts) + "\n") }

func (s *screen) blankLine() { s.write("\n") }

// dims returns the terminal width, whether it is known, and the widest line
// the renderer writes: never more than 79 columns, and never into the last
// column, where some terminals wrap early.
func (s *screen) dims() (int, bool, int) {
	width, known := s.columns()
	if !known || width <= 0 {
		return 0, false, 79
	}
	return width, true, min(width, 80) - 1
}

func (s *screen) tagLines(kind, text string, indent, width int) [][]span {
	tag := tags[kind]
	body := wrapText(text, width-indent-textWidth(tag.marker)-1)
	lines := [][]span{{spaces(indent), strong(tag.marker, tag.markRole), plain(" "), colored(body[0], tag.textRole)}}
	for _, rest := range body[1:] {
		lines = append(lines, []span{spaces(indent + textWidth(tag.marker) + 1), colored(rest, tag.textRole)})
	}
	return lines
}

// notice prints a tagged line below a card: [ok], [x], [!] or [i].
func (s *screen) notice(kind, text string) {
	_, _, limit := s.dims()
	for _, line := range s.tagLines(kind, text, 2, limit) {
		s.emit(line...)
	}
}

func (s *screen) layout(items []item, inner int) [][]span {
	var lines [][]span
	for _, it := range items {
		switch it.kind {
		case itemBlank:
			lines = append(lines, nil)
		case itemText:
			for _, text := range wrapText(it.text, inner) {
				lines = append(lines, []span{colored(text, it.role)})
			}
		case itemTag:
			lines = append(lines, s.tagLines(it.tag, it.text, 0, inner)...)
		case itemOption:
			lines = append(lines, []span{strong("["+it.key+"]", roleBrand), plain(" "), strong(it.text, rolePlain)})
			if it.help != "" {
				for _, help := range wrapText(it.help, inner-4) {
					lines = append(lines, []span{plain("    "), colored(help, roleMuted)})
				}
			}
		case itemPairs:
			labelWidth := 0
			for _, p := range it.pairs {
				labelWidth = max(labelWidth, textWidth(p.label))
			}
			labelWidth += 3
			stacked := inner-labelWidth < 32 // narrow: the value goes under its label
			for _, p := range it.pairs {
				if stacked {
					lines = append(lines, []span{colored(p.label, roleMuted)})
					for _, value := range valueLines(p.value, inner-2) {
						lines = append(lines, []span{plain("  "), colored(value, p.role)})
					}
					continue
				}
				values := valueLines(p.value, inner-labelWidth)
				lines = append(lines, []span{colored(p.label, roleMuted), spaces(labelWidth - textWidth(p.label)), colored(values[0], p.role)})
				for _, value := range values[1:] {
					lines = append(lines, []span{spaces(labelWidth), colored(value, p.role)})
				}
			}
		case itemCode:
			spaced := strings.Join(strings.Split(it.text, ""), " ")
			rule := "+" + strings.Repeat("-", len(spaced)+6) + "+"
			lines = append(lines,
				[]span{colored(rule, roleBorder)},
				[]span{colored("|", roleBorder), plain("   "), strong(spaced, roleCode), plain("   "), colored("|", roleBorder)},
				[]span{colored(rule, roleBorder)})
		}
	}
	return lines
}

// cardStyle is the optional marker and title color of a card.
type cardStyle struct {
	right      string // step, such as "2/6", at the right of the top border
	titleRole  role
	marker     string // [ok] or [!] before the title
	markerRole role
}

// card draws a boxed card. The right border is drawn only when the width is
// known, at least 80 columns, and every character inside is drawn with the
// same width by every terminal; otherwise the card is open on the right, so
// it can never show a broken border.
func (s *screen) card(title string, items []item, style cardStyle) {
	s.flushLogs()
	if style.titleRole == rolePlain {
		style.titleRole = roleBrand
	}
	width, known, limit := s.dims()
	inner := limit - 6
	body := s.layout(items, inner)
	closed := known && width >= 80 && textWidthSafe(title)
	for _, line := range body {
		closed = closed && textWidthSafe(spansText(line))
	}
	head := []span{colored("+-", roleBorder), plain(" ")}
	if style.marker != "" {
		head = append(head, strong(style.marker, style.markerRole), plain(" "))
	}
	head = append(head, strong(title, style.titleRole), plain(" "))
	var tail []span
	if style.right != "" {
		tail = []span{plain(" "), colored(style.right, roleMuted), plain(" ")}
	}
	end := "--"
	if closed {
		end = "-+"
	}
	fill := max(1, limit-textWidth(spansText(head)+spansText(tail))-len(end))
	s.blankLine()
	top := append(append(head, colored(strings.Repeat("-", fill), roleBorder)), tail...)
	s.emit(append(top, colored(end, roleBorder))...)
	for _, line := range body {
		switch {
		case closed:
			padding := strings.Repeat(" ", inner-textWidth(spansText(line))) + "  "
			s.emit(append(append([]span{colored("|", roleBorder), plain("  ")}, line...), plain(padding), colored("|", roleBorder))...)
		case len(line) == 0:
			s.emit(colored("|", roleBorder))
		default:
			s.emit(append([]span{colored("|", roleBorder), plain("  ")}, line...)...)
		}
	}
	if closed {
		s.emit(colored("+"+strings.Repeat("-", limit-2)+"+", roleBorder))
	} else {
		s.emit(colored("+"+strings.Repeat("-", limit-1), roleBorder))
	}
}

// prompt writes the question line; the answer is typed after it. A default
// too long for the line is shown above it, after the word suggested.
func (s *screen) prompt(label, fallback, suggested string) {
	_, _, limit := s.dims()
	fallback = displayValue(fallback)
	if fallback != "" && textWidth(label)+textWidth(fallback)+8 > limit {
		for i, text := range wrapText(fallback, limit-textWidth(suggested)-6) {
			lead := suggested + ": "
			if i > 0 {
				lead = strings.Repeat(" ", textWidth(suggested)+2)
			}
			s.emit(plain("  "), colored(lead, roleMuted), plain(text))
		}
		fallback = ""
	}
	parts := []span{plain("  " + label)}
	if fallback != "" {
		parts = append(parts, plain(" "), colored("["+fallback+"]", roleMuted))
	}
	s.write(s.paint(append(parts, plain(": "))))
}

// banner draws the wordmark, dropped when the terminal is too narrow for it,
// and the subtitle with the dashboard address.
func (s *screen) banner(subtitle, address string) {
	width, known, _ := s.dims()
	widest := 0
	for _, line := range wordmark {
		widest = max(widest, len(line))
	}
	s.blankLine()
	if !known || width >= widest+4 {
		for _, line := range wordmark {
			s.emit(plain("  "), strong(line, roleBrand))
		}
		s.blankLine()
	} else {
		s.emit(plain("  "), strong("OwnGit", roleBrand))
	}
	s.emit(plain("  "), plain(subtitle), plain("   "), colored(displayValue(address), roleMuted))
}

func stepLabel(n, total int) string {
	return strconv.Itoa(n) + "/" + strconv.Itoa(total)
}
