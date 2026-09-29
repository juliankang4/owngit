// Command doclinks checks the links between the repository's documents.
//
// It reads every Markdown file in the repository, meaning every file Git
// tracks or would track, and checks each relative link and image, written in
// Markdown or in HTML: the target must be a file or directory of the
// repository, and a fragment pointing into a Markdown file must name one of
// its headings, by the anchors GitHub gives them, or an HTML id or name.
// Links in Go source to a document with a fragment, written as a repository
// path such as docs/OPERATIONS.md#run-as-a-service or as this repository's
// GitHub address, are checked the same way. Test files are left out, because
// their links are test data. Links to other sites are not followed.
//
// Usage, from anywhere in the repository:
//
//	go run ./tools/doclinks
//
// It prints one line per broken link and exits with status 1 if there is any.
package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"owngit/internal/markdown"
)

func main() {
	problems, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "doclinks:", err)
		os.Exit(2)
	}
	for _, problem := range problems {
		fmt.Println(problem)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "%d broken link(s)\n", len(problems))
		os.Exit(1)
	}
}

func run() ([]string, error) {
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, fmt.Errorf("finding the repository: %w", err)
	}
	root := strings.TrimSpace(string(top))
	list := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	list.Dir = root
	listed, err := list.Output()
	if err != nil {
		return nil, fmt.Errorf("listing the repository's files: %w", err)
	}
	var files []string
	for _, name := range strings.Split(string(listed), "\x00") {
		if name != "" {
			files = append(files, name)
		}
	}
	return newChecker(root, files).check()
}

// checker holds the repository's files, by slash-separated path relative to
// root, and the Markdown files it has parsed.
type checker struct {
	root      string
	files     map[string]bool
	dirs      map[string]bool
	documents map[string]*document
}

func newChecker(root string, files []string) *checker {
	c := &checker{root: root, files: map[string]bool{}, dirs: map[string]bool{".": true}, documents: map[string]*document{}}
	for _, name := range files {
		c.files[name] = true
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			c.dirs[dir] = true
		}
	}
	return c
}

func (c *checker) check() ([]string, error) {
	names := make([]string, 0, len(c.files))
	for name := range c.files {
		names = append(names, name)
	}
	sort.Strings(names)
	var problems []string
	for _, name := range names {
		var found []link
		switch {
		case isMarkdown(name):
			document, err := c.document(name)
			if err != nil {
				return nil, err
			}
			found = document.links
		case strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go"):
			source, err := c.read(name)
			if err != nil {
				return nil, err
			}
			found = sourceLinks(source)
		default:
			continue
		}
		for _, l := range found {
			problem, err := c.resolve(name, l.destination)
			if err != nil {
				return nil, err
			}
			if problem != "" {
				problems = append(problems, fmt.Sprintf("%s:%d: %s: %s", name, l.line, l.destination, problem))
			}
		}
	}
	return problems, nil
}

// resolve checks one destination written in the file from. It returns what is
// wrong with it, or "" if nothing is.
func (c *checker) resolve(from, destination string) (string, error) {
	if destination == "" || strings.HasPrefix(destination, "//") {
		return "", nil
	}
	if parsed, err := url.Parse(destination); err == nil && parsed.Scheme != "" {
		return "", nil
	}
	target, fragment, _ := strings.Cut(destination, "#")
	target, _, _ = strings.Cut(target, "?")
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}
	switch {
	case target == "":
		target = from
	case strings.HasPrefix(target, "/"):
		// GitHub resolves a path starting with "/" from the repository root.
		target = path.Clean(strings.TrimPrefix(target, "/"))
	default:
		target = path.Join(path.Dir(from), target)
	}
	if target == ".." || strings.HasPrefix(target, "../") {
		return "leads outside the repository", nil
	}
	if !c.files[target] && !c.dirs[target] {
		return "no such file or directory in the repository", nil
	}
	if fragment == "" || !c.files[target] || !isMarkdown(target) {
		return "", nil
	}
	document, err := c.document(target)
	if err != nil {
		return "", err
	}
	if !document.anchors[markdown.FragmentAnchor(fragment)] {
		return fmt.Sprintf("%s has no heading or id %q", target, fragment), nil
	}
	return "", nil
}

