package repository

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"owngit/internal/gitexec"
)

// This file holds the browsing reads that name their commit by object ID.
// A page resolves its branch or tag once with ResolveRef, from the cached ref
// snapshot, and passes the commit ID to these reads. Their results depend only
// on object IDs, so they are kept in the object cache (object_cache.go) and a
// repeated view starts no Git process.

// errDirectoryNotFound and errFileNotFound report a folder or a file that a
// commit does not have.
var (
	errDirectoryNotFound = errors.New("directory not found")
	errFileNotFound      = errors.New("file not found")
)

// ResolveRef returns the full ref name and commit ID of requested, a branch
// or tag given as a full ref (refs/heads/x, refs/tags/x) or, for older
// addresses, as a short name that prefers a branch over a tag. An empty
// requested names the default branch. A branch or lightweight tag that points
// at a commit is answered from the ref snapshot without Git. Anything else,
// such as an annotated or nested tag, is peeled to its commit with Git, as
// rev-parse's ^{commit} does, and that answer is cached by the tag's object
// ID. Refs written to the storage folder without OwnGit are seen after OwnGit
// next writes to the repository, as for the ref snapshot.
func (m *Manager) ResolveRef(ctx context.Context, id, requested string) (string, string, error) {
	snapshot, err := m.RefSnapshot(ctx, id)
	if err != nil {
		return "", "", err
	}
	if requested == "" {
		if snapshot.Summary.DefaultBranch == "" {
			return "", "", errors.New("repository has no default branch")
		}
		requested = "refs/heads/" + snapshot.Summary.DefaultBranch
	}
	candidates := []string{requested}
	if strings.HasPrefix(requested, "refs/heads/") || strings.HasPrefix(requested, "refs/tags/") {
		if err := validateShortRef(strings.TrimPrefix(strings.TrimPrefix(requested, "refs/heads/"), "refs/tags/")); err != nil {
			return "", "", err
		}
	} else {
		if err := validateShortRef(requested); err != nil {
			return "", "", err
		}
		// Keep accepting historical short URLs, with the documented
		// branch-first precedence. Newly generated URLs always carry the full
		// ref identity.
		candidates = []string{"refs/heads/" + requested, "refs/tags/" + requested}
	}
	for _, full := range candidates {
		ref, found := snapshotRef(snapshot.Summary, full)
		if !found {
			continue
		}
		if ref.Type == "commit" && isOID(ref.OID) {
			return full, ref.OID, nil
		}
		if commitOID, ok := m.peelToCommit(ctx, id, ref.OID); ok {
			return full, commitOID, nil
		}
	}
	return "", "", errors.New("branch or tag not found")
}

func snapshotRef(summary Summary, full string) (Ref, bool) {
	refs, name := summary.Branches, strings.TrimPrefix(full, "refs/heads/")
	if strings.HasPrefix(full, "refs/tags/") {
		refs, name = summary.Tags, strings.TrimPrefix(full, "refs/tags/")
	}
	for _, ref := range refs {
		if ref.Name == name {
			return ref, true
		}
	}
	return Ref{}, false
}

// peelToCommit returns the commit an object, such as an annotated tag,
// finally points to. ok is false when it points to no commit or Git could not
// tell; only a found commit is cached.
func (m *Manager) peelToCommit(ctx context.Context, id, oid string) (string, bool) {
	if !isOID(oid) {
		return "", false
	}
	result, err := m.cachedRead(ctx, id, "peel", oid, func(repositoryPath string) (cachedResult, bool, error) {
		output, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "rev-parse", "--verify", "--quiet", oid+"^{commit}")
		if err != nil {
			return cachedResult{}, false, err
		}
		commitOID := strings.TrimSpace(string(output.Stdout))
		if !isOID(commitOID) {
			return cachedResult{}, false, errors.New("Git returned an invalid commit ID")
		}
		return cachedResult{data: []byte(commitOID)}, true, nil
	})
	if err != nil {
		return "", false
	}
	return string(result.data), true
}

// cachedRead answers a read of repository id from the object cache, or runs
// read under the repository's read lock. The repository's existence and
// preparation are checked first, on every call. kind and key must name
// everything the result depends on.
func (m *Manager) cachedRead(ctx context.Context, id, kind, key string, read func(repositoryPath string) (cachedResult, bool, error)) (cachedResult, error) {
	repositoryPath, stored, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return cachedResult{}, err
	}
	return m.objects.load(ctx, namespaceFor(id, repositoryPath, stored.CreatedAt), kind, key, func() (cachedResult, bool, error) {
		lock := m.Locks.For(id)
		if err := readLock(ctx, lock); err != nil {
			return cachedResult{}, false, err
		}
		defer lock.RUnlock()
		return read(repositoryPath)
	})
}

