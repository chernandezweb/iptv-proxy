package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	tsPacket        = 188
	tsSyncByte      = 0x47
	packetsPerChunk = 70
	chunkSize       = tsPacket * packetsPerChunk // 13,160 bytes (exact 188-byte TS multiple)
	subscriberBuf   = 128                        // ~1.68 MB buffer per client

	stallTimeout = 8 * time.Second
	healthyAfter = 10 * time.Second
)

// RefChunk wraps an aligned byte slice from sync.Pool with atomic reference counting.
type RefChunk struct {
	data []byte
	n    int
	refs int32
}

var chunkPool = sync.Pool{
	New: func() interface{} {
		return &RefChunk{
			data: make([]byte, chunkSize),
		}
	},
}

func acquireChunk() *RefChunk {
	c := chunkPool.Get().(*RefChunk)
	atomic.StoreInt32(&c.refs, 1)
	c.n = 0
	return c
}

func (c *RefChunk) Retain() {
	atomic.AddInt32(&c.refs, 1)
}

func (c *RefChunk) Release() {
	if atomic.AddInt32(&c.refs, -1) == 0 {
		chunkPool.Put(c)
	}
}

type relaySubscriber struct {
	ip        string
	ch        chan *RefChunk
	done      chan struct{}
	closeOnce sync.Once
}

func (s *relaySubscriber) close() {
	s.closeOnce.Do(func() {
		close(s.done)
	})
}

// ActiveStreamInfo contains metrics about currently multiplexed live channels.
type ActiveStreamInfo struct {
	Key           string    `json:"key"`
	URL           string    `json:"url"`
	Viewers       int       `json:"viewers"`
	StartedAt     time.Time `json:"started_at"`
	Duration      string    `json:"duration"`
	Reconnections int       `json:"reconnections"`
	ClientIPs     []string  `json:"client_ips"`
}

type streamRelay struct {
	hub           *StreamHub
	streamKey     string
	oriURL        *url.URL
	contentType   string
	header        http.Header
	ready         chan struct{}
	upstreamErr   error
	ts            bool
	startedAt     time.Time
	reconnections int

	mu          sync.Mutex
	subscribers map[*relaySubscriber]bool
	cancel      context.CancelFunc
	connCancel  context.CancelFunc
	active      bool
}

// StreamHub manages multiplexed live stream relays so multiple devices watching
// the same channel share a single upstream connection to the IPTV provider,
// with automatic packet alignment, memory-pooled buffers, and drop/stall reconnection.
type StreamHub struct {
	mu     sync.Mutex
	relays map[string]*streamRelay
}

func newStreamHub() *StreamHub {
	return &StreamHub{
		relays: make(map[string]*streamRelay),
	}
}

// GetActiveStreamsInfo returns a summary of all channels currently being watched.
func (h *StreamHub) GetActiveStreamsInfo() []ActiveStreamInfo {
	h.mu.Lock()
	defer h.mu.Unlock()

	var list []ActiveStreamInfo
	for key, r := range h.relays {
		r.mu.Lock()
		ips := make([]string, 0, len(r.subscribers))
		for sub := range r.subscribers {
			ips = append(ips, sub.ip)
		}
		durationStr := time.Since(r.startedAt).Truncate(time.Second).String()
		info := ActiveStreamInfo{
			Key:           key,
			URL:           r.oriURL.String(),
			Viewers:       len(r.subscribers),
			StartedAt:     r.startedAt,
			Duration:      durationStr,
			Reconnections: r.reconnections,
			ClientIPs:     ips,
		}
		r.mu.Unlock()
		list = append(list, info)
	}
	return list
}

func isTS(begin []byte) bool {
	return len(begin) > tsPacket && begin[0] == tsSyncByte && begin[tsPacket] == tsSyncByte
}

