package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResourcesTravelInThePortableArchive checks that an installed user gets
// the coding-tool skill and its guide.
//
// The point of packaging them is that a user with only an artifact can copy
// the skill into a coding tool. Verifying the names alone would pass on an
// empty file, so the contents are checked too.
func TestResourcesTravelInThePortableArchive(t *testing.T) {
	requireGoToolchain(t)
	root := repoRoot(t)
	native := nativeTarget(t)
	dir := t.TempDir()
	noErrf(t, buildCommand([]string{"-source", root, "-out", dir, "-targets", native}), "build")
	noErrf(t, verifyDir(dir, "go"), "verify rejected a fresh build")

	document, err := readManifest(filepath.Join(dir, "manifest.json"))
	noErr(t, err)
	current, err := targetFor(document.Artifacts[0].Target)
	noErr(t, err)
	entries, err := readArchive(filepath.Join(dir, document.Artifacts[0].Name), current.format)
	noErr(t, err)
	byName := map[string]archiveEntry{}
	for _, entry := range entries {
		byName[entry.name] = entry
	}

	for _, name := range releaseResources {
		entry, ok := byName[name]
		if !ok {
			t.Fatalf("the archive does not carry %s", name)
		}
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		noErr(t, err)
		if string(entry.data) != string(source) {
			t.Errorf("%s in the archive does not match the source file", name)
		}
		if entry.mode&0o111 != 0 {
			t.Errorf("%s is packaged executable (mode %04o)", name, entry.mode)
		}
	}

	// Each language version of the guide links to the skill and to the other
	// version by relative path. Keeping the source layout is what makes those
	// links resolve in an unpacked archive.
	for guideName, links := range map[string][]string{
		"docs/CODING_TOOLS.md":    {"../integrations/skills/owngit-checks/SKILL.md", "CODING_TOOLS.ko.md"},
		"docs/CODING_TOOLS.ko.md": {"../integrations/skills/owngit-checks/SKILL.md", "CODING_TOOLS.md"},
	} {
		entry, ok := byName[guideName]
		if !ok {
			t.Errorf("the archive does not carry %s", guideName)
			continue
		}
		guide := string(entry.data)
		for _, link := range links {
			if !strings.Contains(guide, "("+link+")") && !strings.Contains(guide, `"`+link+`"`) {
				t.Fatalf("%s no longer links to %s", guideName, link)
			}
			resolved := filepath.Join(filepath.Dir(guideName), filepath.FromSlash(link))
			if _, ok := byName[filepath.ToSlash(filepath.Clean(resolved))]; !ok {
				t.Errorf("the link from %s to %s does not resolve inside the archive", guideName, link)
			}
		}
	}

	// The skill is the reviewed one, not an edited copy.
	if !strings.Contains(string(byName["integrations/skills/owngit-checks/SKILL.md"].data), "name: owngit-checks") {
		t.Error("the packaged skill has no skill name in its front matter")
	}
}

// TestVerifyRejectsABrokenResourceSet checks the refusals.
//
// Packaging is only useful if a missing, altered or smuggled resource fails
// the build output rather than reaching a user.
func TestVerifyRejectsABrokenResourceSet(t *testing.T) {
	requireGoToolchain(t)
	root := repoRoot(t)
	native := nativeTarget(t)
	dir := t.TempDir()
	noErrf(t, buildCommand([]string{"-source", root, "-out", dir, "-targets", native}), "build")

	t.Run("a resource is missing", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			return removeFile(files, "integrations/skills/owngit-checks/SKILL.md")
		})
		expectVerifyError(t, copied, "integrations/skills/owngit-checks/SKILL.md")
	})

	t.Run("a resource is modified", func(t *testing.T) {
		copied := copyDist(t, dir)
		// The manifest is left alone, so the altered bytes are what fails.
		rewriteDist(t, copied, false, func(files []memFile) []memFile {
			for i := range files {
				if files[i].name == "docs/CODING_TOOLS.md" {
					files[i].data = append(append([]byte{}, files[i].data...), "\nedited\n"...)
				}
			}
			return files
		})
		expectVerifyError(t, copied, "docs/CODING_TOOLS.md")
	})

	t.Run("an undeclared file rides along", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			return append(files, memFile{
				name: "integrations/skills/owngit-checks/EXTRA.md", mode: 0o644,
				data: []byte("undeclared\n"),
			})
		})
		expectVerifyError(t, copied, "undeclared resource")
	})

	t.Run("a resource is emptied", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			for i := range files {
				if files[i].name == "docs/CODING_TOOLS.md" {
					files[i].data = nil
				}
			}
			return files
		})
		expectVerifyError(t, copied, "empty")
	})
}

