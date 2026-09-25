package markdown

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// testBudget is the time an ordinary document may take in these tests. The
// race detector slows goldmark about tenfold, and budgetScale allows for it.
const testBudget = time.Second * budgetScale

// timedConvert renders source in this process, as a child would, and fails
// the test when it uses more than budget of processor time. Processor time,
// unlike the wall clock, does not grow when other programs share the
// machine, and tests in this package do not run in parallel.
func timedConvert(t *testing.T, name, source string, budget time.Duration) (string, error) {
	t.Helper()
	start, wall := processCPU(), time.Now()
	out, err := convert([]byte(source), testResolver(""))
	used := processCPU() - start
	t.Logf("%s: %d bytes, estimate %d, %v processor time, %v wall", name, len(source), estimateCost([]byte(source)), used, time.Since(wall))
	if used > budget {
		t.Errorf("%s: rendering %d bytes used %v of processor time, over %v", name, len(source), used, budget)
	}
	return out, err
}

// alternate interleaves lines of a and b up to MaxSource.
func alternate(a, b string) string { return fillTo(a+"\n"+b, "\n") }

// capped cuts s to MaxSource.
func capped(s string) string {
	if len(s) > MaxSource {
		return s[:MaxSource]
	}
	return s
}

// The shapes that made goldmark slow are refused before rendering, including
// the reviewer's round 2 inputs, which hid one paragraph from a line scan
// that followed the grammar: lines that look like fences or empty list items
// but do not end a paragraph.
func TestSlowShapesAreRefused(t *testing.T) {
	emphasis := strings.Repeat("*a_", 497)
	cases := map[string]string{
		"nested quotes":                   strings.Repeat(">", MaxSource),
		"nested quotes after tabs":        fillTo("\t"+strings.Repeat("> ", 40), "a\n"),
		"nested lists and quotes":         strings.Repeat("- > ", MaxSource/4),
		"link openers on one line":        "x " + strings.Repeat("[a](", MaxSource/4-1),
		"images on one line":              "x " + strings.Repeat("![a](", MaxSource/5-1),
		"emphasis in one paragraph":       wrap(strings.Repeat("*a_", MaxSource/4), 80),
		"strikethrough and emphasis":      wrap(strings.Repeat("~a*", MaxSource/4), 80),
		"processing instructions":         "x\n" + fillTo("x <?", "\n"),
		"declarations":                    "x\n" + fillTo("x <!X", "\n"),
		"unclosed comments":               "x\n" + fillTo("x <!--", "\n"),
		"indented tilde fences (R2)":      alternate("    ~~~", emphasis),
		"tab-indented fences (R2)":        alternate("\t~~~", emphasis),
		"indented backtick fences (R2)":   alternate("    ```", emphasis),
		"empty star items (R2)":           "x\n" + alternate("* ", emphasis),
		"empty plus items (R2)":           "x\n" + alternate("+ ", emphasis),
		"quote markers only":              alternate(">", "> "+emphasis),
		"HTML after an open comment":      "<!--\n\n<table>\n-->\n" + fillTo(emphasis, "\n"),
		"HTML inside a closed fence":      "```\n<table>\n```\n" + fillTo(emphasis, "\n"),
		"uppercase tag, not skipped":      "<TABLE>\n" + fillTo(emphasis, "\n"),
		"indented tag, not skipped":       " <table>\n" + fillTo(emphasis, "\n"),
		"growing fence lengths (R2)":      growingFences(emphasis),
		"emphasis after a fence ends":     "- a\n  ```\n" + fillTo(emphasis, "\n"),
		"emphasis in a block quote":       fillTo("> "+emphasis, "\n"),
		"emphasis after a paragraph item": "x\n" + fillTo("2. "+emphasis, "\n"),
		// Round 3 review.
		"padded table (R3)":             tableRows(4000, 2000),
		"padded narrow table (R3)":      tableRows(100, 3000),
		"unclosed backtick runs (R3)":   backtickOpeners(48<<10) + fillTo("a", "\n"),
		"escaped pipes in tables (R3)":  fillTo("|a|\n|-|\n|`\\|`|\n|`\\|`|", "\n\n"),
		"escaped pipes in a table (R3)": "|a|\n|-|\n" + fillTo("|`\\|`|", "\n"),
	}
	starts := childStarts.Load()
	for name, source := range cases {
		source = capped(source)
		start := processCPU()
		_, err := Render(context.Background(), []byte(source), Links{})
		if !errors.Is(err, ErrTooComplex) {
			t.Errorf("%s: want ErrTooComplex, got %v", name, err)
		}
		if used := processCPU() - start; used > 50*time.Millisecond*budgetScale {
			t.Errorf("%s: refusing used %v of processor time", name, used)
		}
	}
	if _, err := Render(context.Background(), []byte(strings.Repeat("# a\n", MaxSource/4+1)), Links{}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a source above MaxSource: want ErrTooLarge, got %v", err)
	}
	if started := childStarts.Load() - starts; started != 0 {
		t.Errorf("refusing obvious shapes started %d child processes", started)
	}
}

