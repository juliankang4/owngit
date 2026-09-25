package firstrun

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// renderCase is one renderer input and the output of the approved design
// renderer for it, recorded in testdata/render_cases.json. The cases cover
// 80 and 60 columns and an unknown width, English and Korean, color off and
// every color depth and background, open and closed cards, Korean width,
// long paths, characters of uncertain width, and control characters.
type renderCase struct {
	Width  int             `json:"width"`
	Depth  string          `json:"depth"`
	Theme  string          `json:"theme"`
	Kind   string          `json:"kind"`
	Spec   json.RawMessage `json:"spec"`
	Output string          `json:"output"`
}

type cardSpec struct {
	Name      string            `json:"name"`
	Title     string            `json:"title"`
	Right     string            `json:"right"`
	TitleRole string            `json:"title_role"`
	Tag       []string          `json:"tag"`
	Items     []json.RawMessage `json:"items"`
}

var roleNames = map[string]role{
	"": rolePlain, "brand": roleBrand, "border": roleBorder, "muted": roleMuted,
	"ok": roleOK, "err": roleErr, "warn": roleWarn, "code": roleCode,
}

func roleOf(value any) role {
	name, _ := value.(string)
	return roleNames[name]
}

func str(value any) string {
	text, _ := value.(string)
	return text
}

func decodeItem(t *testing.T, raw json.RawMessage) item {
	t.Helper()
	var fields []any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	switch fields[0] {
	case "blank":
		return blankItem()
	case "text":
		return textItem(str(fields[1]), roleOf(fields[2]))
	case "tag":
		return tagItem(str(fields[1]), str(fields[2]))
	case "option":
		return optionItem(str(fields[1]), str(fields[2]), str(fields[3]))
	case "code":
		return codeItem(str(fields[1]))
	case "pairs":
		var pairs []pair
		for _, entry := range fields[1].([]any) {
			values := entry.([]any)
			pairs = append(pairs, pair{label: str(values[0]), value: str(values[1]), role: roleOf(values[2])})
		}
		return pairsItem(pairs)
	}
	t.Fatalf("unknown item %v", fields[0])
	return item{}
}

func testScreen(width int, colors depth, background theme) (*screen, *bytes.Buffer) {
	var out bytes.Buffer
	return &screen{
		out: &out, painter: painter{depth: colors, theme: background},
		columns: func() (int, bool) { return width, width > 0 },
	}, &out
}

