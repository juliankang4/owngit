package importfetch_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/testfixture"
)

const (
	maximumCGIRequest  = 8 << 20
	maximumCGIResponse = 16 << 20
)

type localGit struct {
	t           *testing.T
	path        string
	environment []string
}

func (git *localGit) arguments(arguments ...string) []string {
	base := []string{"-c", "protocol.allow=never", "-c", "core.hooksPath=" + os.DevNull}
	return append(base, arguments...)
}

func (git *localGit) command(ctx context.Context, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, git.path, git.arguments(arguments...)...)
	command.Env = append([]string(nil), git.environment...)
	return command
}

func (git *localGit) run(input []byte, arguments ...string) []byte {
	git.t.Helper()
	command := git.command(git.t.Context(), arguments...)
	command.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		git.t.Fatalf("local synthetic Git %q failed: %v: %s", arguments, err, stderr.String())
	}
	return output
}

type gitCGIFixture struct {
	gitPath     string
	projectRoot string
	environment []string
	// v0Only makes the server ignore the Git-Protocol header, as a server
	// without protocol v2 does.
	v0Only bool

	mu        sync.Mutex
	getCount  int
	postCount int
	postBody  []byte
	serveErr  error
}

func (fixture *gitCGIFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maximumCGIRequest+1))
	_ = request.Body.Close()
	if err != nil || len(body) > maximumCGIRequest {
		fixture.fail(writer, fmt.Errorf("read bounded CGI request"))
		return
	}
	// Discovery asks for v2. A v2 server keeps it for its commands; a server
	// without v2 answers in v0, and the pack request then says version 1.
	protocol := request.Header.Get("Git-Protocol")
	if want := "version=2"; (request.Method == http.MethodGet || !fixture.v0Only) && protocol != want ||
		request.Method == http.MethodPost && fixture.v0Only && protocol != "version=1" {
		fixture.fail(writer, fmt.Errorf("unexpected Git-Protocol request header %q", protocol))
		return
	}
	if fixture.v0Only {
		protocol = ""
	}

	fixture.mu.Lock()
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/source.git/info/refs" && request.URL.RawQuery == "service=git-upload-pack":
		fixture.getCount++
	case request.Method == http.MethodPost && request.URL.Path == "/source.git/git-upload-pack" && request.URL.RawQuery == "":
		fixture.postCount++
		fixture.postBody = append([]byte(nil), body...)
	default:
		fixture.mu.Unlock()
		fixture.fail(writer, fmt.Errorf("unexpected CGI request route"))
		return
	}
	fixture.mu.Unlock()

	contentType := request.Header.Get("Content-Type")
	command := exec.CommandContext(request.Context(), fixture.gitPath,
		"-c", "protocol.allow=never",
		"-c", "core.hooksPath="+os.DevNull,
		"http-backend",
	)
	command.Env = append(append([]string(nil), fixture.environment...),
		"GIT_PROJECT_ROOT="+fixture.projectRoot,
		"GIT_HTTP_EXPORT_ALL=1",
		"REQUEST_METHOD="+request.Method,
		"PATH_INFO="+request.URL.Path,
		"QUERY_STRING="+request.URL.RawQuery,
		"SERVER_PROTOCOL=HTTP/1.1",
		"GIT_PROTOCOL="+protocol,
		"HTTP_GIT_PROTOCOL="+protocol,
		"CONTENT_TYPE="+contentType,
		"CONTENT_LENGTH="+strconv.Itoa(len(body)),
	)
	command.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		fixture.fail(writer, fmt.Errorf("run local Git CGI: %w", err))
		return
	}
	if len(output) > maximumCGIResponse {
		fixture.fail(writer, fmt.Errorf("Git CGI response exceeded fixture limit"))
		return
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	headers, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		fixture.fail(writer, fmt.Errorf("parse Git CGI headers"))
		return
	}
	status := http.StatusOK
	if value := headers.Get("Status"); value != "" {
		fields := strings.Fields(value)
		if len(fields) == 0 {
			fixture.fail(writer, fmt.Errorf("parse Git CGI status"))
			return
		}
		status, err = strconv.Atoi(fields[0])
		if err != nil {
			fixture.fail(writer, fmt.Errorf("parse Git CGI status"))
			return
		}
	}
	mediaType := headers.Get("Content-Type")
	if mediaType == "" {
		fixture.fail(writer, fmt.Errorf("Git CGI omitted content type"))
		return
	}
	writer.Header().Set("Content-Type", mediaType)
	writer.WriteHeader(status)
	if _, err := io.Copy(writer, reader); err != nil {
		fixture.recordError(fmt.Errorf("copy Git CGI response: %w", err))
	}
}