// Tree lists directory at requestedRef. It returns the commit ID the ref
// resolved to.
func (m *Manager) Tree(ctx context.Context, id, requestedRef, directory string) (string, []TreeEntry, error) {
	if err := validateTreePath(directory); err != nil {
		return "", nil, err
	}
	_, commitOID, err := m.ResolveRef(ctx, id, requestedRef)
	if err != nil {
		return "", nil, err
	}
	entries, err := m.TreeAt(ctx, id, commitOID, directory)
	if err != nil {
		return "", nil, err
	}
	return commitOID, entries, nil
}

// TreeAt lists directory, or the top folder when directory is empty, in the
// commit commitOID with one Git process. A missing directory, a file, and a
// directory without entries all report "directory not found".
func (m *Manager) TreeAt(ctx context.Context, id, commitOID, directory string) ([]TreeEntry, error) {
	if !isOID(commitOID) {
		return nil, errors.New("invalid commit ID")
	}
	if err := validateTreePath(directory); err != nil {
		return nil, err
	}
	result, err := m.cachedRead(ctx, id, "tree", commitOID+"\x00"+directory, func(repositoryPath string) (cachedResult, bool, error) {
		args := []string{"--git-dir", ".", "ls-tree", "-z", "-l", commitOID}
		if directory != "" {
			// A pathspec ending in a slash lists the folder's entries and
			// matches nothing when the path is missing or is not a folder.
			// The explicit magic keeps the path literal; it was verified with
			// Git for Windows.
			args = append(args, "--", ":(top,literal)"+directory+"/")
		}
		output, err := m.Git.Run(ctx, repositoryPath, nil, args...)
		if err != nil {
			return cachedResult{}, false, err
		}
		return cachedResult{data: output.Stdout}, true, nil
	})
	if err != nil {
		return nil, err
	}
	entries, err := parseTreeListing(result.data, directory)
	if err != nil {
		return nil, err
	}
	if directory != "" && len(entries) == 0 {
		return nil, errDirectoryNotFound
	}
	return entries, nil
}

// parseTreeListing reads ls-tree -z -l output of the folder directory. With
// a folder, Git names each entry by its full path, which must be directly
// inside that folder.
func parseTreeListing(output []byte, directory string) ([]TreeEntry, error) {
	var entries []TreeEntry
	prefix := ""
	if directory != "" {
		prefix = directory + "/"
	}
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		entry, err := parseTreeEntry(record)
		if err != nil {
			return nil, err
		}
		name, ok := strings.CutPrefix(entry.Name, prefix)
		if !ok || name == "" || strings.Contains(name, "/") {
			return nil, errors.New("Git returned mismatched tree listing data")
		}
		entry.Name, entry.Path = name, entry.Name
		entries = append(entries, entry)
	}
	return entries, nil
}

// ReadBlob reads filePath at requestedRef. It returns the commit ID the ref
// resolved to.
func (m *Manager) ReadBlob(ctx context.Context, id, requestedRef, filePath string, limit int64) (string, Blob, error) {
	if err := validateTreePath(filePath); err != nil || filePath == "" {
		if err == nil {
			err = errors.New("file path is required")
		}
		return "", Blob{}, err
	}
	_, commitOID, err := m.ResolveRef(ctx, id, requestedRef)
	if err != nil {
		return "", Blob{}, err
	}
	blob, _, err := m.FileAt(ctx, id, commitOID, filePath, limit)
	if err != nil {
		return "", Blob{}, err
	}
	return commitOID, blob, nil
}

// PathView is what a path names in a commit: a folder with its entries, or
// a file with the entries of the folder that holds it.
type PathView struct {
	Folder bool
	// File is the file's entry when Folder is false.
	File TreeEntry
	// Entries are the folder's entries, or those of the file's folder.
	Entries []TreeEntry
}

