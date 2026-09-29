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
		files: map[string][]byte{}, override: map[string][]byte{},
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
	release.mu.Unlock()
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
	home, log, sudoLog string
	env                []string
}

func newShInstall(t *testing.T, release *syntheticRelease) *shInstall {
	home := t.TempDir()
	run := &shInstall{home: home, log: filepath.Join(home, "owngit.log"), sudoLog: filepath.Join(home, "sudo.log")}
	fakeBin := filepath.Join(home, "fakebin")
	noErr(t, os.Mkdir(fakeBin, 0o755))
	// sudo lets the command write the folder the test locked, runs it,
	// and records it.
	sudo := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$FAKE_SUDO_LOG\"\nchmod u+w \"$FAKE_SUDO_DIR\"\n\"$@\"; status=$?\nchmod u-w \"$FAKE_SUDO_DIR\"\nexit $status\n"
	noErr(t, os.WriteFile(filepath.Join(fakeBin, "sudo"), []byte(sudo), 0o755))
	run.env = []string{
		"HOME=" + home, "PATH=" + fakeBin + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"OWNGIT_RELEASES=" + release.url(), "CURL_CA_BUNDLE=" + release.caFile,
		"OWNGIT_FAKE_LOG=" + run.log, "FAKE_SUDO_LOG=" + run.sudoLog,
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
		if log := readLog(t, run.sudoLog); log != "" {
			t.Errorf("sudo ran for a folder this account can write: %q", log)
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

	t.Run("a failed service install is a failure and keeps the new program", func(t *testing.T) {
		run := newShInstall(t, release)
		target := filepath.Join(run.home, "bin", "owngit")
		run.mustFail(t, []string{"OWNGIT_FAKE_FAIL=service install"}, "OwnGit 2.0.0 stays at "+target, "--to", target)
		if got := release.versionOf(t, target); got != "2.0.0" {
			t.Fatalf("installed %q, want 2.0.0", got)
		}
	})

	t.Run("sudo only for a folder this account cannot write", func(t *testing.T) {
		if root {
			t.Skip("root can write every folder")
		}
		run := newShInstall(t, release)
		dir := filepath.Join(run.home, "locked")
		target := filepath.Join(dir, "owngit")
		run.must(t, nil, "--version", "1.0.0", "--to", target)
		noErr(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		output := run.must(t, []string{"FAKE_SUDO_DIR=" + dir}, "--to", target)
		if got := release.versionOf(t, target); got != "2.0.0" {
			t.Fatalf("installed %q, want 2.0.0:\n%s", got, output)
		}
		if !strings.Contains(output, "This account cannot write "+dir+", so sudo puts owngit there.") {
			t.Errorf("output does not say why sudo runs:\n%s", output)
		}
		sudo := readLog(t, run.sudoLog)
		if !strings.Contains(sudo, "install -m 0755 ") || !strings.Contains(sudo, "mv -f ") || strings.Contains(sudo, "service") {
			t.Errorf("sudo ran %q; want install -m 0755 and mv, and never the service", sudo)
		}
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

// psInstall is one run of install.ps1 in Windows PowerShell, which trusts
// the synthetic release's certificate for this run only.
type psInstall struct {
	home, log string
	env       []string
	thumb     string
}

func newPsInstall(t *testing.T, release *syntheticRelease) *psInstall {
	home := t.TempDir()
	sum := sha1.Sum(release.server.Certificate().Raw)
	run := &psInstall{home: home, log: filepath.Join(home, "owngit.log"), thumb: strings.ToUpper(hex.EncodeToString(sum[:]))}
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
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
		"[Net.ServicePointManager]::ServerCertificateValidationCallback = { param($s, $c) $c.GetCertHashString() -eq '"+run.thumb+"' }; "+invoke)
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

// mustFailWith checks the whole output of a refused run, which is exactly
// want: the installer's own lines and its message, without PowerShell's
// error record, and exit code 1.
func (run *psInstall) mustFailWith(t *testing.T, env []string, want string, arguments ...string) {
	t.Helper()
	output, err := run.do(t, env, arguments...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("install.ps1 %s: %v, want exit code 1:\n%s", strings.Join(arguments, " "), err, output)
	}
	if got := strings.ReplaceAll(output, "\r\n", "\n"); got != want {
		t.Fatalf("install.ps1 %s printed:\n%s\nwant:\n%s", strings.Join(arguments, " "), got, want)
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
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skipf("needs Windows PowerShell: %v", err)
	}
	release := newSyntheticRelease(t, "1.0.0", "2.0.0")
	folder := func(dir, version string) string {
		return filepath.Join(dir, "owngit_"+version+"_windows_amd64")
	}

	t.Run("fresh install through iex takes the latest release and installs the service", func(t *testing.T) {
		run := newPsInstall(t, release)
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
		run := newPsInstall(t, release)
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
		run := newPsInstall(t, release)
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

	t.Run("no service installs the program only", func(t *testing.T) {
		run := newPsInstall(t, release)
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
		run := newPsInstall(t, release)
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
		run := newPsInstall(t, release)
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

	// The rule is the OwnGit folder of the real Program Files known folder,
	// whatever case or separators -Dir uses; the ProgramFiles variable does
	// not move it. The release address is unreachable, so if the rule
	// failed the run would stop at the download before writing anything
	// into Program Files.
	t.Run("refuses the service folder in Program Files", func(t *testing.T) {
		run := newPsInstall(t, release)
		programFiles := os.Getenv("ProgramFiles")
		if programFiles == "" {
			t.Fatal("no ProgramFiles folder")
		}
		serviceFolder := filepath.Join(programFiles, "OwnGit")
		refused := "owngit install: " + serviceFolder + " belongs to \"owngit service install\"; choose another -Dir.\n"
		unreachable := []string{"OWNGIT_RELEASES=https://127.0.0.1:1/releases"}
		for _, dir := range []string{
			serviceFolder,
			filepath.Join(serviceFolder, "releases"),
			strings.ToLower(filepath.ToSlash(serviceFolder)) + "/",
		} {
			run.mustFailWith(t, unreachable, refused, "-Dir", psQuote(dir))
		}
		moved := append([]string{"ProgramFiles=" + filepath.Join(run.home, "elsewhere")}, unreachable...)
		run.mustFailWith(t, moved, refused, "-Dir", psQuote(serviceFolder))
		// A folder that only starts with the same name is not the service folder.
		run.mustFail(t, unreachable, "Could not download", "-Dir", psQuote(serviceFolder+"-other"))
		if created, _ := filepath.Glob(filepath.Join(serviceFolder+"*", "owngit_*_windows_amd64")); len(created) != 0 {
			t.Fatalf("a refused run created %v", created)
		}
	})
}