func (fixture *gitCGIFixture) fail(writer http.ResponseWriter, err error) {
	fixture.recordError(err)
	http.Error(writer, "synthetic Git CGI fixture failed", http.StatusInternalServerError)
}

func (fixture *gitCGIFixture) recordError(err error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.serveErr == nil {
		fixture.serveErr = err
	}
}

func (fixture *gitCGIFixture) result() (getCount, postCount int, postBody []byte, err error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.getCount, fixture.postCount, append([]byte(nil), fixture.postBody...), fixture.serveErr
}

// A real Git server is imported with protocol v2 and, as a server without v2,
// with protocol v0. Both give the same exact snapshot of HEAD, branches and
// tags.
func TestFetchRealGitCGIIndexesExactSnapshot(t *testing.T) {
	for _, format := range []string{importgit.FormatSHA1, importgit.FormatSHA256} {
		for _, v0Only := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/v0only=%v", format, v0Only), func(t *testing.T) {
				source := newRealGitSource(t, format, 0)
				result, fetchErr := source.fetch(t, v0Only, importfetch.Limits{})
				if fetchErr != nil {
					t.Fatalf("Fetch failed: %v", fetchErr)
				}
				source.assertSnapshot(t, result, v0Only)
			})
		}
	}
}

// Pull request refs are not listed by a v2 server, so they do not count
// toward the ref limit. A server without v2 lists every ref, and the same
// limit then refuses the source by name.
func TestFetchRealGitCGIDoesNotCountPullRequestRefsWithV2(t *testing.T) {
	const pulls = 8
	source := newRealGitSource(t, importgit.FormatSHA1, pulls)
	// HEAD, two branches and a tag are four refs in v2; v0 also lists the
	// peeled tag and the pull request refs.
	limits := importfetch.Limits{Advertisement: importgit.Limits{MaxRefRecords: 4}}
	result, err := source.fetch(t, false, limits)
	if err != nil {
		t.Fatalf("v2 fetch with %d pull request refs: %v", pulls, err)
	}
	source.assertSnapshot(t, result, false)
	for _, reference := range result.Advertisement.Refs {
		if strings.HasPrefix(reference.Name, "refs/pull/") {
			t.Fatalf("v2 listed %q", reference.Name)
		}
	}
	if _, err := source.fetch(t, true, limits); !errors.Is(err, importgit.ErrTooManyRefs) {
		t.Fatalf("v0 fetch with %d pull request refs = %v, want ErrTooManyRefs", pulls, err)
	}
}

type realGitSource struct {
	git         *localGit
	gitPath     string
	root        string
	fetches     int
	stage       string
	environment []string
	content     []byte
	blob, tip   string
	before      []byte
	headBefore  []byte
	expected    map[string]string
	fixture     *gitCGIFixture
}

