package repository

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"owngit/internal/gitexec"
)

// snapshotCache keeps the latest ref snapshot of each repository with the
// write generation of the repository lock it was read at. Every OwnGit ref
// write holds that lock, and releasing it advances the generation, so an entry
// is current exactly while the generation is unchanged. A holder that proves
// it changed no ref, such as a push that updated nothing or an import that
// found every ref unchanged, releases the lock without advancing it. Refs
// written to the storage folder without OwnGit are not seen until OwnGit next
// changes a ref in that repository or restarts.
// Ref data gets half the object-backed page cache's 64 MiB budget. A single
// repository may use at most one quarter, leaving space for other repositories.
const (
	snapshotCapacity   = 64
	snapshotByteBudget = 32 << 20
)

type snapshotCache struct {
	mu    sync.Mutex
	clock uint64
	bytes int64
	// root is the storage folder the entries were read from. Entries of an
	// earlier root are dropped when a snapshot of another root is stored.
	root    string
	entries map[string]snapshotEntry
}

type snapshotEntry struct {
	lock       *gitexec.RepositoryLock
	path       string
	generation uint64
	snapshot   RefSnapshot
	used       uint64
	weight     int64
}

// RefSnapshot returns the repository's ref snapshot. It reads the refs with
// Git only when an OwnGit writer released the repository's write lock since
// the cached snapshot was read. The existence check runs every time, so a
// missing or unreadable repository still reports an error. Errors and
// snapshots incomplete after a failed follow-up read are never cached.
func (m *Manager) RefSnapshot(ctx context.Context, id string) (RefSnapshot, error) {
	return m.refSnapshot(ctx, id, 0, false)
}

// RefSnapshotWithin is RefSnapshot for a page that lists many repositories.
// When a Git operation holds the repository, it waits at most wait for the
// lock. It then returns the last snapshot read, marked Stale, even if a write
// changed the refs since, or, without one, an error that wraps
// ErrRepositoryInUse. One busy repository therefore cannot hold up the whole
// page. A read that failed since drops the last snapshot, so a repository
// that could not be read is never shown from an older listing.
func (m *Manager) RefSnapshotWithin(ctx context.Context, id string, wait time.Duration) (RefSnapshot, error) {
	return m.refSnapshot(ctx, id, wait, false)
}

// RefSnapshotAfterWrites waits for an active writer before checking the
// generation. Background callbacks can arrive before that writer unlocks.
func (m *Manager) RefSnapshotAfterWrites(ctx context.Context, id string) (RefSnapshot, error) {
	return m.refSnapshot(ctx, id, 0, true)
}

func (m *Manager) refSnapshot(ctx context.Context, id string, wait time.Duration, afterWrites bool) (RefSnapshot, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return RefSnapshot{}, err
	}
	if !exists {
		return RefSnapshot{}, ErrRepositoryNotFound
	}
	lock := m.Locks.For(id)
	// Without the lock, a writer may be in progress. The cached snapshot then
	// shows the refs before that write, which is the state the write has not
	// yet replaced.
	if !afterWrites {
		if snapshot, ok := m.snapshots.lookup(id, repositoryPath, lock); ok {
			return snapshot, nil
		}
	}
	lockContext := ctx
	if wait > 0 {
		var cancel context.CancelFunc
		lockContext, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	if err := readLock(lockContext, lock); err != nil {
		if snapshot, ok := m.snapshots.last(id, repositoryPath, lock); ok && wait > 0 && ctx.Err() == nil {
			snapshot.Stale = true
			return snapshot, nil
		}
		return RefSnapshot{}, err
	}
	defer lock.RUnlock()
	return m.lockedRefSnapshot(ctx, id, repositoryPath, lock)
}

// lockedRefSnapshot returns the snapshot of the refs as they are while the
// caller holds the repository's read lock: the cached one when no writer
// released the lock since it was read, and otherwise a new listing.
func (m *Manager) lockedRefSnapshot(ctx context.Context, id, repositoryPath string, lock *gitexec.RepositoryLock) (RefSnapshot, error) {
	if snapshot, ok := m.snapshots.lookup(id, repositoryPath, lock); ok {
		return snapshot, nil
	}
	generation := lock.Generation()
	snapshot, complete, err := m.readRefSnapshot(ctx, repositoryPath)
	if err != nil {
		if ctx.Err() == nil {
			m.snapshots.drop(id)
		}
		return RefSnapshot{}, err
	}
	// RefSnapshot exposes a tip summary, not a full commit message.
	snapshot.Head.Body = ""
	snapshot.reads = &snapshotReads{
		path: repositoryPath, lock: lock, gate: make(chan struct{}, 1),
		branchRefs: slices.Clone(snapshot.Summary.Branches), tagRefs: slices.Clone(snapshot.Summary.Tags), refs: snapshot.refs,
	}
	// A snapshot missing a field because a follow-up read failed is returned
	// once, like an error, and read again next time.
	if complete {
		m.snapshots.store(id, repositoryPath, lock, generation, snapshot)
	}
	return snapshot, nil
}

