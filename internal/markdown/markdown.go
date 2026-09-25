// Package markdown turns a repository's Markdown file into HTML for the file
// view.
//
// Repository content is untrusted. The renderer therefore never passes raw
// HTML through: goldmark's HTML renderer runs without its unsafe option, so
// inline and block HTML are replaced by a comment and script cannot reach the
// page. Every link and image destination goes through a caller-supplied
// Resolver, which decides the final address. A destination the resolver
// refuses is not rendered as a link or an image at all; its text stays.
package markdown

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Rendering cost. Every render runs in a child process that is stopped by
// time, output size and memory; see child.go. That containment does not
// depend on predicting cost. Render still refuses, without starting a child,
// a source whose cost estimateCost already shows to be hopeless, so that such
// a file costs nothing to view. The estimate follows goldmark v1.8.6 as
// measured on an Apple M4 Pro:
//
//   - emphasis and strikethrough delimiters in one paragraph: each one that
//     can close emphasis looks back over the others ("*a_", or "x_" among
//     many snake_case words), up to about 3 ns per pair. The walk follows
//     one pointer per delimiter, so once the list outgrows the processor's
//     cache each step waits for memory: about 50 ns on a GitHub-hosted
//     macOS runner shared with other tests, and 100 to 130 ns here on a
//     loaded or efficiency core, for 50,000 to 100,000 delimiters. Closers
//     after all the other delimiters make every pair a step. Pairs in a
//     run with more than cachedDelimiters are charged 50;
//   - "<!" and "<?", which may open a comment, declaration, CDATA section or
//     processing instruction, scan to the end of their paragraph, about 2 ns
//     times their number times the paragraph size;
//   - "[", "(" and "<" on one long line ("[a](" runs) cost about 0.7 ns times
//     their number times the line length;
//   - a backtick run with no closer of its length scans the rest of its
//     paragraph, about 40 ns per line;
//   - a code span in a table cell looks at every escaped "|" in the document;
//   - a table pads every row to the header's width, about 240 bytes of memory
//     per cell, which counts against memory rather than time.
const (
	// MaxSource is the largest file Render accepts.
	MaxSource = 256 << 10
	// maxCost bounds estimateCost, in units of about a nanosecond of
	// rendering in the server process on the machine the costs were measured
	// on. In a child the same source takes about 1.5 times as long, so a
	// source at this edge takes about 0.8 s there: a fifth of renderBudget,
	// which leaves room for a processor several times slower.
	maxCost = 500_000_000
	// cachedDelimiters is the most emphasis delimiters in one run of lines
	// whose pairs are charged the cached rate. Their nodes take about 400
	// bytes each, about 3 MB for this many. On that runner 13,000
	// delimiters of "*a_" took about twice as long as here, as the other
	// shapes did, so the walk still hit the cache. A blank line ends a run,
	// so documents made of ordinary paragraphs are not affected. A long
	// list or paragraph without blank lines that passes this many "*", "_"
	// or "~" (underscores inside words count) is charged more and shows as
	// source from a smaller size than before.
	cachedDelimiters = 8192
	// maxTableCells bounds the table cells one run of lines can make, about
	// 50 MB of memory. Real tables have a few thousand.
	maxTableCells = 200_000
	// maxNesting bounds the block quote and list markers that open one line.
	maxNesting = 16
	// maxOutput bounds the HTML one document adds to a page. Nesting,
	// headings and reused links can multiply the size of a source.
	maxOutput = 2 << 20
)

// Concurrency and time. At most renderSlots children run at once in the
// process. A request waits at most renderWait for a slot and then shows the
// source instead. A child still running after renderBudget is killed.
const (
	renderSlots  = 2
	renderWait   = 2 * time.Second
	renderBudget = 4 * time.Second
	// refusedSources bounds how many refused sources are remembered, and how
	// many sources that ran out of time once are.
	refusedSources = 256
	// Rendered documents are kept, up to renderedEntries of them and
	// renderedBytes of HTML in all, so browsing does not start a child for
	// every view.
	renderedEntries = 256
	renderedBytes   = 32 << 20
)