// TestResourceCollectionRefusesUnsafeInput checks the source-side rules.
//
// A symlink must not decide what is published, and a missing or empty file is
// a build failure rather than a silently thinner artifact.
func TestResourceCollectionRefusesUnsafeInput(t *testing.T) {
	root := repoRoot(t)
	if _, err := collectResources(root); err != nil {
		t.Fatalf("the real source tree was refused: %v", err)
	}

	stage := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		for _, name := range releaseResources {
			path := filepath.Join(dir, filepath.FromSlash(name))
			noErr(t, os.MkdirAll(filepath.Dir(path), 0o755))
			noErr(t, os.WriteFile(path, []byte("body\n"), 0o644))
		}
		return dir
	}

	t.Run("a symlinked resource", func(t *testing.T) {
		dir := stage(t)
		target := releaseResources[0]
		path := filepath.Join(dir, filepath.FromSlash(target))
		noErr(t, os.Remove(path))
		real := filepath.Join(dir, "elsewhere.md")
		noErr(t, os.WriteFile(real, []byte("body\n"), 0o644))
		if err := os.Symlink(real, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		err := mustFail(t, dir)
		if !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error %q does not name the symlink", err)
		}
	})

	t.Run("an empty resource", func(t *testing.T) {
		dir := stage(t)
		path := filepath.Join(dir, filepath.FromSlash(releaseResources[0]))
		noErr(t, os.WriteFile(path, nil, 0o644))
		err := mustFail(t, dir)
		if !strings.Contains(err.Error(), "empty") {
			t.Fatalf("error %q does not report the empty file", err)
		}
	})

	t.Run("a missing resource", func(t *testing.T) {
		dir := stage(t)
		noErr(t, os.Remove(filepath.Join(dir, filepath.FromSlash(releaseResources[0]))))
		mustFail(t, dir)
	})
}

// TestALinkedAncestorCannotDecideWhatIsPackaged covers the directories above
// each resource, not only the leaf.
//
// A resource sits several directories below the root. If only the final file
// were checked, a linked docs or integrations/skills directory would package
// bytes from outside the source tree while every leaf still looked like an
// ordinary file. Checking just the first directory, or just an escape to an
// external location, is also insufficient: a link deeper in the path and a
// link pointing back inside the same tree both change which bytes ship.
func TestALinkedAncestorCannotDecideWhatIsPackaged(t *testing.T) {
	// Every ancestor directory of every declared resource, which between
	// them cover the first component, a deeper component, and the immediate
	// parent of a resource.
	ancestors := map[string]bool{}
	for _, name := range releaseResources {
		components := strings.Split(name, "/")
		for i := 1; i < len(components); i++ {
			ancestors[strings.Join(components[:i], "/")] = true
		}
	}
	if len(ancestors) < 3 {
		t.Fatalf("the declared resources expose %d ancestor directories; the deeper-component case needs at least 3", len(ancestors))
	}

	for ancestor := range ancestors {
		ancestor := ancestor
		t.Run("external "+ancestor, func(t *testing.T) {
			dir := stageResourceTree(t)
			relinkAncestor(t, dir, ancestor, t.TempDir(), true)
			err := mustFail(t, dir)
			if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "regular") {
				t.Fatalf("error %q does not name the linked ancestor", err)
			}
		})

		// A link that stays inside the tree never leaves the source root, so
		// a check built only around external escape would accept it.
		t.Run("internal "+ancestor, func(t *testing.T) {
			dir := stageResourceTree(t)
			inside := filepath.Join(dir, "internal-copy", filepath.FromSlash(ancestor))
			noErr(t, os.MkdirAll(inside, 0o755))
			relinkAncestor(t, dir, ancestor, inside, true)
			mustFail(t, dir)
		})
	}
}

