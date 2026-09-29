package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"owngit/internal/testfixture"
)

// The installers in packaging/installer run here against a synthetic
// release served over local HTTPS. The program in its archives is
// testdata/fakeowngit, which only records how it was run, so no test
// installs a real service or touches this account's OwnGit.

// syntheticRelease serves releases the way GitHub does:
// /releases/download/vX.Y.Z/NAME, and /releases/latest/download/NAME as a
// redirect to the latest release.
type syntheticRelease struct {
	server  *httptest.Server
	caFile  string
	latest  string
	archive map[string]string // version -> archive name for this computer
	program map[string][]byte // version -> the fake owngit in its archive

	mu       sync.Mutex
	files    map[string][]byte // "vX.Y.Z/NAME" -> content
	override map[string][]byte // replaces files; nil content answers 404
	redirect map[string]string // answers with a redirect to the address
	requests int
}

var fakeBuilds sync.Map // version -> []byte

// fakeOwngit builds testdata/fakeowngit for this computer, labelled version.
func fakeOwngit(t *testing.T, version string) []byte {
	t.Helper()
	if built, ok := fakeBuilds.Load(version); ok {
		return built.([]byte)
	}
	out := filepath.Join(t.TempDir(), "owngit")
	command := exec.Command("go", "build", "-o", out, "-ldflags", "-X main.version="+version, "./testdata/fakeowngit")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake owngit: %v\n%s", err, output)
	}
	data, err := os.ReadFile(out)
	noErr(t, err)
	fakeBuilds.Store(version, data)
	return data
}

// newSyntheticRelease serves every version for this computer; the last one
// is the latest. SHA256SUMS also lists an archive of another platform, so
// the installer has to pick its own.
func newSyntheticRelease(t *testing.T, versions ...string) *syntheticRelease {
	t.Helper()
	release := &syntheticRelease{
		latest: versions[len(versions)-1], archive: map[string]string{}, program: map[string][]byte{},
		files: map[string][]byte{}, override: map[string][]byte{}, redirect: map[string]string{},
	}
	for _, version := range versions {
		program := fakeOwngit(t, version)
		var name string
		var data []byte
		if runtime.GOOS == "windows" {
			name = fmt.Sprintf("owngit_%s_windows_amd64.zip", version)
			data = zipOf(t, map[string][]byte{"owngit.exe": program, "README.txt": []byte("OwnGit " + version + "\n")})
		} else {
			name = fmt.Sprintf("owngit_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
			data = tarGzOf(t, map[string][]byte{"owngit": program, "README.txt": []byte("OwnGit " + version + "\n")})
		}
		other := fmt.Sprintf("owngit_%s_linux_arm64.tar.gz", version)
		if name == other {
			other = fmt.Sprintf("owngit_%s_linux_amd64.tar.gz", version)
		}
		sums := []string{digestLine(data, name), digestLine([]byte("another platform"), other)}
		sort.Strings(sums)
		release.archive[version], release.program[version] = name, program
		release.files["v"+version+"/"+name] = data
		release.files["v"+version+"/SHA256SUMS"] = []byte(strings.Join(sums, "\n") + "\n")
	}
	release.server = httptest.NewTLSServer(http.HandlerFunc(release.serve))
	t.Cleanup(release.server.Close)
	release.caFile = filepath.Join(t.TempDir(), "ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: release.server.Certificate().Raw})
	noErr(t, os.WriteFile(release.caFile, certificate, 0o644))
	return release
}

func (release *syntheticRelease) serve(writer http.ResponseWriter, request *http.Request) {
	if name, ok := strings.CutPrefix(request.URL.Path, "/releases/latest/download/"); ok {
		http.Redirect(writer, request, "/releases/download/v"+release.latest+"/"+name, http.StatusFound)
		return
	}
	path, _ := strings.CutPrefix(request.URL.Path, "/releases/download/")
	release.mu.Lock()
	release.requests++
	data, found := release.files[path]
	if replaced, ok := release.override[path]; ok {
		data, found = replaced, replaced != nil
	}
	target, redirected := release.redirect[path]
	release.mu.Unlock()
	if redirected {
		http.Redirect(writer, request, target, http.StatusFound)
		return
	}
	if !found {
		http.NotFound(writer, request)
		return
	}
	writer.Write(data)
}

// replace serves content (or 404 for nil) at "vX.Y.Z/NAME" for one test.
func (release *syntheticRelease) replace(t *testing.T, path string, content []byte) {
	release.mu.Lock()
	release.override[path] = content
	release.mu.Unlock()
	t.Cleanup(func() {
		release.mu.Lock()
		delete(release.override, path)
		release.mu.Unlock()
	})
}

// redirectToHTTP answers "vX.Y.Z/NAME" for one test with a redirect to the
// same file on a plain HTTP server, and returns that address.
func (release *syntheticRelease) redirectToHTTP(t *testing.T, path string) string {
	content := release.files[path]
	plain := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.Write(content) }))
	t.Cleanup(plain.Close)
	target := plain.URL + "/" + path
	release.mu.Lock()
	release.redirect[path] = target
	release.mu.Unlock()
	t.Cleanup(func() {
		release.mu.Lock()
		delete(release.redirect, path)
		release.mu.Unlock()
	})
	return target
}

func (release *syntheticRelease) url() string { return release.server.URL + "/releases" }

// served is how many files the release has answered for so far.
func (release *syntheticRelease) served() int {
	release.mu.Lock()
	defer release.mu.Unlock()
	return release.requests
}

// versionOf says which release's program the file is, or "" for none.
func (release *syntheticRelease) versionOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for version, program := range release.program {
		if bytes.Equal(data, program) {
			return version
		}
	}
	return "other"
}

