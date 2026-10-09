package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/hostmem"
)

// deltaFixture writes one path twice with similar content and packs the
// repository again, so Git stores the older object as a delta against the
// newer one. It returns the commit, the older object, and the newer object.
// The premise is checked: Git's packing decides, and a fixture that no longer
// stores a delta would make the tests below pass without testing anything.
func deltaFixture(t *testing.T, work string) (commit, older, newer string) {
	t.Helper()
	line := []byte("a line of the delta fixture file\n")
	commitFile(t, work, string(bytes.Repeat(line, 4096)), "large first", "2024-01-01T00:00:00Z")
	commitFile(t, work, string(append(bytes.Repeat(line, 4096), []byte("a change\n")...)), "large second", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	commit = gitOutput(t, work, "rev-parse", "HEAD")
	older = gitOutput(t, work, "rev-parse", "HEAD~1:file.txt")
	newer = gitOutput(t, work, "rev-parse", "HEAD:file.txt")
	remote := gitOutput(t, work, "remote", "get-url", "origin")
	runGit(t, "", "--git-dir", remote, "repack", "-a", "-d", "-f", "--window=10", "--depth=1")
	if base := deltabaseOf(t, remote, older); base == "" {
		t.Fatal("the fixture no longer stores the older object as a delta; the packed shape the tests need is gone")
	}
	return commit, older, newer
}

// deltabaseOf returns the object Git stores oid as a delta against, or "".
func deltabaseOf(t *testing.T, directory, oid string) string {
	t.Helper()
	output := gitInputOutput(t, directory, []byte(oid+"\n"), "cat-file", "--batch-check=%(deltabase)")
	if fields := strings.Fields(output); len(fields) > 0 {
		return strings.Trim(fields[0], "0")
	}
	return ""
}

// The memory a read needs is what Git holds at once: the object, its stored
// base twice and the bases of that chain, since a file can be small and its
// stored base large. The cost is those sizes with the measured overhead, so a
// chain of two objects costs twice the larger one and more than the two sizes
// put together, and a file whose base is larger than itself costs far more than
// its own size (measured on Git 2.47.3: deleting a 4 MiB file whose base is
// 96 MiB used 200 MiB).
func TestRebuildCostFollowsTheWholeDeltaChain(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	_, older, newer := deltaFixture(t, work)
	remote, err := manager.Path("sample")
	noErr(t, err)

	// One object of the fixture holds about 124 KiB, and the older object is
	// stored as a delta against the newer one, so the chain of the older
	// object holds both and the larger one counts twice. The two objects
	// differ by the last line, so each is measured on its own.
	objectSize, err := strconv.ParseInt(gitOutput(t, work, "cat-file", "-s", newer), 10, 64)
	noErr(t, err)
	baseSize, err := strconv.ParseInt(gitOutput(t, work, "cat-file", "-s", older), 10, 64)
	noErr(t, err)
	costs, _, _, err := manager.rebuildCosts(ctx, remote, []string{older, newer}, 1<<20)
	noErr(t, err)
	largest := baseSize
	if objectSize > largest {
		largest = objectSize
	}
	wantOlder := rebuiltCost(largest + baseSize + objectSize)
	if costs[older] != wantOlder {
		t.Errorf("the cost of a delta is %d, want %d (both objects with the larger one twice)", costs[older], wantOlder)
	}
	if costs[newer] != 0 {
		t.Errorf("an object Git does not store as a delta costs %d, want nothing", costs[newer])
	}
	// A bound between one object and a chain of two objects refuses the older
	// object only: the newer one is streamed, not rebuilt.
	bound := rebuiltCost(objectSize) + 1
	entries := []TreeEntry{{Path: "file.txt", OID: older, Type: "blob"}}
	var failure *DeltaRebuildError
	err = manager.checkTreeBudget(ctx, remote, treeListing(entries), 1<<20, bound)
	if !errors.As(err, &failure) {
		t.Fatalf("a chain above the bound was not refused: %v", err)
	}
	if failure.Path != "file.txt" || failure.Bound != bound || failure.Cost != wantOlder {
		t.Errorf("the refusal does not name the file, the cost and the bound: %+v", failure)
	}
	noErr(t, manager.checkTreeBudget(ctx, remote, treeListing([]TreeEntry{{Path: "file.txt", OID: newer, Type: "blob"}}), 1<<20, bound))
	// An unknown memory ceiling refuses nothing.
	noErr(t, manager.checkTreeBudget(ctx, remote, treeListing(entries), 1<<20, 0))
}

// treeListing writes the listing Git prints for entries, so a test can check
// the tree budget without a commit that holds them all.
func treeListing(entries []TreeEntry) []byte {
	var listing bytes.Buffer
	for _, entry := range entries {
		listing.WriteString("100644 blob " + entry.OID + "\t" + entry.Path + "\x00")
	}
	return listing.Bytes()
}

// A changed file is marked as larger than this computer compares as text only
// when the read of one of its sides needs more memory than the bound, so a
// small file keeps its line counts, a deleted file is marked for its old side,
// and a text file whose old version was large is marked too: Git reads that
// object while it writes the patch.
func TestMarkBinaryBySizeFollowsBothSidesOfTheChange(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	_, older, newer := deltaFixture(t, work)
	small := gitInputOutput(t, remotePath(t, manager), []byte("small binary fixture\x00\n"), "hash-object", "-w", "--stdin")
	remote, err := manager.Path("sample")
	noErr(t, err)

	bound := int64(1 << 17)
	changed := []ChangedFile{
		{Path: "added.bin", Status: "A", Binary: true, newOID: older},
		{Path: "deleted.bin", Status: "D", Binary: true, oldOID: older},
		{Path: "small.bin", Status: "A", Binary: true, newOID: small},
		{Path: "text.txt", Status: "M", oldOID: older, newOID: newer},
		{Path: "small.txt", Status: "M", oldOID: small, newOID: small},
	}
	noErr(t, manager.markBinaryBySizeWithin(ctx, remote, changed, bound))
	if !changed[0].TextDiffUnavailable || !changed[1].TextDiffUnavailable || !changed[3].TextDiffUnavailable {
		t.Errorf("a file above the bound on one side is not marked: %+v", changed)
	}
	if changed[2].TextDiffUnavailable || changed[4].TextDiffUnavailable {
		t.Errorf("a file below the bound was marked: %+v", changed[2:])
	}
	// An unknown memory ceiling compares every file as loudly as Git does, so
	// the flag stays off.
	unchanged := []ChangedFile{{Path: "added.bin", Status: "A", Binary: true, newOID: older}}
	noErr(t, manager.markBinaryBySizeWithin(ctx, remote, unchanged, 0))
	if unchanged[0].TextDiffUnavailable {
		t.Errorf("a computer with an unknown ceiling marked a file: %+v", unchanged[0])
	}
}

// A comparison leaves a file above the bound out of its patch read, so Git
// never reads the file whole, and the file keeps its flag so the page can say
// why it has no lines. An added and a deleted file are covered: neither has a
// text diff Git could skip on its own.
func TestCompareLeavesAFileAboveTheBoundOutOfThePatch(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	second, older, _ := deltaFixture(t, work)
	// The object Git stores as a delta holds the older content, so a commit
	// that writes that content to two paths makes an added and a modified
	// file whose read would rebuild it. A later commit deletes the file,
	// which reads the same object from the old side.
	olderContent := bytes.Repeat([]byte("a line of the delta fixture file\n"), 4096)
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), olderContent, 0o600))
	noErr(t, os.WriteFile(filepath.Join(work, "added.txt"), olderContent, 0o600))
	runGit(t, work, "add", "file.txt", "added.txt")
	runGit(t, work, "commit", "-qm", "the old content at two paths")
	changed := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "rm", "-q", "file.txt")
	runGit(t, work, "commit", "-qm", "delete the file")
	removed := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")

	previous := memoryBudget
	memoryBudget = func() int64 { return 1 << 17 }
	defer func() { memoryBudget = previous }()
	if base := deltabaseOf(t, remotePath(t, manager), older); base == "" {
		t.Fatal("the fixture no longer stores the older object as a delta")
	}

	comparison, err := manager.Compare(ctx, "sample", second, changed, 1<<20, 30*time.Second)
	noErr(t, err)
	if len(comparison.Files) != 2 {
		t.Fatalf("the comparison lists %d files, want the added and the modified one", len(comparison.Files))
	}
	if comparison.PatchTooLarge {
		t.Errorf("a comparison whose files fit one command line says its patch was too large to read")
	}
	for _, file := range comparison.Files {
		if !file.TextDiffUnavailable {
			t.Errorf("the file above the bound is not marked: %+v", file)
		}
		if strings.Contains(comparison.Patch, file.Path) {
			t.Errorf("the patch read includes %s, which it should leave out:\n%s", file.Path, comparison.Patch)
		}
	}
	_, patch, truncated, found, err := manager.CompareFile(ctx, "sample", second, changed, "file.txt", 1<<20, 30*time.Second)
	noErr(t, err)
	if !found || truncated || patch != "" {
		t.Errorf("the single file comparison read the file it should leave out: found=%v truncated=%v patch=%q", found, truncated, patch)
	}

	comparison, err = manager.Compare(ctx, "sample", changed, removed, 1<<20, 30*time.Second)
	noErr(t, err)
	if len(comparison.Files) != 1 || !comparison.Files[0].TextDiffUnavailable {
		t.Fatalf("the deleted file above the bound is not marked: %+v", comparison.Files)
	}
	if strings.Contains(comparison.Patch, "file.txt") {
		t.Errorf("the patch read includes the deleted file it should leave out:\n%s", comparison.Patch)
	}
	// A bound above the chain keeps the files in the comparison, which is what
	// a computer with memory to spare does.
	memoryBudget = func() int64 { return 1 << 30 }
	comparison, err = manager.Compare(ctx, "sample", second, changed, 1<<20, 30*time.Second)
	noErr(t, err)
	if len(comparison.Files) != 2 {
		t.Fatalf("the comparison lists %d files, want two", len(comparison.Files))
	}
	for _, file := range comparison.Files {
		if file.TextDiffUnavailable {
			t.Errorf("a file below the bound was left out: %+v", file)
		}
		if !strings.Contains(comparison.Patch, file.Path) {
			t.Errorf("the patch of %s is missing:\n%s", file.Path, comparison.Patch)
		}
	}
}

