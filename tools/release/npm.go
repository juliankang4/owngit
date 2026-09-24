package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// npmBaseName is the npm name of the main package. Each platform package
// appends its npm os and cpu, for example owngit-linux-x64.
const npmBaseName = "owngit"

// npmNodeMajor sets the launcher's engines.node range. The launcher uses only
// long-standing Node APIs (child_process.spawn, require.resolve, and signal
// events), so the floor follows npm instead: Node 18 is the oldest line that
// runs npm 10 (^18.17.0 || >=20.5.0), and older lines bundle npm 9 or earlier.
// The range is advisory; npm warns about it unless engine-strict is set.
const npmNodeMajor = 18

var npmNodeRange = fmt.Sprintf(">=%d", npmNodeMajor)

// npmPlatformLabels name each release target for the package READMEs.
var npmPlatformLabels = map[string]string{
	"darwin/arm64":  "macOS on Apple silicon",
	"linux/amd64":   "Linux on x64",
	"linux/arm64":   "Linux on ARM64",
	"windows/amd64": "Windows on x64",
}

type npmRepository struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// npmPackageJSON fixes the key order of every generated package.json. An
// unready package carries a "//" note and "private", which makes npm refuse to
// publish it.
type npmPackageJSON struct {
	Note                 string            `json:"//,omitempty"`
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Description          string            `json:"description"`
	License              string            `json:"license"`
	Homepage             string            `json:"homepage"`
	Repository           npmRepository     `json:"repository"`
	Private              bool              `json:"private,omitempty"`
	OS                   []string          `json:"os,omitempty"`
	CPU                  []string          `json:"cpu,omitempty"`
	Bin                  map[string]string `json:"bin,omitempty"`
	Files                []string          `json:"files"`
	Engines              map[string]string `json:"engines,omitempty"`
	OptionalDependencies map[string]string `json:"optionalDependencies,omitempty"`
}

// npmPlatform is the launcher's view of one platform package.
type npmPlatform struct {
	Package string `json:"package"`
	Binary  string `json:"binary"`
}

type npmInputs struct {
	root          string
	version       string
	homepage      string
	repositoryURL string
	// unready lists the missing inputs; empty means ready.
	unready  string
	payloads map[string]portablePayload
}

func npmOS(current target) string {
	if current.goos == "windows" {
		return "win32"
	}
	return current.goos
}

func npmCPU(current target) string {
	if current.goarch == "amd64" {
		return "x64"
	}
	return current.goarch
}

func npmPlatformKey(current target) string { return npmOS(current) + "-" + npmCPU(current) }

func npmPackageName(current target) string { return npmBaseName + "-" + npmPlatformKey(current) }

// npmBinaryPath is where a platform package keeps the executable.
func npmBinaryPath(current target) string { return "bin/" + current.binary }

// npmRepositoryURL is the package.json form of a Git repository URL.
func npmRepositoryURL(value string) string { return "git+" + value }

// renderNPM writes one directory per package under outDir, each ready for
// "npm pack" or "npm publish". outDir must be fresh.
func renderNPM(outDir string, inputs npmInputs) error {
	repository := npmRepository{Type: "git", URL: npmRepositoryURL(inputs.repositoryURL)}
	note := ""
	if inputs.unready != "" {
		note = "UNREADY: " + inputs.unready + "; placeholder URLs use " + placeholderURL + " and private blocks npm publish"
	}
	private := inputs.unready != ""

	platforms := map[string]npmPlatform{}
	optional := map[string]string{}
	labels := make([]string, 0, len(releaseTargets))
	for _, current := range releaseTargets {
		payload, ok := inputs.payloads[current.String()]
		if !ok {
			return fmt.Errorf("npm packages need the %s artifact", current)
		}
		if err := checkRecordedBinary(payload); err != nil {
			return err
		}
		name := npmPackageName(current)
		platforms[npmPlatformKey(current)] = npmPlatform{Package: name, Binary: npmBinaryPath(current)}
		optional[name] = inputs.version
		labels = append(labels, npmPlatformLabels[current.String()])

		files, err := npmPlatformFiles(inputs, payload)
		if err != nil {
			return err
		}
		document := npmPackageJSON{
			Note: note, Name: name, Version: inputs.version,
			Description: "The OwnGit executable for " + npmPlatformLabels[current.String()] + ", installed by the owngit package",
			License:     "MIT", Homepage: inputs.homepage, Repository: repository, Private: private,
			OS: []string{npmOS(current)}, CPU: []string{npmCPU(current)},
			Files: []string{npmBinaryPath(current), "README.md", "LICENSE", "THIRD_PARTY_NOTICES/"},
		}
		if err := writeNPMPackage(filepath.Join(outDir, name), document, files); err != nil {
			return err
		}
	}

	license, err := sharedLicense(inputs.payloads)
	if err != nil {
		return err
	}
	platformJSON, err := npmJSON(platforms, "")
	if err != nil {
		return err
	}
	homepageJSON, err := npmJSON(inputs.homepage, "")
	if err != nil {
		return err
	}
	launcher, err := renderNativeTemplate(filepath.Join(inputs.root, "packaging", "npm", "owngit.js.tmpl"), map[string]string{
		"Platforms": string(platformJSON), "Homepage": string(homepageJSON),
	})
	if err != nil {
		return err
	}
	readme, err := renderNativeTemplate(filepath.Join(inputs.root, "packaging", "npm", "README.md.tmpl"), map[string]string{
		"Version": inputs.version, "Platforms": englishList(labels), "Node": fmt.Sprintf("%d or newer", npmNodeMajor),
		"Example": npmPackageName(releaseTargets[0]), "Homepage": inputs.homepage,
	})
	if err != nil {
		return err
	}
	document := npmPackageJSON{
		Note: note, Name: npmBaseName, Version: inputs.version,
		Description: "Private Git storage and browser dashboard on your own computer",
		License:     "MIT", Homepage: inputs.homepage, Repository: repository, Private: private,
		Bin:                  map[string]string{"owngit": "bin/owngit.js"},
		Files:                []string{"bin/owngit.js", "README.md", "LICENSE"},
		Engines:              map[string]string{"node": npmNodeRange},
		OptionalDependencies: optional,
	}
	return writeNPMPackage(filepath.Join(outDir, npmBaseName), document, []nativePackageFile{
		{path: "LICENSE", mode: 0o644, data: license},
		{path: "README.md", mode: 0o644, data: readme},
		{path: "bin/owngit.js", mode: 0o755, data: launcher},
	})
}

