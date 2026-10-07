package repository

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"owngit/internal/gitexec"
	"owngit/internal/hostmem"
)

// A tree entry whose name contains "/" flattens two entries into one path:
// git ls-tree -r then names that path twice. Git warns about the name rather
// than refusing it, so a repository stored before the receive-side object
// check, or one imported with an older list of tolerated message ids, can hold
// such a tree, and a reader that maps flattened paths would silently read one
// of the two entries or merge their data. Those readers refuse it instead.
var ErrRepeatedTreePath = errors.New("the repository contains a tree that names one path twice")

// repeatedTreePath reports whether a full tree listing names one path twice.
// Git writes one record per entry, "<mode> <type> <oid>\t<path>", with a NUL
// after each.
func repeatedTreePath(listing []byte) error {
	seen := make(map[string]struct{})
	for _, record := range bytes.Split(listing, []byte{0}) {
		_, name, found := bytes.Cut(record, []byte{'\t'})
		if !found {
			continue
		}
		if _, duplicate := seen[string(name)]; duplicate {
			return ErrRepeatedTreePath
		}
		seen[string(name)] = struct{}{}
	}
	return nil
}

// treeBudgetChunk is how many blobs one metadata pass of the rebuild check
// covers. A tree with very many files is checked in chunks, so the check holds
// one chunk of paths and one chunk of metadata at a time whatever the tree
// holds, and one chunk is all the metadata bound applies to.
const treeBudgetChunk = 32768

// checkTreeBudget reports the first file of a full tree listing that Git
// cannot rebuild within rebuildBound bytes of memory, the memory this computer
// can give Git at once (see rebuildCosts). It reads object metadata only and
// reports nothing when rebuildBound is 0: an unknown memory ceiling keeps the
// behavior of a computer with memory to spare. metadataAllowance bounds the
// metadata of one pass, which is what the check holds at once: a pass that
// needs more than that fails with ErrTreeCheckTooLarge instead of being read,
// and a tree of any size is checked in passes that each fit.
func (m *Manager) checkTreeBudget(ctx context.Context, repositoryPath string, listing []byte, metadataAllowance, rebuildBound int64) error {
	if rebuildBound <= 0 {
		return nil
	}
	chunk := int64(treeBudgetChunk)
	if fits := metadataAllowance / metadataLineBytes; fits < chunk {
		chunk = fits
	}
	if chunk <= 0 {
		// Not even one object's metadata fits the bound, so no tree can be
		// checked within it.
		return ErrTreeCheckTooLarge
	}
	entries := make([]TreeEntry, 0, chunk)
	check := func() error {
		if len(entries) == 0 {
			return nil
		}
		oids := make([]string, 0, len(entries))
		for _, entry := range entries {
			oids = append(oids, entry.OID)
		}
		costs, _, _, err := m.rebuildCosts(ctx, repositoryPath, oids, metadataAllowance)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if cost := costs[entry.OID]; cost > rebuildBound {
				return &DeltaRebuildError{Path: entry.Path, Cost: cost, Bound: rebuildBound}
			}
		}
		entries = entries[:0]
		return nil
	}
	for _, record := range bytes.Split(listing, []byte{0}) {
		meta, name, found := bytes.Cut(record, []byte{'\t'})
		if !found {
			continue
		}
		fields := bytes.Fields(meta)
		if len(fields) != 3 || !bytes.Equal(fields[1], []byte("blob")) {
			continue
		}
		entries = append(entries, TreeEntry{Path: string(name), OID: string(fields[2]), Type: "blob"})
		if int64(len(entries)) < chunk {
			continue
		}
		if err := check(); err != nil {
			return err
		}
	}
	return check()
}

// treeListingOrder reports a streamed listing whose records do not follow the
// order Git lists the entries of one tree. Git writes the entries of one tree
// in that order, so a record that does not come after the one before it means
// the listing holds entries of two trees at one path: ls-tree -r names only
// leaves, so a directory entry stored twice, or beside a file of the same
// path, is the case this catches. A reader that maps paths would otherwise
// select one of the two entries, or page through rows that share a path.
type treeListingOrder struct {
	previous string
	isTree   bool
	seen     bool
}

