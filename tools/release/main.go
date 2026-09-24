// Command release builds and verifies OwnGit's portable distribution archives,
// builds unsigned native prototypes, and renders package-manager preparation.
//
// Portable archives and Debian prototypes use the Go toolchain and standard
// library only. The macOS prototype also invokes the installed Apple Swift and
// disk-image tools. The application version comes from internal/version, so no
// release version literal is maintained here.
//
// Commands:
//
//	release build     build every release target, archive it, and write checksums
//	release verify    re-check an existing output directory against its manifest
//	release notices   collect or verify third-party notices from the build inputs
//	release packaging render the Homebrew, WinGet, and npm preparation files
//	release native    build unsigned local macOS and Debian prototypes
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/version"
)

// fixedModTime is stamped on every archived entry so that two builds from the
// same inputs produce byte-identical archives. The value is the start of the
// MS-DOS timestamp epoch, which is the earliest time a zip entry can carry.
var fixedModTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	err := run(os.Args[1:])
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	fmt.Fprintf(os.Stderr, "release: %v\n", err)
	os.Exit(1)
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		usage(os.Stdout)
		return errors.New("missing command")
	}
	switch arguments[0] {
	case "build":
		return buildCommand(arguments[1:])
	case "verify":
		return verifyCommand(arguments[1:])
	case "notices":
		return noticesCommand(arguments[1:])
	case "packaging":
		return packagingCommand(arguments[1:])
	case "native":
		return nativeCommand(arguments[1:])
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func usage(writer io.Writer) {
	fmt.Fprint(writer, `Usage: release <command> [options]

Commands:
  build      build every release target, archive it, and write checksums
  verify     re-check an output directory against its manifest
  notices    collect or verify third-party notices from the build inputs
  packaging  render the Homebrew, WinGet, and npm preparation files
  native     build unsigned local macOS and Debian prototypes

Run "release <command> -h" for the options of one command.
`)
}

// moduleRoot resolves a source directory and requires it to be a Go module.
func moduleRoot(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(absolute, "go.mod")); err != nil {
		return "", fmt.Errorf("%s is not a Go module root: %w", absolute, err)
	}
	return absolute, nil
}

// runGo runs the Go toolchain in dir with extra environment entries and
// returns its standard output.
func runGo(goTool, dir string, extraEnv []string, arguments ...string) (string, error) {
	command := exec.Command(goTool, arguments...)
	command.Dir = dir
	command.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s %s: %s", goTool, strings.Join(arguments, " "), message)
	}
	return stdout.String(), nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func sha256Bytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// writeFile writes data with an explicit mode and reports the digest.
func writeFile(path string, data []byte, mode os.FileMode) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return "", err
	}
	// os.WriteFile keeps an existing file's mode, so set it explicitly.
	if err := os.Chmod(path, mode); err != nil {
		return "", err
	}
	return sha256Bytes(data), nil
}

// versionFromSource reads the single version literal from a source tree and
// checks it against the value this tool was compiled with. The two can differ
// when a prebuilt tool runs against another checkout.
func versionFromSource(root string) (string, error) {
	path := filepath.Join(root, "internal", "version", "version.go")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	declared := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "const Version = ") {
			continue
		}
		declared = strings.Trim(strings.TrimPrefix(line, "const Version = "), `"`)
	}
	if declared == "" {
		return "", fmt.Errorf("%s declares no Version constant", path)
	}
	if declared != version.Version {
		return "", fmt.Errorf("source declares version %q but this tool was built with %q", declared, version.Version)
	}
	return declared, nil
}

// goToolchainVersion reports the toolchain version that will build the
// artifacts, for example "go1.27.1".
func goToolchainVersion(goTool, dir string) (string, error) {
	output, err := runGo(goTool, dir, nil, "env", "GOVERSION")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// buildInfo returns the Go toolchain version and the "go version -m" metadata
// of a built binary without the leading file name line. It works for
// cross-compiled binaries.
func buildInfo(goTool, binary string) (string, string, error) {
	output, err := runGo(goTool, filepath.Dir(binary), nil, "version", "-m", binary)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 0 {
		return "", "", fmt.Errorf("%s carries no build metadata", binary)
	}
	goVersion := ""
	if at := strings.LastIndex(lines[0], ": "); at >= 0 {
		goVersion = lines[0][at+2:]
	}
	return goVersion, strings.Join(lines[1:], "\n"), nil
}

// vcsState reports the revision and dirty flag of a source tree that is itself
// a Git work tree root. A copy without .git reports no state.
func vcsState(root string) (revision string, modified bool, ok bool) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", false, false
	}
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false, false
	}
	revision = strings.TrimSpace(string(output))
	status, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return revision, false, true
	}
	return revision, strings.TrimSpace(string(status)) != "", true
}

// parseFlags parses a subcommand's flags and rejects stray arguments. A help
// request prints the subcommand's options and returns flag.ErrHelp, which main
// treats as success.
func parseFlags(set *flag.FlagSet, arguments []string) error {
	set.SetOutput(io.Discard)
	if err := set.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stdout, "Usage: release %s [options]\n\n", set.Name())
			set.SetOutput(os.Stdout)
			set.PrintDefaults()
			return flag.ErrHelp
		}
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments: %v", set.Name(), set.Args())
	}
	return nil
}
