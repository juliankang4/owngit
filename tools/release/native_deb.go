package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type arMember struct {
	name string
	data []byte
}

type debControlData struct {
	Version       string
	Architecture  string
	InstalledSize int64
}

func buildDebPrototype(inputs nativeInputs, outDir, targetName string) (nativeArtifact, error) {
	payload, ok := inputs.payloads[targetName]
	if !ok {
		return nativeArtifact{}, fmt.Errorf("no portable payload for %s", targetName)
	}
	architecture, err := debArchitecture(payload.target.goarch)
	if err != nil {
		return nativeArtifact{}, err
	}
	shared, err := explicitSharedFiles(payload)
	if err != nil {
		return nativeArtifact{}, err
	}
	provenance, err := provenanceBytes(inputs, payload, "deb")
	if err != nil {
		return nativeArtifact{}, err
	}
	desktop, err := readRegularInput(filepath.Join(inputs.root, "packaging", "linux", "owngit.desktop"))
	if err != nil {
		return nativeArtifact{}, err
	}
	readme, err := renderNativeTemplate(filepath.Join(inputs.root, "packaging", "linux", "README.Debian.tmpl"), struct{ Version string }{inputs.manifest.Version})
	if err != nil {
		return nativeArtifact{}, err
	}

	dataFiles := []nativePackageFile{
		{path: "usr/bin/owngit", mode: 0o755, data: payload.binary.data},
		{path: "usr/share/applications/owngit.desktop", mode: 0o644, data: desktop},
		{path: "usr/share/doc/owngit/README.Debian", mode: 0o644, data: readme},
		{path: "usr/share/doc/owngit/package-provenance.json", mode: 0o644, data: provenance},
	}
	for _, file := range shared {
		name := path.Join("usr/share/doc/owngit", file.path)
		dataFiles = append(dataFiles, nativePackageFile{path: name, mode: file.mode, data: file.data})
		if file.path == "LICENSE" {
			dataFiles = append(dataFiles, nativePackageFile{
				path: "usr/share/doc/owngit/copyright", mode: 0o644, data: file.data,
			})
		}
	}
	// The coding-tool resources, taken from the verified portable payload so
	// the package ships the same bytes that artifact was verified with.
	resources, err := resourceFiles(payload)
	if err != nil {
		return nativeArtifact{}, err
	}
	for _, file := range resources {
		dataFiles = append(dataFiles, nativePackageFile{
			path: path.Join("usr/share/doc/owngit", file.path), mode: file.mode, data: file.data,
		})
	}
	for _, file := range dataFiles {
		if err := validateNativePath(file.path); err != nil {
			return nativeArtifact{}, err
		}
	}
	sort.Slice(dataFiles, func(i, j int) bool { return dataFiles[i].path < dataFiles[j].path })

	installedBytes := int64(0)
	for _, file := range dataFiles {
		installedBytes += int64(len(file.data))
	}
	control, err := renderNativeTemplate(filepath.Join(inputs.root, "packaging", "linux", "control.tmpl"), debControlData{
		Version: inputs.manifest.Version, Architecture: architecture,
		InstalledSize: (installedBytes + 1023) / 1024,
	})
	if err != nil {
		return nativeArtifact{}, err
	}
	md5sums := debMD5Sums(dataFiles)
	controlFiles := []nativePackageFile{
		{path: "control", mode: 0o644, data: control},
		{path: "md5sums", mode: 0o644, data: md5sums},
	}
	controlArchive, err := writeDebTarGz(controlFiles)
	if err != nil {
		return nativeArtifact{}, err
	}
	dataArchive, err := writeDebTarGz(dataFiles)
	if err != nil {
		return nativeArtifact{}, err
	}

	members := []arMember{
		{name: "debian-binary", data: []byte("2.0\n")},
		{name: "control.tar.gz", data: controlArchive},
		{name: "data.tar.gz", data: dataArchive},
	}
	var packageBytes bytes.Buffer
	if err := writeAr(&packageBytes, members); err != nil {
		return nativeArtifact{}, err
	}
	name := fmt.Sprintf("owngit_%s_%s.deb", inputs.manifest.Version, architecture)
	packagePath := filepath.Join(outDir, name)
	if _, err := writeFile(packagePath, packageBytes.Bytes(), 0o644); err != nil {
		return nativeArtifact{}, err
	}
	if err := verifyDebPackage(packagePath, controlFiles, dataFiles); err != nil {
		return nativeArtifact{}, err
	}
	digest, err := sha256File(packagePath)
	if err != nil {
		return nativeArtifact{}, err
	}
	info, err := os.Stat(packagePath)
	if err != nil {
		return nativeArtifact{}, err
	}
	manifestFiles := append([]nativePackageFile{
		{path: "debian-binary", mode: 0o644, data: []byte("2.0\n")},
		{path: "DEBIAN/control", mode: 0o644, data: control},
		{path: "DEBIAN/md5sums", mode: 0o644, data: md5sums},
	}, dataFiles...)
	return nativeArtifact{
		Format: "deb", Target: targetName, Name: name, SHA256: digest, Size: info.Size(),
		Prototype: true, PublisherSigned: false, Notarized: false, StructureVerified: true,
		NativeInstallVerified: false, PublicReady: false,
		Toolchain:        "Go standard library " + runtime.Version(),
		PortableArtifact: payload.built.Name, PortableArtifactSHA256: payload.built.SHA256,
		ApplicationBinarySHA256:  payload.binary.sha,
		PackagedProvenanceSHA256: sha256Bytes(provenance), Files: fileEntries(manifestFiles),
	}, nil
}

