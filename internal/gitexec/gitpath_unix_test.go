//go:build !windows

package gitexec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGit writes an executable shell script that answers --exec-path and
// --version and records every call in a log beside it. Runner environments
// are isolated, so the answers are baked into the script.
func fakeGit(t *testing.T, directory, execPath, version string) string {
	t.Helper()
	noErr(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, "git")
	script := fmt.Sprintf(`#!/bin/sh
echo "$1" >>%q
case "$1" in
  --exec-path) printf '%%s\n' %q ;;
  --version) printf '%%s\n' %q ;;
  *) exit 2 ;;
esac
`, path+".calls", execPath, version)
	noErr(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}

func fakeGitCalls(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path + ".calls")
	if os.IsNotExist(err) {
		return ""
	}
	noErr(t, err)
	return strings.TrimSpace(string(content))
}

// useFakeShim makes path the macOS shim on a darwin platform and puts its
// directory alone on PATH.
func useFakeShim(t *testing.T, path string) {
	t.Helper()
	originalShim, originalGOOS, originalTimeout := macOSGitShim, gitShimGOOS, shimProbeTimeout
	macOSGitShim, gitShimGOOS, shimProbeTimeout = path, "darwin", 2*time.Second
	t.Cleanup(func() { macOSGitShim, gitShimGOOS, shimProbeTimeout = originalShim, originalGOOS, originalTimeout })
	t.Setenv("PATH", filepath.Dir(path))
}

const fakeVersion = "git version 2.54.0 (Apple Git-157)"

func TestNewUsesSameVersionGitBehindMacOSShim(t *testing.T) {
	root := t.TempDir()
	execPath := filepath.Join(root, "libexec", "git-core")
	direct := fakeGit(t, execPath, execPath, fakeVersion)
	shim := fakeGit(t, filepath.Join(root, "usr-bin"), execPath, fakeVersion)
	useFakeShim(t, shim)

	runner, err := New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	if runner.GitPath != direct || runner.GitSource != gitSourceDirect {
		t.Fatalf("GitPath=%q source=%q, want %q behind the shim", runner.GitPath, runner.GitSource, direct)
	}
	if calls := fakeGitCalls(t, shim); calls != "--exec-path\n--version" {
		t.Fatalf("shim calls=%q", calls)
	}
	if calls := fakeGitCalls(t, direct); calls != "--version" {
		t.Fatalf("direct calls=%q", calls)
	}
}

func TestNewFollowsSymlinkToMacOSShim(t *testing.T) {
	root := t.TempDir()
	execPath := filepath.Join(root, "libexec", "git-core")
	direct := fakeGit(t, execPath, execPath, fakeVersion)
	shim := fakeGit(t, filepath.Join(root, "usr-bin"), execPath, fakeVersion)
	useFakeShim(t, shim)
	linkDirectory := filepath.Join(root, "links")
	noErr(t, os.Mkdir(linkDirectory, 0o700))
	noErr(t, os.Symlink(shim, filepath.Join(linkDirectory, "git")))
	t.Setenv("PATH", linkDirectory)

	runner, err := New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	if runner.GitPath != direct {
		t.Fatalf("GitPath=%q, want %q", runner.GitPath, direct)
	}
}

