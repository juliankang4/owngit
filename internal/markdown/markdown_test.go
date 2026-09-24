package markdown

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

var addressAttr = regexp.MustCompile(`(?:href|src)="([^"]*)"`)

func testResolver(dir string) Resolver {
	return RepositoryResolver(dir,
		func(p string) string { return "/repositories/r1/code?path=" + p },
		func(p string) string { return "/repositories/r1/raw?path=" + p },
	)
}

func render(t *testing.T, dir, source string) string {
	t.Helper()
	out, err := convert([]byte(source), testResolver(dir))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	return out
}

func TestRawHTMLIsNeverPassedThrough(t *testing.T) {
	cases := []string{
		"<script>alert(1)</script>",
		"text <script>alert(1)</script> text",
		"<img src=x onerror=alert(1)>",
		"<iframe src=\"https://example.test\"></iframe>",
		"<div onclick=\"alert(1)\">block</div>",
		"<a href=\"javascript:alert(1)\">x</a>",
		"<style>body{display:none}</style>",
	}
	for _, source := range cases {
		out := render(t, "", source)
		lower := strings.ToLower(out)
		for _, bad := range []string{"<script", "<img", "<iframe", "onclick", "onerror", "javascript:", "<style", "<div"} {
			if strings.Contains(lower, bad) {
				t.Errorf("source %q rendered %q, which contains %q", source, out, bad)
			}
		}
	}
}

func TestUnsafeLinkSchemesAreDropped(t *testing.T) {
	cases := map[string]string{
		"[x](javascript:alert(1))":                  "javascript",
		"[x](JaVaScRiPt:alert(1))":                  "javascript",
		"[x](vbscript:msgbox)":                      "vbscript",
		"[x](data:text/html;base64,PHNjcmlwdD4=)":   "data:",
		"![x](data:image/png;base64,iVBORw0KGgo=)":  "data:",
		"[x](file:///etc/passwd)":                   "file:",
		"<javascript:alert(1)>":                     "javascript",
		"[x](  javascript:alert(1)  )":              "javascript",
		"[x](java\tscript:alert(1))":                "script:",
		"![x](mailto:someone@example.test)":         "mailto",
		"[x](//evil.example.test/path)":             "evil",
		"[x](ftp://files.example.test/archive.zip)": "ftp",
		// Character references are decoded before the scheme is judged.
		"[x](javascript&#58;alert(1))":             "javascript",
		"[x](&#106;avascript:alert(1))":            "javascript",
		"[x](java&#x73;cript:alert(1))":            "javascript",
		"[x](javascript&colon;alert(1))":           "javascript",
		"[x](data&colon;text/html,x)":              "data",
		"[x][r]\n\n[r]: javascript:alert(1)":       "javascript",
		"[x][r]\n\n[r]: &#106;avascript:alert(1)":  "javascript",
		"![x][r]\n\n[r]: data:image/png;base64,AA": "data",
	}
	for source, bad := range cases {
		out := render(t, "", source)
		for _, attr := range addressAttr.FindAllStringSubmatch(out, -1) {
			if strings.Contains(strings.ToLower(attr[1]), bad) {
				t.Errorf("source %q rendered an address %q", source, attr[1])
			}
		}
		if strings.Contains(source, "[x]") && !strings.Contains(out, "x") {
			t.Errorf("source %q lost its text: %q", source, out)
		}
	}
}

func TestExternalLinksAreMarked(t *testing.T) {
	out := render(t, "", "[site](https://example.test/a) and https://example.test/bare and [mail](mailto:a@example.test)")
	if got := strings.Count(out, `rel="noopener noreferrer"`); got != 3 {
		t.Fatalf("want 3 external links marked, got %d in %q", got, out)
	}
	if !strings.Contains(out, `href="https://example.test/a"`) || !strings.Contains(out, `href="https://example.test/bare"`) {
		t.Fatalf("external addresses missing: %q", out)
	}
}