func debArchitecture(goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64":
		return goarch, nil
	default:
		return "", fmt.Errorf("no Debian architecture mapping for %s", goarch)
	}
}

func debMD5Sums(files []nativePackageFile) []byte {
	var lines []string
	for _, file := range files {
		digest := md5.Sum(file.data) // Debian uses md5sums for damage detection, not artifact trust.
		lines = append(lines, hex.EncodeToString(digest[:])+"  "+file.path)
	}
	sort.Strings(lines)
	return []byte(strings.Join(lines, "\n") + "\n")
}

func writeDebTarGz(files []nativePackageFile) ([]byte, error) {
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	compressed.Header.ModTime = fixedModTime
	compressed.Header.OS = 255
	archive := tar.NewWriter(compressed)

	directories := map[string]bool{}
	for _, file := range files {
		for current := path.Dir(file.path); current != "."; current = path.Dir(current) {
			directories[current] = true
		}
	}
	directoryNames := make([]string, 0, len(directories))
	for name := range directories {
		directoryNames = append(directoryNames, name)
	}
	sort.Slice(directoryNames, func(i, j int) bool {
		leftDepth := strings.Count(directoryNames[i], "/")
		rightDepth := strings.Count(directoryNames[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return directoryNames[i] < directoryNames[j]
	})
	for _, name := range directoryNames {
		header := &tar.Header{
			Name: "./" + name + "/", Typeflag: tar.TypeDir, Mode: 0o755,
			Uid: 0, Gid: 0, Uname: "root", Gname: "root", ModTime: fixedModTime,
			Format: tar.FormatGNU,
		}
		if err := archive.WriteHeader(header); err != nil {
			return nil, err
		}
	}
	for _, file := range files {
		header := &tar.Header{
			Name: "./" + file.path, Typeflag: tar.TypeReg, Mode: file.mode,
			Size: int64(len(file.data)), Uid: 0, Gid: 0, Uname: "root", Gname: "root",
			ModTime: fixedModTime, Format: tar.FormatGNU,
		}
		if err := archive.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := archive.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := compressed.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeAr(writer io.Writer, members []arMember) error {
	if _, err := io.WriteString(writer, "!<arch>\n"); err != nil {
		return err
	}
	for _, member := range members {
		if member.name == "" || len(member.name) > 15 || strings.ContainsAny(member.name, " /\n\r\t") {
			return fmt.Errorf("invalid ar member name %q", member.name)
		}
		header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n",
			member.name+"/", fixedModTime.Unix(), 0, 0, "100644", len(member.data))
		if len(header) != 60 {
			return fmt.Errorf("ar header for %s is %d bytes", member.name, len(header))
		}
		if _, err := io.WriteString(writer, header); err != nil {
			return err
		}
		if _, err := writer.Write(member.data); err != nil {
			return err
		}
		if len(member.data)%2 != 0 {
			if _, err := writer.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	return nil
}

func readAr(data []byte) ([]arMember, error) {
	if !bytes.HasPrefix(data, []byte("!<arch>\n")) {
		return nil, errors.New("Debian package has no ar signature")
	}
	data = data[8:]
	var members []arMember
	for len(data) > 0 {
		if len(data) < 60 {
			return nil, errors.New("truncated ar member header")
		}
		header := data[:60]
		data = data[60:]
		if string(header[58:60]) != "`\n" {
			return nil, errors.New("invalid ar member trailer")
		}
		name := strings.TrimSpace(string(header[:16]))
		name = strings.TrimSuffix(name, "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if err != nil || size < 0 || size > int64(len(data)) {
			return nil, fmt.Errorf("invalid ar member size for %s", name)
		}
		memberData := append([]byte(nil), data[:size]...)
		data = data[size:]
		if size%2 != 0 {
			if len(data) == 0 || data[0] != '\n' {
				return nil, fmt.Errorf("invalid ar padding after %s", name)
			}
			data = data[1:]
		}
		members = append(members, arMember{name: name, data: memberData})
	}
	return members, nil
}

func verifyDebPackage(packagePath string, controlFiles, dataFiles []nativePackageFile) error {
	data, err := os.ReadFile(packagePath)
	if err != nil {
		return err
	}
	members, err := readAr(data)
	if err != nil {
		return err
	}
	wantNames := []string{"debian-binary", "control.tar.gz", "data.tar.gz"}
	if len(members) != len(wantNames) {
		return fmt.Errorf("Debian package has %d members, want %d", len(members), len(wantNames))
	}
	for index, want := range wantNames {
		if members[index].name != want {
			return fmt.Errorf("Debian package member %d is %s, want %s", index, members[index].name, want)
		}
	}
	if string(members[0].data) != "2.0\n" {
		return errors.New("Debian package has an invalid format marker")
	}
	if err := verifyDebTar(members[1].data, controlFiles); err != nil {
		return fmt.Errorf("control archive: %w", err)
	}
	if err := verifyDebTar(members[2].data, dataFiles); err != nil {
		return fmt.Errorf("data archive: %w", err)
	}
	return nil
}

func verifyDebTar(compressed []byte, expected []nativePackageFile) error {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return err
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	actual := map[string]nativePackageFile{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(header.Name, "./")
		if header.Typeflag == tar.TypeDir {
			if header.Mode != 0o755 {
				return fmt.Errorf("directory %s has mode %04o", name, header.Mode)
			}
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("entry %s is not a regular file", name)
		}
		if err := validateNativePath(name); err != nil {
			return err
		}
		if _, duplicate := actual[name]; duplicate {
			return fmt.Errorf("entry %s appears twice", name)
		}
		body, err := io.ReadAll(archive)
		if err != nil {
			return err
		}
		actual[name] = nativePackageFile{path: name, mode: header.Mode, data: body}
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("archive has %d regular files, want %d", len(actual), len(expected))
	}
	for _, want := range expected {
		got, ok := actual[want.path]
		if !ok {
			return fmt.Errorf("archive is missing %s", want.path)
		}
		if got.mode != want.mode || !bytes.Equal(got.data, want.data) {
			return fmt.Errorf("archive entry %s does not match its staged content", want.path)
		}
	}
	return nil
}
