package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// macOSGitShim is the trampoline that macOS installs as /usr/bin/git. Every
// call starts xcrun, which then starts the real Git. Tests replace the path,
// the platform and the probe timeout.
var (
	macOSGitShim     = "/usr/bin/git"
	gitShimGOOS      = runtime.GOOS
	shimProbeTimeout = 5 * time.Second
)

// Values of Runner.GitSource.
const (
	gitSourceFlag   = "given with --git"
	gitSourcePath   = "found on PATH"
	gitSourceDirect = "found on PATH; the Git behind the macOS /usr/bin/git shim, same version"
	gitSourceKept   = "found on PATH; kept the macOS /usr/bin/git shim: "
)

// preferGitBehindShim returns the Git executable to use for found, a Git that
// New located on PATH, and a note for the startup log. On macOS, when found
// resolves to the /usr/bin/git shim, it returns <exec-path>/git instead if
// that is an executable that reports exactly the same version. Every probe
// runs in the runner's isolated environment with a short timeout, and any
// failure keeps found.
func (r *Runner) preferGitBehindShim(found string) (string, string) {
	if gitShimGOOS != "darwin" || !sameFile(found, macOSGitShim) {
		return found, gitSourcePath
	}
	keep := func(reason string) (string, string) { return found, gitSourceKept + reason }
	probe := func(runner *Runner, args ...string) (string, error) {
		result, err := runner.RunWithLimits(context.Background(), "", nil, CommandLimits{Timeout: shimProbeTimeout, OutputLimit: 4096}, args...)
		return strings.TrimSpace(string(result.Stdout)), err
	}
	execPath, err := probe(r, "--exec-path")
	if err != nil {
		return keep("git --exec-path failed")
	}
	if !filepath.IsAbs(execPath) {
		return keep("git --exec-path did not report an absolute path")
	}
	candidate := filepath.Join(execPath, "git")
	info, err := os.Stat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return keep("no executable git in " + execPath)
	}
	if sameFile(candidate, macOSGitShim) {
		return keep("the exec path holds the shim itself")
	}
	shimVersion, err := probe(r, "--version")
	if err != nil || !strings.HasPrefix(shimVersion, "git version ") {
		return keep("git --version failed")
	}
	direct := *r
	direct.GitPath = candidate
	directVersion, err := probe(&direct, "--version")
	if err != nil || directVersion != shimVersion {
		return keep(candidate + " reports a different version")
	}
	return candidate, gitSourceDirect
}

// sameFile reports whether both paths resolve to the same existing file.
func sameFile(first, second string) bool {
	firstInfo, err := os.Stat(first)
	if err != nil {
		return false
	}
	secondInfo, err := os.Stat(second)
	return err == nil && os.SameFile(firstInfo, secondInfo)
}