var slots = make(chan struct{}, renderSlots)

var (
	// ErrTooLarge reports a source above MaxSource.
	ErrTooLarge = errors.New("markdown source too large to render")
	// ErrTooComplex reports a source whose estimated or actual time,
	// memory or output is too high.
	ErrTooComplex = errors.New("markdown source too complex to render")
	// ErrBusy reports that no render slot came free in time.
	ErrBusy = errors.New("markdown renderer busy")
	// ErrUnavailable reports that no child process could render the
	// source: none is configured with SetHelper, or it could not start.
	ErrUnavailable = errors.New("markdown renderer unavailable")
)

// Links says where the links of a rendered document point. It is sent to the
// child process, so it holds data rather than functions.
type Links struct {
	// Dir is the directory of the document in the repository, "" at the
	// root. Relative links are resolved against it.
	Dir string `json:"dir"`
	// File and Raw are address prefixes for a repository path: the
	// query-escaped path is appended to File for a link to a file and to Raw
	// for an image. An empty Raw drops relative images.
	File string `json:"file"`
	Raw  string `json:"raw"`
}

func (l Links) resolver() Resolver {
	var raw func(string) string
	if l.Raw != "" {
		raw = func(repoPath string) string { return l.Raw + url.QueryEscape(repoPath) }
	}
	return RepositoryResolver(l.Dir, func(repoPath string) string { return l.File + url.QueryEscape(repoPath) }, raw)
}

// Target is a resolved destination.
type Target struct {
	// URL is the address written into the page.
	URL string
	// External marks an address outside OwnGit. Such links get
	// rel="noopener noreferrer", and such images are shown as links, because
	// the page's content security policy does not load them.
	External bool
}

// Resolver maps one destination written in the file to the address the page
// should use. image is true for an image source. It returns false to drop the
// destination.
type Resolver func(destination string, image bool) (Target, bool)

// Render converts source to HTML in a child process. The result contains no
// raw HTML from source, and every link and image address was resolved with
// links.
//
// A source above MaxSource returns ErrTooLarge. A source estimated to be too
// costly, or that ran out of memory or output once, returns ErrTooComplex
// without a child; so does one that ran out of time using at least half of
// renderBudget in processor time, or twice. When no render slot comes free
// before ctx ends or renderWait passes, Render returns ErrBusy. When no child
// can run at all, Render logs the cause and returns ErrUnavailable, which is
// not remembered. A render that has started is not cancelled by ctx: it ends
// within renderBudget, and its result is kept for the next view.
func Render(ctx context.Context, source []byte, links Links) (string, error) {
	if len(source) > MaxSource {
		return "", ErrTooLarge
	}
	content := sha256.Sum256(source)
	key := renderKey(content, links)
	if html, ok := rendered.get(key); ok {
		return html, nil
	}
	if refused.has(content) || estimateCost(source) > maxCost {
		return "", ErrTooComplex
	}
	timer := time.NewTimer(renderWait)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return "", ErrBusy
	case <-timer.C:
		return "", ErrBusy
	}
	defer func() { <-slots }()
	// Another request may have rendered or refused it while this one waited.
	if html, ok := rendered.get(key); ok {
		return html, nil
	}
	if refused.has(content) {
		return "", ErrTooComplex
	}
	result := renderInChild(source, links)
	if result.err != nil {
		switch rememberFailure(result.documentFault, result.timedOut(), result.cpu(), killAfter, slow.has(content)) {
		case rememberNothing:
			if errors.Is(result.err, ErrUnavailable) {
				logUnavailable(result.reason)
			}
		case rememberSlow:
			slow.add(content)
		case rememberRefused:
			refused.add(content)
		}
		return "", result.err
	}
	rendered.put(key, result.html)
	return result.html, nil
}

