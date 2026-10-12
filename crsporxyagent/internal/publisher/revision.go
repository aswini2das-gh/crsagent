package publisher

import "sync"

// RevisionTracker tracks monotonic revision per node for idempotent deduplication
type RevisionTracker struct {
	mu        sync.RWMutex
	revisions map[string]int64 // nodeUID -> revision
}

// NewRevisionTracker creates a new RevisionTracker
func NewRevisionTracker() *RevisionTracker {
	return &RevisionTracker{
		revisions: make(map[string]int64),
	}
}

// Next atomically increments and returns the next revision for the given node
func (rt *RevisionTracker) Next(nodeUID string) int64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.revisions[nodeUID]++
	return rt.revisions[nodeUID]
}

// Current returns the current revision for the given node without incrementing
func (rt *RevisionTracker) Current(nodeUID string) int64 {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.revisions[nodeUID]
}

// Remove deletes the revision tracking for the given node
func (rt *RevisionTracker) Remove(nodeUID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.revisions, nodeUID)
}