// shellQuoteForTest quotes a word for a POSIX shell.
func shellQuoteForTest(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

func digestLine(data []byte, name string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name
}

func tarGzOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for _, name := range sortedKeys(files) {
		noErr(t, archive.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(files[name])), Typeflag: tar.TypeReg}))
		_, err := archive.Write(files[name])
		noErr(t, err)
	}
	noErr(t, archive.Close())
	noErr(t, compressed.Close())
	return buffer.Bytes()
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, name := range sortedKeys(files) {
		writer, err := archive.Create(name)
		noErr(t, err)
		_, err = writer.Write(files[name])
		noErr(t, err)
	}
	noErr(t, archive.Close())
	return buffer.Bytes()
}

func sortedKeys(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	noErr(t, err)
	return string(data)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	noErr(t, err)
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// shInstall is one run of install.sh, read from standard input as
// "curl ... | sh -s -- ARGS" reads it. Its output is not a terminal.
type shInstall struct {
	home, log string
	env       []string
}

func newShInstall(t *testing.T, release *syntheticRelease) *shInstall {
	home := t.TempDir()
	run := &shInstall{home: home, log: filepath.Join(home, "owngit.log")}
	run.env = []string{
		"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"OWNGIT_RELEASES=" + release.url(), "CURL_CA_BUNDLE=" + release.caFile,
		"OWNGIT_FAKE_LOG=" + run.log,
	}
	return run
}

func (run *shInstall) do(t *testing.T, env []string, arguments ...string) (string, error) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join(repoRoot(t), "packaging", "installer", "install.sh"))
	noErr(t, err)
	command := exec.Command("sh", append([]string{"-s", "--"}, arguments...)...)
	command.Stdin = bytes.NewReader(script)
	command.Env = append(append([]string{}, run.env...), env...)
	command.Dir = run.home
	output, err := command.CombinedOutput()
	return string(output), err
}