// stageResourceTree writes a complete synthetic resource tree and confirms it
// is accepted before a case alters it.
func stageResourceTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range releaseResources {
		path := filepath.Join(dir, filepath.FromSlash(name))
		noErr(t, os.MkdirAll(filepath.Dir(path), 0o755))
		noErr(t, os.WriteFile(path, []byte("body\n"), 0o644))
	}
	if _, err := collectResources(dir); err != nil {
		t.Fatalf("the synthetic tree was refused before it was altered: %v", err)
	}
	return dir
}

// relinkAncestor replaces one ancestor directory with a link to target. The
// target is filled with the resources the original ancestor held, so the
// refusal is about the link rather than a missing file.
func relinkAncestor(t *testing.T, root, ancestor, target string, populate bool) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(ancestor))
	if populate {
		for _, name := range releaseResources {
			if !strings.HasPrefix(name, ancestor+"/") {
				continue
			}
			moved := filepath.Join(target, filepath.FromSlash(strings.TrimPrefix(name, ancestor+"/")))
			noErr(t, os.MkdirAll(filepath.Dir(moved), 0o755))
			noErr(t, os.WriteFile(moved, []byte("relocated body\n"), 0o644))
		}
	}
	noErr(t, os.RemoveAll(path))
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// TestAReachableRootIsStillAccepted keeps the ancestor rule from refusing a
// legitimate checkout.
//
// A source tree is often reached through a linked path, so the root itself is
// resolved and only the components below it have to be real.
func TestAReachableRootIsStillAccepted(t *testing.T) {
	dir := stageResourceTree(t)
	linked := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(dir, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	files, err := collectResources(linked)
	noErrf(t, err, "a source root reached through a link was refused")
	if len(files) != len(releaseResources) {
		t.Fatalf("collected %d files, want %d", len(files), len(releaseResources))
	}
	for _, file := range files {
		if file.sha == "" || file.size == 0 {
			t.Errorf("%s was collected without a digest or size", file.name)
		}
	}

	// A root that is not a directory is refused before any component walk,
	// so the failure names the root rather than a confusing missing child.
	notADirectory := filepath.Join(t.TempDir(), "root-file")
	noErr(t, os.WriteFile(notADirectory, []byte("not a tree\n"), 0o644))
	err = mustFail(t, notADirectory)
	if !strings.Contains(err.Error(), "source root") {
		t.Fatalf("error %q does not name the source root", err)
	}
}

func mustFail(t *testing.T, root string) error {
	t.Helper()
	files, err := collectResources(root)
	if err == nil {
		t.Fatalf("collectResources accepted an unsafe tree and returned %d files", len(files))
	}
	t.Logf("rejected: %v", err)
	return err
}

// TestTheMacAppCarriesTheResources checks the app bundle layout.
//
// The manifest file list is what the packaged bundle is built and verified
// from, so the recorded paths and digests are the layout.
func TestTheMacAppCarriesTheResources(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("macOS prototype requires the native Apple Silicon toolchain")
	}
	root := repoRoot(t)
	out := filepath.Join(t.TempDir(), "native-mac-resources")
	if err := nativeCommand([]string{
		"-source", root, "-manifest", filepath.Join(sharedDist(t), "manifest.json"),
		"-out", out, "-formats", "macos", "-baseline", testNativeBaseline,
	}); err != nil {
		t.Fatalf("native build: %v", err)
	}
	document := readNativeManifest(t, out)
	if len(document.Artifacts) != 1 {
		t.Fatalf("native manifest has %d artifacts", len(document.Artifacts))
	}
	recorded := map[string]fileEntry{}
	for _, file := range document.Artifacts[0].Files {
		recorded[file.Path] = file
	}
	for _, name := range releaseResources {
		bundled := "OwnGit.app/Contents/Resources/" + name
		file, ok := recorded[bundled]
		if !ok {
			t.Fatalf("the app does not carry %s", bundled)
		}
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		noErr(t, err)
		if file.SHA256 != sha256Bytes(source) {
			t.Errorf("%s does not match the source file", bundled)
		}
		if file.Mode != "0644" {
			t.Errorf("%s is recorded with mode %s", bundled, file.Mode)
		}
	}
}