// CachedHeadDate returns the author date of the default branch tip from the
// last ref snapshot read for id. It runs no Git process and does not wait for
// the repository lock, so a page can use it for every repository. ok is false
// when no snapshot was read since OwnGit started or the default branch had no
// commit. The date may be older than the refs after an outside write.
func (m *Manager) CachedHeadDate(id string) (time.Time, bool) {
	m.snapshots.mu.Lock()
	defer m.snapshots.mu.Unlock()
	entry, ok := m.snapshots.entries[id]
	if !ok || !entry.snapshot.HeadFound || entry.snapshot.Head.AuthoredAt.IsZero() {
		return time.Time{}, false
	}
	return entry.snapshot.Head.AuthoredAt, true
}

// ForgetRefSnapshots drops cached snapshots, language counts and object
// reads of repositories not in present.
func (m *Manager) ForgetRefSnapshots(present []string) {
	m.snapshots.forget(present)
	m.languages.forget(present)
	m.objects.forget(present)
}

func (cache *snapshotCache) lookup(id, path string, lock *gitexec.RepositoryLock) (RefSnapshot, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[id]
	if !ok || entry.lock != lock || entry.path != path || entry.generation != lock.Generation() {
		return RefSnapshot{}, false
	}
	cache.clock++
	entry.used = cache.clock
	cache.entries[id] = entry
	return entry.snapshot.clone(), true
}

// last returns the latest stored snapshot of the repository, whatever writes
// happened since.
func (cache *snapshotCache) last(id, path string, lock *gitexec.RepositoryLock) (RefSnapshot, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[id]
	if !ok || entry.lock != lock || entry.path != path {
		return RefSnapshot{}, false
	}
	return entry.snapshot.clone(), true
}

// store keeps snapshot, read at generation, unless the entry already holds a
// snapshot of the same repository read at the same or a later generation.
func (cache *snapshotCache) store(id, path string, lock *gitexec.RepositoryLock, generation uint64, snapshot RefSnapshot) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if root := filepath.Dir(path); cache.entries == nil || cache.root != root {
		cache.root, cache.entries, cache.bytes = root, make(map[string]snapshotEntry), 0
	}
	if entry, ok := cache.entries[id]; ok && entry.lock == lock && entry.path == path && entry.generation >= generation {
		return
	}
	cache.removeLocked(id)
	weight := snapshotWeight(snapshot)
	if weight > snapshotByteBudget/4 {
		return
	}
	cache.clock++
	cache.entries[id] = snapshotEntry{lock: lock, path: path, generation: generation, snapshot: snapshot.clone(), used: cache.clock, weight: weight}
	cache.bytes += weight
	cache.evictLocked()
}

func (cache *snapshotCache) evictLocked() {
	for len(cache.entries) > snapshotCapacity || cache.bytes > snapshotByteBudget {
		oldest, used := "", cache.clock
		for candidate, entry := range cache.entries {
			if entry.used <= used {
				oldest, used = candidate, entry.used
			}
		}
		cache.removeLocked(oldest)
	}
}

func (cache *snapshotCache) drop(id string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.removeLocked(id)
}

func (cache *snapshotCache) forget(present []string) {
	keep := make(map[string]bool, len(present))
	for _, id := range present {
		keep[id] = true
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for id := range cache.entries {
		if !keep[id] {
			cache.removeLocked(id)
		}
	}
}

func (cache *snapshotCache) removeLocked(id string) {
	if entry, ok := cache.entries[id]; ok {
		cache.bytes -= entry.weight
		delete(cache.entries, id)
	}
}

// reweigh runs with the derived-read gate held. Results belonging to an
// evicted, replaced or oversized snapshot remain request-local, not cached.
func (cache *snapshotCache) reweigh(id string, reads *snapshotReads) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[id]
	if !ok || entry.snapshot.reads != reads {
		return
	}
	weight := snapshotWeight(entry.snapshot)
	if weight > snapshotByteBudget/4 {
		cache.removeLocked(id)
		return
	}
	cache.bytes += weight - entry.weight
	entry.weight = weight
	cache.entries[id] = entry
	cache.evictLocked()
}

