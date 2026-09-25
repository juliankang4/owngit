package firstrun

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Color roles used by the renderer. Every colored item also carries a text
// cue ([ok], [x], [!], [i]), so color is never the only signal.
type role int

const (
	rolePlain  role = iota
	roleBrand       // wordmark, card titles, choice keys
	roleBorder      // card borders
	roleMuted       // help text and hints
	roleOK          // [ok]
	roleErr         // [x]
	roleWarn        // [!]
	roleCode        // the approval code
)

// Color depths, from none to 24-bit.
type depth int

const (
	depthNone depth = iota
	depth16
	depth256
	depthTrue
)

// Terminal background themes. themeUnknown uses colors that stay readable on
// both white and near-black and leaves help text in the terminal's own color.
type theme int

const (
	themeUnknown theme = iota
	themeDark
	themeLight
)

type rgbColor struct{ r, g, b uint8 }

// palettes hold the truecolor value of each role per theme. Each reaches the
// contrast target of the approved design against typical backgrounds of its
// theme. A missing role keeps the terminal's own color.
var palettes = map[theme]map[role]rgbColor{
	themeLight: {roleBrand: {0x0A, 0x62, 0xC9}, roleBorder: {0x8C, 0x95, 0x9F}, roleMuted: {0x5A, 0x63, 0x6E},
		roleOK: {0x1A, 0x7F, 0x37}, roleErr: {0xC8, 0x24, 0x2B}, roleWarn: {0x8A, 0x5A, 0x00}, roleCode: {0x7A, 0x3F, 0xD1}},
	themeDark: {roleBrand: {0x5E, 0xA2, 0xF0}, roleBorder: {0x6E, 0x76, 0x81}, roleMuted: {0xA2, 0xAB, 0xB6},
		roleOK: {0x46, 0xC2, 0x5A}, roleErr: {0xFF, 0x7B, 0x72}, roleWarn: {0xE0, 0xA4, 0x3A}, roleCode: {0xD2, 0xA8, 0xFF}},
	themeUnknown: {roleBrand: {0x12, 0x7B, 0xF3}, roleBorder: {0x75, 0x7F, 0x89},
		roleOK: {0x24, 0x90, 0x3F}, roleErr: {0xE4, 0x41, 0x41}, roleWarn: {0xAD, 0x71, 0x09}, roleCode: {0xA0, 0x5D, 0xE2}},
}

// xterm256 holds hand-picked 256-color indexes with the same hue and contrast
// target, for terminals without truecolor such as macOS Terminal.
var xterm256 = map[theme]map[role]int{
	themeLight:   {roleBrand: 26, roleBorder: 245, roleMuted: 241, roleOK: 22, roleErr: 160, roleWarn: 94, roleCode: 91},
	themeDark:    {roleBrand: 75, roleBorder: 243, roleMuted: 145, roleOK: 71, roleErr: 209, roleWarn: 179, roleCode: 183},
	themeUnknown: {roleBrand: 32, roleBorder: 244, roleOK: 65, roleErr: 167, roleWarn: 130, roleCode: 134},
}

// ansi16 is the basic 16-color fallback. The terminal theme decides the real
// color, so yellow is never used on a light background, and an unknown
// background gets no hue for the brand.
var ansi16 = map[theme]map[role]string{
	themeLight:   {roleBrand: "34", roleBorder: "90", roleOK: "32", roleErr: "31", roleCode: "35"},
	themeDark:    {roleBrand: "94", roleBorder: "90", roleOK: "32", roleErr: "91", roleWarn: "33", roleCode: "95"},
	themeUnknown: {roleOK: "32", roleErr: "31"},
}

// painter turns a role and text into text wrapped in SGR codes.
type painter struct {
	depth depth
	theme theme
}

func (p painter) code(r role) string {
	if r == rolePlain || p.depth == depthNone {
		return ""
	}
	if p.depth == depth16 {
		return ansi16[p.theme][r]
	}
	color, ok := palettes[p.theme][r]
	if !ok {
		return ""
	}
	if p.depth == depth256 {
		return "38;5;" + strconv.Itoa(xterm256[p.theme][r])
	}
	return fmt.Sprintf("38;2;%d;%d;%d", color.r, color.g, color.b)
}

func (p painter) paint(r role, text string, bold bool) string {
	if p.depth == depthNone || text == "" {
		return text
	}
	var codes []string
	if bold {
		codes = append(codes, "1")
	}
	if code := p.code(r); code != "" {
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return text
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + text + "\x1b[0m"
}

// colorDepth chooses the color depth from the environment. NO_COLOR (any
// non-empty value), TERM=dumb, or output that is not a terminal turn color
// off.
func colorDepth(getenv func(string) string, terminal bool) depth {
	if !terminal || getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return depthNone
	}
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return depthTrue
	}
	if strings.Contains(getenv("TERM"), "256color") {
		return depth256
	}
	return depth16
}

// colorFGBGTheme reads the background from COLORFGBG ("15;0" is light text
// on a dark background).
func colorFGBGTheme(getenv func(string) string) (theme, bool) {
	parts := strings.Split(getenv("COLORFGBG"), ";")
	if len(parts) < 2 {
		return themeUnknown, false
	}
	background, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || background < 0 {
		return themeUnknown, false
	}
	switch background {
	case 7, 9, 10, 11, 12, 13, 14, 15:
		return themeLight, true
	}
	return themeDark, true
}

// backgroundQuery asks the terminal for its background color (OSC 11),
// followed by a device-attributes request that every terminal answers, so
// the reader never waits for a reply that will not come.
const backgroundQuery = "\x1b]11;?\x07\x1b[c"

var (
	backgroundReply = regexp.MustCompile(`\x1b\]11;rgba?:([0-9a-fA-F]+)/([0-9a-fA-F]+)/([0-9a-fA-F]+)`)
	deviceReply     = regexp.MustCompile(`\x1b\[\?[0-9;]*c`)
	queryReplies    = regexp.MustCompile(`\x1b\]11;[^\x07\x1b]*(\x07|\x1b\\)|\x1b\[\?[0-9;]*c`)
)

// parseBackground reads the terminal's replies to backgroundQuery. It
// returns the theme, whether one was found, and the bytes that were not part
// of a reply, which are keys typed meanwhile and must not be lost.
func parseBackground(buffer []byte) (theme, bool, []byte) {
	rest := queryReplies.ReplaceAll(buffer, nil)
	match := backgroundReply.FindSubmatch(buffer)
	if match == nil {
		return themeUnknown, false, rest
	}
	channel := func(hex []byte) float64 {
		value, _ := strconv.ParseUint(string(hex), 16, 64)
		maximum := uint64(1)<<(4*len(hex)) - 1
		return float64(value) / float64(maximum)
	}
	if len(match[1]) > 4 || len(match[2]) > 4 || len(match[3]) > 4 {
		return themeUnknown, false, rest
	}
	luminance := 0.2126*channel(match[1]) + 0.7152*channel(match[2]) + 0.0722*channel(match[3])
	if luminance > 0.5 {
		return themeLight, true, rest
	}
	return themeDark, true, rest
}

// queryComplete reports whether the device-attributes reply has arrived.
func queryComplete(buffer []byte) bool {
	return deviceReply.Match(buffer)
}

// stripSGR removes color codes, for tests and plain-text comparisons.
func stripSGR(s string) string {
	return string(sgrPattern.ReplaceAll([]byte(s), nil))
}

var sgrPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)
