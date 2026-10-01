package server

import (
	"sync"
	"time"
)

type cachedResponse struct {
	payload     []byte
	contentType string
	storedAt    time.Time
}

type responseCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]cachedResponse
}

func newResponseCache(ttl time.Duration) *responseCache {
	if ttl <= 0 {
		return &responseCache{ttl: ttl}
	}

	return &responseCache{
		ttl:     ttl,
		entries: make(map[string]cachedResponse),
	}
}

func (c *responseCache) Get(key string) (cachedResponse, bool, bool) {
	if c == nil || c.ttl <= 0 {
		return cachedResponse{}, false, false
	}

	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return cachedResponse{}, false, false
	}

	isExpired := time.Since(entry.storedAt) > c.ttl

	return entry, true, isExpired
}

func (c *responseCache) Set(key string, payload []byte, contentType string) {
	if c == nil || c.ttl <= 0 {
		return
	}

	buf := make([]byte, len(payload))
	copy(buf, payload)

	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]cachedResponse)
	}
	c.entries[key] = cachedResponse{
		payload:     buf,
		contentType: contentType,
		storedAt:    time.Now(),
	}
	c.mu.Unlock()
}

func (c *responseCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = make(map[string]cachedResponse)
	c.mu.Unlock()
}

// chunkCache caches short-lived immutable HLS video segments (.ts chunks) in memory
// so multiple clients watching the same stream don't make duplicate upstream calls.
type chunkCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]cachedResponse
}

func newChunkCache(ttl time.Duration) *chunkCache {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	cc := &chunkCache{
		ttl:     ttl,
		entries: make(map[string]cachedResponse),
	}
	go cc.cleanupLoop()
	return cc
}

func (c *chunkCache) cleanupLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for k, v := range c.entries {
			if now.Sub(v.storedAt) > c.ttl {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}

func (c *chunkCache) Get(key string) ([]byte, string, bool) {
	if c == nil {
		return nil, "", false
	}
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Since(entry.storedAt) > c.ttl {
		return nil, "", false
	}
	return entry.payload, entry.contentType, true
}

func (c *chunkCache) Set(key string, payload []byte, contentType string) {
	if c == nil || len(payload) == 0 {
		return
	}
	c.mu.Lock()
	// Evict if cache exceeds 150 items to keep RAM bounded
	if len(c.entries) > 150 {
		c.entries = make(map[string]cachedResponse)
	}
	buf := make([]byte, len(payload))
	copy(buf, payload)
	c.entries[key] = cachedResponse{
		payload:     buf,
		contentType: contentType,
		storedAt:    time.Now(),
	}
	c.mu.Unlock()
}

func (c *chunkCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = make(map[string]cachedResponse)
	c.mu.Unlock()
}