// remotePath returns the bare repository the manager reads for its fixture.
func remotePath(t *testing.T, manager *Manager) string {
	t.Helper()
	path, err := manager.Path("sample")
	noErr(t, err)
	return path
}

// deepChainFixture commits many versions of one small file, each with one line
// changed, and packs the repository again, so Git stores the middle versions at
// the end of a long delta chain. It returns the object with the deepest chain
// and that depth. The premise is checked by the caller: Git's packing decides,
// and a fixture that no longer holds a deep chain would make the tests below
// pass without testing anything.
func deepChainFixture(t *testing.T, work string, versions int) (deepest string, depth int) {
	t.Helper()
	content := strings.Repeat("a line of the deep chain fixture\n", 80)
	for index := 0; index < versions; index++ {
		lines := strings.Split(content, "\n")
		if position := index % len(lines); position < len(lines) {
			lines[position] = fmt.Sprintf("changed %d", index)
		}
		content = strings.Join(lines, "\n")
		commitFile(t, work, content, fmt.Sprintf("version %d", index), commitTimestamp(index))
	}
	runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	remote := gitOutput(t, work, "remote", "get-url", "origin")
	runGit(t, "", "--git-dir", remote, "repack", "-a", "-d", "-f", "--window=50", "--depth=50")
	indexes, err := filepath.Glob(filepath.Join(remote, "objects", "pack", "*.idx"))
	noErr(t, err)
	if len(indexes) != 1 {
		t.Fatalf("the packed fixture holds %d pack indexes, want one", len(indexes))
	}
	// verify-pack names the chain depth of every object, one process for all of
	// them.
	return deepestObject(t, remote, indexes[0])
}