func (run *shInstall) must(t *testing.T, env []string, arguments ...string) string {
	t.Helper()
	output, err := run.do(t, env, arguments...)
	if err != nil {
		t.Fatalf("install.sh %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return output
}

func (run *shInstall) mustFail(t *testing.T, env []string, want string, arguments ...string) string {
	t.Helper()
	output, err := run.do(t, env, arguments...)
	if err == nil {
		t.Fatalf("install.sh %s succeeded:\n%s", strings.Join(arguments, " "), output)
	}
	if !strings.Contains(output, want) {
		t.Fatalf("install.sh %s failed without %q:\n%s", strings.Join(arguments, " "), want, output)
	}
	return output
}

func TestInstallSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is for Linux and macOS; TestInstallPs1 covers Windows")
	}
	requireGoToolchain(t)
	for _, tool := range []string{"sh", "curl", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("install.sh needs %s: %v", tool, err)
		}
	}
	release := newSyntheticRelease(t, "1.0.0", "2.0.0")
	root := os.Geteuid() == 0

	t.Run("fresh install takes the latest release and installs the service", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, ".local", "bin", "owngit")
		var arguments []string
		if root {
			// Root's default is /usr/local/bin, which a test leaves alone.
			arguments = []string{"--to", target}
		}
		output := run.must(t, nil, arguments...)
		if got := release.versionOf(t, target); got != "2.0.0" {
			t.Fatalf("installed %q, want 2.0.0:\n%s", got, output)
		}
		info, err := os.Stat(target)
		noErr(t, err)
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode %v, want 0755", info.Mode().Perm())
		}
		for _, want := range []string{"Installed OwnGit 2.0.0 at " + target, "is not on your PATH", "fake owngit 2.0.0: service install"} {
			if !strings.Contains(output, want) {
				t.Errorf("output lacks %q:\n%s", want, output)
			}
		}
		if log := readLog(t, run.log); log != "2.0.0 service install\n" {
			t.Errorf("owngit ran as %q, want one service install", log)
		}
		if names := dirNames(t, filepath.Dir(target)); len(names) != 1 {
			t.Errorf("the folder holds %v, want only owngit", names)
		}
	})

	t.Run("rerun keeps the file and installs the service again", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		run.must(t, nil, "--to", target)
		before, err := os.Stat(target)
		noErr(t, err)
		output := run.must(t, nil, "--to", target)
		after, err := os.Stat(target)
		noErr(t, err)
		if !os.SameFile(before, after) || !strings.Contains(output, "OwnGit 2.0.0 is already at "+target) {
			t.Fatalf("the rerun replaced the program:\n%s", output)
		}
		if log := readLog(t, run.log); log != "2.0.0 service install\n2.0.0 service install\n" {
			t.Errorf("owngit ran as %q", log)
		}
	})

	t.Run("pinned version replaces a newer one", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		run.must(t, nil, "--to", target)
		output := run.must(t, nil, "--version", "v1.0.0", "--to", target)
		if got := release.versionOf(t, target); got != "1.0.0" {
			t.Fatalf("installed %q, want 1.0.0:\n%s", got, output)
		}
		if log := readLog(t, run.log); log != "2.0.0 service install\n1.0.0 service install\n" {
			t.Errorf("owngit ran as %q", log)
		}
	})

	t.Run("an archive that does not match SHA256SUMS changes nothing", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		run.must(t, nil, "--version", "1.0.0", "--to", target)
		tampered := append(append([]byte{}, release.files["v2.0.0/"+release.archive["2.0.0"]]...), 0)
		release.replace(t, "v2.0.0/"+release.archive["2.0.0"], tampered)
		run.mustFail(t, nil, "does not match the release's SHA256SUMS", "--to", target)
		if got := release.versionOf(t, target); got != "1.0.0" {
			t.Fatalf("the program is now %q, want 1.0.0", got)
		}
		if log := readLog(t, run.log); log != "1.0.0 service install\n" {
			t.Errorf("owngit ran as %q after the refusal", log)
		}
		if names := dirNames(t, filepath.Dir(target)); len(names) != 1 {
			t.Errorf("the folder holds %v, want only owngit", names)
		}
	})

	t.Run("a redirect to plain HTTP is refused", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		run.must(t, nil, "--version", "1.0.0", "--to", target)
		release.redirectToHTTP(t, "v2.0.0/"+release.archive["2.0.0"])
		run.mustFail(t, nil, "could not download "+release.url()+"/download/v2.0.0/"+release.archive["2.0.0"]+"; nothing was changed", "--to", target)
		if got := release.versionOf(t, target); got != "1.0.0" {
			t.Fatalf("the program is now %q, want 1.0.0", got)
		}
	})

	t.Run("a failed download keeps the installed version", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		run.must(t, nil, "--version", "1.0.0", "--to", target)
		release.replace(t, "v2.0.0/"+release.archive["2.0.0"], nil)
		run.mustFail(t, nil, "could not download", "--to", target)
		run.mustFail(t, nil, "could not download "+release.url()+"/download/v9.9.9/SHA256SUMS", "--version", "9.9.9", "--to", target)
		if got := release.versionOf(t, target); got != "1.0.0" {
			t.Fatalf("the program is now %q, want 1.0.0", got)
		}
		if log := readLog(t, run.log); log != "1.0.0 service install\n" {
			t.Errorf("owngit ran as %q", log)
		}
	})

	t.Run("no service installs the program only", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		output := run.must(t, nil, "--no-service", "--to", target)
		if got := release.versionOf(t, target); got != "2.0.0" {
			t.Fatalf("installed %q, want 2.0.0", got)
		}
		if log := readLog(t, run.log); log != "" {
			t.Errorf("owngit ran as %q with --no-service", log)
		}
		for _, want := range []string{target + " serve", target + " service install"} {
			if !strings.Contains(output, want) {
				t.Errorf("output lacks %q:\n%s", want, output)
			}
		}
	})

	// Every printed command keeps the program's path one word: run as
	// printed from the installer's own folder, each runs only the program,
	// and the path's quote, semicolon and $() do nothing.
	t.Run("printed commands quote the path", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "a b'c;$(touch marker)", "owngit")
		printed := func(output, prefix string) string {
			for _, line := range strings.Split(output, "\n") {
				if _, command, found := strings.Cut(line, prefix); found {
					return command
				}
			}
			t.Fatalf("no line with %q:\n%s", prefix, output)
			return ""
		}
		output := run.must(t, nil, "--no-service", "--to", target)
		failed, _ := run.do(t, []string{"OWNGIT_FAKE_FAIL=service install"}, "--to", target)
		for _, command := range []string{
			printed(output, "Run it now with: "),
			printed(output, "Or run it as a service that starts by itself: "),
			printed(failed, "after fixing what it reported, run: "),
		} {
			noErr(t, os.WriteFile(run.log, nil, 0o644))
			shell := exec.Command("sh", "-c", command)
			shell.Env, shell.Dir = run.env, run.home
			if out, err := shell.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s", command, err, out)
			}
			want := "2.0.0 service install\n"
			if strings.HasSuffix(command, " serve") {
				want = "2.0.0 serve\n"
			}
			if log := readLog(t, run.log); log != want {
				t.Errorf("%s ran owngit as %q", command, log)
			}
		}
		if _, err := os.Stat(filepath.Join(run.home, "marker")); !os.IsNotExist(err) {
			t.Fatalf("a printed command ran the path as shell syntax (%v)", err)
		}
	})

	t.Run("a failed service install is a failure and keeps the new program", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		run.mustFail(t, []string{"OWNGIT_FAKE_FAIL=service install"}, "OwnGit 2.0.0 stays at "+target, "--to", target)
		if got := release.versionOf(t, target); got != "2.0.0" {
			t.Fatalf("installed %q, want 2.0.0", got)
		}
	})

	// The installer uses the real sudo, which it finds at its system path,
	// so this runs only where sudo asks for no password (as on CI runners).
	t.Run("sudo only for a folder this account cannot write", func(t *testing.T) {
		if root {
			t.Skip("root can write every folder")
		}
		if exec.Command("sudo", "-n", "true").Run() != nil {
			t.Skip("needs sudo without a password")
		}
		run := newShInstall(t, release)
		dir := filepath.Join(run.home, "locked")
		target := filepath.Join(dir, "owngit")
		old := filepath.Join(run.home, "owngit-1.0.0")
		noErr(t, os.WriteFile(old, release.program["1.0.0"], 0o755))
		for _, command := range [][]string{{"install", "-d", "-m", "0755", "-o", "root", "-g", "0", dir}, {"install", "-m", "0755", "-o", "root", "-g", "0", old, target}} {
			if output, err := exec.Command("sudo", append([]string{"-n"}, command...)...).CombinedOutput(); err != nil {
				t.Fatalf("sudo %v: %v\n%s", command, err, output)
			}
		}
		t.Cleanup(func() { exec.Command("sudo", "-n", "chown", "-R", fmt.Sprint(os.Getuid()), dir).Run() })
		output := run.must(t, nil, "--to", target)
		if got := release.versionOf(t, target); got != "2.0.0" {
			t.Fatalf("installed %q, want 2.0.0:\n%s", got, output)
		}
		if !strings.Contains(output, "This account cannot write "+dir+", so sudo puts owngit there.") {
			t.Errorf("output does not say why sudo runs:\n%s", output)
		}
		info, err := os.Stat(target)
		noErr(t, err)
		if owner := fileOwner(info); owner != 0 || info.Mode().Perm() != 0o755 {
			t.Errorf("the program belongs to %d with mode %v, want root and 0755", owner, info.Mode().Perm())
		}
		if names := dirNames(t, dir); len(names) != 1 {
			t.Errorf("the folder holds %v, want only owngit", names)
		}
		if log := readLog(t, run.log); log != "2.0.0 service install\n" {
			t.Errorf("owngit ran as %q", log)
		}
	})

	// Programs earlier in PATH with the names of the tools the installer
	// uses are never run: it takes its tools from the system folders.
	t.Run("tools come from the system folders, not PATH", func(t *testing.T) {
		run := newShInstall(t, release)
		fakes := filepath.Join(run.home, "fakes")
		marker := filepath.Join(run.home, "fake-ran")
		noErr(t, os.Mkdir(fakes, 0o755))
		for _, name := range []string{"curl", "tar", "sha256sum", "shasum", "install", "mktemp", "id", "sudo", "grep", "cut", "dirname", "mv", "rm", "mkdir", "ls", "uname", "readlink", "sysctl", "sh"} {
			noErr(t, os.WriteFile(filepath.Join(fakes, name), []byte("#!/bin/sh\necho \"$0 $*\" >>"+shellQuoteForTest(marker)+"\nexit 1\n"), 0o755))
		}
		target := filepath.Join(run.home, "bin", "owngit")
		output := run.must(t, []string{"PATH=" + fakes + ":/usr/bin:/bin:/usr/sbin:/sbin"}, "--to", target)
		if got := release.versionOf(t, target); got != "2.0.0" {
			t.Fatalf("installed %q, want 2.0.0:\n%s", got, output)
		}
		if ran := readLog(t, marker); ran != "" {
			t.Fatalf("the installer ran programs from PATH:\n%s", ran)
		}
	})

	t.Run("SHA256SUMS must list exactly one archive for this computer", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		sums := "v2.0.0/SHA256SUMS"
		name := release.archive["2.0.0"]
		release.replace(t, sums, append(append([]byte{}, release.files[sums]...), []byte(digestLine([]byte("another build"), name)+"\n")...))
		run.mustFail(t, nil, "does not list one archive", "--to", target)
		release.replace(t, sums, []byte(digestLine([]byte("another platform"), "owngit_2.0.0_plan9_amd64.tar.gz")+"\n"))
		run.mustFail(t, nil, "does not list one archive", "--to", target)
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("a refused run left %s", target)
		}
	})

	// Another account must not be able to change the folder that gets the
	// program, a folder on the way to it, or the folder that holds the
	// download folder; the mode bits stand in for that account here.
	t.Run("folders another account can change are refused before downloading", func(t *testing.T) {
		run := newShInstall(t, release)
		base := filepath.Join(run.home, "ways")
		noErr(t, os.Mkdir(base, 0o755))
		// The installer names folders by the path the system follows.
		base, err := filepath.EvalSymlinks(base)
		noErr(t, err)
		folder := func(name string, mode os.FileMode) string {
			path := filepath.Join(base, name)
			noErr(t, os.MkdirAll(path, 0o755))
			noErr(t, os.Chmod(path, mode))
			return path
		}
		shared := folder("shared", 0o757)
		sticky := folder("sticky", os.ModeSticky|0o777)
		above := folder("above", 0o757)
		below := folder(filepath.Join("above", "private"), 0o755)
		noErr(t, os.Chmod(above, 0o757))
		// Links: one in the shared folder that leads to a private folder
		// (another account could point it elsewhere after the check), and
		// a private link, absolute or relative, that leads to the shared
		// folder.
		mine := folder("mine", 0o755)
		noErr(t, os.Symlink(folder("dest", 0o755), filepath.Join(shared, "link")))
		noErr(t, os.Symlink(shared, filepath.Join(mine, "to-shared")))
		noErr(t, os.Symlink(filepath.Join("..", "shared"), filepath.Join(mine, "up-to-shared")))
		before := release.served()
		for _, tc := range []struct{ target, refused, env string }{
			{filepath.Join(shared, "link", "owngit"), shared + " (every account can write it)", ""},
			{filepath.Join(mine, "to-shared", "owngit"), shared + " (every account can write it)", ""},
			{filepath.Join(mine, "up-to-shared", "new", "owngit"), shared + " (every account can write it)", ""},
			{filepath.Join(run.home, "bin", "owngit"), shared + " (every account can write it); set TMPDIR", "TMPDIR=" + filepath.Join(mine, "to-shared")},
			{filepath.Join(shared, "owngit"), shared + " (every account can write it)", ""},
			{filepath.Join(shared, "new", "owngit"), shared + " (every account can write it)", ""},
			{filepath.Join(sticky, "owngit"), sticky + " (every account can write it)", ""},
			{filepath.Join(below, "owngit"), above + " (every account can write it)", ""},
			{filepath.Join(run.home, "bin", "owngit"), shared + " (every account can write it); set TMPDIR", "TMPDIR=" + shared},
		} {
			var env []string
			if tc.env != "" {
				env = []string{tc.env}
			}
			run.mustFail(t, env, "another account can change "+tc.refused, "--to", tc.target)
			if _, err := os.Stat(tc.target); !os.IsNotExist(err) {
				t.Errorf("a refused run left %s", tc.target)
			}
		}
		if served := release.served(); served != before {
			t.Errorf("refused runs downloaded %d files", served-before)
		}
		// A sticky folder that every account can write is fine above the
		// folder that gets the program, as /tmp is for every other case.
		target := filepath.Join(folder(filepath.Join("sticky", "mine"), 0o755), "owngit")
		run.must(t, nil, "--no-service", "--to", target)
		// A private link to a private folder is fine, and so is /tmp,
		// which on macOS is a link to /private/tmp, and a private link
		// to it.
		noErr(t, os.Symlink(filepath.Join(base, "dest"), filepath.Join(mine, "to-dest")))
		noErr(t, os.Symlink("/tmp", filepath.Join(mine, "to-tmp")))
		run.must(t, []string{"TMPDIR=/tmp"}, "--no-service", "--to", filepath.Join(mine, "to-dest", "owngit"))
		if got := release.versionOf(t, filepath.Join(base, "dest", "owngit")); got != "2.0.0" {
			t.Errorf("the private link led to version %q", got)
		}
		run.must(t, []string{"TMPDIR=" + filepath.Join(mine, "to-tmp")}, "--no-service", "--to", filepath.Join(mine, "owngit"))
	})

	// The other folder rules: a folder that belongs to another account, one
	// whose group (not this account's private group) can write it, and one
	// with an access list that lets others change it. Changing an owner or
	// group needs root, so those cases run as root or with sudo that asks
	// for no password.
	t.Run("another owner, a shared group and an access list are refused", func(t *testing.T) {
		run := newShInstall(t, release)
		asRoot := func(t *testing.T, command ...string) {
			t.Helper()
			if !root {
				if exec.Command("sudo", "-n", "true").Run() != nil {
					t.Skip("needs root or sudo without a password")
				}
				command = append([]string{"sudo", "-n"}, command...)
			}
			if output, err := exec.Command(command[0], command[1:]...).CombinedOutput(); err != nil {
				t.Fatalf("%v: %v\n%s", command, err, output)
			}
		}
		refused := func(t *testing.T, dir, reason string) {
			t.Helper()
			before := release.served()
			run.mustFail(t, nil, "another account can change "+dir+" ("+reason+")", "--to", filepath.Join(dir, "owngit"))
			if served := release.served(); served != before {
				t.Errorf("a refused run downloaded %d files", served-before)
			}
		}
		folder := func(t *testing.T, mode os.FileMode) string {
			dir := filepath.Join(run.home, strings.ReplaceAll(t.Name(), "/", "-"))
			noErr(t, os.Mkdir(dir, 0o755))
			noErr(t, os.Chmod(dir, mode))
			// The installer names folders by the path the system follows.
			dir, err := filepath.EvalSymlinks(dir)
			noErr(t, err)
			return dir
		}
		t.Run("another owner", func(t *testing.T) {
			dir := folder(t, 0o755)
			asRoot(t, "chown", "65534", dir)
			t.Cleanup(func() { asRoot(t, "chown", fmt.Sprint(os.Getuid()), dir) })
			refused(t, dir, "it belongs to another account")
		})
		t.Run("a link of another account", func(t *testing.T) {
			dir := folder(t, 0o755)
			link := filepath.Join(dir, "link")
			noErr(t, os.Mkdir(filepath.Join(dir, "dest"), 0o755))
			noErr(t, os.Symlink(filepath.Join(dir, "dest"), link))
			asRoot(t, "chown", "-h", "65534", link)
			before := release.served()
			run.mustFail(t, nil, "another account can change "+link+" (it is a link that belongs to another account)", "--to", filepath.Join(link, "owngit"))
			if served := release.served(); served != before {
				t.Errorf("a refused run downloaded %d files", served-before)
			}
		})
		t.Run("a group that is not this account's own", func(t *testing.T) {
			dir := folder(t, 0o775)
			asRoot(t, "chgrp", "54321", dir)
			refused(t, dir, "its group can write it")
		})
		t.Run("an access list", func(t *testing.T) {
			dir := folder(t, 0o755)
			switch runtime.GOOS {
			case "linux":
				setfacl, err := exec.LookPath("setfacl")
				if err != nil {
					t.Skip("setfacl is not installed")
				}
				if output, err := exec.Command(setfacl, "-m", "u:65534:rx", dir).CombinedOutput(); err != nil {
					t.Skipf("this file system takes no access list: %v\n%s", err, output)
				}
				refused(t, dir, "it has an access list")
			case "darwin":
				if output, err := exec.Command("/bin/chmod", "+a", "everyone allow add_file", dir).CombinedOutput(); err != nil {
					t.Fatalf("chmod +a: %v\n%s", err, output)
				}
				refused(t, dir, "its access list lets others change it")
			}
		})
	})

	t.Run("a link at the target is refused before downloading", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		other := filepath.Join(run.home, "lib", "node_modules", "owngit", "bin", "owngit.js")
		noErr(t, os.MkdirAll(filepath.Dir(target), 0o755))
		noErr(t, os.MkdirAll(filepath.Dir(other), 0o755))
		noErr(t, os.WriteFile(other, []byte("another install\n"), 0o755))
		noErr(t, os.Symlink(other, target))
		before := release.served()
		run.mustFail(t, nil, target+" is a link to "+other+", which another install may own", "--to", target)
		if link, err := os.Readlink(target); err != nil || link != other {
			t.Fatalf("the link is now %q, %v", link, err)
		}
		if data, err := os.ReadFile(other); err != nil || string(data) != "another install\n" {
			t.Fatalf("the linked file is now %q, %v", data, err)
		}
		if served := release.served(); served != before {
			t.Errorf("the refused run downloaded %d files", served-before)
		}
		if log := readLog(t, run.log); log != "" {
			t.Errorf("owngit ran as %q", log)
		}
	})

	t.Run("refuses plain HTTP and a malformed version before downloading", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		run.mustFail(t, []string{"OWNGIT_RELEASES=http://127.0.0.1:1/releases"}, "must be an https:// address", "--to", target)
		run.mustFail(t, nil, "takes a release number", "--version", "1.2", "--to", target)
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("a refused run left %s", target)
		}
	})
}

