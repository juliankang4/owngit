package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Coding-tool resources shipped beside the binary.
//
// An installed user needs the skill and its guide without cloning the source,
// so they travel in every artifact. The guide ships in English and Korean,
// and each version links to the other. They are declared here by name rather than
// discovered by walking a directory, for the same reason the notice set is:
// packaging must not silently gain or lose a file.
//
// The archive keeps each resource at its source-relative path. The guide links
// to the skill with a relative path, and preserving the layout keeps that link
// working in the installed tree without maintaining a rewritten copy. The same
// holds for the link between the two language versions of the guide.
var releaseResources = []string{
	"docs/CODING_TOOLS.md",
	"docs/CODING_TOOLS.ko.md",
	"integrations/skills/owngit-checks/SKILL.md",
}

// resourcePrefixes are the archive directories the resources own. Nothing else
// may appear under them, which is what makes an undeclared addition visible.
var resourcePrefixes = []string{"docs/", "integrations/"}

// collectResources reads the declared resources from the source tree.
//
// Checking only the leaf is not enough. A resource sits several directories
// below the root, and a linked docs or integrations/skills directory would
// package bytes from outside the source tree while every leaf still looked
// like a real file. Every component below the authoritative root is therefore
// checked.
//
// The returned entries carry the approved bytes, not just a path. Later stages
// package that snapshot, so a change made after the check cannot reach an
// archive. This bounds what a build reads, not what another process on the
// same account can do; a user who can write these files can also change the
// source the build compiles.
func collectResources(root string) ([]stagedFile, error) {
	// The root itself may legitimately be reached through a link, so it is
	// resolved once and its resolved form is the authority. Everything below
	// it must then be a real path, not a link leading back out.
	base, err := canonicalResourceRoot(root)
	if err != nil {
		return nil, err
	}
	files := make([]stagedFile, 0, len(releaseResources))
	for _, name := range releaseResources {
		if err := validateRelativePath(name); err != nil {
			return nil, fmt.Errorf("resource %q: %w", name, err)
		}
		file, err := readResourceUnderRoot(base, name)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

// canonicalResourceRoot resolves the source root to the identity the component
// checks are measured against.
func canonicalResourceRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve source root: %w", err)
	}
	identity, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve source root identity: %w", err)
	}
	info, err := os.Stat(identity)
	if err != nil {
		return "", fmt.Errorf("inspect source root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("source root %s is not a directory", identity)
	}
	return filepath.Clean(identity), nil
}

// readResourceUnderRoot returns one declared resource as a verified entry with
// its approved bytes.
func readResourceUnderRoot(base, name string) (stagedFile, error) {
	path, leaf, err := inspectResource(base, name)
	if err != nil {
		return stagedFile{}, err
	}
	data, digest, err := readApprovedFile(path, leaf)
	if err != nil {
		return stagedFile{}, err
	}
	if len(data) == 0 {
		return stagedFile{}, fmt.Errorf("%s is empty", path)
	}
	return stagedFile{
		name: name, path: path, mode: 0o644,
		size: int64(len(data)), sha: digest, data: data,
	}, nil
}

// inspectResource walks one declared resource path component by component and
// returns the leaf path with the identity the walk accepted.
//
// Each intermediate component must be a real directory and the leaf a real
// regular file. os.Lstat does not follow the final component, so a link is
// seen rather than resolved. On Windows a reparse point surfaces as a symlink
// or an irregular mode, so both are refused. The leaf is inspected with
// lstatIdentity so its identity is fixed at this check.
func inspectResource(base, name string) (string, os.FileInfo, error) {
	components := strings.Split(name, "/")
	path := base
	for index, component := range components {
		path = filepath.Join(path, component)
		last := index == len(components)-1
		var info os.FileInfo
		var err error
		if last {
			info, err = lstatIdentity(path)
		} else {
			info, err = os.Lstat(path)
		}
		if err != nil {
			return "", nil, err
		}
		mode := info.Mode()
		if mode&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("%s is a symlink; a packaged resource must be a real path under %s", path, base)
		}
		if mode&os.ModeIrregular != 0 {
			return "", nil, fmt.Errorf("%s is not a regular path; a packaged resource must not cross a reparse boundary", path)
		}
		if !last {
			if !mode.IsDir() {
				return "", nil, fmt.Errorf("%s is not a directory", path)
			}
			continue
		}
		if !mode.IsRegular() {
			return "", nil, fmt.Errorf("%s is not a regular file", path)
		}
		return path, info, nil
	}
	return "", nil, fmt.Errorf("resource %q has no path components", name)
}