// commitTimestamp returns a date a fixture with many versions can order, for up
// to about a year of them.
func commitTimestamp(index int) string {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Minute).Format(time.RFC3339)
}

// deepestObject returns the packed object with the longest delta chain and that
// depth, as verify-pack reports it.
func deepestObject(t *testing.T, remote, index string) (deepest string, depth int) {
	t.Helper()
	for _, line := range strings.Split(gitOutput(t, remote, "verify-pack", "-v", index), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[1] != "blob" {
			continue
		}
		if value, err := strconv.Atoi(fields[5]); err == nil && value > depth {
			deepest, depth = fields[0], value
		}
	}
	return deepest, depth
}

// The charge of a deep chain is the sizes Git holds at once, not the sum of the
// whole chain: the largest size twice and the next two once each. Measured on
// Git 2.47.3 with one 16 MiB object: 39 MiB at depth 1 and 55 MiB at depths 2
// to 14, while the sum of the chain grows with every base it holds. A charge
// that grew with the depth would refuse ordinary deep-chain files on every
// host size (see REPORT.md).
func TestRebuildCostKeepsTheSizesGitHolds(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	head, depth := deepChainFixture(t, work, 40)
	if depth < 3 {
		t.Fatalf("the fixture stores the file at depth %d, so it does not hold a deep chain", depth)
	}
	chain := chainSizesAndSum(t, remotePath(t, manager), head)
	costs, sizes, _, err := manager.rebuildCosts(ctx, remotePath(t, manager), []string{head}, 1<<20)
	noErr(t, err)
	if sizes[head] != chain.own {
		t.Errorf("the walk answered a size of %d, want the object's own %d", sizes[head], chain.own)
	}
	if want := rebuiltCost(chain.held); costs[head] != want {
		t.Errorf("the cost of a chain of %d bases is %d, want %d (the %d largest sizes with the largest counted twice)", depth, costs[head], want, chainCopies)
	}
	if whole := rebuiltCost(chain.total + chain.held); costs[head] >= whole {
		t.Errorf("the cost %d grows with the chain, up to %d", costs[head], whole)
	}
}