// TryPlaySharedStream checks if the stream can be relayed or attached to an existing relay.
func (h *StreamHub) TryPlaySharedStream(ctx *gin.Context, cfg *Config, client *http.Client, oriURL *url.URL) bool {
	// Only apply shared relay to live streams without Range requests (linear live broadcasts)
	if ctx.Request.Header.Get("Range") != "" {
		return false
	}

	streamKey := oriURL.Path
	if streamKey == "" {
		return false
	}

	h.mu.Lock()
	relay, exists := h.relays[streamKey]
	if !exists {
		reqCtx, cancel := context.WithCancel(context.Background())
		relay = &streamRelay{
			hub:         h,
			streamKey:   streamKey,
			oriURL:      oriURL,
			subscribers: make(map[*relaySubscriber]bool),
			cancel:      cancel,
			active:      true,
			startedAt:   time.Now(),
			ready:       make(chan struct{}),
		}
		h.relays[streamKey] = relay
		h.mu.Unlock()

		// Start upstream reader for this relay
		go relay.startUpstream(reqCtx, cfg, client)
	} else {
		h.mu.Unlock()
	}

	// Wait for upstream connection to be established (up to 8 seconds)
	select {
	case <-relay.ready:
	case <-ctx.Request.Context().Done():
		return true
	case <-time.After(8 * time.Second):
		return false
	}

	if relay.upstreamErr != nil {
		return false
	}

	// Register this client as a subscriber
	sub := &relaySubscriber{
		ip:   ctx.ClientIP(),
		ch:   make(chan *RefChunk, subscriberBuf),
		done: make(chan struct{}),
	}

	relay.mu.Lock()
	relay.subscribers[sub] = true
	viewersCount := len(relay.subscribers)
	contentType := relay.contentType
	header := relay.header.Clone()
	relay.mu.Unlock()

	log.Printf("[iptv-proxy] Shared stream hub: Client %s tuned into %s (Active viewers sharing this 1 upstream connection: %d)", ctx.ClientIP(), streamKey, viewersCount)

	defer func() {
		relay.mu.Lock()
		delete(relay.subscribers, sub)
		remaining := len(relay.subscribers)
		relay.mu.Unlock()

		sub.close()
		// Drain unconsumed chunks in channel so they are returned to pool
		for {
			select {
			case chunk := <-sub.ch:
				if chunk != nil {
					chunk.Release()
				}
			default:
				goto drained
			}
		}
	drained:
		log.Printf("[iptv-proxy] Client %s disconnected from %s (Remaining viewers: %d)", ctx.ClientIP(), streamKey, remaining)

		if remaining == 0 {
			// Grace period before tearing down upstream in case viewer is just reconnecting/switching
			time.AfterFunc(3*time.Second, func() {
				relay.mu.Lock()
				if len(relay.subscribers) == 0 && relay.active {
					relay.active = false
					relay.cancel()
					h.mu.Lock()
					delete(h.relays, streamKey)
					h.mu.Unlock()
					log.Printf("[iptv-proxy] Shared stream hub: Closed upstream for %s (no remaining viewers)", streamKey)
				}
				relay.mu.Unlock()
			})
		}
	}()

	// Send headers
	if contentType != "" {
		ctx.Header("Content-Type", contentType)
	}
	if header != nil {
		mergeHttpHeader(ctx.Writer.Header(), header)
	}
	ctx.Status(http.StatusOK)

	// Stream chunks to viewer
	ctx.Stream(func(w io.Writer) bool {
		select {
		case chunk, ok := <-sub.ch:
			if !ok || chunk == nil {
				return false
			}
			_, err := w.Write(chunk.data[:chunk.n])
			chunk.Release()
			return err == nil
		case <-ctx.Request.Context().Done():
			return false
		case <-sub.done:
			return false
		}
	})

	return true
}