func (order *treeListingOrder) add(path string, isTree bool) error {
	// One tree holds one entry per name, so two records of one path are the
	// entries of two trees at that path, whether they are both files, both
	// folders, or one of each. A name that only comes before one of its
	// neighbours cannot come from one tree either, because Git writes the
	// entries of a tree in this order.
	if order.seen && (path == order.previous || !treePathLess(order.previous, order.isTree, path, isTree)) {
		return ErrRepeatedTreePath
	}
	order.previous, order.isTree, order.seen = path, isTree, true
	return nil
}

// treePathLess reports whether the entry named path sorts before the one named
// other in the order Git lists the entries of one tree, which is
// base_name_compare: a tree entry sorts as if its name ended with the
// separator, so the file a.txt comes before the directory a. Both names belong
// to the same listing, so their shared prefix cancels.
func treePathLess(path string, isTree bool, other string, otherIsTree bool) bool {
	common := 0
	for common < len(path) && common < len(other) && path[common] == other[common] {
		common++
	}
	left, right := byte(0), byte(0)
	switch {
	case common < len(path):
		left = path[common]
	case isTree:
		left = '/'
	}
	switch {
	case common < len(other):
		right = other[common]
	case otherIsTree:
		right = '/'
	}
	return left < right
}

// identityPath reports whether fullPath is the requested path or one of its
// ancestors. The listing of a read of the requested path holds those records:
// ls-tree -t prints the record of every directory it descends into, and the
// pathspecs of treeListingSpecs print the entry stored at each level, a file
// beside a directory of the same name included.
func identityPath(requested, fullPath string) bool {
	return fullPath == requested || strings.HasPrefix(requested, fullPath+"/")
}

// treeIdentityCheck refuses a listing that names the requested path, or one of
// its ancestors, twice. Two records at one of those paths mean that level
// holds two entries of the component the path continues with: two folders of
// one path, or a file beside a folder of it. A reader that maps the requested
// path would merge the children of both, or show one and hide the other, so it
// refuses the tree instead.
type treeIdentityCheck struct {
	requested string
	counts    map[string]int
}

func newTreeIdentityCheck(requested string) *treeIdentityCheck {
	if requested == "" {
		return nil
	}
	return &treeIdentityCheck{requested: requested, counts: make(map[string]int)}
}

// level counts one record of the requested path or of an ancestor level, and
// reports whether the record belongs to such a level. A level that arrives
// twice is refused.
func (check *treeIdentityCheck) level(fullPath string) (bool, error) {
	if check == nil || !identityPath(check.requested, fullPath) {
		return false, nil
	}
	check.counts[fullPath]++
	if check.counts[fullPath] > 1 {
		return true, ErrRepeatedTreePath
	}
	return true, nil
}

// treeSpecMagic starts the explicit pathspec magic, which keeps every path
// literal and was verified with Git for Windows.
const treeSpecMagic = ":(top,literal)"

// treeSpecLimit bounds the pathspec bytes of one read. One pathspec per level
// of the requested path is what makes a same-name file beside a directory of an
// ancestor level visible, and the sum of all prefix lengths grows with the
// square of the depth: a path of MaximumTreePathBytes reached about 4.2 MB
// above depth 2000, past the argument limit of every platform, and the Windows
// command line of 32,767 characters was passed near depth 60. The bound keeps
// the command line of a read far below that limit, and a deeper path passes its
// own pathspec and its folder's only. A valid path is never refused because of
// its depth.
//
// Above the bound these readers no longer see a same-name file beside a
// directory of an ancestor level. ls-tree -t still prints the record of every
// directory it descends into, so two directories at an ancestor level are still
// refused, and a read of the colliding path itself still refuses both shapes,
// because the requested path and its folder always keep their pathspecs. A
// tree is not checked again when it is restored from a backup or when it was
// stored before this change, so the gap remains for such a tree; new pushes are
// refused by receive.fsckObjects (Git's duplicateEntries) and imports by
// index-pack --strict, and the archive refuses the whole tree through
// VerifyTreePaths. This residual is a known limitation, accepted for this unit.
const treeSpecLimit = 8 << 10

