package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// syntheticManifest writes a manifest with every release target and distinct
// digests. The aur format reads only names and digests, so no build is needed.
// change, when not nil, edits each artifact before it is written.
func syntheticManifest(t *testing.T, version string, change func(*artifact)) (string, map[string]artifact) {
	t.Helper()
	artifacts := map[string]artifact{}
	document := manifest{Version: version}
	for index, current := range releaseTargets {
		built := artifact{
			Target: current.String(),
			Name:   current.archiveName(version),
			SHA256: strings.Repeat(fmt.Sprintf("%x", index+10), 64),
			Size:   int64(1000 + index),
		}
		if change != nil {
			change(&built)
		}
		artifacts[built.Target] = built
		document.Artifacts = append(document.Artifacts, built)
	}
	data, err := json.Marshal(document)
	noErr(t, err)
	path := filepath.Join(t.TempDir(), "manifest.json")
	noErr(t, os.WriteFile(path, data, 0o644))
	return path, artifacts
}

var pkgbuildAssignment = regexp.MustCompile(`^([a-z0-9_]+)=(.*)$`)

// parsePKGBUILD reads the one-line assignments of a rendered PKGBUILD: plain
// words, single-quoted words with \' escapes between them, and arrays of them.
func parsePKGBUILD(t *testing.T, text string) map[string][]string {
	t.Helper()
	fields := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		match := pkgbuildAssignment.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		value := match[2]
		if strings.HasPrefix(value, "(") {
			if !strings.HasSuffix(value, ")") {
				t.Fatalf("array assignment spans lines: %q", line)
			}
			value = value[1 : len(value)-1]
		}
		fields[match[1]] = shellWords(t, value)
	}
	return fields
}