// chainReading is one object's delta chain: the sizes Git holds at once while
// rebuilding the object, the object's own size in bytes, and the sum of every
// object of the chain.
type chainReading struct {
	own   int64
	held  int64
	total int64
}

// chainSizesAndSum walks the stored delta chain of oid and reads its sizes, so
// a test can check the charge against the chain it saw.
func chainSizesAndSum(t *testing.T, directory, oid string) chainReading {
	t.Helper()
	var sizes []int
	reading := chainReading{}
	for oid != "" {
		size, err := strconv.Atoi(gitOutput(t, directory, "cat-file", "-s", oid))
		noErr(t, err)
		sizes = append(sizes, size)
		if reading.own == 0 {
			reading.own = int64(size)
		}
		reading.total += int64(size)
		base := strings.TrimSpace(gitInputOutput(t, directory, []byte(oid+"\n"), "cat-file", "--batch-check=%(deltabase)"))
		if base == "" || allZeroes(base) {
			break
		}
		oid = base
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	for index, size := range sizes {
		if index == chainCopies {
			break
		}
		reading.held += int64(size)
	}
	if len(sizes) > 0 {
		// The largest size counts twice: the read holds the stored base and the
		// delta that applies to it.
		reading.held += int64(sizes[0])
	}
	return reading
}

// A file whose own size is below the line can still be stored as a delta on a
// much larger base, and reading it makes Git rebuild that base in memory:
// measured on Git 2.47.3, a 7 MiB file whose base holds 192 MiB used 395 MiB,
// while the file's own size stays far below the line the server compares text
// at. The file view, the raw download and the pinned reads refuse such a file
// before Git runs, with the memory reason, instead of reading it under one slot
// of the shared gate; the same file reads when the computer can give Git the
// memory, and a file Git stores on its own is never refused this way.
func TestBlobReadRefusesAFileWhoseStoredDeltaIsExpensive(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	commit, older, newer := deltaFixture(t, work)
	runGit(t, "", "--git-dir", remotePath(t, manager), "update-ref", "refs/tags/delta", gitOutput(t, work, "rev-parse", "HEAD~1"))
	size, err := strconv.ParseInt(gitOutput(t, work, "cat-file", "-s", older), 10, 64)
	noErr(t, err)
	entry := TreeEntry{Path: "file.txt", OID: older, Type: "blob", Size: size}
	previous := memoryBudget
	defer func() { memoryBudget = previous }()
	// One byte above the line: an object Git stores as a delta is above it.
	memoryBudget = func() int64 { return 1 }
	blob, err := manager.BlobAt(ctx, "sample", entry, 1<<20)
	noErr(t, err)
	if !blob.TooLarge || !blob.TooLargeMemory || len(blob.Content) != 0 {
		t.Errorf("a file whose stored delta cannot be rebuilt here was read as %+v", blob)
	}
	metadataSize, metadata, err := manager.ReadBlobMetadata(ctx, "sample", "refs/tags/delta", "file.txt")
	if err != nil || metadataSize != size || metadata.OID != older || !metadata.TooLarge || !metadata.TooLargeMemory || len(metadata.Content) != 0 {
		t.Errorf("delta metadata size=%d blob=%+v err=%v; want the same memory refusal", metadataSize, metadata, err)
	}
	plain, err := manager.BlobAt(ctx, "sample", TreeEntry{Path: "file.txt", OID: newer, Type: "blob", Size: size}, 1<<20)
	noErr(t, err)
	if plain.TooLarge || len(plain.Content) == 0 {
		t.Errorf("a file Git stores on its own was refused: %+v", plain)
	}
	// The pinned price refuses the same object for the same reason. A caller
	// that reads a whole tree prices it in one call, so the object read itself
	// checks the size line only.
	pinned, err := manager.PinRepository(ctx, "sample", commit, commit)
	noErr(t, err)
	if err := pinned.CheckBlobRebuilds(ctx, []string{older}); !errors.Is(err, ErrPinnedBlobTooLarge) {
		t.Errorf("the pinned price answers %v, want %v", err, ErrPinnedBlobTooLarge)
	}
	if content, err := pinned.ReadBlobObject(ctx, older, size); err != nil || int64(len(content)) != size {
		t.Errorf("the pinned object read answers %d bytes, err=%v, want %d", len(content), err, size)
	}
	// A computer that can give Git the memory reads the same file.
	memoryBudget = func() int64 { return 1 << 40 }
	blob, err = manager.BlobAt(ctx, "sample", entry, 1<<20)
	noErr(t, err)
	if blob.TooLarge || len(blob.Content) != int(size) {
		t.Errorf("a file within the memory line was not read: tooLarge=%v bytes=%d want=%d", blob.TooLarge, len(blob.Content), size)
	}
}

func TestRepackRefreshesCachedBlobCostsAndComparisonMarks(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Git repacks before and after a simulated failure")
	}
	previous := memoryBudget
	memoryBudget = func() int64 { return 1 }
	defer func() { memoryBudget = previous }()

	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", failed), func(t *testing.T) {
			ctx := context.Background()
			manager, remote, work := newTestRepository(t)
			_, older, _ := deltaFixture(t, work)
			size, err := strconv.ParseInt(gitOutput(t, work, "cat-file", "-s", older), 10, 64)
			noErr(t, err)
			entry := TreeEntry{Path: "file.txt", OID: older, Type: "blob", Size: size}
			blob, err := manager.BlobAt(ctx, "sample", entry, 1<<20)
			noErr(t, err)
			files := []ChangedFile{{Path: "file.txt", Status: "D", oldOID: older}}
			noErr(t, manager.markBinaryBySize(ctx, "sample", files))
			if !blob.TooLargeMemory || !files[0].TextDiffUnavailable {
				t.Fatal("the delta did not prime both refusal caches")
			}

			args := []string{"repack", "-a", "-d", "-f", "--depth=0"}
			var failure error
			if failed {
				failure = errors.New("repack reported failure after publishing")
				manager.maintenanceHook = func(context.Context, string, []string) error {
					runGit(t, "", append([]string{"--git-dir", remote}, args...)...)
					return failure
				}
			}
			lock := manager.Locks.For("sample")
			generation := lock.Generation()
			noErr(t, lock.LockContext(ctx))
			if err := manager.maintenanceStep(ctx, "sample", lock, time.Minute, args); !errors.Is(err, failure) {
				t.Fatalf("repack: %v, want %v", err, failure)
			}
			if !failed && lock.Generation() != generation {
				t.Fatal("repack invalidated the ref snapshot generation")
			}
			if base := deltabaseOf(t, remote, older); base != "" {
				t.Fatalf("repack left a delta on %s", base)
			}
			blob, err = manager.BlobAt(ctx, "sample", entry, 1<<20)
			noErr(t, err)
			files[0].TextDiffUnavailable = false
			noErr(t, manager.markBinaryBySize(ctx, "sample", files))
			if blob.TooLarge || int64(len(blob.Content)) != size || files[0].TextDiffUnavailable {
				t.Fatalf("repacked file is still refused: tooLarge=%t bytes=%d mark=%t", blob.TooLarge, len(blob.Content), files[0].TextDiffUnavailable)
			}
		})
	}
}