// Weights intentionally count strings at every stored reference, even when
// backing data is shared. Fixed overheads include records, allocation slack,
// map buckets and dates. They are conservative estimates, not measured RSS.
func snapshotWeight(snapshot RefSnapshot) int64 {
	weight := int64(1024 + len(snapshot.Summary.DefaultBranch) + len(snapshot.Summary.DefaultOID) + len(snapshot.ActivityKey))
	weight += refWeight(snapshot.Summary.Branches) + refWeight(snapshot.Summary.Tags) + refWeight(snapshot.refs)
	weight += int64(cap(snapshot.activityRefs)) * 64
	for _, ref := range snapshot.activityRefs {
		weight += int64(len(ref.name) + len(ref.oid))
	}
	weight += commitWeight(snapshot.Head)
	if reads := snapshot.reads; reads != nil {
		weight += int64(512 + len(reads.path))
		weight += refWeight(reads.branchRefs) + refWeight(reads.tagRefs)
		// reads.refs shares snapshot.refs, so its backing array is counted once.
		for _, commits := range []map[string]Commit{reads.metadata, reads.branches, reads.tags} {
			for key, commit := range commits {
				weight += int64(96+len(key)) + commitWeight(commit)
			}
		}
		for key, object := range reads.peeled {
			weight += int64(128 + len(key) + len(object.oid) + len(object.objectType))
		}
		weight += int64(cap(reads.retained)) * 192
		for _, ref := range reads.retained {
			weight += int64(len(ref.Kind)+len(ref.Source)+len(ref.OID)+len(ref.CommitOID)) + commitWeight(ref.Commit)
		}
	}
	return weight
}

func refWeight(refs []Ref) int64 {
	weight := int64(cap(refs)) * 96
	for _, ref := range refs {
		weight += int64(len(ref.Name) + len(ref.OID) + len(ref.Type))
	}
	return weight
}

func commitWeight(commit Commit) int64 {
	weight := int64(1024 + len(commit.OID) + len(commit.AuthorName) + len(commit.AuthorEmail) + len(commit.CommitterName) + len(commit.CommitterEmail) + len(commit.Subject) + len(commit.Body))
	weight += int64(cap(commit.Parents)) * 32
	for _, parent := range commit.Parents {
		weight += int64(len(parent))
	}
	return weight
}

// clone copies public mutable data. Package-private ref slices are immutable
// after construction and shared, including with readers of retained history.
func (snapshot RefSnapshot) clone() RefSnapshot {
	snapshot.Summary.Branches = slices.Clone(snapshot.Summary.Branches)
	snapshot.Summary.Tags = slices.Clone(snapshot.Summary.Tags)
	snapshot.Head.Parents = slices.Clone(snapshot.Head.Parents)
	return snapshot
}

// snapshotReads belongs to one immutable ref snapshot, not to a request or
// an authorization decision. Its gate coalesces readers and lets waiters cancel.
// Failures are never stored. Commit and peeled-tag data is keyed by object ID.
type snapshotReads struct {
	path          string
	lock          *gitexec.RepositoryLock
	gate          chan struct{}
	branchRefs    []Ref
	tagRefs       []Ref
	refs          []Ref
	metadata      map[string]Commit
	peeled        map[string]peeledRetainedObject
	branches      map[string]Commit
	tags          map[string]Commit
	retained      []RetainedRef
	retainedKnown bool
}

func (m *Manager) snapshotRead(ctx context.Context, id string, snapshot RefSnapshot) (*snapshotReads, func(), error) {
	if snapshot.Stale {
		return nil, nil, ErrRepositoryInUse
	}
	path, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return nil, nil, ErrRepositoryNotFound
	}
	reads := snapshot.reads
	if reads == nil || reads.path != path || reads.lock != m.Locks.For(id) {
		return nil, nil, errors.New("ref snapshot belongs to another repository")
	}
	if err := readLock(ctx, reads.lock); err != nil {
		return nil, nil, err
	}
	select {
	case reads.gate <- struct{}{}:
		return reads, func() {
			m.snapshots.reweigh(id, reads)
			<-reads.gate
			reads.lock.RUnlock()
		}, nil
	case <-ctx.Done():
		reads.lock.RUnlock()
		return nil, nil, ctx.Err()
	}
}