// What Render remembers about a source whose child failed.
const (
	rememberNothing = iota // the server failed, not the document
	rememberSlow           // out of time on a busy machine: try once more
	rememberRefused        // the document failed: do not try again
)

// rememberFailure decides what a failed child says about its source.
// documentFault and timedOut come from the child's result, cpu is the
// processor time it used, limit the time it was allowed, and slowBefore
// reports that the source already ran out of time once on a busy machine.
//
// A child that ran out of time having used less than half of limit in
// processor time spent most of it waiting for a processor, so the machine
// was busy; such a source is refused only when it runs out of time again.
func rememberFailure(documentFault, timedOut bool, cpu, limit time.Duration, slowBefore bool) int {
	switch {
	case !documentFault:
		return rememberNothing
	case timedOut && cpu < limit/2 && !slowBefore:
		return rememberSlow
	default:
		return rememberRefused
	}
}

// renderKey names one rendering: the source and everything that changes its
// HTML. The page language does not.
func renderKey(content [sha256.Size]byte, links Links) [sha256.Size]byte {
	hash := sha256.New()
	hash.Write(content[:])
	for _, part := range []string{links.Dir, links.File, links.Raw} {
		fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	var key [sha256.Size]byte
	hash.Sum(key[:0])
	return key
}

// convert renders source in this process with no checks. Only a child
// process, and tests, call it.
func convert(source []byte, resolve Resolver) (string, error) {
	engine := goldmark.New(
		// GitHub Flavored Markdown: tables, strikethrough, task lists, and
		// bare web addresses as links.
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(&destinations{resolve: resolve}, 100)),
		),
		// No renderer options: in particular not html.WithUnsafe, so raw HTML
		// in the file is omitted rather than written into the page.
	)
	// Writing past the limit stops the render at once, so the HTML never
	// grows past it and no more time is spent on it.
	out := &cappedWriter{limit: maxOutput, exceeded: func() { panic(errOutputTooLarge) }}
	parseContext := parser.NewContext(parser.WithIDs(&headingIDs{used: map[string]int{}}))
	if err := convertInto(engine, source, out, parseContext); err != nil {
		return "", err
	}
	return out.String(), nil
}

func convertInto(engine goldmark.Markdown, source []byte, out io.Writer, parseContext parser.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if recovered != errOutputTooLarge {
				panic(recovered)
			}
			err = ErrTooComplex
		}
	}()
	return engine.Convert(source, out, parser.WithContext(parseContext))
}

// slow remembers sources that ran out of time once on a busy machine.
var slow = &refusedSet{seen: map[[sha256.Size]byte]bool{}}

// unavailableLog limits the log to one line per cause every
// unavailableLogEvery, however many views fail the same way.
var unavailableLog = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

const unavailableLogEvery = 10 * time.Minute

// logUnavailable records why no child could render. The line names the
// cause, never the document.
func logUnavailable(cause string) {
	unavailableLog.Lock()
	defer unavailableLog.Unlock()
	if last, ok := unavailableLog.last[cause]; ok && time.Since(last) < unavailableLogEvery {
		return
	}
	unavailableLog.last[cause] = time.Now()
	log.Printf("Markdown documents are shown as source: %s", cause)
}

// refused remembers sources that could not be rendered, by content hash, so
// the same file is not tried again while the process runs. Time, memory and
// output size depend on the source, not on the page that shows it.
var refused = &refusedSet{seen: map[[sha256.Size]byte]bool{}}

type refusedSet struct {
	mu    sync.Mutex
	seen  map[[sha256.Size]byte]bool
	order [][sha256.Size]byte
}

func (r *refusedSet) has(key [sha256.Size]byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[key]
}

func (r *refusedSet) add(key [sha256.Size]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[key] {
		return
	}
	if len(r.order) == refusedSources {
		delete(r.seen, r.order[0])
		r.order = r.order[1:]
	}
	r.seen[key] = true
	r.order = append(r.order, key)
}

