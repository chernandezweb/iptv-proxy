package server

import (
	"container/list"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type cachedResponse struct {
	key         string
	payload     []byte
	contentType string
	storedAt    time.Time
	sizeBytes   int64
}

// responseCache provides a thread-safe, byte-bounded LRU cache with TTL expiration.
type responseCache struct {
	ttl          time.Duration
	maxBytes     int64
	currentBytes int64
	mu           sync.RWMutex
	entries      map[string]*list.Element
	evictList    *list.List
}

func newResponseCache(ttl time.Duration) *responseCache {
	return newResponseCacheWithLimit(ttl, 128*1024*1024) // 128 MB default limit
}

func newResponseCacheWithLimit(ttl time.Duration, maxBytes int64) *responseCache {
	if maxBytes <= 0 {
		maxBytes = 128 * 1024 * 1024
	}
	return &responseCache{
		ttl:       ttl,
		maxBytes:  maxBytes,
		entries:   make(map[string]*list.Element),
		evictList: list.New(),
	}
}

func (c *responseCache) Get(key string) (cachedResponse, bool, bool) {
	if c == nil || c.ttl <= 0 {
		return cachedResponse{}, false, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[key]
	if !ok {
		return cachedResponse{}, false, false
	}

	entry := elem.Value.(*cachedResponse)
	isExpired := time.Since(entry.storedAt) > c.ttl

	// Move to front (most recently used)
	c.evictList.MoveToFront(elem)

	// Return a copy of entry data
	return *entry, true, isExpired
}

func (c *responseCache) Set(key string, payload []byte, contentType string) {
	if c == nil || c.ttl <= 0 || len(payload) == 0 {
		return
	}

	entrySize := int64(len(payload) + len(key) + len(contentType) + 64)
	if entrySize > c.maxBytes {
		return // Entry larger than whole cache budget
	}

	buf := make([]byte, len(payload))
	copy(buf, payload)

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, exists := c.entries[key]; exists {
		c.evictList.MoveToFront(elem)
		oldEntry := elem.Value.(*cachedResponse)
		c.currentBytes += entrySize - oldEntry.sizeBytes
		oldEntry.payload = buf
		oldEntry.contentType = contentType
		oldEntry.storedAt = time.Now()
		oldEntry.sizeBytes = entrySize
	} else {
		entry := &cachedResponse{
			key:         key,
			payload:     buf,
			contentType: contentType,
			storedAt:    time.Now(),
			sizeBytes:   entrySize,
		}
		elem := c.evictList.PushFront(entry)
		c.entries[key] = elem
		c.currentBytes += entrySize
	}

	// Evict LRU entries if exceeding budget
	for c.currentBytes > c.maxBytes && c.evictList.Len() > 0 {
		oldest := c.evictList.Back()
		if oldest == nil {
			break
		}
		c.evictList.Remove(oldest)
		e := oldest.Value.(*cachedResponse)
		delete(c.entries, e.key)
		c.currentBytes -= e.sizeBytes
	}
}

func (c *responseCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*list.Element)
	c.evictList.Init()
	c.currentBytes = 0
}

type inFlightGroup struct {
	mu    sync.Mutex
	calls map[string]*inFlightCall
}

type inFlightCall struct {
	done chan struct{}
}

func newInFlightGroup() *inFlightGroup {
	return &inFlightGroup{
		calls: make(map[string]*inFlightCall),
	}
}

// Start checks if a request for key is already in flight.
func (g *inFlightGroup) Start(key string) (waited bool, doneFunc func()) {
	if g == nil {
		return false, func() {}
	}
	g.mu.Lock()
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-call.done
		return true, func() {}
	}

	call := &inFlightCall{
		done: make(chan struct{}),
	}
	g.calls[key] = call
	g.mu.Unlock()

	return false, func() {
		g.mu.Lock()
		delete(g.calls, key)
		close(call.done)
		g.mu.Unlock()
	}
}