// The price of a whole pinned set must keep the pinned error contract: a
// repository write in progress answers ErrPinnedRepositoryBusy at once, so the
// caller's retry loop runs again. Waiting for the registered repository lock
// instead would hold a read slot behind the writer and fail with another error,
// which the retry loop does not treat as a reason to try again.
func TestPinnedBlobPricingReportsABusyRepository(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	commit, older, _ := deltaFixture(t, work)
	pinned, err := manager.PinRepository(ctx, "sample", commit, commit)
	noErr(t, err)
	previous := memoryBudget
	defer func() { memoryBudget = previous }()
	memoryBudget = func() int64 { return 1 << 30 }
	lock := manager.Locks.For("sample")
	lock.Lock()
	defer lock.Unlock()
	// A context that is already nearly over still gets the busy answer, not a
	// lock wait that ends in a different error.
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = pinned.CheckBlobRebuilds(short, []string{older})
	if !errors.Is(err, ErrPinnedRepositoryBusy) {
		t.Errorf("the price answers %v, want %v", err, ErrPinnedRepositoryBusy)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("the price waited %s behind a writer", elapsed)
	}
}

// Each level of the metadata walk is released before the next one is read, so
// the bound applies to one level and not to the sum of the walk: a deep chain
// must not read as a tree with too many objects, and a level that alone needs
// more than the bound is still refused. Before this, a tree of 80,000 blobs
// with deep chains passed the metadata bound of a 512 MiB host and its archive
// was refused as too large to check.
func TestMetadataBoundAppliesToOneLevelAtATime(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	head, depth := deepChainFixture(t, work, 40)
	if depth < 3 {
		t.Fatalf("the fixture stores the file at depth %d, so it does not hold a deep chain", depth)
	}
	remote := remotePath(t, manager)
	allowance := int64(2 * metadataLineBytes)
	_, _, read, err := manager.rebuildCosts(ctx, remote, []string{head}, allowance)
	noErr(t, err)
	if read <= allowance {
		t.Fatalf("the walk read %d bytes, which a %d byte total bound would already refuse", read, allowance)
	}
	// Three objects in the first level need more than the same bound in one
	// pass, which no release can make fit.
	second := gitOutput(t, work, "rev-parse", "HEAD~1:file.txt")
	third := gitOutput(t, work, "rev-parse", "HEAD~2:file.txt")
	if _, _, _, err := manager.rebuildCosts(ctx, remote, []string{head, second, third}, allowance); !errors.Is(err, ErrTreeCheckTooLarge) {
		t.Fatalf("one level of three objects was not refused: %v", err)
	}
}