// rendered keeps recent renderings, least recently used first out.
var rendered = &renderedSet{entries: map[[sha256.Size]byte]*list.Element{}, order: list.New()}

type renderedSet struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]*list.Element
	order   *list.List // of *renderedEntry, most recent at the front
	size    int
}

type renderedEntry struct {
	key  [sha256.Size]byte
	html string
}

func (r *renderedSet) get(key [sha256.Size]byte) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	element, ok := r.entries[key]
	if !ok {
		return "", false
	}
	r.order.MoveToFront(element)
	return element.Value.(*renderedEntry).html, true
}

func (r *renderedSet) put(key [sha256.Size]byte, html string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[key]; ok || len(html) > renderedBytes {
		return
	}
	r.entries[key] = r.order.PushFront(&renderedEntry{key: key, html: html})
	r.size += len(html)
	for r.order.Len() > renderedEntries || r.size > renderedBytes {
		oldest := r.order.Back()
		entry := oldest.Value.(*renderedEntry)
		r.order.Remove(oldest)
		delete(r.entries, entry.key)
		r.size -= len(entry.html)
	}
}

// estimateCost returns an upper estimate of the time goldmark needs for
// source, in the units of maxCost, or more than maxCost when a line opens
// more than maxNesting containers or a run of lines could make more than
// maxTableCells table cells.
//
// It does not parse Markdown, because a scan that follows the grammar almost
// but not exactly can be led to miss cost. It relies on one rule instead: a
// blank line (only spaces, tabs and line-end characters, as goldmark decides)
// always ends a paragraph, so every paragraph, heading and table cell lies
// inside one run of non-blank lines. Counting per run can only overcount.
// Fenced code is counted too. The one exception is a block of HTML, whose
// lines goldmark never reads for inline Markdown; see htmlBlockStart.
func estimateCost(source []byte) int64 {
	return estimate(source, nil)
}

// estimate is estimateCost. It calls skipped, when not nil, with the offsets
// of each line it leaves out as part of an HTML block.
func estimate(source []byte, skipped func(start, end int)) int64 {
	var total int64
	offset := 0
	// Counts for the current run of non-blank lines.
	var delimiters, closers, rawOpeners, linkOpeners, backtickRuns, size, lines, widest int64
	// Counts for the whole document.
	var allBacktickRuns, escapedPipes int64
	tooManyCells := false
	endRun := func() {
		pairCost := int64(3)
		if delimiters > cachedDelimiters {
			pairCost = 50
		}
		total += pairCost*delimiters*closers + 2*rawOpeners*size + linkOpeners*size/16 + 40*backtickRuns*lines
		// A table row has at most the header's cells, but a row with fewer
		// is padded to them, so every line may cost the widest line's cells.
		if (widest+1)*lines > maxTableCells {
			tooManyCells = true
		}
		delimiters, closers, rawOpeners, linkOpeners, backtickRuns, size, lines, widest = 0, 0, 0, 0, 0, 0, 0, 0
	}
	inHTML := false
	var raw rawHTML
	for len(source) > 0 && total <= maxCost && !tooManyCells {
		end := bytes.IndexByte(source, '\n')
		var line []byte
		if end < 0 {
			line, source = source, nil
		} else {
			line, source = source[:end], source[end+1:]
		}
		lineStart := offset
		offset += len(line) + 1
		if util.IsBlank(line) {
			endRun()
			inHTML = false
			continue
		}
		// A fence that goldmark had open when the HTML block seemed to start
		// may close here, and the lines after it would be read again.
		if inHTML && fenceLike(line) {
			inHTML = false
		}
		if !inHTML && !raw.open() && htmlBlockStart(line) {
			inHTML = true
		}
		raw.update(line)
		if inHTML {
			if skipped != nil {
				skipped(lineStart, lineStart+len(line))
			}
			continue
		}
		if nesting(line) > maxNesting {
			return maxCost + 1
		}
		var lineOpeners, pipes, lineBackticks int64
		for index, c := range line {
			switch c {
			case '|':
				pipes++
				if index > 0 && line[index-1] == '\\' {
					escapedPipes++
				}
			case '`':
				if index == 0 || line[index-1] != '`' {
					lineBackticks++
				}
			case '*', '_', '~':
				delimiters++
				// An underscore between two letters or digits can neither
				// open nor close emphasis; any other delimiter may close.
				if c != '_' || index == 0 || index+1 == len(line) || !isAlphanumeric(line[index-1]) || !isAlphanumeric(line[index+1]) {
					closers++
				}
			case '<':
				lineOpeners++
				// Comments, declarations, CDATA and processing instructions
				// are the raw HTML that scans on to the end of a paragraph.
				if index+1 < len(line) && (line[index+1] == '!' || line[index+1] == '?') {
					rawOpeners++
				}
			case '[', '(':
				linkOpeners++
				lineOpeners++
			}
		}
		total += lineOpeners * int64(len(line))
		size += int64(len(line)) + 1
		lines++
		widest = max(widest, pipes)
		backtickRuns += lineBackticks
		allBacktickRuns += lineBackticks
	}
	endRun()
	// Each code span in a table cell looks at every escaped pipe.
	total += allBacktickRuns * escapedPipes
	if tooManyCells {
		return maxCost + 1
	}
	return total
}

func isAlphanumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// htmlBlockTags are the tags that open an HTML block of goldmark's type 6,
// which may interrupt a paragraph and ends only at a blank line. The list is
// a subset of goldmark's; TestHTMLBlocksAreSkippedAsGoldmarkDoes checks each
// against goldmark itself.
var htmlBlockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"center": true, "dd": true, "details": true, "div": true, "dl": true,
	"dt": true, "figcaption": true, "figure": true, "footer": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"header": true, "hr": true, "li": true, "main": true, "nav": true,
	"ol": true, "p": true, "section": true, "summary": true, "table": true,
	"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true,
	"tr": true, "ul": true,
}

// htmlBlockStart reports a line that starts an HTML block of type 6 at the
// top level: it begins in the first column with "<" or "</" and one of
// htmlBlockTags, followed by a space, ">", "/>", or the end of the line.
// Such a line cannot continue a list item or a quote, and it interrupts a
// paragraph, so from it to the next blank line goldmark reads no inline
// Markdown, unless the line was inside fenced code or another kind of HTML
// block; estimateCost handles those cases.
func htmlBlockStart(line []byte) bool {
	if len(line) < 2 || line[0] != '<' {
		return false
	}
	rest := line[1:]
	if rest[0] == '/' {
		rest = rest[1:]
	}
	n := 0
	for n < len(rest) && (rest[n] >= 'a' && rest[n] <= 'z' || rest[n] >= '0' && rest[n] <= '9' && n > 0) {
		n++
	}
	if !htmlBlockTags[string(rest[:n])] {
		return false
	}
	rest = rest[n:]
	// goldmark accepts a carriage return only as part of the line end.
	return len(rest) == 0 || rest[0] == ' ' || rest[0] == '>' || string(rest) == "\r" || bytes.HasPrefix(rest, []byte("/>"))
}

// fenceLike reports a line that could open or close fenced code in any
// container: after spaces, tabs and container markers, three backticks or
// tildes.
func fenceLike(line []byte) bool {
	trimmed := bytes.TrimLeft(line, " \t>-*+0123456789.)")
	return bytes.HasPrefix(trimmed, []byte("```")) || bytes.HasPrefix(trimmed, []byte("~~~"))
}