func shellWords(t *testing.T, text string) []string {
	t.Helper()
	var words []string
	var word strings.Builder
	inWord, quoted, escaped := false, false, false
	for _, r := range text {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case quoted && r == '\'':
			quoted = false
		case quoted:
			word.WriteRune(r)
		case r == '\'':
			quoted, inWord = true, true
		case r == '\\':
			escaped, inWord = true, true
		case r == '"' || r == '$':
			t.Fatalf("unexpected shell syntax in %q", text)
		case r == ' ':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if quoted || escaped {
		t.Fatalf("unterminated quote or escape in %q", text)
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// srcinfoFor writes the .SRCINFO that "makepkg --printsrcinfo" prints for
// the fields this PKGBUILD uses, in makepkg's attribute order.
func srcinfoFor(fields map[string][]string) string {
	var out strings.Builder
	write := func(key string) {
		for _, value := range fields[key] {
			fmt.Fprintf(&out, "\t%s = %s\n", key, value)
		}
	}
	fmt.Fprintf(&out, "pkgbase = %s\n", fields["pkgname"][0])
	for _, key := range []string{
		"pkgdesc", "pkgver", "pkgrel", "epoch", "url", "install", "changelog",
		"arch", "groups", "license", "checkdepends", "makedepends", "depends", "optdepends",
		"provides", "conflicts", "replaces", "noextract", "options", "backup",
		"source", "validpgpkeys", "sha256sums",
	} {
		write(key)
	}
	for _, arch := range fields["arch"] {
		for _, key := range []string{"source", "provides", "conflicts", "depends", "replaces", "optdepends", "makedepends", "checkdepends", "sha256sums"} {
			write(key + "_" + arch)
		}
	}
	fmt.Fprintf(&out, "\npkgname = %s\n", fields["pkgname"][0])
	return out.String()
}

func TestAURPackaging(t *testing.T) {
	root := repoRoot(t)
	version, err := versionFromSource(root)
	noErr(t, err)
	manifestPath, artifacts := syntheticManifest(t, version, nil)
	baseURL := "https://example.test/owngit/releases/download/v" + version
	// The quote checks the shell encoding of a URL that validation accepts.
	homepage := "https://example.test/own'git"
	ready := []string{
		"-source", root, "-manifest", manifestPath, "-formats", "aur", "-strict",
		"-base-url", baseURL, "-homepage", homepage,
		"-aur-maintainer", "Example Maintainer <maintainer at example dot test>",
	}

	out := t.TempDir()
	noErrf(t, packagingCommand(append(append([]string{}, ready...), "-out", out)), "strict aur render")
	pkgbuild := readText(t, filepath.Join(out, "aur", "PKGBUILD"))
	srcinfo := readText(t, filepath.Join(out, "aur", ".SRCINFO"))
	if !strings.HasPrefix(pkgbuild, "# Maintainer: Example Maintainer <maintainer at example dot test>\n") {
		t.Fatalf("PKGBUILD does not start with the maintainer line:\n%s", pkgbuild)
	}
	if strings.Contains(pkgbuild+srcinfo, "UNREADY") || strings.Contains(pkgbuild+srcinfo, "SKIP") {
		t.Fatal("a ready render carries UNREADY or SKIP")
	}
	for _, name := range []string{"owngit.rb", "OwnGit.Owngit.yaml", "npm"} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("an aur-only render wrote %s", name)
		}
	}

	fields := parsePKGBUILD(t, pkgbuild)
	want := map[string][]string{
		"pkgname":            {"owngit-bin"},
		"pkgver":             {version},
		"pkgrel":             {"1"},
		"url":                {homepage},
		"arch":               {"x86_64", "aarch64"},
		"license":            {"MIT"},
		"depends":            {"git"},
		"provides":           {"owngit"},
		"conflicts":          {"owngit"},
		"source_x86_64":      {baseURL + "/" + artifacts["linux/amd64"].Name},
		"sha256sums_x86_64":  {artifacts["linux/amd64"].SHA256},
		"source_aarch64":     {baseURL + "/" + artifacts["linux/arm64"].Name},
		"sha256sums_aarch64": {artifacts["linux/arm64"].SHA256},
	}
	for key, values := range want {
		if strings.Join(fields[key], "\n") != strings.Join(values, "\n") {
			t.Errorf("%s = %q, want %q", key, fields[key], values)
		}
	}
	if _, ok := fields["source"]; ok {
		t.Error("PKGBUILD has an architecture-independent source")
	}
	for _, line := range []string{
		`install -Dm755 owngit "$pkgdir/usr/bin/owngit"`,
		`install -Dm644 LICENSE "$pkgdir/usr/share/licenses/$pkgname/LICENSE"`,
		`local doc="$pkgdir/usr/share/doc/$pkgname"`,
	} {
		if !strings.Contains(pkgbuild, line) {
			t.Errorf("PKGBUILD lacks %q", line)
		}
	}
	if got := srcinfoFor(fields); srcinfo != got {
		t.Fatalf(".SRCINFO does not match the PKGBUILD fields\ngot:\n%s\nwant:\n%s", srcinfo, got)
	}

	t.Run("strict requires the maintainer", func(t *testing.T) {
		arguments := append([]string{}, ready[:len(ready)-2]...)
		err := packagingCommand(append(arguments, "-out", t.TempDir()))
		if err == nil || !strings.Contains(err.Error(), "missing required inputs: aur-maintainer") {
			t.Fatalf("strict render without a maintainer returned %v", err)
		}
	})

	t.Run("unready", func(t *testing.T) {
		out := t.TempDir()
		noErrf(t, packagingCommand([]string{"-source", root, "-manifest", manifestPath, "-formats", "aur", "-out", out}), "unready render")
		for _, name := range []string{"PKGBUILD", ".SRCINFO"} {
			text := readText(t, filepath.Join(out, "aur", name))
			if !strings.Contains(text, "UNREADY: missing input(s): aur-maintainer, base-url, homepage") {
				t.Fatalf("%s carries no UNREADY marker:\n%s", name, text)
			}
			if !strings.Contains(text, placeholderURL) {
				t.Fatalf("%s does not use the placeholder URL", name)
			}
		}
	})

	t.Run("unsafe manifest values are refused for every format", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			change func(*artifact)
			want   string
		}{
			{"shell in digest", func(built *artifact) {
				if built.Target == "linux/amd64" {
					built.SHA256 = "abc') ; touch /tmp/x ; ('"
				}
			}, "64 lowercase hexadecimal"},
			{"uppercase digest", func(built *artifact) {
				if built.Target == "linux/arm64" {
					built.SHA256 = strings.ToUpper(built.SHA256)
				}
			}, "64 lowercase hexadecimal"},
			{"short digest", func(built *artifact) {
				if built.Target == "windows/amd64" {
					built.SHA256 = built.SHA256[:63]
				}
			}, "64 lowercase hexadecimal"},
			{"archive name", func(built *artifact) {
				if built.Target == "darwin/arm64" {
					built.Name = `owngit"; system("true`
				}
			}, "manifest names the darwin/arm64 archive"},
		} {
			for _, format := range []string{"aur", "homebrew", "winget"} {
				t.Run(test.name+" "+format, func(t *testing.T) {
					path, _ := syntheticManifest(t, version, test.change)
					out := t.TempDir()
					err := packagingCommand([]string{
						"-source", root, "-manifest", path, "-formats", format, "-out", out,
						"-base-url", baseURL, "-homepage", "https://example.test/owngit",
					})
					if err == nil || !strings.Contains(err.Error(), test.want) {
						t.Fatalf("packaging returned %v, want an error containing %q", err, test.want)
					}
					if entries, _ := os.ReadDir(out); len(entries) != 0 {
						t.Fatalf("a refused manifest still wrote %d entries", len(entries))
					}
				})
			}
		}
	})

	t.Run("rejected maintainer", func(t *testing.T) {
		arguments := append(append([]string{}, ready...), "-aur-maintainer", "Name\n# injected", "-out", t.TempDir())
		if err := packagingCommand(arguments); err == nil || !strings.Contains(err.Error(), "control character") {
			t.Fatalf("maintainer with a newline returned %v", err)
		}
	})
}

func TestValidatePkgver(t *testing.T) {
	for _, version := range []string{"1.0.3", "1.1.0rc1", "1.1.0_rc.1", "2026.09.25"} {
		if err := validatePkgver(version); err != nil {
			t.Errorf("validatePkgver(%q) = %v", version, err)
		}
	}
	for _, version := range []string{"", "1.1.0-rc.1", "1:1.0", "1.0/2", "1.0 2", "1.0\t2", "1.0é"} {
		if err := validatePkgver(version); err == nil {
			t.Errorf("validatePkgver(%q) accepted an invalid version", version)
		}
	}
}

func TestShellString(t *testing.T) {
	for value, want := range map[string]string{
		"plain":     `'plain'`,
		"it's":      `'it'\''s'`,
		`$HOME "x"`: `'$HOME "x"'`,
	} {
		if got := shellString(value); got != want {
			t.Errorf("shellString(%q) = %s, want %s", value, got, want)
		}
	}
}