// PathAt looks up filePath in the commit commitOID with one Git process, and
// none when cached. One listing asks for the entries of the path's parent
// folder and of the path itself: Git lists a folder's entries in place of
// the folder, and a file among its neighbours. An empty filePath is the top
// folder. A missing path, and one that is neither a file nor a folder with
// entries, reports "file not found".
func (m *Manager) PathAt(ctx context.Context, id, commitOID, filePath string) (PathView, error) {
	if filePath == "" {
		entries, err := m.TreeAt(ctx, id, commitOID, "")
		return PathView{Folder: true, Entries: entries}, err
	}
	if !isOID(commitOID) {
		return PathView{}, errors.New("invalid commit ID")
	}
	if err := validateTreePath(filePath); err != nil {
		return PathView{}, err
	}
	parent, parentSpec := "", ":(top)"
	if separator := strings.LastIndexByte(filePath, '/'); separator >= 0 {
		parent = filePath[:separator]
		parentSpec = ":(top,literal)" + parent + "/"
	}
	result, err := m.cachedRead(ctx, id, "path", commitOID+"\x00"+filePath, func(repositoryPath string) (cachedResult, bool, error) {
		output, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "ls-tree", "-z", "-l", commitOID, "--", parentSpec, ":(top,literal)"+filePath+"/")
		if err != nil {
			return cachedResult{}, false, err
		}
		return cachedResult{data: output.Stdout}, true, nil
	})
	if err != nil {
		return PathView{}, err
	}
	var siblings, children []TreeEntry
	for _, record := range bytes.Split(result.data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		entry, err := parseTreeEntry(record)
		if err != nil {
			return PathView{}, err
		}
		full := entry.Name
		if name, ok := strings.CutPrefix(full, filePath+"/"); ok && name != "" && !strings.Contains(name, "/") {
			entry.Name, entry.Path = name, full
			children = append(children, entry)
			continue
		}
		name := full
		if parent != "" {
			var ok bool
			if name, ok = strings.CutPrefix(full, parent+"/"); !ok {
				name = ""
			}
		}
		if name == "" || strings.Contains(name, "/") {
			return PathView{}, errors.New("Git returned mismatched tree listing data")
		}
		entry.Name, entry.Path = name, full
		siblings = append(siblings, entry)
	}
	if len(children) > 0 {
		return PathView{Folder: true, Entries: children}, nil
	}
	for _, entry := range siblings {
		if entry.Path == filePath && entry.Type == "blob" {
			return PathView{File: entry, Entries: siblings}, nil
		}
	}
	return PathView{}, errFileNotFound
}

// FileAt reads filePath in the commit commitOID and lists the folder that
// holds it: one listing, which gives the file's object ID, and one read of
// the file. At most limit bytes of the file are returned; Truncated reports a
// longer file.
func (m *Manager) FileAt(ctx context.Context, id, commitOID, filePath string, limit int64) (Blob, []TreeEntry, error) {
	if filePath == "" {
		return Blob{}, nil, errors.New("file path is required")
	}
	view, err := m.PathAt(ctx, id, commitOID, filePath)
	if err != nil {
		return Blob{}, nil, err
	}
	if view.Folder {
		return Blob{}, nil, errFileNotFound
	}
	blob, err := m.BlobAt(ctx, id, view.File, limit)
	return blob, view.Entries, err
}

// BlobAt reads the file entry, as listed by TreeAt, by its object ID. A file
// no larger than one cache entry is read whole and cached; the caller gets at
// most limit bytes of it.
func (m *Manager) BlobAt(ctx context.Context, id string, entry TreeEntry, limit int64) (Blob, error) {
	if !isOID(entry.OID) || entry.Type != "blob" {
		return Blob{}, errors.New("file not found")
	}
	if limit <= 0 {
		limit = 2 << 20
	}
	// A whole file fits in the cache only when its listed size does; a
	// larger one is read up to limit and not kept.
	whole := entry.Size >= 0 && entry.Size <= objectCacheItem
	readLimit := limit + 1
	key := entry.OID + "\x00" + strconv.FormatInt(readLimit, 10)
	if whole {
		readLimit, key = entry.Size+1, entry.OID
	}
	result, err := m.cachedRead(ctx, id, "blob", key, func(repositoryPath string) (cachedResult, bool, error) {
		output, err := m.Git.RunWithOutputLimit(ctx, repositoryPath, nil, readLimit, "--git-dir", ".", "cat-file", "blob", entry.OID)
		var limitErr *gitexec.LimitError
		if err != nil && !errors.As(err, &limitErr) {
			return cachedResult{}, false, err
		}
		complete := err == nil
		return cachedResult{data: output.Stdout, truncated: !complete}, complete && whole, nil
	})
	if err != nil {
		return Blob{}, err
	}
	blob := Blob{Path: entry.Path, OID: entry.OID, Content: result.data, Truncated: result.truncated}
	if int64(len(blob.Content)) > limit {
		blob.Content = blob.Content[:limit]
		blob.Truncated = true
	}
	blob.Binary = bytes.IndexByte(blob.Content, 0) >= 0
	return blob, nil
}