func (r *streamRelay) startUpstream(ctx context.Context, cfg *Config, client *http.Client) {
	connCtx, connCancel := context.WithCancel(ctx)
	r.connCancel = connCancel

	resp, err := r.openConn(connCtx, cfg, client, r.oriURL)
	if err != nil || (resp != nil && resp.StatusCode >= 400) {
		if err == nil {
			err = fmt.Errorf("upstream returned status %d", resp.StatusCode)
		}
		log.Printf("[iptv-proxy] Error opening shared stream for %s: %v", r.streamKey, err)
		r.upstreamErr = err
		close(r.ready)
		r.hub.mu.Lock()
		delete(r.hub.relays, r.streamKey)
		r.hub.mu.Unlock()
		r.cancel()
		return
	}

	reader := bufio.NewReaderSize(resp.Body, chunkSize*2)
	begin, _ := reader.Peek(tsPacket + 1)

	r.mu.Lock()
	r.contentType = resp.Header.Get("Content-Type")
	r.header = resp.Header.Clone()
	r.ts = isTS(begin)
	r.mu.Unlock()
	close(r.ready)

	r.runStreamLoop(ctx, cfg, client, resp, reader)
}

func (r *streamRelay) openConn(ctx context.Context, cfg *Config, client *http.Client, targetURL *url.URL) (*http.Response, error) {
	ginCtx := &gin.Context{
		Request: (&http.Request{
			Method: "GET",
			URL:    targetURL,
			Header: make(http.Header),
		}).WithContext(ctx),
	}
	return cfg.forwardStreamRequest(ginCtx, client, targetURL, false)
}

// broadcast fans out a reference-counted chunk to all active subscribers.
func (r *streamRelay) broadcast(chunk *RefChunk) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	for sub := range r.subscribers {
		chunk.Retain()
		select {
		case sub.ch <- chunk:
		default:
			// Client buffer backed up (slow client network) -> drop chunk for this client only to avoid stalling hub
			chunk.Release()
		}
	}
	return len(r.subscribers) > 0
}

// broadcastDiscontinuity injects a 188-byte MPEG-TS Adaptation packet with Discontinuity Indicator bit (0x80)
// so hardware decoders smoothly reset PCR/PTS clocks during mid-stream failover.
func (r *streamRelay) broadcastDiscontinuity() {
	chunk := acquireChunk()
	pkt := chunk.data[:tsPacket]
	pkt[0] = tsSyncByte // 0x47
	pkt[1] = 0x1F       // Null/Adaptation PID 0x1FFF
	pkt[2] = 0xFF
	pkt[3] = 0x20       // Adaptation field only, no payload
	pkt[4] = 183        // Adaptation field length
	pkt[5] = 0x80       // Discontinuity indicator = 1 (bit 7)
	for i := 6; i < tsPacket; i++ {
		pkt[i] = 0xFF   // Stuffing bytes
	}
	chunk.n = tsPacket
	r.broadcast(chunk)
	chunk.Release()
}