func TestRendererMatchesTheApprovedDesign(t *testing.T) {
	content, err := os.ReadFile("testdata/render_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []renderCase
	if err := json.Unmarshal(content, &cases); err != nil {
		t.Fatal(err)
	}
	depths := map[string]depth{"none": depthNone, "16": depth16, "256": depth256, "truecolor": depthTrue}
	themes := map[string]theme{"unknown": themeUnknown, "dark": themeDark, "light": themeLight}
	for i, c := range cases {
		s, out := testScreen(c.Width, depths[c.Depth], themes[c.Theme])
		name := c.Kind
		switch c.Kind {
		case "card":
			var spec cardSpec
			if err := json.Unmarshal(c.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			name += " " + spec.Name
			var items []item
			for _, raw := range spec.Items {
				items = append(items, decodeItem(t, raw))
			}
			style := cardStyle{right: spec.Right, titleRole: roleNames[spec.TitleRole]}
			if len(spec.Tag) == 2 {
				style.marker, style.markerRole = spec.Tag[0], roleNames[spec.Tag[1]]
			}
			s.card(spec.Title, items, style)
		case "say":
			var spec struct{ Tag, Text string }
			_ = json.Unmarshal(c.Spec, &spec)
			s.notice(spec.Tag, spec.Text)
		case "prompt":
			var spec struct{ Label, Default, Suggested string }
			_ = json.Unmarshal(c.Spec, &spec)
			s.prompt(spec.Label, spec.Default, spec.Suggested)
		case "banner":
			s.banner("First-run setup", "http://127.0.0.1:7654/")
		}
		if got := out.String(); got != c.Output {
			t.Errorf("case %d %s (width %d, %s, %s):\n got: %q\nwant: %q", i, name, c.Width, c.Depth, c.Theme, got, c.Output)
		}
	}
}

// Every line stays inside 79 columns, and a closed card's right border is in
// the same column on every line, for English and Korean.
func TestCardsStayInsideTheirWidth(t *testing.T) {
	for _, width := range []int{80, 120, 60, 50, 0} {
		s, out := testScreen(width, depthNone, themeUnknown)
		s.card("설치를 마쳤습니다", []item{
			pairsItem([]pair{{label: "저장소 폴더", value: "/Volumes/NAS/공유 폴더/개발팀/저장소 모음/OwnGit-Repositories-archive-2026"}}),
			textItem("OwnGit은 이 폴더 안에 저장소를 만들고, 폴더에 있던 파일은 그대로 둡니다. 비밀번호는 입력해도 화면에 나타나지 않습니다.", roleMuted),
		}, cardStyle{right: "1/6"})
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")[1:]
		closed := strings.HasSuffix(lines[0], "-+")
		if closed != (width >= 80) {
			t.Errorf("width %d: closed=%v", width, closed)
		}
		limit := 79
		if width > 0 && width < 80 {
			limit = width - 1
		}
		for _, line := range lines {
			if textWidth(line) > limit {
				t.Errorf("width %d: line of %d columns: %q", width, textWidth(line), line)
			}
			if closed && textWidth(line) != limit {
				t.Errorf("width %d: closed card line is %d columns: %q", width, textWidth(line), line)
			}
		}
	}
}

func TestDisplayValueEscapesControlCharacters(t *testing.T) {
	got := displayValue("a\tb\nc\rd\x1b[2Je\x07f\u009bg")
	want := `a\tb\nc\rd\x1b[2Je\x07f\x9bg`
	if got != want {
		t.Fatalf("displayValue=%q want %q", got, want)
	}
	// Direction controls are shown as escapes, so a path reads in the order
	// it is stored.
	if got := displayValue("/tmp/\u202egpj.exe\u2066x\u200f"); got != `/tmp/\u202egpj.exe\u2066x\u200f` {
		t.Fatalf("direction controls: %q", got)
	}
	// Decomposed Hangul, as macOS file names often hold it, becomes whole
	// syllables, so the width is right.
	if got := displayValue("\u1112\u1161\u11ab\u1100\u1173\u11af"); got != "한글" {
		t.Fatalf("composed=%q", got)
	}
	if textWidth("한글") != 4 || textWidth("e\u0301") != 1 {
		t.Fatal("width of Korean or a combining mark is wrong")
	}
	if !textWidthSafe("저장소 folder") || textWidthSafe("Café") || textWidthSafe("🗂") {
		t.Fatal("width safety is wrong")
	}
}

func TestColorFollowsTheEnvironment(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	for _, c := range []struct {
		values   map[string]string
		terminal bool
		want     depth
	}{
		{map[string]string{"TERM": "xterm-256color"}, true, depth256},
		{map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"}, true, depthNone},
		{map[string]string{"TERM": "dumb"}, true, depthNone},
		{map[string]string{"TERM": "xterm", "COLORTERM": "truecolor"}, true, depthTrue},
		{map[string]string{"TERM": "xterm"}, true, depth16},
		{map[string]string{"TERM": "xterm-256color"}, false, depthNone},
	} {
		if got := colorDepth(env(c.values), c.terminal); got != c.want {
			t.Errorf("%v terminal=%v: depth %v want %v", c.values, c.terminal, got, c.want)
		}
	}
	if found, ok := colorFGBGTheme(env(map[string]string{"COLORFGBG": "0;15"})); !ok || found != themeLight {
		t.Error("COLORFGBG light background not read")
	}
	if found, ok := colorFGBGTheme(env(map[string]string{"COLORFGBG": "15;0"})); !ok || found != themeDark {
		t.Error("COLORFGBG dark background not read")
	}
}

// The background reply is read and removed, and keys typed while waiting for
// it are kept.
func TestBackgroundReplyKeepsTypedKeys(t *testing.T) {
	reply := []byte("2\x1b]11;rgb:ffff/ffff/ffff\x07\x1b[?62;22c")
	found, ok, rest := parseBackground(reply)
	if !ok || found != themeLight || string(rest) != "2" {
		t.Fatalf("theme=%v ok=%v rest=%q", found, ok, rest)
	}
	found, ok, _ = parseBackground([]byte("\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\\x1b[?1;2c"))
	if !ok || found != themeDark {
		t.Fatalf("dark background theme=%v ok=%v", found, ok)
	}
	if _, ok, rest := parseBackground([]byte("\x1b[?1;2c")); ok || len(rest) != 0 {
		t.Fatal("a terminal without a background reply was read as one")
	}
}

// The generated width tables are sorted and disjoint, which runeWidth's
// binary search needs, and give the expected widths for known characters.
func TestWidthTablesAreSortedAndDisjoint(t *testing.T) {
	for name, ranges := range map[string][]runeRange{"wide": wideRanges, "combining": combiningRanges} {
		for i, r := range ranges {
			if r.lo > r.hi || (i > 0 && r.lo <= ranges[i-1].hi) {
				t.Fatalf("%s table out of order at %d: %X", name, i, r)
			}
		}
	}
	for r, want := range map[rune]int{'a': 1, '가': 2, '\u0301': 0, '中': 2, '\uFF01': 2, '\uFE0F': 1, 0x1F600: 2, 0x1F5C2: 1} {
		if got := runeWidth(r); got != want {
			t.Errorf("width of %U = %d, want %d", r, got, want)
		}
	}
}