// rawHTML tracks whether goldmark may be inside an HTML block of types 1 to
// 5 (script, comment, processing instruction, declaration, CDATA). Those
// span blank lines and end at a marker, and a line inside one that looks
// like a type 6 start is not one. The state is set by an opener anywhere in
// a line and cleared only by a line holding the matching end marker, which
// is where goldmark ends such a block at the latest.
type rawHTML [5]bool

var rawHTMLMarkers = [5]struct{ open, close []string }{
	{[]string{"<script", "<pre", "<style", "<textarea"}, []string{"</script>", "</pre>", "</style>", "</textarea>"}},
	{[]string{"<!--"}, []string{"-->"}},
	{[]string{"<?"}, []string{"?>"}},
	{nil, []string{">"}}, // opened by "<!" and a letter; see update
	{[]string{"<![cdata["}, []string{"]]>"}},
}

func (r *rawHTML) open() bool {
	return r[0] || r[1] || r[2] || r[3] || r[4]
}

func (r *rawHTML) update(line []byte) {
	if bytes.IndexByte(line, '<') < 0 && bytes.IndexByte(line, '>') < 0 {
		return
	}
	lower := bytes.ToLower(line)
	for kind, markers := range rawHTMLMarkers {
		closed := false
		for _, marker := range markers.close {
			if bytes.Contains(lower, []byte(marker)) {
				closed = true
			}
		}
		if closed {
			r[kind] = false
			continue
		}
		for _, marker := range markers.open {
			if bytes.Contains(lower, []byte(marker)) {
				r[kind] = true
			}
		}
		if kind == 3 {
			for index := bytes.Index(lower, []byte("<!")); index >= 0 && index+2 < len(lower); {
				if c := lower[index+2]; c >= 'a' && c <= 'z' {
					r[kind] = true
					break
				}
				next := bytes.Index(lower[index+2:], []byte("<!"))
				if next < 0 {
					break
				}
				index += 2 + next
			}
		}
	}
}

// nesting counts the block quote and list markers that open line, which is
// the depth of containers the line starts.
func nesting(line []byte) int {
	depth := 0
	for {
		spaces := 0
		for spaces < len(line) && (line[spaces] == ' ' || line[spaces] == '\t') {
			spaces++
		}
		line = line[spaces:]
		switch {
		case len(line) > 0 && line[0] == '>':
			line = line[1:]
		case len(line) > 1 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && (line[1] == ' ' || line[1] == '\t'):
			line = line[2:]
		default:
			digits := 0
			for digits < len(line) && digits < 10 && line[digits] >= '0' && line[digits] <= '9' {
				digits++
			}
			if digits == 0 || digits+1 >= len(line) || (line[digits] != '.' && line[digits] != ')') || (line[digits+1] != ' ' && line[digits+1] != '\t') {
				return depth
			}
			line = line[digits+2:]
		}
		depth++
	}
}

// headingIDs gives headings GitHub's anchors behind a prefix, so a README's
// table of contents written for GitHub still works, and a heading such as
// "main" can never take the id of an element of OwnGit's own page.
//
// used maps every anchor handed out to the last number tried for it as a
// base, as GitHub's slugger does. Each heading therefore costs time in its
// own length only, however many headings repeat.
type headingIDs struct {
	used map[string]int
}

// IDPrefix starts every heading anchor. A link written as "#setup" in the
// file is rewritten to "#" + IDPrefix + "setup".
const IDPrefix = "md-"

func (ids *headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := Slug(string(value))
	id := base
	if _, taken := ids.used[id]; taken {
		for {
			ids.used[base]++
			id = base + "-" + strconv.Itoa(ids.used[base])
			if _, taken := ids.used[id]; !taken {
				break
			}
		}
	}
	ids.used[id] = 0
	return []byte(IDPrefix + id)
}

func (ids *headingIDs) Put(value []byte) {
	id := strings.TrimPrefix(string(value), IDPrefix)
	if _, taken := ids.used[id]; !taken {
		ids.used[id] = 0
	}
}