// readApprovedFile reads the file the component walk accepted and returns its
// bytes and digest.
//
// A regular mode and a matching size are not an identity: the leaf could be
// replaced between the walk and the open, and the replacement could be the
// same size. os.SameFile compares the underlying file identity, so the opened
// handle is confirmed to be the inspected file. walked must come from
// lstatIdentity, which fixes that identity at the check. The path is checked
// again after the read, which turns a replacement during the read into a build
// failure instead of a silently packaged file.
func readApprovedFile(path string, walked os.FileInfo) ([]byte, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !opened.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s is not a regular file", path)
	}
	if walked == nil || !os.SameFile(walked, opened) {
		return nil, "", fmt.Errorf("%s was replaced between the check and the read", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) != opened.Size() {
		return nil, "", fmt.Errorf("%s changed while it was read", path)
	}
	// Re-checking the path shows a replacement that happened during the read.
	// It does not make the read atomic, and it cannot stop a same-account
	// writer; it makes such a change fail the build rather than pass silently.
	after, err := lstatIdentity(path)
	if err != nil {
		return nil, "", err
	}
	if !after.Mode().IsRegular() || !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return nil, "", fmt.Errorf("%s changed while it was read", path)
	}
	return data, sha256Bytes(data), nil
}

// lstatIdentity inspects path without following a final link, like os.Lstat,
// and fixes the file identity at the time of the call.
//
// os.SameFile needs that. On Unix every Lstat result carries the device and
// inode. On Windows a path-based os.Lstat of an ordinary file records only the
// path, and os.SameFile looks up the volume and file index from that path when
// it compares, so a leaf replaced after the check would be compared as the
// replacement. os.Root.Lstat inspects the entry through an opened handle, which
// records the volume and file index immediately on Windows and uses fstatat on
// Unix.
func lstatIdentity(path string) (os.FileInfo, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	info, err := parent.Lstat(filepath.Base(path))
	if pathErr, ok := err.(*os.PathError); ok {
		// Report the full path the caller named rather than the root-relative leaf.
		return nil, &os.PathError{Op: "lstat", Path: path, Err: pathErr.Err}
	}
	return info, err
}

// verifyResourceSet checks the resources inside a built archive.
//
// The entry digests are already compared with the manifest, so this answers a
// different question: whether the declared set is complete and whether the
// directories it owns gained anything undeclared.
func verifyResourceSet(entries []archiveEntry) error {
	byName := map[string]archiveEntry{}
	for _, entry := range entries {
		byName[entry.name] = entry
	}
	declared := map[string]bool{}
	for _, name := range releaseResources {
		declared[name] = true
		entry, ok := byName[name]
		if !ok {
			return fmt.Errorf("archive is missing the declared resource %s", name)
		}
		if entry.size == 0 || len(entry.data) == 0 {
			return fmt.Errorf("the packaged resource %s is empty", name)
		}
		if entry.mode&0o111 != 0 {
			return fmt.Errorf("%s is executable (mode %04o); a resource is data", name, entry.mode)
		}
	}
	for name := range byName {
		owned := false
		for _, prefix := range resourcePrefixes {
			if strings.HasPrefix(name, prefix) {
				owned = true
				break
			}
		}
		if owned && !declared[name] {
			return fmt.Errorf("archive contains the undeclared resource %s", name)
		}
	}
	return nil
}

// resourceFiles pulls the declared resources out of a verified portable
// payload, so a native package ships the same bytes the portable archive was
// verified with rather than reading the source tree again.
func resourceFiles(payload portablePayload) ([]nativePackageFile, error) {
	files := make([]nativePackageFile, 0, len(releaseResources))
	for _, name := range releaseResources {
		entry, ok := payload.entries[name]
		if !ok {
			return nil, fmt.Errorf("portable artifact does not contain the resource %s", name)
		}
		if err := validateNativePath(name); err != nil {
			return nil, err
		}
		if len(entry.data) == 0 {
			return nil, fmt.Errorf("the portable resource %s is empty", name)
		}
		files = append(files, nativePackageFile{path: name, mode: 0o644, data: entry.data})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}