// chunkCache caches short-lived immutable HLS video segments (.ts chunks) in memory with a strict byte limit.
type chunkCache struct {
	ttl          time.Duration
	maxBytes     int64
	currentBytes int64
	mu           sync.RWMutex
	entries      map[string]*list.Element
	evictList    *list.List
}

func newChunkCache(ttl time.Duration) *chunkCache {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	cc := &chunkCache{
		ttl:       ttl,
		maxBytes:  64 * 1024 * 1024, // 64 MB RAM limit for chunks
		entries:   make(map[string]*list.Element),
		evictList: list.New(),
	}
	go cc.cleanupLoop()
	return cc
}

func (c *chunkCache) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		// Evict expired entries from back to front
		for elem := c.evictList.Back(); elem != nil; {
			prev := elem.Prev()
			entry := elem.Value.(*cachedResponse)
			if now.Sub(entry.storedAt) > c.ttl {
				c.evictList.Remove(elem)
				delete(c.entries, entry.key)
				c.currentBytes -= entry.sizeBytes
			}
			elem = prev
		}
		c.mu.Unlock()
	}
}

func (c *chunkCache) Get(key string) ([]byte, string, bool) {
	if c == nil {
		return nil, "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[key]
	if !ok {
		return nil, "", false
	}
	entry := elem.Value.(*cachedResponse)
	if time.Since(entry.storedAt) > c.ttl {
		c.evictList.Remove(elem)
		delete(c.entries, key)
		c.currentBytes -= entry.sizeBytes
		return nil, "", false
	}

	c.evictList.MoveToFront(elem)
	return entry.payload, entry.contentType, true
}

func (c *chunkCache) Set(key string, payload []byte, contentType string) {
	if c == nil || len(payload) == 0 {
		return
	}

	entrySize := int64(len(payload) + len(key) + len(contentType) + 64)
	if entrySize > c.maxBytes {
		return
	}

	buf := make([]byte, len(payload))
	copy(buf, payload)

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, exists := c.entries[key]; exists {
		c.evictList.MoveToFront(elem)
		entry := elem.Value.(*cachedResponse)
		c.currentBytes += entrySize - entry.sizeBytes
		entry.payload = buf
		entry.contentType = contentType
		entry.storedAt = time.Now()
		entry.sizeBytes = entrySize
	} else {
		entry := &cachedResponse{
			key:         key,
			payload:     buf,
			contentType: contentType,
			storedAt:    time.Now(),
			sizeBytes:   entrySize,
		}
		elem := c.evictList.PushFront(entry)
		c.entries[key] = elem
		c.currentBytes += entrySize
	}

	// Evict LRU entries if exceeding budget
	for c.currentBytes > c.maxBytes && c.evictList.Len() > 0 {
		oldest := c.evictList.Back()
		if oldest == nil {
			break
		}
		c.evictList.Remove(oldest)
		e := oldest.Value.(*cachedResponse)
		delete(c.entries, e.key)
		c.currentBytes -= e.sizeBytes
	}
}

func (c *chunkCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*list.Element)
	c.evictList.Init()
	c.currentBytes = 0
}

// StartTempFileJanitor sweeps temporary M3U playlist scratch files older than maxAge.
func StartTempFileJanitor(ctx context.Context, maxAge time.Duration) {
	dirs := []string{os.TempDir(), "/tmp"}
	sweep := func() {
		cutoff := time.Now().Add(-maxAge)
		seen := make(map[string]bool)
		for _, dir := range dirs {
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() || (!strings.Contains(name, "iptv-proxy") && !strings.HasSuffix(name, ".iptv-proxy.m3u")) {
					continue
				}
				info, err := entry.Info()
				if err == nil && info.ModTime().Before(cutoff) {
					path := filepath.Join(dir, name)
					if errRem := os.Remove(path); errRem == nil {
						log.Printf("[iptv-proxy] Janitor purged stale temp file: %s", path)
					}
				}
			}
		}
	}

	// Run once immediately on startup
	sweep()

	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}