// TestTheDebianPackageInstallsTheResources checks the installed DEB layout.
//
// A user who installed the package, rather than unpacking an archive, still
// has to be able to find and copy the skill.
func TestTheDebianPackageInstallsTheResources(t *testing.T) {
	root := repoRoot(t)
	portable := sharedDist(t)
	out := filepath.Join(t.TempDir(), "native-resources")
	if err := nativeCommand([]string{
		"-source", root, "-manifest", filepath.Join(portable, "manifest.json"),
		"-out", out, "-formats", "deb", "-baseline", testNativeBaseline,
	}); err != nil {
		t.Fatalf("native build: %v", err)
	}
	document := readNativeManifest(t, out)
	if len(document.Artifacts) == 0 {
		t.Fatal("the native manifest lists no artifacts")
	}

	for _, built := range document.Artifacts {
		packageData, err := os.ReadFile(filepath.Join(out, built.Name))
		noErr(t, err)
		members, err := readAr(packageData)
		noErr(t, err)
		data := readDebTarFiles(t, members[2].data)
		for _, name := range releaseResources {
			installed := "usr/share/doc/owngit/" + name
			file, ok := data[installed]
			if !ok {
				t.Fatalf("%s does not install %s", built.Name, installed)
			}
			source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			noErr(t, err)
			if string(file.data) != string(source) {
				t.Errorf("%s in %s does not match the source file", installed, built.Name)
			}
			if file.mode&0o111 != 0 {
				t.Errorf("%s is installed executable (mode %04o)", installed, file.mode)
			}
		}
		// The documented install path is what the README tells a user to
		// look at, so a silent relocation has to fail here.
		instructions := data["usr/share/doc/owngit/README.Debian"]
		if !strings.Contains(string(instructions.data), "/usr/share/doc/owngit/integrations/skills/owngit-checks/SKILL.md") {
			t.Errorf("%s does not tell the user where the skill is installed", built.Name)
		}
	}
}

// TestNativeResourcesComeFromTheVerifiedPayload checks the native layouts.
//
// A native package must ship the bytes the portable artifact was verified
// with. Reading the source tree again would let the two disagree.
func TestNativeResourcesComeFromTheVerifiedPayload(t *testing.T) {
	payload := portablePayload{entries: map[string]archiveEntry{}}
	for _, name := range releaseResources {
		payload.entries[name] = archiveEntry{name: name, data: []byte("verified " + name + "\n")}
	}

	files, err := resourceFiles(payload)
	noErrf(t, err, "resourceFiles rejected a complete payload")
	if len(files) != len(releaseResources) {
		t.Fatalf("resourceFiles returned %d files, want %d", len(files), len(releaseResources))
	}
	for _, file := range files {
		if string(file.data) != "verified "+file.path+"\n" {
			t.Errorf("%s does not carry the payload bytes", file.path)
		}
		if file.mode&0o111 != 0 {
			t.Errorf("%s is executable (mode %04o)", file.path, file.mode)
		}
	}

	// A payload that lost a resource fails rather than producing a package
	// without it.
	delete(payload.entries, releaseResources[0])
	if _, err := resourceFiles(payload); err == nil {
		t.Fatal("resourceFiles accepted a payload with a missing resource")
	} else {
		t.Logf("rejected: %v", err)
	}
}