func (r *streamRelay) runStreamLoop(ctx context.Context, cfg *Config, client *http.Client, initialResp *http.Response, initialReader *bufio.Reader) {
	currentResp := initialResp
	currentReader := initialReader

	defer func() {
		if currentResp != nil && currentResp.Body != nil {
			_ = currentResp.Body.Close()
		}
		r.hub.mu.Lock()
		delete(r.hub.relays, r.streamKey)
		r.hub.mu.Unlock()

		r.mu.Lock()
		r.active = false
		for sub := range r.subscribers {
			sub.close()
		}
		r.mu.Unlock()
	}()

	var partial []byte
	openedAt := time.Now()

	// Watchdog timer: drops connection if upstream is silent for stallTimeout
	watchdog := time.AfterFunc(stallTimeout, func() {
		if r.connCancel != nil {
			log.Printf("[iptv-proxy] Upstream for %s stalled (>%v silent), dropping for reconnect", r.streamKey, stallTimeout)
			r.connCancel()
		}
	})
	defer watchdog.Stop()

	retries := []time.Duration{0, 300 * time.Millisecond, 800 * time.Millisecond, 1500 * time.Millisecond, 3000 * time.Millisecond}
	attempt := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		chunk := acquireChunk()
		n, readErr := currentReader.Read(chunk.data)
		if n > 0 {
			watchdog.Reset(stallTimeout)
			if len(partial) > 0 {
				// Reassemble partial packet from previous read
				combined := append(partial, chunk.data[:n]...)
				partial = nil
				if r.ts {
					whole := len(combined) - (len(combined) % tsPacket)
					partial = append(partial, combined[whole:]...)
					combined = combined[:whole]
				}
				if len(combined) > 0 {
					pChunk := acquireChunk()
					copy(pChunk.data, combined)
					pChunk.n = len(combined)
					if !r.broadcast(pChunk) {
						pChunk.Release()
						chunk.Release()
						return
					}
					pChunk.Release()
				}
			} else {
				if r.ts {
					whole := n - (n % tsPacket)
					if whole < n {
						partial = append(partial, chunk.data[whole:n]...)
					}
					chunk.n = whole
				} else {
					chunk.n = n
				}

				if chunk.n > 0 {
					if !r.broadcast(chunk) {
						chunk.Release()
						return
					}
				}
			}
		}
		chunk.Release()

		if readErr == nil {
			continue
		}

		// Read error / EOF / Drop -> Reconnect or failover across backup URLs while viewers are still waiting
		_ = currentResp.Body.Close()
		partial = nil
		watchdog.Stop()

		if time.Since(openedAt) >= healthyAfter {
			attempt = 0
		}

		// Inject MPEG-TS Discontinuity Marker before reconnecting to reset decoders smoothly
		if r.ts {
			r.broadcastDiscontinuity()
		}

		// Collect backup URLs for hot-swap failover
		candidateURLs := []*url.URL{r.oriURL}
		if cfg != nil {
			for _, bURLStr := range cfg.GetAllProviderURLs() {
				if bParsed, errP := url.Parse(bURLStr); errP == nil && bParsed.Host != r.oriURL.Host {
					newURL := *r.oriURL
					newURL.Scheme = bParsed.Scheme
					newURL.Host = bParsed.Host
					candidateURLs = append(candidateURLs, &newURL)
				}
			}
		}

		reconnected := false
		for attempt < len(retries) {
			waitDur := retries[attempt]
			attempt++

			select {
			case <-ctx.Done():
				return
			case <-time.After(waitDur):
			}

			// Check if any viewers are still waiting
			r.mu.Lock()
			hasViewers := len(r.subscribers) > 0
			r.mu.Unlock()
			if !hasViewers {
				return
			}

			if r.connCancel != nil {
				r.connCancel()
			}
			newConnCtx, newConnCancel := context.WithCancel(ctx)
			r.connCancel = newConnCancel

			// Try candidate URLs in rotation
			targetDialURL := candidateURLs[(attempt-1)%len(candidateURLs)]
			log.Printf("[iptv-proxy] Upstream dropped for %s, attempt %d/%d reconnecting to %s...", r.streamKey, attempt, len(retries), targetDialURL.Host)
			newResp, newErr := r.openConn(newConnCtx, cfg, client, targetDialURL)
			if newErr == nil && newResp != nil && newResp.StatusCode == http.StatusOK {
				currentResp = newResp
				currentReader = bufio.NewReaderSize(newResp.Body, chunkSize*2)
				openedAt = time.Now()
				watchdog.Reset(stallTimeout)
				r.mu.Lock()
				r.reconnections++
				r.mu.Unlock()
				log.Printf("[iptv-proxy] Shared stream for %s reconnected successfully via %s! Viewers unaffected.", r.streamKey, targetDialURL.Host)
				reconnected = true
				break
			}
			if newResp != nil && newResp.Body != nil {
				_ = newResp.Body.Close()
			}
		}

		if !reconnected {
			log.Printf("[iptv-proxy] Upstream reconnect failed for %s after %d attempts", r.streamKey, attempt)
			return
		}
	}
}