// Slug is GitHub's heading anchor rule: lower case; letters (of any
// script), marks, digits, "-" and "_" are kept; each space becomes "-"; every
// other character is dropped. Runs of "-" are not merged, so "Setup / Install"
// becomes "setup--install", as on GitHub.
func Slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.M, r) || unicode.Is(unicode.Pc, r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fragmentID rewrites a link's fragment to the anchor Generate gives the
// heading it names. GitHub matches fragments without regard to case, so the
// fragment is lowered as the anchors are.
func fragmentID(fragment string) string {
	if decoded, err := url.PathUnescape(fragment); err == nil {
		fragment = decoded
	}
	return "#" + (&url.URL{Fragment: IDPrefix + strings.ToLower(fragment)}).EscapedFragment()
}

// destinations rewrites every link and image through the resolver.
type destinations struct {
	resolve Resolver
}

func (d *destinations) Transform(document *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	var links []*ast.Link
	var images []*ast.Image
	var autos []*ast.AutoLink
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Link:
			links = append(links, n)
		case *ast.Image:
			images = append(images, n)
		case *ast.AutoLink:
			autos = append(autos, n)
		}
		return ast.WalkContinue, nil
	})
	for _, link := range links {
		target, ok := d.resolve(decodeReferences(link.Destination), false)
		if !ok {
			unwrap(link)
			continue
		}
		link.Destination = destinationBytes(target.URL)
		if target.External {
			link.SetAttributeString("rel", []byte("noopener noreferrer"))
		}
	}
	for _, image := range images {
		target, ok := d.resolve(decodeReferences(image.Destination), true)
		if !ok {
			unwrap(image)
			continue
		}
		if target.External {
			// The page does not load outside images, so the picture becomes
			// a link to it that shows its description. Inside a link, such as
			// a badge, a link cannot hold another; the description stays and
			// the outer link keeps its target.
			if insideLink(image) {
				unwrap(image)
				continue
			}
			link := ast.NewLink()
			link.Destination = destinationBytes(target.URL)
			link.Title = image.Title
			link.SetAttributeString("rel", []byte("noopener noreferrer"))
			parent := image.Parent()
			for child := image.FirstChild(); child != nil; {
				next := child.NextSibling()
				link.AppendChild(link, child)
				child = next
			}
			parent.ReplaceChild(parent, image, link)
			continue
		}
		image.Destination = destinationBytes(target.URL)
	}
	for _, auto := range autos {
		// Bare addresses are http, https, or mail only; they still go through
		// the resolver so one rule decides every address. goldmark gives a
		// mail address without its scheme.
		label := auto.URL(source)
		destination := string(label)
		if auto.AutoLinkType == ast.AutoLinkEmail {
			destination = "mailto:" + destination
		}
		target, ok := d.resolve(destination, false)
		if !ok || !target.External || insideLink(auto) {
			unwrapAuto(auto, label)
			continue
		}
		link := ast.NewLink()
		link.Destination = destinationBytes(target.URL)
		link.SetAttributeString("rel", []byte("noopener noreferrer"))
		link.AppendChild(link, ast.NewString(auto.Label(source)))
		auto.Parent().ReplaceChild(auto.Parent(), auto, link)
	}
}

// insideLink reports whether node is in the text of a link.
func insideLink(node ast.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if _, ok := parent.(*ast.Link); ok {
			return true
		}
	}
	return false
}

// destinationBytes stores a resolved address in a node. The HTML renderer
// reads character references in a destination once more, so each "&" is
// written as "&amp;" and the page gets the address unchanged.
func destinationBytes(address string) []byte {
	return []byte(strings.ReplaceAll(address, "&", "&amp;"))
}

// decodeReferences resolves character references such as "&amp;" or
// "&#58;" in a destination. goldmark leaves them in the node and decodes them
// only when writing the page, so the resolver would otherwise judge a
// different address from the one the browser follows.
func decodeReferences(destination []byte) string {
	return string(util.ResolveEntityNames(util.ResolveNumericReferences(destination)))
}