// newRealGitSource creates a bare repository with two branches, an annotated
// tag and the given number of pull request refs.
func newRealGitSource(t *testing.T, format string, pulls int) *realGitSource {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find local Git: %v", err)
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	template := filepath.Join(root, "template")
	for _, directory := range []string{home, template} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_AUTHOR_NAME=Import Fixture",
		"GIT_AUTHOR_EMAIL=import@example.invalid",
		"GIT_COMMITTER_NAME=Import Fixture",
		"GIT_COMMITTER_EMAIL=import@example.invalid",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00+0000",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00+0000",
		"LC_ALL=C",
	}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "TMPDIR", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	environment = testfixture.GitEnvironment(environment)
	git := &localGit{t: t, path: gitPath, environment: environment}

	source := filepath.Join(root, "source.git")
	git.run(nil, "init", "--bare", "--template="+template, "--initial-branch=main", "--object-format="+format, source)
	content := []byte("exact synthetic import content\n")
	blob := strings.TrimSpace(string(git.run(content, "-C", source, "hash-object", "-w", "--stdin")))
	tree := strings.TrimSpace(string(git.run([]byte(fmt.Sprintf("100644 blob %s\tfile.txt\n", blob)), "-C", source, "mktree")))
	first := strings.TrimSpace(string(git.run(nil, "-C", source, "commit-tree", tree, "-m", "first")))
	tip := strings.TrimSpace(string(git.run(nil, "-C", source, "commit-tree", tree, "-p", first, "-m", "second")))
	git.run(nil, "-C", source, "update-ref", "refs/heads/main", tip)
	git.run(nil, "-C", source, "update-ref", "refs/heads/기능", first)
	git.run(nil, "-C", source, "tag", "-a", "v1", "-m", "synthetic tag", first)
	expected := parseRefs(t, git.run(nil, "-C", source, "for-each-ref", "--format=%(refname) %(objectname)"))
	for pull := 1; pull <= pulls; pull++ {
		pr := strings.TrimSpace(string(git.run(nil, "-C", source, "commit-tree", tree, "-p", tip, "-m", fmt.Sprintf("pull %d", pull))))
		git.run(nil, "-C", source, "update-ref", fmt.Sprintf("refs/pull/%d/head", pull), pr)
	}
	return &realGitSource{
		git: git, gitPath: gitPath, root: root, environment: environment,
		content: content, blob: blob, tip: tip, expected: expected,
		before:     git.run(nil, "-C", source, "for-each-ref", "--format=%(refname) %(objectname)"),
		headBefore: git.run(nil, "-C", source, "symbolic-ref", "HEAD"),
	}
}

// fetch serves the source with a new server and imports it into a new
// staging repository with strict index-pack, the way the import does.
func (source *realGitSource) fetch(t *testing.T, v0Only bool, limits importfetch.Limits) (*importfetch.Result, error) {
	t.Helper()
	git := source.git
	source.fetches++
	source.stage = filepath.Join(source.root, fmt.Sprintf("stage%d.git", source.fetches))
	source.fixture = &gitCGIFixture{gitPath: source.gitPath, projectRoot: source.root, environment: source.environment, v0Only: v0Only}
	server := httptest.NewTLSServer(source.fixture)
	defer server.Close()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	format := strings.TrimSpace(string(git.run(nil, "-C", filepath.Join(source.root, "source.git"), "rev-parse", "--show-object-format")))
	git.run(nil, "init", "--bare", "--initial-branch=main", "--object-format="+format, source.stage)
	var indexOutput []byte
	var indexErr error
	var indexStderr bytes.Buffer
	result, fetchErr := importfetch.Fetch(t.Context(), importfetch.Request{
		URL:                 server.URL + "/source.git",
		AllowPrivateNetwork: true,
		RootCAPEM:           certificate,
		Limits:              limits,
	}, func(ctx context.Context, advertisement *importgit.Advertisement, reader io.Reader) error {
		if advertisement.ObjectFormat != format {
			return fmt.Errorf("consumer received wrong object format")
		}
		command := git.command(ctx, "-C", source.stage, "index-pack", "--stdin", "--strict", "--keep")
		command.Stdin = reader
		command.Stderr = &indexStderr
		indexOutput, indexErr = command.Output()
		return indexErr
	})
	if _, _, _, serveErr := source.fixture.result(); serveErr != nil {
		t.Fatalf("synthetic Git CGI failed: %v", serveErr)
	}
	if fetchErr == nil && len(bytes.TrimSpace(indexOutput)) == 0 {
		t.Fatal("strict index-pack returned no pack identity")
	}
	if indexErr != nil {
		t.Fatalf("strict index-pack failed: %v: %s", indexErr, indexStderr.String())
	}
	return result, fetchErr
}

