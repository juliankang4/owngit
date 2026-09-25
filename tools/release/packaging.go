package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// packagingData is the template input for the Homebrew, WinGet, and Arch Linux
// files. The formats have independent readiness, because they need different
// inputs.
type packagingData struct {
	Version         string
	BaseURL         string
	Homepage        string
	Tap             string
	PackageID       string
	Publisher       string
	PublisherURL    string
	HomebrewUnready string
	WingetUnready   string
	AURMaintainer   string
	AURUnready      string
	DarwinArm64     artifactRef
	LinuxAMD64      artifactRef
	LinuxARM64      artifactRef
	WindowsAMD64    artifactRef
}

type artifactRef struct {
	Name        string
	SHA256      string
	SHA256Upper string
	Size        int64
}

// placeholder values are used only when the matching input is absent. Every
// file that uses one carries an UNREADY header naming the missing input.
const (
	placeholderURL        = "https://example.invalid"
	placeholderPackageID  = "OwnGit.Owngit"
	placeholderPublisher  = "OwnGit"
	placeholderMaintainer = "OwnGit"
)

var (
	tapPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	sha256Pattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	packageIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

func packagingCommand(arguments []string) error {
	set := flag.NewFlagSet("packaging", flag.ContinueOnError)
	source := set.String("source", ".", "module root holding the packaging templates")
	manifestPath := set.String("manifest", "dist/manifest.json", "artifact manifest written by \"release build\"")
	out := set.String("out", "dist/packaging", "output directory")
	formats := set.String("formats", "all", "comma-separated formats to render: homebrew, winget, npm, aur, or all")
	baseURL := set.String("base-url", "", "release download base URL, for example https://host/owner/owngit/releases/download/v1.0.0")
	homepage := set.String("homepage", "", "project homepage URL")
	repositoryURL := set.String("repository-url", "", "npm Git repository URL, for example https://host/owner/owngit.git")
	tap := set.String("tap", "", "Homebrew tap repository, for example owner/homebrew-owngit")
	packageID := set.String("package-id", "", "WinGet package identifier, for example Owner.Owngit")
	publisher := set.String("publisher", "", "WinGet publisher display name")
	publisherURL := set.String("publisher-url", "", "WinGet publisher URL")
	aurMaintainer := set.String("aur-maintainer", "", "Arch Linux PKGBUILD maintainer line, for example \"Name <address at example dot org>\"")
	strict := set.Bool("strict", false, "fail when a selected format is missing an input instead of marking it unready")
	goTool := set.String("go", "go", "Go toolchain command used to verify the portable output for npm")
	if err := parseFlags(set, arguments); err != nil {
		return err
	}

	root, err := moduleRoot(*source)
	if err != nil {
		return err
	}
	selected, err := selectFormats(*formats)
	if err != nil {
		return err
	}
	document, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	sourceVersion, err := versionFromSource(root)
	if err != nil {
		return err
	}
	if document.Version != sourceVersion {
		return fmt.Errorf("manifest version %q does not match the source version %q", document.Version, sourceVersion)
	}

	data := packagingData{Version: document.Version}
	downloadMissing := []string{}
	homepageMissing := []string{}
	repository := ""
	check := func(name, value string, target *string, validate func(string) error, missing *[]string) error {
		if strings.TrimSpace(value) == "" {
			*missing = append(*missing, name)
			return nil
		}
		if err := validate(value); err != nil {
			return fmt.Errorf("-%s: %w", name, err)
		}
		*target = value
		return nil
	}
	if err := check("base-url", *baseURL, &data.BaseURL, validateURL, &downloadMissing); err != nil {
		return err
	}
	if err := check("homepage", *homepage, &data.Homepage, validateURL, &homepageMissing); err != nil {
		return err
	}
	homebrewMissing := append(append([]string{}, downloadMissing...), homepageMissing...)
	wingetMissing := append(append([]string{}, downloadMissing...), homepageMissing...)
	npmMissing := append([]string{}, homepageMissing...)
	aurMissing := append(append([]string{}, downloadMissing...), homepageMissing...)
	if err := check("repository-url", *repositoryURL, &repository, validateURL, &npmMissing); err != nil {
		return err
	}
	if err := check("tap", *tap, &data.Tap, validateTap, &homebrewMissing); err != nil {
		return err
	}
	if err := check("package-id", *packageID, &data.PackageID, validatePackageID, &wingetMissing); err != nil {
		return err
	}
	if err := check("publisher", *publisher, &data.Publisher, validateNonEmpty, &wingetMissing); err != nil {
		return err
	}
	if err := check("publisher-url", *publisherURL, &data.PublisherURL, validateURL, &wingetMissing); err != nil {
		return err
	}
	if err := check("aur-maintainer", *aurMaintainer, &data.AURMaintainer, validateNonEmpty, &aurMissing); err != nil {
		return err
	}

	var missing []string
	if selected["homebrew"] {
		missing = append(missing, homebrewMissing...)
	}
	if selected["winget"] {
		missing = append(missing, wingetMissing...)
	}
	if selected["npm"] {
		missing = append(missing, npmMissing...)
	}
	if selected["aur"] {
		missing = append(missing, aurMissing...)
	}
	missing = uniqueSorted(missing)
	if len(missing) > 0 && *strict {
		return fmt.Errorf("missing required inputs: %s", strings.Join(missing, ", "))
	}
	// makepkg refuses some version strings, so an Arch package for such a
	// version is unready.
	var aurProblems []string
	if selected["aur"] {
		if err := validatePkgver(document.Version); err != nil {
			if *strict {
				return fmt.Errorf("aur: %w", err)
			}
			aurProblems = append(aurProblems, err.Error())
		}
	}

	if data.BaseURL == "" {
		data.BaseURL = placeholderURL
	}
	if data.Homepage == "" {
		data.Homepage = placeholderURL
	}
	if repository == "" {
		repository = placeholderURL + "/owngit.git"
	}
	if data.PublisherURL == "" {
		data.PublisherURL = placeholderURL
	}
	if data.PackageID == "" {
		data.PackageID = placeholderPackageID
	}
	if data.Publisher == "" {
		data.Publisher = placeholderPublisher
	}
	if data.AURMaintainer == "" {
		data.AURMaintainer = placeholderMaintainer
	}
	if selected["homebrew"] && len(homebrewMissing) > 0 {
		data.HomebrewUnready = unreadyText(homebrewMissing)
	}
	if selected["winget"] && len(wingetMissing) > 0 {
		data.WingetUnready = unreadyText(wingetMissing)
	}
	if selected["aur"] && len(aurMissing) > 0 {
		aurProblems = append([]string{unreadyText(aurMissing)}, aurProblems...)
	}
	data.AURUnready = strings.Join(aurProblems, "; ")

	refs := map[string]*artifactRef{
		"darwin/arm64":  &data.DarwinArm64,
		"linux/amd64":   &data.LinuxAMD64,
		"linux/arm64":   &data.LinuxARM64,
		"windows/amd64": &data.WindowsAMD64,
	}
	// Manifest values end up in files that shells, Ruby, and package managers
	// read, so they are checked before any format is rendered.
	for _, built := range document.Artifacts {
		ref, ok := refs[built.Target]
		if !ok {
			continue
		}
		current, err := targetFor(built.Target)
		if err != nil {
			return err
		}
		if want := current.archiveName(document.Version); built.Name != want {
			return fmt.Errorf("manifest names the %s archive %q, want %q", built.Target, built.Name, want)
		}
		if !sha256Pattern.MatchString(built.SHA256) {
			return fmt.Errorf("manifest SHA-256 of %s is not 64 lowercase hexadecimal characters", built.Name)
		}
		*ref = artifactRef{Name: built.Name, SHA256: built.SHA256, SHA256Upper: strings.ToUpper(built.SHA256), Size: built.Size}
	}
	for _, name := range []string{"darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"} {
		if refs[name].Name == "" {
			return fmt.Errorf("manifest has no %s artifact; build every release target before rendering packaging", name)
		}
	}

	outDir, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	// npm packages embed the executables, so they are built only from a
	// verified snapshot of the portable output, and only into a fresh
	// directory. Both are checked before any format writes a file.
	var npm npmInputs
	var npmDir string
	if selected["npm"] {
		if npmDir, err = npmOutputDir(outDir); err != nil {
			return err
		}
		targets := make([]string, 0, len(releaseTargets))
		for _, current := range releaseTargets {
			targets = append(targets, current.String())
		}
		portable, err := loadVerifiedPortable(*manifestPath, sourceVersion, *goTool, targets, os.RemoveAll)
		if err != nil {
			return err
		}
		npm = npmInputs{
			root: root, version: portable.manifest.Version, homepage: data.Homepage,
			repositoryURL: repository, payloads: portable.payloads,
		}
		if len(npmMissing) > 0 {
			npm.unready = unreadyInputs(npmMissing)
		}
	}
	rendered := []struct {
		format       string
		templatePath string
		outputPath   string
	}{
		{"homebrew", filepath.Join(root, "packaging", "homebrew", "owngit.rb.tmpl"), filepath.Join(outDir, "owngit.rb")},
		{"winget", filepath.Join(root, "packaging", "winget", "version.yaml.tmpl"), filepath.Join(outDir, data.PackageID+".yaml")},
		{"winget", filepath.Join(root, "packaging", "winget", "installer.yaml.tmpl"), filepath.Join(outDir, data.PackageID+".installer.yaml")},
		{"winget", filepath.Join(root, "packaging", "winget", "locale.en-US.yaml.tmpl"), filepath.Join(outDir, data.PackageID+".locale.en-US.yaml")},
		{"aur", filepath.Join(root, "packaging", "aur", "PKGBUILD.tmpl"), filepath.Join(outDir, "aur", "PKGBUILD")},
		{"aur", filepath.Join(root, "packaging", "aur", "SRCINFO.tmpl"), filepath.Join(outDir, "aur", ".SRCINFO")},
	}
	for _, item := range rendered {
		if !selected[item.format] {
			continue
		}
		body, err := renderTemplate(item.templatePath, data)
		if err != nil {
			return err
		}
		if _, err := writeFile(item.outputPath, body, 0o644); err != nil {
			return err
		}
		fmt.Printf("rendered %s\n", item.outputPath)
	}
	if selected["npm"] {
		if _, err := createFreshOutput("npm", npmDir); err != nil {
			return err
		}
		if err := renderNPM(npmDir, npm); err != nil {
			return fmt.Errorf("npm packages: %w; partial output remains at %s", err, npmDir)
		}
		fmt.Printf("rendered %s\n", npmDir)
	}
	for _, format := range []string{"homebrew", "winget", "npm", "aur"} {
		if !selected[format] {
			continue
		}
		formatMissing := map[string][]string{"homebrew": homebrewMissing, "winget": wingetMissing, "npm": npmMissing, "aur": aurMissing}[format]
		if len(formatMissing) > 0 {
			fmt.Printf("UNREADY %s: missing %s\n", format, strings.Join(uniqueSorted(formatMissing), ", "))
		} else if format == "aur" && data.AURUnready != "" {
			fmt.Printf("UNREADY %s: %s\n", format, data.AURUnready)
		} else {
			fmt.Printf("ready %s: every input was supplied\n", format)
		}
	}
	return nil
}

func unreadyText(missing []string) string {
	return unreadyInputs(missing) + "; placeholder values are marked below"
}

func unreadyInputs(missing []string) string {
	return "missing input(s): " + strings.Join(uniqueSorted(missing), ", ")
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

func selectFormats(list string) (map[string]bool, error) {
	selected := map[string]bool{}
	for _, item := range strings.Split(list, ",") {
		switch strings.TrimSpace(item) {
		case "":
		case "all":
			selected["homebrew"] = true
			selected["winget"] = true
			selected["npm"] = true
			selected["aur"] = true
		case "homebrew", "winget", "npm", "aur":
			selected[strings.TrimSpace(item)] = true
		default:
			return nil, fmt.Errorf("unknown format %q; use homebrew, winget, npm, aur, or all", strings.TrimSpace(item))
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no format selected")
	}
	return selected, nil
}

func renderTemplate(path string, data packagingData) ([]byte, error) {
	parsed, err := template.New(filepath.Base(path)).Funcs(template.FuncMap{
		"ruby": rubyString,
		"yaml": yamlString,
		"sh":   shellString,
	}).ParseFiles(path)
	if err != nil {
		return nil, err
	}
	var buffer strings.Builder
	if err := parsed.Execute(&buffer, data); err != nil {
		return nil, err
	}
	return []byte(buffer.String()), nil
}

// rubyString escapes a value for the body of a Ruby double-quoted string. Ruby
// interpolates #{...}, #$global, and #@instance, so every # is escaped.
func rubyString(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '#':
			builder.WriteString(`\#`)
		default:
			writeEscapedRune(&builder, r)
		}
	}
	return builder.String()
}

// shellString renders a single-quoted shell word, as used in a PKGBUILD.
func shellString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// yamlString renders a double-quoted YAML scalar.
func yamlString(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		default:
			writeEscapedRune(&builder, r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func writeEscapedRune(builder *strings.Builder, r rune) {
	if r < 0x20 || r == 0x7f {
		fmt.Fprintf(builder, `\u%04X`, r)
		return
	}
	builder.WriteRune(r)
}

func validateURL(value string) error {
	if err := validateNoControl(value); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%q must use http or https", value)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%q has no host", value)
	}
	if parsed.User != nil {
		return fmt.Errorf("%q must not carry credentials", value)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%q must not carry a query or fragment", value)
	}
	if strings.HasSuffix(value, "/") {
		return fmt.Errorf("%q must not end with a slash", value)
	}
	return nil
}

func validateTap(value string) error {
	if err := validateNoControl(value); err != nil {
		return err
	}
	if !tapPattern.MatchString(value) {
		return fmt.Errorf("%q must look like owner/repository", value)
	}
	return nil
}

func validatePackageID(value string) error {
	if err := validateNoControl(value); err != nil {
		return err
	}
	if !packageIDPattern.MatchString(value) {
		return fmt.Errorf("%q must look like Publisher.Package", value)
	}
	return nil
}

// validatePkgver accepts the version characters makepkg allows in pkgver:
// printable ASCII without whitespace, hyphens, colons, or slashes.
func validatePkgver(version string) error {
	if version == "" {
		return fmt.Errorf("empty version is not a valid Arch Linux pkgver")
	}
	for _, r := range version {
		if r <= ' ' || r > '~' || strings.ContainsRune("-:/", r) {
			return fmt.Errorf("version %q is not a valid Arch Linux pkgver (no hyphen, colon, slash, whitespace, or non-ASCII character)", version)
		}
	}
	return nil
}

func validateNonEmpty(value string) error {
	if err := validateNoControl(value); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("must not be empty")
	}
	return nil
}

func validateNoControl(value string) error {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%q contains a control character", value)
		}
	}
	return nil
}