func isMarkdown(name string) bool { return strings.HasSuffix(name, ".md") }

func (c *checker) read(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(c.root, filepath.FromSlash(name)))
}

type link struct {
	line        int
	destination string
}

type document struct {
	links   []link
	anchors map[string]bool
}

// document reads and parses a Markdown file once.
func (c *checker) document(name string) (*document, error) {
	if parsed, ok := c.documents[name]; ok {
		return parsed, nil
	}
	source, err := c.read(name)
	if err != nil {
		return nil, err
	}
	parsed := parseMarkdown(source)
	c.documents[name] = parsed
	return parsed, nil
}

var (
	markdownParser = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	).Parser()
	htmlLink = regexp.MustCompile(`(?i)\b(?:href|src)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	htmlID   = regexp.MustCompile(`(?i)\b(?:id|name)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	// A document with a fragment, as a repository path or as this
	// repository's GitHub address.
	sourceLink = regexp.MustCompile(`(?:https://github\.com/juliankang4/owngit/blob/[^/\s"'` + "`" + `]+/)?([\w./-]+\.md#[\w%\p{L}\p{M}\p{N}-]+)`)
)

// parseMarkdown finds the links and anchors of a Markdown file the way GitHub
// renders it. Code is not parsed for links.
func parseMarkdown(source []byte) *document {
	context := parser.NewContext(parser.WithIDs(markdown.NewHeadingIDs("")))
	root := markdownParser.Parse(text.NewReader(source), parser.WithContext(context))
	lines := newLineIndex(source)
	d := &document{anchors: map[string]bool{}}
	html := func(offset int, raw []byte) {
		for _, match := range htmlLink.FindAllSubmatch(raw, -1) {
			d.links = append(d.links, link{lines.line(offset), string(match[1]) + string(match[2])})
		}
		for _, match := range htmlID.FindAllSubmatch(raw, -1) {
			d.anchors[strings.ToLower(string(match[1])+string(match[2]))] = true
		}
	}
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Heading:
			if id, ok := n.AttributeString("id"); ok {
				d.anchors[string(id.([]byte))] = true
			}
		case *ast.Link:
			d.links = append(d.links, link{lines.line(position(n)), string(n.Destination)})
		case *ast.Image:
			d.links = append(d.links, link{lines.line(position(n)), string(n.Destination)})
		case *ast.RawHTML:
			for i := 0; i < n.Segments.Len(); i++ {
				segment := n.Segments.At(i)
				html(segment.Start, segment.Value(source))
			}
		case *ast.HTMLBlock:
			for i := 0; i < n.Lines().Len(); i++ {
				segment := n.Lines().At(i)
				html(segment.Start, segment.Value(source))
			}
		}
		return ast.WalkContinue, nil
	})
	return d
}

// position gives the offset of a link's text, or of the block holding it.
func position(node ast.Node) int {
	for n := node; n != nil; n = n.FirstChild() {
		if t, ok := n.(*ast.Text); ok {
			return t.Segment.Start
		}
	}
	for n := node.Parent(); n != nil; n = n.Parent() {
		if n.Type() == ast.TypeBlock && n.Lines().Len() > 0 {
			return n.Lines().At(0).Start
		}
	}
	return 0
}

// sourceLinks finds the links to documents with a fragment in Go source.
// Their paths start at the repository root, so each is given as a path
// starting with "/".
func sourceLinks(source []byte) []link {
	lines := newLineIndex(source)
	var links []link
	for _, match := range sourceLink.FindAllSubmatchIndex(source, -1) {
		links = append(links, link{lines.line(match[0]), "/" + string(source[match[2]:match[3]])})
	}
	return links
}

// lineIndex turns byte offsets into line numbers.
type lineIndex []int

func newLineIndex(source []byte) lineIndex {
	starts := lineIndex{0}
	for i, b := range source {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func (l lineIndex) line(offset int) int {
	return sort.SearchInts(l, offset+1)
}