// unwrap replaces a node with its children, keeping the text.
func unwrap(node ast.Node) {
	parent := node.Parent()
	if parent == nil {
		return
	}
	for child := node.FirstChild(); child != nil; {
		next := child.NextSibling()
		parent.InsertBefore(parent, node, child)
		child = next
	}
	parent.RemoveChild(parent, node)
}

func unwrapAuto(auto *ast.AutoLink, label []byte) {
	parent := auto.Parent()
	if parent == nil {
		return
	}
	parent.ReplaceChild(parent, auto, ast.NewString(label))
}

// RepositoryResolver builds the usual resolver for a file in a repository.
//
// dir is the directory of the Markdown file within the repository ("" at the
// root). fileURL and rawURL build the OwnGit addresses for a repository path;
// rawURL may be nil, in which case relative images are dropped.
//
// Web and mail addresses are kept as external links. A fragment-only link
// points at the rewritten heading anchors. A relative path is resolved against
// dir and must stay inside the repository. Every other scheme, including
// javascript: and data:, is refused.
func RepositoryResolver(dir string, fileURL, rawURL func(repoPath string) string) Resolver {
	return func(destination string, image bool) (Target, bool) {
		destination = strings.TrimSpace(destination)
		if destination == "" {
			return Target{}, false
		}
		if strings.HasPrefix(destination, "#") {
			if image || len(destination) == 1 {
				return Target{}, false
			}
			return Target{URL: fragmentID(destination[1:])}, true
		}
		parsed, err := url.Parse(destination)
		if err != nil {
			return Target{}, false
		}
		if parsed.Scheme != "" || parsed.Host != "" || strings.HasPrefix(destination, "//") {
			switch strings.ToLower(parsed.Scheme) {
			case "http", "https":
				if parsed.Host == "" {
					return Target{}, false
				}
				return Target{URL: parsed.String(), External: true}, true
			case "mailto":
				if image {
					return Target{}, false
				}
				return Target{URL: parsed.String(), External: true}, true
			default:
				return Target{}, false
			}
		}
		// A relative path. A query is not part of a repository path and is
		// dropped. A fragment is kept only for a Markdown file, whose rendered
		// headings carry the same prefixed IDs as this document's.
		repoPath := parsed.Path
		if repoPath == "" {
			return Target{}, false
		}
		if strings.HasPrefix(repoPath, "/") {
			repoPath = path.Clean(repoPath)
		} else {
			repoPath = path.Clean("/" + path.Join(dir, repoPath))
		}
		// path.Clean on a rooted path never climbs above "/", so a path that
		// tried to leave the repository ends at the root instead. Such a link
		// is refused rather than quietly pointed at the root.
		if escapes(dir, parsed.Path) {
			return Target{}, false
		}
		repoPath = strings.TrimPrefix(repoPath, "/")
		if image {
			if rawURL == nil || repoPath == "" {
				return Target{}, false
			}
			return Target{URL: rawURL(repoPath)}, true
		}
		target := fileURL(repoPath)
		if parsed.Fragment != "" && IsDocument(repoPath) {
			target += fragmentID(parsed.EscapedFragment())
		}
		return Target{URL: target}, true
	}
}

// IsDocument reports whether a repository path is a Markdown file that the
// code view renders as a document.
func IsDocument(repoPath string) bool {
	switch strings.ToLower(path.Ext(repoPath)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// escapes reports whether a relative reference climbs above the repository
// root when resolved against dir.
func escapes(dir, reference string) bool {
	if strings.HasPrefix(reference, "/") {
		dir = ""
		reference = strings.TrimPrefix(reference, "/")
	}
	depth := 0
	if dir != "" {
		depth = len(strings.Split(strings.Trim(dir, "/"), "/"))
	}
	for _, part := range strings.Split(reference, "/") {
		switch part {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}