// RefTipsAt returns branch or tag tips from exactly snapshot's refs, even if
// a writer has since advanced them. Only immutable object IDs reach Git.
func (m *Manager) RefTipsAt(ctx context.Context, id string, snapshot RefSnapshot, tags bool) (map[string]Commit, error) {
	reads, release, err := m.snapshotRead(ctx, id, snapshot)
	if err != nil {
		return nil, err
	}
	defer release()
	cached, refs := &reads.branches, reads.branchRefs
	if tags {
		cached, refs = &reads.tags, reads.tagRefs
	}
	if *cached == nil {
		tips, err := refTipsWithReads(ctx, m.Git, reads.path, refs, reads)
		if err != nil {
			return nil, err
		}
		*cached = tips
	}
	tips := make(map[string]Commit, len(*cached))
	for name, commit := range *cached {
		commit.Parents = slices.Clone(commit.Parents)
		tips[name] = commit
	}
	return tips, nil
}

// RefCommitAt pins an already selected full ref to snapshot. An absent ref
// returns no OID, so callers can keep their existing handling of other refs.
func (m *Manager) RefCommitAt(ctx context.Context, id string, snapshot RefSnapshot, full string) (string, error) {
	if snapshot.Stale {
		return "", ErrRepositoryInUse
	}
	path, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrRepositoryNotFound
	}
	reads := snapshot.reads
	if reads == nil || reads.path != path || reads.lock != m.Locks.For(id) {
		return "", errors.New("ref snapshot belongs to another repository")
	}
	for _, ref := range reads.refs {
		if ref.Name != full {
			continue
		}
		// Immutable snapshot data needs no repository lock, even while a
		// writer runs. Only a missing object read must wait for that writer.
		if ref.Type == "commit" {
			return ref.OID, nil
		}
		if ref.Type != "tag" {
			return "", ErrNotFound
		}
		select {
		case reads.gate <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		object, cached := reads.peeled[ref.OID]
		<-reads.gate
		if !cached {
			locked, release, err := m.snapshotRead(ctx, id, snapshot)
			if err != nil {
				return "", err
			}
			defer release()
			peeled, err := readPeeledTags(ctx, m.Git, locked.path, []string{ref.OID}, locked)
			if err != nil {
				return "", err
			}
			object = peeled[ref.OID]
		}
		if object.objectType == "commit" {
			return object.oid, nil
		}
		return "", ErrNotFound
	}
	return "", nil
}

// RetainedRefsAt keeps history and its provenance tied to snapshot's refs.
func (m *Manager) RetainedRefsAt(ctx context.Context, id string, snapshot RefSnapshot) ([]RetainedRef, error) {
	reads, release, err := m.snapshotRead(ctx, id, snapshot)
	if err != nil {
		return nil, err
	}
	defer release()
	if !reads.retainedKnown {
		retained, err := retainedRefsFromSnapshot(ctx, m.Git, reads.path, reads.refs, reads)
		if err != nil {
			return nil, err
		}
		reads.retained, reads.retainedKnown = retained, true
	}
	retained := slices.Clone(reads.retained)
	for index := range retained {
		retained[index].Commit.Parents = slices.Clone(retained[index].Commit.Parents)
	}
	return retained, nil
}

func readCommitMetadata(ctx context.Context, runner retainedRunner, path string, oids []string, reads *snapshotReads) (map[string]Commit, error) {
	if reads == nil {
		return commitMetadataByOID(ctx, runner, path, oids)
	}
	if reads.metadata == nil {
		reads.metadata = make(map[string]Commit)
	}
	var missing []string
	for _, oid := range oids {
		if _, ok := reads.metadata[oid]; !ok {
			missing = append(missing, oid)
		}
	}
	metadata, err := commitMetadataWithFormat(ctx, runner, path, missing, tipLogFormat)
	if err != nil {
		return nil, err
	}
	for oid, commit := range metadata {
		reads.metadata[oid] = commit
	}
	return reads.metadata, nil
}

func readPeeledTags(ctx context.Context, runner retainedRunner, path string, oids []string, reads *snapshotReads) (map[string]peeledRetainedObject, error) {
	if reads == nil {
		return batchPeelRetainedTags(ctx, runner, path, oids)
	}
	if reads.peeled == nil {
		reads.peeled = make(map[string]peeledRetainedObject)
	}
	var missing []string
	for _, oid := range oids {
		if _, ok := reads.peeled[oid]; !ok {
			missing = append(missing, oid)
		}
	}
	if len(missing) > 0 {
		peeled, err := batchPeelRetainedTags(ctx, runner, path, missing)
		if err != nil {
			return nil, err
		}
		for oid, object := range peeled {
			reads.peeled[oid] = object
		}
	}
	return reads.peeled, nil
}