// backtickOpeners writes backtick runs of growing length, none closed.
func backtickOpeners(size int) string {
	var b strings.Builder
	for i := 1; b.Len()+i+1 <= size; i++ {
		b.WriteString("e" + strings.Repeat("`", i))
	}
	return b.String() + "\n"
}

func growingFences(line string) string {
	var b strings.Builder
	for i := 3; ; i++ {
		fence := "    " + strings.Repeat("`", i%40+3)
		if b.Len()+len(fence)+len(line)+2 > MaxSource {
			return b.String()
		}
		b.WriteString(fence + "\n" + line + "\n")
	}
}

// Each costly shape, cut to the largest prefix inside the estimate's budget,
// renders inside the time a child is allowed. It is measured in processor
// time, about 0.5 s on this desktop, an eighth of renderBudget, so the check
// holds on a machine busy with other tests and on a slower one.
// Containment does not depend on it.
func TestShapesAtTheBudgetRenderInTime(t *testing.T) {
	if budgetScale > 1 {
		t.Skip("timing against renderBudget means nothing under the race detector")
	}
	emphasis := strings.Repeat("*a_", 497)
	cases := map[string]string{
		"emphasis paragraph":         wrap(strings.Repeat("*a_", MaxSource/3), 80),
		"emphasis paragraphs of 4K":  fillTo(wrap(strings.Repeat("*a_", 1365), 80), "\n\n"),
		"mixed delimiters":           wrap(strings.Repeat("***a___ ", MaxSource/8), 80),
		"closers among snake_case":   wrap(strings.Repeat(strings.Repeat("a_b_c_d ", 10)+"x_ ", MaxSource/83), 80),
		"emphasis among snake_case":  wrap(strings.Repeat(strings.Repeat("a_b_c_d ", 10)+"*x* ", MaxSource/84), 80),
		"closers after snake_case":   closersAfterSnakeCase(),
		"link openers on long lines": wrap("x "+strings.Repeat("[a](", MaxSource/4-1), 4<<10),
		"images on long lines":       wrap("x "+strings.Repeat("![a](", MaxSource/5-1), 4<<10),
		"processing instructions":    "x\n" + fillTo("x <?", "\n"),
		"declarations":               "x\n" + fillTo("x <!X", "\n"),
		"comments":                   "x\n" + fillTo("x <!--", "\n"),
		"CDATA":                      "x\n" + fillTo("x <![CDATA[", "\n"),
		"empty items (R2)":           "x\n" + alternate("* ", emphasis),
		"indented fences (R2)":       alternate("    ~~~", emphasis),
		"repeated headings":          fillTo("# a", "\n"),
		"nested quotes":              fillTo(strings.Repeat(">", maxNesting)+"a", "\n"),
		"nested lists and quotes":    fillTo(strings.Repeat("- > ", maxNesting/2)+"a", "\n"),
		"definitions and uses":       definitionsAndUses(),
	}
	for name, source := range cases {
		source = largestAccepted(source)
		if len(source) < 1024 {
			t.Errorf("%s: only %d bytes are accepted", name, len(source))
			continue
		}
		out, err := timedConvert(t, name, source, renderBudget)
		// A result too large for a page is discarded, which is also a quick
		// fallback to the source.
		if err != nil && !errors.Is(err, ErrTooComplex) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "repeated headings" && (err != nil || !strings.Contains(out, `id="md-a-65535"`)) {
			t.Errorf("repeated headings did not get numbered anchors: %v", err)
		}
	}
}