// Commits lists up to limit commits of requestedRef, newest first. It returns
// the commit ID the ref resolved to.
func (m *Manager) Commits(ctx context.Context, id, requestedRef string, limit int) (string, []Commit, error) {
	_, commitOID, err := m.ResolveRef(ctx, id, requestedRef)
	if err != nil {
		return "", nil, err
	}
	commits, err := m.CommitsAt(ctx, id, commitOID, limit)
	return commitOID, commits, err
}

// CommitPageSize is how many commits the list of commits shows.
const CommitPageSize = 100

func commitListKey(commitOID string, limit int) string {
	return commitOID + "\x00" + strconv.Itoa(limit)
}

// CommitsAt lists up to limit commits reachable from commitOID, newest
// first, with one Git process.
func (m *Manager) CommitsAt(ctx context.Context, id, commitOID string, limit int) ([]Commit, error) {
	if !isOID(commitOID) {
		return nil, errors.New("invalid commit ID")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	result, err := m.cachedRead(ctx, id, "log", commitListKey(commitOID, limit), func(repositoryPath string) (cachedResult, bool, error) {
		output, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "log", "-z", "--no-decorate", "--max-count="+strconv.Itoa(limit), "--format="+commitLogFormat, commitOID)
		if err != nil {
			return cachedResult{}, false, err
		}
		return cachedResult{data: output.Stdout}, true, nil
	})
	if err != nil {
		return nil, err
	}
	return parseCommits(result.data)
}

// CommitReachableFrom reports whether commitOID is rootOID or one of its
// ancestors. The answer never changes for the two IDs, so it is cached.
func (m *Manager) CommitReachableFrom(ctx context.Context, id, rootOID, commitOID string) (bool, error) {
	if !isOID(rootOID) || !isOID(commitOID) {
		return false, errors.New("invalid commit ID")
	}
	if rootOID == commitOID {
		return true, nil
	}
	// A commit opened from the list of commits is in that list's cached
	// read, which answers without Git.
	if repositoryPath, stored, exists, err := m.ExistingPath(ctx, id); err == nil && exists {
		if listed, ok := m.objects.peek(namespaceFor(id, repositoryPath, stored.CreatedAt), "log", commitListKey(rootOID, CommitPageSize)); ok {
			if commits, err := parseCommits(listed.data); err == nil {
				for _, commit := range commits {
					if commit.OID == commitOID {
						return true, nil
					}
				}
			}
		}
	}
	result, err := m.cachedRead(ctx, id, "ancestor", commitOID+"\x00"+rootOID, func(repositoryPath string) (cachedResult, bool, error) {
		_, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "merge-base", "--is-ancestor", commitOID, rootOID)
		if err == nil {
			return cachedResult{data: []byte{1}}, true, nil
		}
		if code, ok := gitexec.ExitCode(err); ok && code == 1 && ctx.Err() == nil {
			return cachedResult{data: []byte{0}}, true, nil
		}
		return cachedResult{}, false, err
	})
	if err != nil {
		return false, err
	}
	return len(result.data) == 1 && result.data[0] == 1, nil
}

// CommitFiles reads a commit's metadata and the files it changes, with their
// line counts, in one Git process. Renames are not detected, so each changed
// path is listed once with its own status. A merge commit lists no files.
func (m *Manager) CommitFiles(ctx context.Context, id, oid string) (Commit, []ChangedFile, error) {
	if !isOID(oid) {
		return Commit{}, nil, errors.New("invalid commit ID")
	}
	result, err := m.cachedRead(ctx, id, "commit-files", oid, func(repositoryPath string) (cachedResult, bool, error) {
		output, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "log", "--no-walk", "--max-count=1", "-z", "--no-decorate",
			"--format="+commitLogFormat, "--raw", "--numstat", "--no-renames", "--root", "--no-ext-diff", "--no-textconv", oid)
		if err != nil {
			return cachedResult{}, false, errors.New("commit not found")
		}
		return cachedResult{data: output.Stdout}, true, nil
	})
	if err != nil {
		return Commit{}, nil, err
	}
	// The ten metadata fields each end with NUL. When the commit changes
	// files, a newline and the file records follow.
	data := result.data
	end := 0
	for field := 0; field < 10; field++ {
		next := bytes.IndexByte(data[end:], 0)
		if next < 0 {
			return Commit{}, nil, errors.New("Git returned malformed commit metadata")
		}
		end += next + 1
	}
	commits, err := parseCommits(data[:end])
	if err != nil || len(commits) != 1 || commits[0].OID != oid {
		return Commit{}, nil, errors.New("Git returned malformed commit metadata")
	}
	files, _, complete, _ := parseChanges(bytes.TrimPrefix(data[end:], []byte{'\n'}))
	if !complete {
		return Commit{}, nil, errors.New("Git returned malformed changed-file records")
	}
	return commits[0], files, nil
}