func TestRelativeLinksStayInTheRepository(t *testing.T) {
	out := render(t, "docs/guide", "[a](setup.md) [b](../README.md) [c](/cmd/main.go) [d](./img/x.png#frag) [e](../../../../etc/passwd) [f](#Quick-Start) [g](../README.md#Install)")
	for _, want := range []string{
		`href="/repositories/r1/code?path=docs/guide/setup.md"`,
		`href="/repositories/r1/code?path=docs/README.md"`,
		`href="/repositories/r1/code?path=cmd/main.go"`,
		// A fragment is kept only where the target renders headings.
		`href="/repositories/r1/code?path=docs/guide/img/x.png"`,
		`href="#md-quick-start"`,
		`href="/repositories/r1/code?path=docs/README.md#md-install"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %q", want, out)
		}
	}
	if strings.Contains(out, "passwd") && strings.Contains(out, "path=etc") {
		t.Errorf("a path above the repository root became a link: %q", out)
	}
	if !strings.Contains(out, ">e<") && !strings.Contains(out, " e ") {
		t.Errorf("a refused link lost its text: %q", out)
	}
}

func TestImagePaths(t *testing.T) {
	out := render(t, "docs", "![diagram](assets/d.png) ![up](../logo.svg) ![out](../../x.png) ![badge](https://img.example.test/b.svg)")
	for _, want := range []string{
		`src="/repositories/r1/raw?path=docs/assets/d.png"`,
		`src="/repositories/r1/raw?path=logo.svg"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %q", want, out)
		}
	}
	if strings.Contains(out, "img.example.test/b.svg\"") && strings.Contains(out, "<img src=\"https://") {
		t.Errorf("an outside image is loaded by the page: %q", out)
	}
	if !strings.Contains(out, `<a href="https://img.example.test/b.svg" rel="noopener noreferrer">badge</a>`) {
		t.Errorf("an outside image should become a link: %q", out)
	}
	if strings.Contains(out, "path=x.png") || strings.Contains(out, "../../x.png") {
		t.Errorf("an image above the root was kept: %q", out)
	}
}

func TestHeadingAnchorsFollowGitHub(t *testing.T) {
	out := render(t, "", "# main\n\n## 설치 안내\n\n## main\n\n## Setup / Install\n\n## Install & Run\n\n## my_func()\n\n## Quick Start!\n\n## a-1\n\n## a\n\n## a\n\n## 🚀 Deploy\n")
	for _, want := range []string{
		`id="md-main"`, `id="md-설치-안내"`, `id="md-main-1"`,
		`id="md-setup--install"`, `id="md-install--run"`, `id="md-my_func"`, `id="md-quick-start"`,
		// GitHub skips a suffix that another heading already took.
		`id="md-a-1"`, `id="md-a"`, `id="md-a-2"`,
		`id="md--deploy"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %q", want, out)
		}
	}
	if strings.Contains(out, `id="main"`) {
		t.Errorf("a heading took a page id: %q", out)
	}
}

// A table of contents written for GitHub jumps to the headings, whatever the
// case of its fragments.
func TestFragmentLinksReachTheirHeadings(t *testing.T) {
	out := render(t, "", "[a](#Setup--Install) [b](#install--run) [c](#%EC%84%A4%EC%B9%98-%EC%95%88%EB%82%B4) [d](#설치-안내)\n\n## Setup / Install\n\n## Install & Run\n\n## 설치 안내\n")
	for _, want := range []string{
		`href="#md-setup--install"`, `id="md-setup--install"`,
		`href="#md-install--run"`, `id="md-install--run"`,
		`href="#md-%EC%84%A4%EC%B9%98-%EC%95%88%EB%82%B4"`, `id="md-설치-안내"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %q", want, out)
		}
	}
}

func TestMailAddressesAreLinks(t *testing.T) {
	out := render(t, "", "Write to <someone@example.test> or other@example.test.")
	for _, want := range []string{`href="mailto:someone@example.test"`, `href="mailto:other@example.test"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %q", want, out)
		}
	}
}

func TestCharacterReferencesInAddresses(t *testing.T) {
	out := render(t, "docs", "[a](a&amp;b.md) [b](c%20d.md) [c][r] [site](https://example.test/?q=1&amp;copy;=2)\n\n[r]: ../x&#46;md\n")
	for _, want := range []string{
		`href="/repositories/r1/code?path=docs/a&amp;b.md"`,
		`href="/repositories/r1/code?path=docs/c%20d.md"`,
		`href="/repositories/r1/code?path=x.md"`,
		// An address that decodes to "&copy;" keeps it, not a copyright sign.
		`href="https://example.test/?q=1&amp;copy;=2"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %q", want, out)
		}
	}
}

func TestTablesAndCode(t *testing.T) {
	out := render(t, "", "| a | b |\n| - | - |\n| 1 | 2 |\n\n```go\nfunc main() { fmt.Println(\"<b>\") }\n```\n")
	if !strings.Contains(out, "<table>") || !strings.Contains(out, "<pre><code") || !strings.Contains(out, "&lt;b&gt;") {
		t.Fatalf("unexpected rendering: %q", out)
	}
}

func TestTooLarge(t *testing.T) {
	if _, err := Render(context.Background(), make([]byte, MaxSource+1), Links{}); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}