// psInstall is one run of install.ps1 in Windows PowerShell or PowerShell 7,
// which trusts the synthetic release's certificate for this run only.
type psInstall struct {
	shell, home, log string
	env              []string
	thumb            string
}

func newPsInstall(t *testing.T, release *syntheticRelease, shell string) *psInstall {
	home := t.TempDir()
	sum := sha1.Sum(release.server.Certificate().Raw)
	run := &psInstall{shell: shell, home: home, log: filepath.Join(home, "owngit.log"), thumb: strings.ToUpper(hex.EncodeToString(sum[:]))}
	run.env = append(os.Environ(),
		"LOCALAPPDATA="+home,
		"OWNGIT_RELEASES="+release.url(), "OWNGIT_FAKE_LOG="+run.log)
	return run
}

func psQuote(word string) string { return "'" + strings.ReplaceAll(word, "'", "''") + "'" }

// do runs the installer as "irm ... | iex" does without arguments, and as
// "& ([scriptblock]::Create(...)) ARGS" with them.
func (run *psInstall) do(t *testing.T, env []string, arguments ...string) (string, error) {
	t.Helper()
	script := psQuote(filepath.Join(repoRoot(t), "packaging", "installer", "install.ps1"))
	invoke := "[IO.File]::ReadAllText(" + script + ") | Invoke-Expression"
	if len(arguments) > 0 {
		invoke = "& ([scriptblock]::Create([IO.File]::ReadAllText(" + script + "))) " + strings.Join(arguments, " ")
	}
	return run.powerShell(env, trustTestCertificate+"[OwnGitTestTrust]::Thumb = '"+run.thumb+"'; "+invoke)
}

