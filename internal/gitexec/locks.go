package gitexec

import "sync"

// Locks coordinates Git writers, readers, and maintenance per repository.
type Locks struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

func NewLocks() *Locks {
	return &Locks{locks: make(map[string]*sync.RWMutex)}
}

func (l *Locks) For(repositoryID string) *sync.RWMutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock := l.locks[repositoryID]
	if lock == nil {
		lock = &sync.RWMutex{}
		l.locks[repositoryID] = lock
	}
	return lock
}
