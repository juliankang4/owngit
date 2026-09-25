package repository

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

// objectCache keeps Git read results that are fixed by the object IDs they
// were read for: folder listings of a commit, file contents, commit metadata
// and changed files, diffs, merge bases, ancestry answers and tag peeling.
// Objects never change, so an entry needs no invalidation while its
// repository exists.
//
// Entries are raw Git output, parsed again on every use, so a listing does
// not depend on the path it was reached by. Each repository has its own
// namespace, named by its ID, storage path and creation time and by a count
// of its deletions, so a deleted repository's results never answer for a new
// repository with the same name. The cache is bounded by total bytes, by
// entry count and by the size of one entry, and evicts the least recently
// used entry first. Concurrent misses for one key run Git once. Errors are
// never cached, and callers run their existence and preparation checks
// before every lookup.
type objectCache struct {
	mu      sync.Mutex
	bytes   int64
	order   *list.List
	entries map[objectKey]*list.Element
	flights map[objectKey]*objectFlight
	// epochs counts the deletions of each repository ID.
	epochs map[string]uint64
}

// Bounds of the object cache. Variables so tests can lower them.
var (
	objectCacheBytes   int64 = 64 << 20
	objectCacheEntries       = 4096
	objectCacheItem    int64 = 4 << 20
)

// objectNamespace identifies one repository for the cache.
type objectNamespace struct {
	id       string
	identity string
}

type objectKey struct {
	namespace objectNamespace
	epoch     uint64
	kind      string
	key       string
}

// cachedResult is one Git read. Truncated says the read stopped at a limit
// that is part of its key, so the same read gives the same result.
type cachedResult struct {
	data      []byte
	truncated bool
}

func (result cachedResult) clone() cachedResult {
	result.data = bytes.Clone(result.data)
	return result
}

type objectEntry struct {
	key    objectKey
	result cachedResult
}

type objectFlight struct {
	done   chan struct{}
	result cachedResult
	err    error
}

// namespaceFor names the repository stored at repositoryPath and created at
// createdAt.
func namespaceFor(id, repositoryPath string, createdAt time.Time) objectNamespace {
	return objectNamespace{id: id, identity: repositoryPath + "\x00" + strconv.FormatInt(createdAt.UnixNano(), 10)}
}

// load returns the cached result for kind and key, or runs read once for all
// concurrent callers and caches what it returns when cacheable is true. read
// runs Git under the repository lock. A caller that waits for another
// caller's read stops waiting when its own ctx ends, and reads for itself when
// the other read ended with its context.
func (cache *objectCache) load(ctx context.Context, namespace objectNamespace, kind, key string, read func() (result cachedResult, cacheable bool, err error)) (cachedResult, error) {
	for {
		cache.mu.Lock()
		if cache.entries == nil {
			cache.entries = make(map[objectKey]*list.Element)
			cache.flights = make(map[objectKey]*objectFlight)
			cache.epochs = make(map[string]uint64)
			cache.order = list.New()
		}
		cacheKey := objectKey{namespace: namespace, epoch: cache.epochs[namespace.id], kind: kind, key: key}
		if element, ok := cache.entries[cacheKey]; ok {
			cache.order.MoveToFront(element)
			result := element.Value.(*objectEntry).result.clone()
			cache.mu.Unlock()
			return result, nil
		}
		if flight, ok := cache.flights[cacheKey]; ok {
			cache.mu.Unlock()
			select {
			case <-flight.done:
			case <-ctx.Done():
				return cachedResult{}, ctx.Err()
			}
			if flight.err == nil {
				return flight.result.clone(), nil
			}
			if errors.Is(flight.err, context.Canceled) || errors.Is(flight.err, context.DeadlineExceeded) {
				continue
			}
			return cachedResult{}, flight.err
		}
		flight := &objectFlight{done: make(chan struct{})}
		cache.flights[cacheKey] = flight
		cache.mu.Unlock()

		result, cacheable, err := read()
		flight.result, flight.err = result, err
		cache.mu.Lock()
		delete(cache.flights, cacheKey)
		if err == nil && cacheable && cache.epochs[namespace.id] == cacheKey.epoch {
			cache.storeLocked(cacheKey, result)
		}
		cache.mu.Unlock()
		close(flight.done)
		if err != nil {
			return cachedResult{}, err
		}
		return result.clone(), nil
	}
}

// peek returns a copy of a cached result without reading anything.
func (cache *objectCache) peek(namespace objectNamespace, kind, key string) (cachedResult, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		return cachedResult{}, false
	}
	element, ok := cache.entries[objectKey{namespace: namespace, epoch: cache.epochs[namespace.id], kind: kind, key: key}]
	if !ok {
		return cachedResult{}, false
	}
	cache.order.MoveToFront(element)
	return element.Value.(*objectEntry).result.clone(), true
}

// storeLocked keeps a copy of result and evicts the least recently used
// entries past the bounds. The caller holds cache.mu.
func (cache *objectCache) storeLocked(key objectKey, result cachedResult) {
	size := int64(len(result.data)) + int64(len(key.kind)+len(key.key)+len(key.namespace.identity)) + 64
	if size > objectCacheItem || size > objectCacheBytes {
		return
	}
	if element, ok := cache.entries[key]; ok {
		cache.removeLocked(element)
	}
	entry := &objectEntry{key: key, result: result.clone()}
	cache.entries[key] = cache.order.PushFront(entry)
	cache.bytes += size
	for cache.bytes > objectCacheBytes || len(cache.entries) > objectCacheEntries {
		cache.removeLocked(cache.order.Back())
	}
}

func (cache *objectCache) removeLocked(element *list.Element) {
	entry := element.Value.(*objectEntry)
	cache.order.Remove(element)
	delete(cache.entries, entry.key)
	cache.bytes -= int64(len(entry.result.data)) + int64(len(entry.key.kind)+len(entry.key.key)+len(entry.key.namespace.identity)) + 64
}

// drop forgets every result of repository id and makes reads that are still
// running for it store nothing.
func (cache *objectCache) drop(id string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		return
	}
	cache.epochs[id]++
	for element := cache.order.Front(); element != nil; {
		next := element.Next()
		if element.Value.(*objectEntry).key.namespace.id == id {
			cache.removeLocked(element)
		}
		element = next
	}
}

// forget drops the results of repositories not in present.
func (cache *objectCache) forget(present []string) {
	keep := make(map[string]bool, len(present))
	for _, id := range present {
		keep[id] = true
	}
	cache.mu.Lock()
	var gone []string
	if cache.entries != nil {
		seen := map[string]bool{}
		for element := cache.order.Front(); element != nil; element = element.Next() {
			id := element.Value.(*objectEntry).key.namespace.id
			if !keep[id] && !seen[id] {
				seen[id] = true
				gone = append(gone, id)
			}
		}
	}
	cache.mu.Unlock()
	for _, id := range gone {
		cache.drop(id)
	}
}

// usage reports the cached entry count and bytes, for tests.
func (cache *objectCache) usage() (int, int64) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return len(cache.entries), cache.bytes
}