// checkRecordedBinary refuses an executable whose digest differs from the one
// the portable manifest records for it.
func checkRecordedBinary(payload portablePayload) error {
	for _, file := range payload.built.Files {
		if file.Path != payload.target.binary {
			continue
		}
		if file.SHA256 != payload.binary.sha || sha256Bytes(payload.binary.data) != file.SHA256 {
			return fmt.Errorf("%s in %s has sha256 %s, but the portable manifest records %s", file.Path, payload.built.Name, sha256Bytes(payload.binary.data), file.SHA256)
		}
		return nil
	}
	return fmt.Errorf("the portable manifest records no %s for %s", payload.target.binary, payload.built.Name)
}

// npmPlatformFiles lists a platform package's executable, license, notices,
// and README.
func npmPlatformFiles(inputs npmInputs, payload portablePayload) ([]nativePackageFile, error) {
	shared, err := explicitSharedFiles(payload)
	if err != nil {
		return nil, err
	}
	readme, err := renderNativeTemplate(filepath.Join(inputs.root, "packaging", "npm", "platform-README.md.tmpl"), map[string]string{
		"Name": npmPackageName(payload.target), "Version": inputs.version,
		"Platform": npmPlatformLabels[payload.target.String()], "Homepage": inputs.homepage,
	})
	if err != nil {
		return nil, err
	}
	files := append(shared,
		nativePackageFile{path: npmBinaryPath(payload.target), mode: 0o755, data: payload.binary.data},
		nativePackageFile{path: "README.md", mode: 0o644, data: readme},
	)
	return files, nil
}

// sharedLicense returns the LICENSE that every artifact carries, refusing
// artifacts that disagree.
func sharedLicense(payloads map[string]portablePayload) ([]byte, error) {
	var license []byte
	for _, current := range releaseTargets {
		entry, ok := payloads[current.String()].entries["LICENSE"]
		if !ok {
			return nil, fmt.Errorf("the %s artifact has no LICENSE", current)
		}
		if license != nil && !bytes.Equal(license, entry.data) {
			return nil, fmt.Errorf("the %s artifact carries a different LICENSE", current)
		}
		license = entry.data
	}
	return license, nil
}

// writeNPMPackage writes package.json and files into a new package directory
// with fixed modes: 0755 for directories and executables, 0644 otherwise.
func writeNPMPackage(dir string, document npmPackageJSON, files []nativePackageFile) error {
	manifestJSON, err := npmJSON(document, "  ")
	if err != nil {
		return err
	}
	files = append(files, nativePackageFile{path: "package.json", mode: 0o644, data: append(manifestJSON, '\n')})
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	for index, file := range files {
		if err := validateNativePath(file.path); err != nil {
			return err
		}
		if index > 0 && files[index-1].path == file.path {
			return fmt.Errorf("npm package %s lists %s twice", filepath.Base(dir), file.path)
		}
		if _, err := writeFile(filepath.Join(dir, filepath.FromSlash(file.path)), file.data, os.FileMode(file.mode)); err != nil {
			return err
		}
	}
	// MkdirAll applies the umask, so directory modes are set afterwards.
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o755)
		}
		return nil
	})
}

// npmJSON encodes without HTML escaping, so URLs keep their characters.
// Struct fields keep their declared order and map keys are sorted.
func npmJSON(value any, indent string) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", indent)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func englishList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

// npmOutputDir is the fresh directory that holds the npm packages.
func npmOutputDir(outDir string) (string, error) {
	path := filepath.Join(outDir, "npm")
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("npm output %s already exists; move it away or choose a fresh -out", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return path, nil
}