// treeListingSpecs returns the pathspecs of one listing of path: every level of
// the path without a trailing slash, so the record stored at each level
// arrives, and the path with one, so the entries of the folder arrive. ls-tree
// -t prints the record of each directory it descends into, so these pathspecs
// make the one streaming listing carry the records of the path's levels as
// well. A path whose levels would pass treeSpecLimit keeps its own pathspec and
// its folder's only.
func treeListingSpecs(path string) []string {
	if path == "" {
		return nil
	}
	size := len(treeSpecMagic) + len(path) + 1
	for prefix := path; prefix != ""; prefix = treeParentPath(prefix) {
		size += len(treeSpecMagic) + len(prefix)
	}
	if size > treeSpecLimit {
		return []string{treeSpecMagic + path, treeSpecMagic + path + "/"}
	}
	specs := make([]string, 0, strings.Count(path, "/")+2)
	for prefix := path; prefix != ""; prefix = treeParentPath(prefix) {
		specs = append(specs, treeSpecMagic+prefix)
	}
	return append(specs, treeSpecMagic+path+"/")
}

// treeParentPath returns the folder that holds path, or "" at the top level.
func treeParentPath(path string) string {
	if separator := strings.LastIndexByte(path, '/'); separator >= 0 {
		return path[:separator]
	}
	return ""
}

// treeEntryName reports the name of the entry fullPath inside directory. A
// record named from a deeper level, and a stored name that already holds the
// separator, name no entry of that folder.
func treeEntryName(fullPath, directory string) (string, bool) {
	if directory == "" {
		if fullPath == "" || strings.Contains(fullPath, "/") {
			return "", false
		}
		return fullPath, true
	}
	name, ok := strings.CutPrefix(fullPath, directory+"/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// treeAboveEntry reports whether fullPath is an entry of a level above
// directory. The pathspecs of treeListingSpecs print such entries beside the
// records the read uses, and a read drops them.
func treeAboveEntry(fullPath, directory string) bool {
	for directory != "" {
		directory = treeParentPath(directory)
		if _, ok := treeEntryName(fullPath, directory); ok {
			return true
		}
	}
	return false
}

// VerifyTreePaths reads the whole tree of commitOID and refuses it when one
// path appears twice, the way every reader of the tree does. ls-tree -r lists
// leaves only, so -t adds the tree entries themselves: a file beside a
// directory of the same path, and two directories of one path, are then one
// path twice. It also reports the first file whose rebuild as a stored delta
// would need more memory than the computer can give Git at once; Git rebuilds
// such a file in memory while it writes an archive, and no file threshold
// bounds that (rebuildBound 0 checks nothing beyond the paths). limit bounds
// the listing, and the metadata of the check is bounded by the memory model of
// this computer, so a tree too large to check fails with the runner's limit
// error or with ErrTreeCheckTooLarge instead of being used unchecked. The
// caller holds the repository read lock.
func (m *Manager) VerifyTreePaths(ctx context.Context, repositoryPath, commitOID string, limit, rebuildBound int64) error {
	result, err := m.Git.RunWithLimits(ctx, repositoryPath, nil, gitexec.CommandLimits{OutputLimit: limit},
		"--git-dir", ".", "ls-tree", "-r", "-t", "-z", commitOID)
	if err != nil {
		return err
	}
	if err := repeatedTreePath(result.Stdout); err != nil {
		return err
	}
	return m.checkTreeBudget(ctx, repositoryPath, result.Stdout, hostmem.TreeMetadataBound(hostmem.Ceiling()), rebuildBound)
}