// TestTheArchiveNoteDescribesARelativeInvocation checks the wording of the
// note placed inside every portable archive.
//
// The note prints ./owngit or .\owngit.exe. Those work while the shell is in
// the unpacked directory, and calling them a full path would send a reader
// looking for something that does not exist. Unpacking adds nothing to PATH,
// so the note has to say what to use from anywhere else.
func TestTheArchiveNoteDescribesARelativeInvocation(t *testing.T) {
	root := repoRoot(t)
	templatePath := filepath.Join(root, "packaging", "archive", "README.txt.tmpl")
	for _, current := range releaseTargets {
		rendered, err := renderArchiveReadme(templatePath, "1.0.0", current)
		noErrf(t, err, "%s", current)
		note := string(rendered)
		invocation := "./" + current.binary
		if current.goos == "windows" {
			invocation = `.\` + current.binary
		}
		if !strings.Contains(note, invocation) {
			t.Errorf("%s: the note does not show %s", current, invocation)
		}
		if !strings.Contains(note, "relative invocation") {
			t.Errorf("%s: the note does not call %s a relative invocation", current, invocation)
		}
		if !strings.Contains(note, "absolute path") {
			t.Errorf("%s: the note does not say what to use from another directory", current)
		}
		// The defect being fixed: the note called the relative form a full
		// path, which is what a reader would then try to use elsewhere.
		if strings.Contains(note, "full path to "+invocation) {
			t.Errorf("%s: the note still calls %s a full path", current, invocation)
		}
		// The resources the note points at have to be the packaged ones.
		for _, name := range releaseResources {
			if !strings.Contains(note, name) {
				t.Errorf("%s: the note does not point at %s", current, name)
			}
		}
	}
}

// TestAReplacementAfterTheCheckCannotReachTheArchive is the production-boundary
// regression for the collect-then-package flow.
//
// It drives the real functions the build uses: collectResources for the
// approved snapshot, then writeTarGz and writeZip, then the production reader
// and the manifest comparison. A file replaced after collection must not be
// packaged, and the archive must agree with the manifest the same run records.
func TestAReplacementAfterTheCheckCannotReachTheArchive(t *testing.T) {
	const original = "original resource body\n"
	// A same-size replacement defeats a size-only check, which is why the
	// identity comparison matters.
	replacement := strings.Repeat("x", len(original)-1) + "\n"
	if len(replacement) != len(original) {
		t.Fatalf("the replacement is %d bytes and the original %d", len(replacement), len(original))
	}

	for _, format := range []string{"tar.gz", "zip"} {
		format := format
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range releaseResources {
				path := filepath.Join(root, filepath.FromSlash(name))
				noErr(t, os.MkdirAll(filepath.Dir(path), 0o755))
				noErr(t, os.WriteFile(path, []byte(original), 0o644))
			}
			resources, err := collectResources(root)
			noErrf(t, err, "collectResources")
			target := resources[0]

			// An atomic replacement, the case a re-stat and re-hash would
			// silently adopt.
			pending := filepath.Join(root, "pending-replacement")
			noErr(t, os.WriteFile(pending, []byte(replacement), 0o644))
			noErr(t, os.Rename(pending, target.path))

			archive := filepath.Join(t.TempDir(), "resources."+format)
			if format == "zip" {
				err = writeZip(archive, resources)
			} else {
				err = writeTarGz(archive, resources)
			}
			noErrf(t, err, "writing the archive")
			entries, err := readArchive(archive, format)
			noErrf(t, err, "readArchive")

			// The manifest this run would record.
			var built artifact
			for _, file := range resources {
				built.Files = append(built.Files, fileEntry{
					Path: file.name, Mode: fmt.Sprintf("%04o", file.mode),
					Size: file.size, SHA256: file.sha,
				})
			}
			noErrf(t, compareEntries(built, entries), "the archive and the manifest disagree")
			noErrf(t, verifyResourceSet(entries), "verifyResourceSet")

			for _, entry := range entries {
				if entry.name != target.name {
					continue
				}
				if string(entry.data) == replacement {
					t.Fatal("the archive carries bytes written after the check")
				}
				if string(entry.data) != original {
					t.Fatalf("the archive carries unexpected bytes %q", entry.data)
				}
				if entry.sha != target.sha {
					t.Fatalf("archived digest %s does not match the approved %s", entry.sha, target.sha)
				}
			}
		})
	}
}

// TestTheStagedSnapshotAndTheManifestCannotDisagree checks the fail-closed
// path in the archive writer.
//
// The writer packages approved bytes, so a staged entry whose recorded size or
// digest no longer describes those bytes is a build failure rather than an
// archive that quietly contradicts its own manifest.
func TestTheStagedSnapshotAndTheManifestCannotDisagree(t *testing.T) {
	root := stageResourceTree(t)
	resources, err := collectResources(root)
	noErr(t, err)

	cases := map[string]func(stagedFile) stagedFile{
		"a digest that does not describe the bytes": func(file stagedFile) stagedFile {
			file.sha = strings.Repeat("0", 64)
			return file
		},
		"a size that does not describe the bytes": func(file stagedFile) stagedFile {
			file.size++
			return file
		},
	}
	for label, mutate := range cases {
		label, mutate := label, mutate
		t.Run(label, func(t *testing.T) {
			staged := append([]stagedFile(nil), resources...)
			staged[0] = mutate(staged[0])
			archive := filepath.Join(t.TempDir(), "resources.tar.gz")
			err := writeTarGz(archive, staged)
			if err == nil {
				t.Fatal("the writer accepted a snapshot that contradicts its manifest entry")
			}
			if !strings.Contains(err.Error(), staged[0].name) {
				t.Fatalf("error %q does not name the entry", err)
			}
			t.Logf("rejected: %v", err)
		})
	}
}

// TestAnApprovedSnapshotIsBoundToTheCheckedFile covers the identity binding.
//
// A regular mode and a matching size are not an identity. The check confirms
// the opened handle is the file the walk inspected, and re-checks afterwards,
// so a swap is refused instead of adopted.
func TestAnApprovedSnapshotIsBoundToTheCheckedFile(t *testing.T) {
	root := stageResourceTree(t)
	resources, err := collectResources(root)
	noErr(t, err)
	for _, file := range resources {
		if len(file.data) == 0 {
			t.Fatalf("%s was collected without its approved bytes", file.name)
		}
		if int64(len(file.data)) != file.size {
			t.Fatalf("%s holds %d bytes but records size %d", file.name, len(file.data), file.size)
		}
		if sha256Bytes(file.data) != file.sha {
			t.Fatalf("%s records a digest that does not describe its bytes", file.name)
		}
		// The snapshot is independent of the file it came from.
		onDisk, err := os.ReadFile(file.path)
		noErr(t, err)
		noErr(t, os.WriteFile(file.path, []byte("changed after collection\n"), 0o644))
		if sha256Bytes(file.data) != file.sha || string(file.data) != string(onDisk) {
			t.Fatalf("%s snapshot followed a later change to the file", file.name)
		}
	}

	// A leaf whose identity changes between the walk and the read is refused
	// rather than read as the inspected file. The walk is the production one,
	// so on Windows this also shows that it fixes the identity at the check
	// instead of leaving os.SameFile to look it up from the path afterwards.
	swapped, err := canonicalResourceRoot(stageResourceTree(t))
	noErr(t, err)
	leaf, walked, err := inspectResource(swapped, releaseResources[0])
	noErr(t, err)
	other := filepath.Join(swapped, "other-file")
	noErr(t, os.WriteFile(other, []byte("body\n"), 0o644))
	noErr(t, os.Rename(other, leaf))
	if _, _, err := readApprovedFile(leaf, walked); err == nil {
		t.Fatal("a replaced leaf was read as the inspected file")
	} else if !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("error %q does not report the replacement", err)
	} else {
		t.Logf("rejected: %v", err)
	}
}

// TestBuildTargetStagesResourcesAsApprovedSnapshots checks the assembly step
// inside buildTarget itself.
//
// The boundary test above drives collection and the writers directly, so it
// stays green if buildTarget goes back to re-adding the resources by path.
// This reads the source of that function and requires the snapshot to be
// appended rather than passed through the re-stat and re-hash closure, which
// is the specific regression that was reported.
func TestBuildTargetStagesResourcesAsApprovedSnapshots(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "tools", "release", "build.go"))
	noErr(t, err)
	body := string(data)
	start := strings.Index(body, "resources, err := collectResources(root)")
	if start < 0 {
		t.Fatal("buildTarget no longer collects the resources")
	}
	end := strings.Index(body[start:], "archivePath :=")
	if end < 0 {
		t.Fatal("the staging section could not be bounded")
	}
	section := body[start : start+end]

	if !strings.Contains(section, "files = append(files, resources...)") {
		t.Error("buildTarget does not stage the approved snapshots directly")
	}
	// add() re-stats and re-hashes its path argument, which discards the
	// checked identity and adopts whatever the path resolves to later.
	if strings.Contains(section, "add(resource.name") || strings.Contains(section, "add(resource.") {
		t.Error("buildTarget re-adds the resources by path, discarding the approved snapshot")
	}
}

// TestTheApprovedSnapshotSurvivesTheWholeStagedSet confirms the snapshot
// reaches the archive when it travels with the other staged entries.
//
// buildTarget stages a binary, a license, a rendered note and the notices
// alongside the resources. Those have no snapshot and are streamed from disk,
// so this checks the two kinds coexist rather than one path overwriting the
// other.
func TestTheApprovedSnapshotSurvivesTheWholeStagedSet(t *testing.T) {
	root := stageResourceTree(t)
	resources, err := collectResources(root)
	noErr(t, err)

	// A streamed entry, staged the way the build stages its own files.
	streamedPath := filepath.Join(t.TempDir(), "README.txt")
	streamed := []byte("streamed entry\n")
	noErr(t, os.WriteFile(streamedPath, streamed, 0o644))
	files := []stagedFile{{
		name: "README.txt", path: streamedPath, mode: 0o644,
		size: int64(len(streamed)), sha: sha256Bytes(streamed),
	}}
	files = append(files, resources...)

	// Replace a resource after collection, and change nothing about the
	// streamed entry.
	target := resources[0]
	pending := filepath.Join(root, "pending")
	noErr(t, os.WriteFile(pending, []byte("replacement body\n"), 0o644))
	noErr(t, os.Rename(pending, target.path))

	archive := filepath.Join(t.TempDir(), "mixed.tar.gz")
	noErrf(t, writeTarGz(archive, files), "writeTarGz")
	entries, err := readArchive(archive, "tar.gz")
	noErr(t, err)
	byName := map[string]archiveEntry{}
	for _, entry := range entries {
		byName[entry.name] = entry
	}
	if got := string(byName["README.txt"].data); got != string(streamed) {
		t.Errorf("the streamed entry carries %q", got)
	}
	if got := byName[target.name].sha; got != target.sha {
		t.Errorf("the resource carries digest %s, want the approved %s", got, target.sha)
	}
	if strings.Contains(string(byName[target.name].data), "replacement") {
		t.Error("the archive carries bytes written after the check")
	}
}