// closersAfterSnakeCase is a long paragraph of snake_case words followed by
// as many closers as the estimate admits. Each closer walks back over every
// delimiter, so the walk is as long as the estimate allows and the list is
// far larger than a processor cache.
func closersAfterSnakeCase() string {
	head := wrap(strings.Repeat("a_b_c_d ", (MaxSource-8<<10)/8), 80) + "\n"
	source := func(closers int) string { return head + wrap(strings.Repeat("x_ ", closers), 80) + "\n" }
	low, high := 0, 8<<10/3
	for low < high {
		middle := (low + high + 1) / 2
		if estimateCost([]byte(source(middle))) <= maxCost {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return source(low)
}

func definitionsAndUses() string {
	var b strings.Builder
	for i := 0; b.Len() < MaxSource/2; i++ {
		fmt.Fprintf(&b, "[d%d]: https://example.test/%d \"Title %d\"\n", i, i, i)
	}
	b.WriteString("\n")
	for i := 0; b.Len() < MaxSource-64; i++ {
		fmt.Fprintf(&b, "See [d%d] and [text][d%d].\n", i, i)
	}
	return b.String()
}

// Documents written by people, including the shapes the round 2 check
// refused, render.
func TestCommonDocumentsRender(t *testing.T) {
	for name, source := range map[string]string{
		"contributors table":      contributorsReadme(300),
		"changelog with 700 refs": changelog(700),
		"long reference table":    referenceTable(1200),
		"option table":            optionTable(400),
		"ordered list":            orderedList(600),
		"badge line":              badgeLine(80),
		"code-heavy guide":        codeGuide(),
		"ordinary README":         ordinaryReadme(),
	} {
		if len(source) > MaxSource {
			t.Fatalf("%s: fixture is %d bytes", name, len(source))
		}
		out, err := timedConvert(t, name, source, testBudget)
		if err != nil || !strings.Contains(out, "<h1") {
			t.Errorf("%s (%d bytes) was not rendered: %v", name, len(source), err)
		}
	}
}

func contributorsReadme(people int) string {
	var b strings.Builder
	b.WriteString("# Project\n\n[![npm](https://img.shields.io/npm/v/x.svg)](https://npm.im/x) [![All Contributors](https://img.shields.io/badge/all_contributors-300-orange.svg)](#contributors-)\n\n")
	b.WriteString("## Contributors ✨\n\nThanks goes to these wonderful people ([emoji key](https://allcontributors.org/docs/en/emoji-key)):\n\n")
	b.WriteString("<!-- ALL-CONTRIBUTORS-LIST:START - Do not remove or modify this section -->\n<!-- prettier-ignore-start -->\n<!-- markdownlint-disable -->\n<table>\n  <tbody>\n")
	for i := 0; i < people; i++ {
		if i%7 == 0 {
			b.WriteString("    <tr>\n")
		}
		fmt.Fprintf(&b, "      <td align=\"center\" valign=\"top\" width=\"14.28%%\"><a href=\"https://example.test/u_%d\"><img src=\"https://example.test/u/%d?v=4?s=100\" width=\"100px;\" alt=\"User_%d\"/><br /><sub><b>User_%d</b></sub></a><br /><a href=\"https://example.test/o/r/commits?author=u_%d\" title=\"Code\">💻</a> <a href=\"#ideas-u_%d\" title=\"Ideas\">🤔</a></td>\n", i, i, i, i, i, i)
		if i%7 == 6 {
			b.WriteString("    </tr>\n")
		}
	}
	b.WriteString("  </tbody>\n</table>\n\n<!-- markdownlint-restore -->\n<!-- prettier-ignore-end -->\n\n<!-- ALL-CONTRIBUTORS-LIST:END -->\n")
	return b.String()
}

func changelog(definitions int) string {
	var b strings.Builder
	b.WriteString("# Changes\n\n")
	for v := definitions; v > 0; v-- {
		if v > definitions-150 {
			fmt.Fprintf(&b, "## Version 11.%d.0\n\n- fix(`lang_%d`) handle `snake_case` names ([#%d][]) [@user_%d][]\n- enh(*core*) faster _matching_ ([#%d][])\n\n", v, v, 3000+v, v, 4000+v)
		}
	}
	for v := 0; v < definitions; v++ {
		fmt.Fprintf(&b, "[#%d]: https://example.test/pull/%d\n", 3000+v, 3000+v)
	}
	return b.String()
}

func referenceTable(rows int) string {
	var b strings.Builder
	b.WriteString("# Property information\n\n| Property | Attribute | Space | Kind |\n| --- | --- | --- | --- |\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "| `aria_prop_%d` | `aria-prop-%d` | `html` | [`booleanish`](#booleanish) |\n", i, i)
	}
	return b.String()
}

func optionTable(rows int) string {
	var b strings.Builder
	b.WriteString("# Options\n\n| Option | Default | Description |\n| --- | --- | --- |\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "| `max_retry_count_%d` | `*` | Sets the *retry* limit for `job_%d`; see [docs](https://example.test/o_%d). |\n", i, i, i)
	}
	return b.String()
}

func orderedList(items int) string {
	var b strings.Builder
	b.WriteString("# Steps\n\n")
	for i := 1; i <= items; i++ {
		fmt.Fprintf(&b, "%d. Run `setup_step_%d` with __care__ and read [step_%d](docs/step_%d.md)\n", i, i, i, i)
	}
	return b.String()
}

func badgeLine(badges int) string {
	var b strings.Builder
	b.WriteString("# Badges\n\n")
	for i := 0; i < badges; i++ {
		fmt.Fprintf(&b, "[![Badge_%d](https://img.shields.io/badge/b_%d-ok-green.svg?style=flat_square)](https://example.test/b_%d) ", i, i, i)
	}
	b.WriteString("\n")
	return b.String()
}

func codeGuide() string {
	var b strings.Builder
	b.WriteString("# Guide\n\nInstall and run:\n\n")
	for section := 0; b.Len() < 200<<10; section++ {
		fmt.Fprintf(&b, "## Part %d\n\n```python\n", section)
		for i := 0; i < 150; i++ {
			fmt.Fprintf(&b, "    def __init__(self, *args, **kw_%d): self._items[%d] = (args[0], kw_%d.get(\"a_b\"))  # <tag_%d>\n", i, i, i, i)
		}
		b.WriteString("```\n\n```html\n")
		for i := 0; i < 60; i++ {
			fmt.Fprintf(&b, "<div class=\"row_%d\"><a href=\"#x_%d\">[link_%d]</a> <em>*text*</em></div>\n", i, i, i)
		}
		b.WriteString("```\n\n")
	}
	return b.String()
}

func ordinaryReadme() string {
	var b strings.Builder
	b.WriteString("# Tool\n\n<p align=\"center\">\n  <img src=\"docs/logo.png\" width=\"200\" alt=\"logo\">\n</p>\n\n")
	for i := 0; b.Len() < 120<<10; i++ {
		fmt.Fprintf(&b, "## Section %d\n\nSome *emphasis*, some __strong__ text, a [link](https://example.test/%d) and `code_%d`.\nA second line with snake_case_name and <kbd>Ctrl</kbd>.\n\n- item with **bold**\n- item with [ref][r%d]\n  - nested `item`\n\n> Note: read the ~~old~~ new docs.\n\n```sh\nowngit serve --state-dir ~/state_%d\n```\n\n[r%d]: https://example.test/r/%d\n\n", i, i, i, i, i, i, i)
	}
	return b.String()
}

// Lines that the estimate leaves out as HTML blocks must be lines goldmark
// does not read for inline Markdown, or a document could hide its cost
// there. This checks that against goldmark's own parse, for every tag in
// htmlBlockTags after every kind of block, and for generated documents.
func TestHTMLBlocksAreSkippedAsGoldmarkDoes(t *testing.T) {
	contexts := []string{
		"", "para\n", "para\npara\n", "- item\n", "* item\n  more\n", "1. a\n   > b\n", "> quote\n", "> a\nlazy\n",
		"| a |\n| --- |\n| b |\n", "- a\n\n  para\n", "```\ncode\n```\n", "```\n", "~~~\n", "    code\n", "<!-- x -->\n",
		"<!--\n", "<?\n", "<!X\n", "<![CDATA[\n", "<script>\n", "<pre>\n", "# h\n", "[a]: /b\n", "[a]:\n", "a\n===\n",
		"<custom>\n", "- ```\n", "> ```\n", "<div>\n", "***\n",
	}
	forms := []string{"<%s>", "<%s class=\"x\">", "</%s>", "<%s/>", "<%s", "<%s>text</%s>"}
	checked := 0
	for tag := range htmlBlockTags {
		for _, form := range forms {
			line := strings.ReplaceAll(form, "%s", tag)
			for _, context := range contexts {
				source := context + line + "\nZZ *a_ [b](c) <!-- d\nZZ second\n\nafter *e*\n"
				checkSkippedLines(t, source)
				checked++
			}
		}
	}
	for seed := int64(0); seed < 300; seed++ {
		checkSkippedLines(t, generate(seed, 8<<10))
	}
	t.Logf("checked %d contexts and 300 generated documents", checked)
}

func checkSkippedLines(t *testing.T, source string) {
	t.Helper()
	var skipped [][2]int
	estimate([]byte(source), func(start, end int) { skipped = append(skipped, [2]int{start, end}) })
	if len(skipped) == 0 {
		return
	}
	inSkipped := func(offset int) bool {
		for _, r := range skipped {
			if offset >= r[0] && offset < r[1] {
				return true
			}
		}
		return false
	}
	document := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader([]byte(source)))
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.HTMLBlock, *ast.CodeBlock, *ast.FencedCodeBlock:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			if inSkipped(n.Segment.Start) {
				t.Fatalf("goldmark read skipped text %q as Markdown in:\n%s", n.Segment.Value([]byte(source)), source)
			}
		case *ast.RawHTML:
			for i := 0; i < n.Segments.Len(); i++ {
				if inSkipped(n.Segments.At(i).Start) {
					t.Fatalf("goldmark read skipped HTML as inline in:\n%s", source)
				}
			}
		}
		if node.Type() == ast.TypeBlock {
			lines := node.Lines()
			for i := 0; i < lines.Len(); i++ {
				if inSkipped(lines.At(i).Start) {
					t.Fatalf("goldmark kept a skipped line in a %s in:\n%s", node.Kind(), source)
				}
			}
		}
		return ast.WalkContinue, nil
	})
}

