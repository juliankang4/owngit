package importfetch_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/pem"
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
	if request.Header.Get("Git-Protocol") != "version=1" {
		fixture.fail(writer, fmt.Errorf("missing protocol v1 request header"))
		return
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
		"GIT_PROTOCOL=version=1",
		"HTTP_GIT_PROTOCOL=version=1",
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

func TestFetchRealGitCGIIndexesExactSnapshot(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find local Git: %v", err)
	}
	for _, format := range []string{importgit.FormatSHA1, importgit.FormatSHA256} {
		t.Run(format, func(t *testing.T) {
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
			before := git.run(nil, "-C", source, "for-each-ref", "--format=%(refname) %(objectname)")
			headBefore := git.run(nil, "-C", source, "symbolic-ref", "HEAD")
			expectedRefs := parseRefs(t, before)

			fixture := &gitCGIFixture{gitPath: gitPath, projectRoot: root, environment: environment}
			server := httptest.NewTLSServer(fixture)
			t.Cleanup(server.Close)
			certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

			stage := filepath.Join(root, "stage.git")
			git.run(nil, "init", "--bare", "--template="+template, "--initial-branch=main", "--object-format="+format, stage)
			var indexOutput []byte
			var indexErr error
			var indexStderr bytes.Buffer
			result, fetchErr := importfetch.Fetch(t.Context(), importfetch.Request{
				URL:                 server.URL + "/source.git",
				AllowPrivateNetwork: true,
				RootCAPEM:           certificate,
			}, func(ctx context.Context, advertisement *importgit.Advertisement, reader io.Reader) error {
				if advertisement.ObjectFormat != format {
					return fmt.Errorf("consumer received wrong object format")
				}
				command := git.command(ctx, "-C", stage, "index-pack", "--stdin", "--strict", "--keep")
				command.Stdin = reader
				command.Stderr = &indexStderr
				indexOutput, indexErr = command.Output()
				return indexErr
			})
			getCount, postCount, postBody, serveErr := fixture.result()
			if serveErr != nil {
				t.Fatalf("synthetic Git CGI failed: %v", serveErr)
			}
			if fetchErr != nil {
				if indexErr != nil {
					t.Fatalf("Fetch and strict index-pack failed: %v: %s", indexErr, indexStderr.String())
				}
				t.Fatalf("Fetch failed: %v", fetchErr)
			}
			if len(bytes.TrimSpace(indexOutput)) == 0 {
				t.Fatal("strict index-pack returned no pack identity")
			}
			if getCount != 1 || postCount != 1 {
				t.Fatalf("request counts = GET %d POST %d, want one each", getCount, postCount)
			}
			if bytes.Contains(postBody, []byte("have ")) {
				t.Fatal("upload-pack request sent a have line")
			}
			if result.Advertisement.ProtocolVersion != 1 || result.Advertisement.ObjectFormat != format || !result.Advertisement.ObjectFormatAdvertised {
				t.Fatalf("advertisement protocol=%d format=%q advertised=%v", result.Advertisement.ProtocolVersion, result.Advertisement.ObjectFormat, result.Advertisement.ObjectFormatAdvertised)
			}
			if result.PackBytes <= 4 {
				t.Fatalf("pack bytes = %d", result.PackBytes)
			}
			assertExactRefs(t, result.Advertisement, expectedRefs)
			if !result.Advertisement.Head.Advertised || result.Advertisement.Head.OID != tip || result.Advertisement.Head.SymrefTarget != "refs/heads/main" {
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
			git.run(nil, "-C", stage, "cat-file", "-e", blob+"^{blob}")
			if transferred := git.run(nil, "-C", stage, "cat-file", "blob", tip+":file.txt"); !bytes.Equal(transferred, content) {
				t.Fatalf("transferred blob = %q, want exact fixture bytes", transferred)
			}
			if stageRefs := git.run(nil, "-C", stage, "for-each-ref", "--format=%(refname) %(objectname)"); len(stageRefs) != 0 {
				t.Fatalf("transport or indexer published staging refs: %q", stageRefs)
			}
			after := git.run(nil, "-C", source, "for-each-ref", "--format=%(refname) %(objectname)")
			headAfter := git.run(nil, "-C", source, "symbolic-ref", "HEAD")
			if !bytes.Equal(before, after) || !bytes.Equal(headBefore, headAfter) {
				t.Fatalf("source refs changed: before=%q/%q after=%q/%q", before, headBefore, after, headAfter)
			}
		})
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