// CommitPatch reads the text diff of commit oid against its parent, or
// against nothing for a root commit, with one Git process. With filePath it
// reads that file alone; otherwise every changed file except those in
// excluded. Output past limit is cut, the process is stopped, and truncated
// is set; the last file's part may then be incomplete. The result is cached
// for the same arguments.
func (m *Manager) CommitPatch(ctx context.Context, id, oid, filePath string, excluded []string, limit int64) (string, bool, error) {
	if !isOID(oid) {
		return "", false, errors.New("invalid commit ID")
	}
	if filePath != "" {
		if err := validateTreePath(filePath); err != nil {
			return "", false, err
		}
	}
	args := []string{"--git-dir", ".", "diff-tree", "-p", "--root", "--no-commit-id", "-r", "--no-renames",
		"--no-ext-diff", "--no-textconv", "--unified=3", "--src-prefix=a/", "--dst-prefix=b/", oid}
	if filePath != "" {
		args = append(args, "--", ":(top,literal)"+filePath)
	} else if len(excluded) > 0 {
		args = append(args, "--")
		for _, path := range excluded {
			args = append(args, ":(exclude,top,literal)"+path)
		}
	}
	key := strings.Join(args[3:], "\x00") + "\x00" + strconv.FormatInt(limit, 10)
	result, err := m.cachedRead(ctx, id, "commit-patch", key, func(repositoryPath string) (cachedResult, bool, error) {
		output, err := m.Git.RunWithLimits(ctx, repositoryPath, nil, gitexec.CommandLimits{OutputLimit: limit, StopAtOutputLimit: true}, args...)
		var limitErr *gitexec.LimitError
		if err != nil && !errors.As(err, &limitErr) {
			return cachedResult{}, false, err
		}
		return cachedResult{data: output.Stdout, truncated: err != nil}, true, nil
	})
	if err != nil {
		return "", false, err
	}
	return string(result.data), result.truncated, nil
}

// parseChanges reads the file records of a -z --raw --numstat diff at the
// start of data: first ":modes oids status" and the path for every file,
// then "additions<TAB>deletions<TAB>path" for every file, each ended by NUL.
// It returns the files in Git's order and the offset after the records. A
// NUL right after them, which Git writes before a patch, is included in the
// offset and reported by separated. complete is false when data ends inside
// the records, as a cut output does; files then holds the files read in
// full.
func parseChanges(data []byte) (files []ChangedFile, end int, complete, separated bool) {
	position := 0
	token := func() ([]byte, bool) {
		next := bytes.IndexByte(data[position:], 0)
		if next < 0 {
			return nil, false
		}
		value := data[position : position+next]
		position += next + 1
		return value, true
	}
	for position < len(data) && data[position] == ':' {
		start := position
		meta, ok := token()
		if !ok {
			return files, start, false, false
		}
		path, ok := token()
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 5 || len(path) == 0 {
			return files, start, false, false
		}
		files = append(files, ChangedFile{Path: string(path), Status: changedStatus(fields[4][0])})
	}
	counts := make(map[string]lineCount, len(files))
	for position < len(data) {
		start := position
		record, ok := token()
		if !ok {
			return files, start, false, false
		}
		if len(record) == 0 {
			// The separator before a patch.
			separated = true
			break
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(fields) != 3 || len(fields[2]) == 0 {
			return files, start, false, false
		}
		count := lineCount{}
		if string(fields[0]) == "-" || string(fields[1]) == "-" {
			count.binary = true
		} else {
			count.additions, _ = strconv.Atoi(string(fields[0]))
			count.deletions, _ = strconv.Atoi(string(fields[1]))
		}
		counts[string(fields[2])] = count
	}
	for index := range files {
		count, ok := counts[files[index].Path]
		if !ok {
			// Every listed file has a count; a missing one means the counts
			// were cut off.
			return files[:index], position, false, separated
		}
		files[index].Additions, files[index].Deletions, files[index].Binary = count.additions, count.deletions, count.binary
	}
	return files, position, true, separated
}