// trustTestCertificate makes .NET accept the synthetic release's certificate
// in this PowerShell only. The callback is compiled, because PowerShell 7
// calls it on a thread where a script block cannot run, and the delegate is
// made in C#, because Windows PowerShell 5.1 cannot convert a method to one.
const trustTestCertificate = `Add-Type -TypeDefinition 'public static class OwnGitTestTrust {
  public static string Thumb;
  public static readonly System.Net.Security.RemoteCertificateValidationCallback Callback = Check;
  static bool Check(object sender, System.Security.Cryptography.X509Certificates.X509Certificate certificate, System.Security.Cryptography.X509Certificates.X509Chain chain, System.Net.Security.SslPolicyErrors errors) { return certificate.GetCertHashString() == Thumb; }
}'
[Net.ServicePointManager]::ServerCertificateValidationCallback = [OwnGitTestTrust]::Callback
`

// powerShell runs a command in the environment of this run.
func (run *psInstall) powerShell(env []string, script string) (string, error) {
	command := exec.Command(run.shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.Env = append(append([]string{}, run.env...), env...)
	command.Dir = run.home
	output, err := command.CombinedOutput()
	return string(output), err
}

func (run *psInstall) must(t *testing.T, env []string, arguments ...string) string {
	t.Helper()
	output, err := run.do(t, env, arguments...)
	if err != nil {
		t.Fatalf("install.ps1 %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return output
}

// mustFailWith checks a refused run: exit code 1, and output that starts
// exactly with want (the installer's own lines and its one-line message,
// kept whole so a command in it can be copied), followed only by
// PowerShell's record of "OwnGit was not installed."
func (run *psInstall) mustFailWith(t *testing.T, env []string, want string, arguments ...string) {
	t.Helper()
	output, err := run.do(t, env, arguments...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("install.ps1 %s: %v, want exit code 1:\n%s", strings.Join(arguments, " "), err, output)
	}
	got := strings.ReplaceAll(output, "\r\n", "\n")
	rest, found := strings.CutPrefix(got, want)
	if !found || !strings.Contains(rest, "OwnGit was not installed.") || strings.Contains(rest, "owngit install:") {
		t.Fatalf("install.ps1 %s printed:\n%s\nwant it to start with:\n%s", strings.Join(arguments, " "), got, want)
	}
}

func (run *psInstall) mustFail(t *testing.T, env []string, want string, arguments ...string) {
	t.Helper()
	output, err := run.do(t, env, arguments...)
	if err == nil {
		t.Fatalf("install.ps1 %s succeeded:\n%s", strings.Join(arguments, " "), output)
	}
	// PowerShell wraps long error lines.
	if !strings.Contains(strings.Join(strings.Fields(output), " "), want) {
		t.Fatalf("install.ps1 %s failed without %q:\n%s", strings.Join(arguments, " "), want, output)
	}
}

func TestInstallPs1(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 is for Windows; TestInstallSh covers Linux and macOS")
	}
	requireGoToolchain(t)
	if runtime.GOARCH != "amd64" {
		t.Skip("OwnGit for Windows is released for x64")
	}
	release := newSyntheticRelease(t, "1.0.0", "2.0.0")
	// Every case runs in Windows PowerShell 5.1 and in PowerShell 7.
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) { installPs1Cases(t, release, shell) })
}