// A delta chain deeper than the bound the check applies cannot be priced, and
// it is not a tree with too many files either: a chain that long means the
// object metadata disagrees with Git's own packing, so no read of it can be
// bounded. The walk names that shape with an error of its own, which the
// archive path turns into its own message for the owner.
//
// The bound is lowered for the test: 40 versions reach depth 31, while the
// bound a real host uses is far above what any packer builds for them (measured
// on Git 2.47.3: 400 versions reach depth 52).
func TestDeepChainIsRefusedAsItsOwnShape(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	previousDepth := maxChainDepth
	maxChainDepth = 5
	defer func() { maxChainDepth = previousDepth }()
	head, depth := deepChainFixture(t, work, 40)
	if depth <= maxChainDepth {
		t.Fatalf("the fixture stores its deepest object at depth %d, not deeper than the %d the test allows", depth, maxChainDepth)
	}
	remote := remotePath(t, manager)
	_, _, _, err := manager.rebuildCosts(ctx, remote, []string{head}, 1<<20)
	if !errors.Is(err, ErrDeltaChainTooDeep) {
		t.Fatalf("a chain of depth %d was priced instead of refused: %v", depth, err)
	}
	if !errors.Is(err, ErrTreeCheckTooLarge) {
		t.Errorf("the refusal no longer answers the general tree check: %v", err)
	}
}