func TestNewKeepsMacOSShimWhenTheGitBehindItIsUnsuitable(t *testing.T) {
	cases := map[string]func(t *testing.T, root, execPath string) (shim string){
		"different version": func(t *testing.T, root, execPath string) string {
			fakeGit(t, execPath, execPath, "git version 2.53.0")
			return fakeGit(t, filepath.Join(root, "usr-bin"), execPath, fakeVersion)
		},
		"no git in exec path": func(t *testing.T, root, execPath string) string {
			noErr(t, os.MkdirAll(execPath, 0o700))
			return fakeGit(t, filepath.Join(root, "usr-bin"), execPath, fakeVersion)
		},
		"git in exec path is not executable": func(t *testing.T, root, execPath string) string {
			direct := fakeGit(t, execPath, execPath, fakeVersion)
			noErr(t, os.Chmod(direct, 0o600))
			return fakeGit(t, filepath.Join(root, "usr-bin"), execPath, fakeVersion)
		},
		"git in exec path is a directory": func(t *testing.T, root, execPath string) string {
			noErr(t, os.MkdirAll(filepath.Join(execPath, "git"), 0o700))
			return fakeGit(t, filepath.Join(root, "usr-bin"), execPath, fakeVersion)
		},
		"relative exec path": func(t *testing.T, root, execPath string) string {
			return fakeGit(t, filepath.Join(root, "usr-bin"), "libexec/git-core", fakeVersion)
		},
		"exec path holds the shim": func(t *testing.T, root, execPath string) string {
			directory := filepath.Join(root, "usr-bin")
			return fakeGit(t, directory, directory, fakeVersion)
		},
		"exec path fails": func(t *testing.T, root, execPath string) string {
			fakeGit(t, execPath, execPath, fakeVersion)
			return writeScript(t, filepath.Join(root, "usr-bin"), "#!/bin/sh\nexit 1\n")
		},
		"direct version fails": func(t *testing.T, root, execPath string) string {
			writeScript(t, execPath, "#!/bin/sh\nexit 1\n")
			return fakeGit(t, filepath.Join(root, "usr-bin"), execPath, fakeVersion)
		},
		"shim hangs": func(t *testing.T, root, execPath string) string {
			fakeGit(t, execPath, execPath, fakeVersion)
			shimProbeTimeout = 200 * time.Millisecond
			return writeScript(t, filepath.Join(root, "usr-bin"), "#!/bin/sh\nexec sleep 30\n")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			execPath := filepath.Join(root, "libexec", "git-core")
			useFakeShim(t, filepath.Join(root, "usr-bin", "git"))
			shim := setup(t, root, execPath)
			started := time.Now()
			runner, err := New("", filepath.Join(root, "runtime"))
			noErr(t, err)
			if runner.GitPath != shim || !strings.HasPrefix(runner.GitSource, gitSourceKept) {
				t.Fatalf("GitPath=%q source=%q, want the shim %q kept", runner.GitPath, runner.GitSource, shim)
			}
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("New took %s", elapsed)
			}
		})
	}
}

func TestNewNeverReplacesExplicitOrOtherGit(t *testing.T) {
	root := t.TempDir()
	execPath := filepath.Join(root, "libexec", "git-core")
	direct := fakeGit(t, execPath, execPath, fakeVersion)
	shim := fakeGit(t, filepath.Join(root, "usr-bin"), execPath, fakeVersion)
	useFakeShim(t, shim)

	explicit, err := New(shim, filepath.Join(root, "runtime"))
	noErr(t, err)
	if explicit.GitPath != shim || explicit.GitSource != gitSourceFlag {
		t.Fatalf("explicit GitPath=%q source=%q", explicit.GitPath, explicit.GitSource)
	}

	other := fakeGit(t, filepath.Join(root, "homebrew"), execPath, fakeVersion)
	t.Setenv("PATH", filepath.Dir(other))
	found, err := New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	if found.GitPath != other || found.GitSource != gitSourcePath {
		t.Fatalf("PATH GitPath=%q source=%q", found.GitPath, found.GitSource)
	}

	t.Setenv("PATH", filepath.Dir(shim))
	gitShimGOOS = "linux"
	elsewhere, err := New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	if elsewhere.GitPath != shim || elsewhere.GitSource != gitSourcePath {
		t.Fatalf("non-macOS GitPath=%q source=%q", elsewhere.GitPath, elsewhere.GitSource)
	}

	for _, path := range []string{shim, direct, other} {
		if calls := fakeGitCalls(t, path); calls != "" {
			t.Fatalf("%s ran %q; no probe may run", path, calls)
		}
	}
}

func writeScript(t *testing.T, directory, script string) string {
	t.Helper()
	noErr(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, "git")
	noErr(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}