func installPs1Cases(t *testing.T, release *syntheticRelease, shell string) {
	folder := func(dir, version string) string {
		return filepath.Join(dir, "owngit_"+version+"_windows_amd64")
	}

	t.Run("fresh install through iex takes the latest release and installs the service", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		dir := filepath.Join(run.home, "Programs", "OwnGit")
		output := run.must(t, nil)
		program := filepath.Join(folder(dir, "2.0.0"), "owngit.exe")
		if got := release.versionOf(t, program); got != "2.0.0" {
			t.Fatalf("installed %q, want 2.0.0:\n%s", got, output)
		}
		if _, err := os.Stat(filepath.Join(folder(dir, "2.0.0"), "README.txt")); err != nil {
			t.Errorf("the release folder lacks the rest of the archive: %v", err)
		}
		if log := readLog(t, run.log); log != "2.0.0 service install\n" {
			t.Errorf("owngit ran as %q:\n%s", log, output)
		}
		if names := dirNames(t, dir); len(names) != 1 {
			t.Errorf("%s holds %v, want only the release folder", dir, names)
		}
		// A rerun finds the same release and installs the service again.
		output = run.must(t, nil)
		if !strings.Contains(output, "OwnGit 2.0.0 is already in "+folder(dir, "2.0.0")) {
			t.Errorf("the rerun did not recognise the release:\n%s", output)
		}
		if log := readLog(t, run.log); log != "2.0.0 service install\n2.0.0 service install\n" {
			t.Errorf("owngit ran as %q", log)
		}
	})

	t.Run("pinned version gets its own folder", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		dir := filepath.Join(run.home, "og")
		run.must(t, nil, "-Dir", psQuote(dir))
		run.must(t, nil, "-Version", "1.0.0", "-Dir", psQuote(dir))
		if release.versionOf(t, filepath.Join(folder(dir, "1.0.0"), "owngit.exe")) != "1.0.0" || release.versionOf(t, filepath.Join(folder(dir, "2.0.0"), "owngit.exe")) != "2.0.0" {
			t.Fatalf("%s holds %v", dir, dirNames(t, dir))
		}
		if log := readLog(t, run.log); log != "2.0.0 service install\n1.0.0 service install\n" {
			t.Errorf("owngit ran as %q", log)
		}
	})

	t.Run("a mismatched or failed download changes nothing", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		dir := filepath.Join(run.home, "og")
		run.must(t, nil, "-Version", "1.0.0", "-Dir", psQuote(dir))
		path := "v2.0.0/" + release.archive["2.0.0"]
		release.replace(t, path, append(append([]byte{}, release.files[path]...), 0))
		run.mustFail(t, nil, "does not match the release's SHA256SUMS", "-Dir", psQuote(dir))
		release.replace(t, path, nil)
		run.mustFail(t, nil, "Could not download", "-Dir", psQuote(dir))
		if names := dirNames(t, dir); strings.Join(names, ",") != "owngit_1.0.0_windows_amd64" {
			t.Fatalf("%s holds %v, want only the 1.0.0 folder", dir, names)
		}
		if log := readLog(t, run.log); log != "1.0.0 service install\n" {
			t.Errorf("owngit ran as %q", log)
		}
	})

	// A failed install fails whatever runs it, however PowerShell was
	// started: a script file (-File) stops at the installer and exits with
	// 1, as -Command does.
	t.Run("a failure fails the command under -File and -Command", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		script := psQuote(filepath.Join(repoRoot(t), "packaging", "installer", "install.ps1"))
		wrapper := filepath.Join(run.home, "wrapper.ps1")
		body := trustTestCertificate + "[OwnGitTestTrust]::Thumb = '" + run.thumb + "'\n" +
			"[IO.File]::ReadAllText(" + script + ") | Invoke-Expression\n'the wrapper went on'\n"
		noErr(t, os.WriteFile(wrapper, []byte(body), 0o644))
		for _, mode := range [][]string{{"-File", wrapper}, {"-Command", "& " + psQuote(wrapper)}} {
			command := exec.Command(run.shell, append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass"}, mode...)...)
			command.Env = append(append([]string{}, run.env...), "OWNGIT_RELEASES=https://127.0.0.1:1/releases")
			command.Dir = run.home
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Errorf("%s: %v, want exit code 1:\n%s", mode[0], err, output)
			}
			if !strings.Contains(string(output), "owngit install: Could not download https://127.0.0.1:1/releases/latest/download/SHA256SUMS") || strings.Contains(string(output), "the wrapper went on") {
				t.Errorf("%s printed:\n%s", mode[0], output)
			}
		}
	})

	t.Run("a redirect to plain HTTP is refused", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		dir := filepath.Join(run.home, "og")
		path := "v2.0.0/" + release.archive["2.0.0"]
		plain := release.redirectToHTTP(t, path)
		run.mustFailWith(t, nil, "owngit install: Could not download "+release.url()+"/download/"+path+" (it leads to "+plain+", which is not HTTPS). Nothing was changed.\n", "-Dir", psQuote(dir))
		if names := dirNames(t, dir); len(names) != 0 {
			t.Fatalf("%s holds %v", dir, names)
		}
	})

	// Another account must not be able to change -Dir, a folder on the way
	// to it, or TEMP. BUILTIN\Users stands in for that account.
	t.Run("folders another account can change are refused before downloading", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		icacls := func(path string, grant string) {
			if output, err := exec.Command("icacls", path, "/grant", "*S-1-5-32-545:"+grant).CombinedOutput(); err != nil {
				t.Fatalf("icacls %s: %v\n%s", path, err, output)
			}
		}
		shared := filepath.Join(run.home, "shared")
		noErr(t, os.Mkdir(shared, 0o755))
		icacls(shared, "(OI)(CI)M")
		above := filepath.Join(run.home, "above")
		below := filepath.Join(above, "private")
		noErr(t, os.MkdirAll(below, 0o755))
		icacls(above, "(DC)")
		before := release.served()
		run.mustFail(t, nil, "Another account can change "+shared+" (BUILTIN\\Users can change it); choose a folder", "-Dir", psQuote(shared))
		run.mustFail(t, nil, "Another account can change "+shared+" (BUILTIN\\Users can change it); choose a folder", "-Dir", psQuote(filepath.Join(shared, "new")))
		run.mustFail(t, nil, "Another account can change "+above+" (BUILTIN\\Users can change it); choose a folder", "-Dir", psQuote(below))
		run.mustFail(t, []string{"TEMP=" + shared, "TMP=" + shared}, "Another account can change "+shared+" (BUILTIN\\Users can change it); set TEMP", "-Dir", psQuote(filepath.Join(run.home, "og")))
		if served := release.served(); served != before {
			t.Errorf("refused runs downloaded %d files", served-before)
		}
		if names := dirNames(t, shared); len(names) != 0 {
			t.Errorf("%s holds %v", shared, names)
		}
	})

	t.Run("no service installs the program only", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		dir := filepath.Join(run.home, "og")
		output := run.must(t, nil, "-NoService", "-Dir", psQuote(dir))
		program := filepath.Join(folder(dir, "2.0.0"), "owngit.exe")
		if release.versionOf(t, program) != "2.0.0" {
			t.Fatalf("not installed:\n%s", output)
		}
		if log := readLog(t, run.log); log != "" {
			t.Errorf("owngit ran as %q with -NoService", log)
		}
		for _, want := range []string{"& " + psQuote(program) + " serve", "& " + psQuote(program) + " service install"} {
			if !strings.Contains(output, want) {
				t.Errorf("output lacks %q:\n%s", want, output)
			}
		}
	})

	// The same failure through iex and through the script block: the
	// installer's lines and one plain message, and a failed command.
	t.Run("a failed service install is a failure", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		failing := []string{"OWNGIT_FAKE_FAIL=service install"}
		want := func(dir string) string {
			release := folder(dir, "2.0.0")
			return "Installed OwnGit 2.0.0 in " + release + ".\n" +
				"fake owngit 2.0.0: service install\n" +
				"owngit install: \"owngit service install\" did not finish. OwnGit 2.0.0 stays in " + release +
				"; after fixing what it reported, run: & " + psQuote(filepath.Join(release, "owngit.exe")) + " service install\n"
		}
		run.mustFailWith(t, failing, want(filepath.Join(run.home, "Programs", "OwnGit")))
		dir := filepath.Join(run.home, "og")
		run.mustFailWith(t, failing, want(dir), "-Dir", psQuote(dir))
	})

	t.Run("brackets in the folder are literal", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		dir := filepath.Join(run.home, "O’Brien [x]")
		decoy := filepath.Join(run.home, "O’Brien x")
		noErr(t, os.Mkdir(decoy, 0o755))
		run.must(t, nil, "-NoService", "-Dir", psQuote(strings.ReplaceAll(dir, "’", "’’")))
		if release.versionOf(t, filepath.Join(folder(dir, "2.0.0"), "owngit.exe")) != "2.0.0" {
			t.Fatalf("not installed in %s; home holds %v", dir, dirNames(t, run.home))
		}
		if names := dirNames(t, decoy); len(names) != 0 {
			t.Errorf("the decoy folder got %v", names)
		}
	})

	// A drive root stays a root: "owngit update" prints -Dir 'D:\' for a
	// program unpacked at a drive root, and D: alone would mean the current
	// folder of that drive. A free drive letter is mapped to a temporary
	// folder for the test.
	t.Run("a drive root stays a root", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		letter := ""
		for drive := 'Z'; drive >= 'H'; drive-- {
			if _, err := os.Stat(string(drive) + `:\`); err != nil {
				letter = string(drive) + ":"
				break
			}
		}
		if letter == "" {
			t.Fatal("no free drive letter")
		}
		mapped := t.TempDir()
		if output, err := exec.Command("subst", letter, mapped).CombinedOutput(); err != nil {
			t.Fatalf("subst %s: %v\n%s", letter, err, output)
		}
		t.Cleanup(func() { exec.Command("subst", letter, "/D").Run() })
		output := run.must(t, nil, "-NoService", "-Dir", psQuote(letter+`\`))
		if got := release.versionOf(t, filepath.Join(folder(mapped, "2.0.0"), "owngit.exe")); got != "2.0.0" {
			t.Fatalf("not installed at the root of %s (%s holds %v):\n%s", letter, mapped, dirNames(t, mapped), output)
		}
		if !strings.Contains(output, "Installed OwnGit 2.0.0 in "+letter+`\owngit_2.0.0_windows_amd64.`) {
			t.Errorf("output:\n%s", output)
		}
	})

	// The rule is the OwnGit folder of the 64-bit Program Files, which
	// ProgramW6432 names (ProgramFiles in a 64-bit PowerShell). The test
	// points both at a temporary folder and first proves that PowerShell
	// sees them, so it never touches the real Program Files; the release
	// address is unreachable, so a failed rule would stop at the download.
	t.Run("refuses the service folder in Program Files", func(t *testing.T) {
		run := newPsInstall(t, release, shell)
		programFiles := filepath.Join(run.home, "PF")
		unreachable := "OWNGIT_RELEASES=https://127.0.0.1:1/releases"
		env := []string{"ProgramW6432=" + programFiles, "ProgramFiles=" + programFiles, unreachable}
		seen, err := run.powerShell(env, "$env:ProgramW6432; $env:ProgramFiles")
		if want := programFiles + "\n" + programFiles + "\n"; err != nil || strings.ReplaceAll(seen, "\r\n", "\n") != want {
			t.Fatalf("PowerShell sees ProgramW6432 and ProgramFiles as %q (%v), not %q; the rule cannot be tested without the real Program Files", seen, err, programFiles)
		}
		serviceFolder := filepath.Join(programFiles, "OwnGit")
		refused := "owngit install: " + serviceFolder + " belongs to \"owngit service install\"; choose another -Dir.\n"
		for _, dir := range []string{
			serviceFolder,
			filepath.Join(serviceFolder, "releases"),
			strings.ToLower(filepath.ToSlash(serviceFolder)) + "/",
		} {
			run.mustFailWith(t, env, refused, "-Dir", psQuote(dir))
		}
		// ProgramW6432 comes first, as for a 32-bit PowerShell whose
		// ProgramFiles names Program Files (x86).
		x86 := []string{"ProgramW6432=" + programFiles, "ProgramFiles=" + filepath.Join(run.home, "PF86"), unreachable}
		run.mustFailWith(t, x86, refused, "-Dir", psQuote(serviceFolder))
		// A folder that only starts with the same name is not the service folder.
		run.mustFail(t, env, "Could not download", "-Dir", psQuote(serviceFolder+"-other"))
		if names := dirNames(t, programFiles); len(names) != 0 {
			t.Fatalf("a refused run created %v in %s", names, programFiles)
		}
	})
}

// Every published document and script shows the one-line installers only
// in their safe forms: curl and sh from the system folders, HTTPS for every
// redirect, and no redirect for irm. The example commands at the top of
// each script are the ones in OPERATIONS, as they are.
func TestInstallerCommandsAreTheSafeForms(t *testing.T) {
	root := repoRoot(t)
	operations := readText(t, filepath.Join(root, "docs", "OPERATIONS.md"))
	checked := 0
	noErr(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(name) {
		case ".md", ".sh", ".ps1":
		default:
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		for number, line := range strings.Split(readText(t, path), "\n") {
			shell := strings.Contains(line, "curl") && strings.Contains(line, "install.sh")
			powerShell := strings.Contains(line, "install.ps1") && (strings.Contains(line, "irm") || strings.Contains(line, "Invoke-RestMethod") || strings.Contains(line, "Invoke-WebRequest") || strings.Contains(line, "iwr ") || strings.Contains(line, "DownloadString"))
			switch {
			case shell && (!strings.Contains(line, "/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL ") || !strings.Contains(line, "| /bin/sh")),
				powerShell && (!strings.Contains(line, "irm -MaximumRedirection 0 ") || strings.Contains(line, "DownloadString")):
				t.Errorf("%s:%d shows an unsafe installer command: %s", relative, number+1, line)
			case shell || powerShell:
				checked++
			}
		}
		if filepath.Dir(relative) == filepath.Join("packaging", "installer") {
			for _, line := range strings.Split(readText(t, path), "\n") {
				if command, found := strings.CutPrefix(line, "#   "); found && !strings.Contains(operations, "\n"+command+"\n") {
					t.Errorf("%s shows %q, which is not a line of docs/OPERATIONS.md", relative, command)
				}
			}
		}
		return nil
	}))
	if checked < 8 {
		t.Errorf("found only %d installer commands; the check is not reading the documents", checked)
	}
}