// filesAboveTheLineFixture commits many near-identical files and packs the
// repository again, so Git stores most of them as deltas. It returns the commit
// that adds them and how many of them Git stores as a delta.
func filesAboveTheLineFixture(t *testing.T, manager *Manager, work string, count int) (commit string, deltas int) {
	t.Helper()
	files := make(map[string]string, count)
	line := strings.Repeat("a line of the many-file fixture\n", 40)
	for index := 0; index < count; index++ {
		files[fmt.Sprintf("f%03d.txt", index)] = line + fmt.Sprintf("file %d\n", index)
	}
	commit = commitTree(t, manager, work, "main", files)
	remote := remotePath(t, manager)
	runGit(t, "", "--git-dir", remote, "repack", "-a", "-d", "-f", "--window=250", "--depth=50")
	oids := make([]string, 0, count)
	for name := range files {
		oids = append(oids, gitOutput(t, work, "rev-parse", "HEAD:"+name))
	}
	output := gitInputOutput(t, remote, []byte(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch-check=%(deltabase)")
	for _, base := range strings.Fields(output) {
		if !allZeroes(base) {
			deltas++
		}
	}
	return commit, deltas
}

// A comparison that holds more files above the memory line than one command
// line can leave out reads no patch at all, and says so with a flag of its own:
// a caller then names the cause instead of reporting a limit that was never
// reached, and the CLI names the files that are missing.
func TestCompareNamesTooManyFilesAboveTheLine(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	base := commitTree(t, manager, work, "main", map[string]string{"base.txt": "base\n"})
	commit, deltas := filesAboveTheLineFixture(t, manager, work, excludedPathLimit+20)
	if deltas <= excludedPathLimit {
		t.Fatalf("the fixture stores %d of its files as deltas, not more than the %d path limit; Git's packing changed", deltas, excludedPathLimit)
	}
	previous := memoryBudget
	// One byte above the line: every file Git stores as a delta is above it.
	memoryBudget = func() int64 { return 1 }
	defer func() { memoryBudget = previous }()
	comparison, err := manager.Compare(ctx, "sample", base, commit, 1<<20, 30*time.Second)
	noErr(t, err)
	if !comparison.PatchTruncated || !comparison.PatchTooLarge || comparison.Patch != "" || comparison.FilesTruncated {
		t.Fatalf("patch=%d bytes truncated=%v tooLarge=%v filesTruncated=%v",
			len(comparison.Patch), comparison.PatchTruncated, comparison.PatchTooLarge, comparison.FilesTruncated)
	}
	// The count read was left out with the patch, so no file carries a count
	// that no read established: a zero here would be a claim about the file.
	for _, file := range comparison.Files {
		if file.CountsRead {
			t.Errorf("%s carries line counts although the count read was left out", file.Path)
		}
	}
}

// A comparison whose files above the memory line fit one command line still
// reads the line counts of the files below it. The files above the line are
// left out of that read by name, and their counts stay unknown rather than
// zero; every other file's counts were read, so a page can show them.
func TestCompareLeavesUnreadCountsUnknown(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	base := commitTree(t, manager, work, "main", map[string]string{"base.txt": "base\n"})
	commit, deltas := filesAboveTheLineFixture(t, manager, work, excludedPathLimit)
	if deltas == 0 || deltas > excludedPathLimit {
		t.Fatalf("the fixture stores %d of its files as deltas, want one or more and at most %d", deltas, excludedPathLimit)
	}
	previous := memoryBudget
	// One byte above the line: every file Git stores as a delta is above it,
	// and a file Git stores on its own is not charged at all.
	memoryBudget = func() int64 { return 1 }
	defer func() { memoryBudget = previous }()
	comparison, err := manager.Compare(ctx, "sample", base, commit, 1<<20, 30*time.Second)
	noErr(t, err)
	if comparison.PatchTooLarge {
		t.Fatalf("the fixture is above the %d path limit, so it measures the other case", excludedPathLimit)
	}
	read := 0
	for _, file := range comparison.Files {
		switch {
		case file.TextDiffUnavailable && file.CountsRead:
			t.Errorf("%s is above the line and yet carries line counts", file.Path)
		case file.CountsRead:
			read++
		case !file.TextDiffUnavailable:
			t.Errorf("%s is below the line but its counts were not read", file.Path)
		}
	}
	if read == 0 {
		t.Error("no file of the comparison carries line counts, so the count read did not run")
	}
}

// A read of blob content holds a slot of the shared memory gate, so reads that
// are each below the per-object bound cannot together use more memory than the
// computer allows. The slot is taken before the repository read lock: a reader
// that waited for a slot while holding a lock could wait on a transfer that
// holds a slot and waits for that lock, and neither would ever run. A read
// that finds no slot within its wait reports the gate as busy instead of
// reading anyway, so a small computer refuses work it cannot hold rather than
// being killed.
func TestBlobReadTakesAMemorySlotBeforeTheRepositoryLock(t *testing.T) {
	ctx := context.Background()
	manager, _, work := newTestRepository(t)
	commitTree(t, manager, work, "main", map[string]string{"file.txt": "bounded read\n", "second.txt": "second read\n"})
	commit := gitOutput(t, work, "rev-parse", "HEAD")

	gate := hostmem.NewGate(1)
	hostmem.Shared.Store(gate)
	defer hostmem.Shared.Store(nil)
	held, err := gate.Acquire(ctx)
	noErr(t, err)
	previous := gitexec.ReadSlotWait
	gitexec.ReadSlotWait = time.Minute
	defer func() { gitexec.ReadSlotWait = previous }()

	finished := make(chan error, 1)
	go func() {
		_, _, err := manager.FileAt(ctx, "sample", commit, "file.txt", 1<<20)
		finished <- err
	}()
	// The read announces itself at the gate before it takes the lock, so a
	// waiter at the gate is a read that holds no lock. A write lock proves
	// that: it needs every reader out.
	for deadline := time.Now().Add(10 * time.Second); gate.Waiting() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the read never reached the memory gate")
		}
		runtime.Gosched()
	}
	lock := manager.Locks.For("sample")
	if !lock.TryLock() {
		t.Fatal("the read holds the repository lock while it waits for a memory slot")
	}
	lock.Unlock()
	held()
	if err := <-finished; err != nil {
		t.Fatalf("the read did not finish after the slot was released: %v", err)
	}
	// A read that finds no free slot within its wait is refused, not read. It
	// reads another file, so no earlier read answers it from its cache.
	held, err = gate.Acquire(ctx)
	noErr(t, err)
	gitexec.ReadSlotWait = 20 * time.Millisecond
	if _, _, err := manager.FileAt(ctx, "sample", commit, "second.txt", 1<<20); !errors.Is(err, gitexec.ErrReadMemoryBusy) {
		t.Fatalf("read error=%v, want a busy memory gate", err)
	}
	held()
}