func (source *realGitSource) assertSnapshot(t *testing.T, result *importfetch.Result, v0Only bool) {
	t.Helper()
	git := source.git
	stage := source.stage
	getCount, postCount, postBody, _ := source.fixture.result()
	// A v2 source answers discovery, ls-refs and fetch; a v0 source answers
	// discovery and the pack request.
	wantVersion, wantPosts := 2, 2
	if v0Only {
		wantVersion, wantPosts = 0, 1
	}
	if getCount != 1 || postCount != wantPosts {
		t.Fatalf("request counts = GET %d POST %d, want 1 and %d", getCount, postCount, wantPosts)
	}
	if bytes.Contains(postBody, []byte("have ")) {
		t.Fatal("upload-pack request sent a have line")
	}
	format := result.Advertisement.ObjectFormat
	if result.Advertisement.ProtocolVersion != wantVersion || !result.Advertisement.ObjectFormatAdvertised {
		t.Fatalf("advertisement protocol=%d format=%q advertised=%v", result.Advertisement.ProtocolVersion, format, result.Advertisement.ObjectFormatAdvertised)
	}
	if result.PackBytes <= 4 {
		t.Fatalf("pack bytes = %d", result.PackBytes)
	}
	assertExactRefs(t, result.Advertisement, source.expected)
	if !result.Advertisement.Head.Advertised || result.Advertisement.Head.OID != source.tip || result.Advertisement.Head.SymrefTarget != "refs/heads/main" {
		t.Fatalf("advertised HEAD = %+v", result.Advertisement.Head)
	}

	wanted := make(map[string]struct{})
	for _, reference := range result.Advertisement.Refs {
		wanted[reference.OID] = struct{}{}
	}
	wantedOIDs := make([]string, 0, len(wanted))
	for oid := range wanted {
		wantedOIDs = append(wantedOIDs, oid)
	}
	sort.Strings(wantedOIDs)
	for _, oid := range wantedOIDs {
		git.run(nil, "-C", stage, "cat-file", "-e", oid+"^{object}")
	}
	git.run(nil, "-C", stage, "cat-file", "-e", source.blob+"^{blob}")
	if transferred := git.run(nil, "-C", stage, "cat-file", "blob", source.tip+":file.txt"); !bytes.Equal(transferred, source.content) {
		t.Fatalf("transferred blob = %q, want exact fixture bytes", transferred)
	}
	if stageRefs := git.run(nil, "-C", stage, "for-each-ref", "--format=%(refname) %(objectname)"); len(stageRefs) != 0 {
		t.Fatalf("transport or indexer published staging refs: %q", stageRefs)
	}
	sourcePath := filepath.Join(source.root, "source.git")
	after := git.run(nil, "-C", sourcePath, "for-each-ref", "--format=%(refname) %(objectname)")
	headAfter := git.run(nil, "-C", sourcePath, "symbolic-ref", "HEAD")
	if !bytes.Equal(source.before, after) || !bytes.Equal(source.headBefore, headAfter) {
		t.Fatalf("source refs changed: before=%q/%q after=%q/%q", source.before, source.headBefore, after, headAfter)
	}
}

func parseRefs(t *testing.T, output []byte) map[string]string {
	t.Helper()
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		name, oid, ok := strings.Cut(line, " ")
		if !ok || name == "" || oid == "" {
			t.Fatalf("invalid synthetic ref listing %q", line)
		}
		refs[name] = oid
	}
	return refs
}

func assertExactRefs(t *testing.T, advertisement *importgit.Advertisement, expected map[string]string) {
	t.Helper()
	actual := make(map[string]string)
	for _, reference := range advertisement.Refs {
		if strings.HasPrefix(reference.Name, "refs/") {
			actual[reference.Name] = reference.OID
		}
	}
	if len(actual) != len(expected) {
		t.Fatalf("advertised refs = %v, want %v", actual, expected)
	}
	for name, oid := range expected {
		if actual[name] != oid {
			t.Fatalf("advertised ref %q = %q, want %q", name, actual[name], oid)
		}
	}
}
