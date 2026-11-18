//go:build with_utls

package tls

import (
	"container/heap"
	"crypto/sha256"
	"sync"
	"time"
)

const (
	realityReplayCapacity           = 65536
	realityDefaultMaxTimeDifference = time.Hour
)

type realityReplayEntry struct {
	hash    [sha256.Size]byte
	expires time.Time
}

type realityReplayExpirations []realityReplayEntry

func (h realityReplayExpirations) Len() int {
	return len(h)
}

func (h realityReplayExpirations) Less(i, j int) bool {
	return h[i].expires.Before(h[j].expires)
}

func (h realityReplayExpirations) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *realityReplayExpirations) Push(v any) {
	*h = append(*h, v.(realityReplayEntry))
}

func (h *realityReplayExpirations) Pop() any {
	last := len(*h) - 1
	entry := (*h)[last]
	*h = (*h)[:last]
	return entry
}

type realityReplayCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]time.Time
	order   realityReplayExpirations
}

func newRealityReplayCache() *realityReplayCache {
	return &realityReplayCache{entries: make(map[[sha256.Size]byte]time.Time)}
}

func (c *realityReplayCache) seen(hello []byte, now, expires time.Time) bool {
	hash := sha256.Sum256(hello)
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.order) > 0 && now.After(c.order[0].expires) {
		oldest := heap.Pop(&c.order).(realityReplayEntry)
		delete(c.entries, oldest.hash)
	}
	if _, ok := c.entries[hash]; ok || now.After(expires) {
		return true
	}
	// An unexpired entry must never be displaced by another connection. If
	// there is no room to remember a new hello, send it to the target too.
	if len(c.entries) >= realityReplayCapacity {
		return true
	}
	c.entries[hash] = expires
	heap.Push(&c.order, realityReplayEntry{hash: hash, expires: expires})
	return false
}