// Generated documents at the edge of the budget render within it. The
// generator mixes the line kinds that decide paragraph boundaries with
// costly inline shapes; see generate. go test -fuzz=FuzzEstimate explores
// further.
func FuzzEstimate(f *testing.F) {
	seeds := int64(24)
	if budgetScale > 1 {
		seeds = 4
	}
	for seed := int64(0); seed < seeds; seed++ {
		f.Add(seed, 64<<10)
	}
	f.Add(int64(5406), 256<<10)
	f.Fuzz(func(t *testing.T, seed int64, size int) {
		if size <= 0 || size > MaxSource {
			return
		}
		source := largestAccepted(generate(seed, size))
		checkSkippedLines(t, source)
		start := processCPU()
		if _, err := convert([]byte(source), testResolver("")); err != nil && !errors.Is(err, ErrTooComplex) {
			t.Fatal(err)
		}
		if used := processCPU() - start; used > renderBudget*budgetScale {
			t.Fatalf("seed %d size %d: an accepted source of %d bytes used %v of processor time", seed, size, len(source), used)
		}
	})
}

func TestRefusedSourcesAreBounded(t *testing.T) {
	set := &refusedSet{seen: map[[sha256.Size]byte]bool{}}
	key := func(i int) [sha256.Size]byte { return sha256.Sum256([]byte(fmt.Sprint(i))) }
	for i := 0; i < refusedSources+10; i++ {
		set.add(key(i))
	}
	if len(set.seen) != refusedSources || len(set.order) != refusedSources {
		t.Fatalf("kept %d sources", len(set.seen))
	}
	if set.has(key(0)) || set.has(key(9)) || !set.has(key(10)) || !set.has(key(refusedSources+9)) {
		t.Fatal("the oldest source was not the one forgotten")
	}
}

// When every render slot is taken, a request waits no longer than its
// context allows and then gets ErrBusy, so the page falls back to source.
func TestBusyRendererFallsBack(t *testing.T) {
	for i := 0; i < cap(slots); i++ {
		slots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(slots); i++ {
			<-slots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Render(ctx, []byte("# waiting for a slot\n"), Links{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > renderWait {
		t.Fatalf("waited %v for a slot", elapsed)
	}
}
