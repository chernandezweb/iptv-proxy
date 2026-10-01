package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type relaySubscriber struct {
	ch   chan []byte
	done chan struct{}
}

type streamRelay struct {
	hub         *StreamHub
	streamKey   string
	oriURL      *url.URL
	contentType string
	header      http.Header

	mu          sync.Mutex
	subscribers map[*relaySubscriber]bool
	cancel      context.CancelFunc
	active      bool
}

// StreamHub manages multiplexed live stream relays so multiple devices watching
// the same channel share a single upstream connection to the IPTV provider.
type StreamHub struct {
	mu     sync.Mutex
	relays map[string]*streamRelay
}

func newStreamHub() *StreamHub {
	return &StreamHub{
		relays: make(map[string]*streamRelay),
	}
}

// TryPlaySharedStream checks if the stream can be relayed or attached to an existing relay.
// Returns true if handled by the relay hub.
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
		// Start a new relay
		reqCtx, cancel := context.WithCancel(context.Background())
		relay = &streamRelay{
			hub:         h,
			streamKey:   streamKey,
			oriURL:      oriURL,
			subscribers: make(map[*relaySubscriber]bool),
			cancel:      cancel,
			active:      true,
		}
		h.relays[streamKey] = relay
		h.mu.Unlock()

		// Start upstream reader for this relay
		go relay.startUpstream(reqCtx, cfg, client)
	} else {
		h.mu.Unlock()
	}

	// Register this client as a subscriber
	sub := &relaySubscriber{
		ch:   make(chan []byte, 128),
		done: make(chan struct{}),
	}

	relay.mu.Lock()
	relay.subscribers[sub] = true
	viewersCount := len(relay.subscribers)
	contentType := relay.contentType
	header := relay.header.Clone()
	relay.mu.Unlock()

	log.Printf("[iptv-proxy] Shared stream relay: Client %s tuned into %s (Active viewers sharing this upstream connection: %d)", ctx.ClientIP(), streamKey, viewersCount)

	defer func() {
		relay.mu.Lock()
		delete(relay.subscribers, sub)
		remaining := len(relay.subscribers)
		relay.mu.Unlock()

		close(sub.done)
		log.Printf("[iptv-proxy] Client %s disconnected from %s (Remaining viewers: %d)", ctx.ClientIP(), streamKey, remaining)

		if remaining == 0 {
			// Grace period before tearing down upstream in case viewer is just reconnecting
			time.AfterFunc(3*time.Second, func() {
				relay.mu.Lock()
				if len(relay.subscribers) == 0 && relay.active {
					relay.active = false
					relay.cancel()
					h.mu.Lock()
					delete(h.relays, streamKey)
					h.mu.Unlock()
					log.Printf("[iptv-proxy] Shared stream relay for %s stopped (no active viewers)", streamKey)
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
			if !ok {
				return false
			}
			_, err := w.Write(chunk)
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
	ginCtx := &gin.Context{
		Request: (&http.Request{
			Method: "GET",
			URL:    r.oriURL,
			Header: make(http.Header),
		}).WithContext(ctx),
	}

	resp, err := cfg.forwardStreamRequest(ginCtx, client, r.oriURL, false)
	if err != nil {
		log.Printf("[iptv-proxy] Error starting shared relay upstream for %s: %v", r.streamKey, err)
		r.hub.mu.Lock()
		delete(r.hub.relays, r.streamKey)
		r.hub.mu.Unlock()
		r.cancel()
		return
	}
	defer resp.Body.Close()

	r.mu.Lock()
	r.contentType = resp.Header.Get("Content-Type")
	r.header = resp.Header.Clone()
	r.mu.Unlock()

	buf := make([]byte, 128*1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			r.mu.Lock()
			for sub := range r.subscribers {
				select {
				case sub.ch <- chunk:
				default:
					// Drop chunk if subscriber buffer is backed up to avoid lagging other viewers
				}
			}
			r.mu.Unlock()
		}

		if readErr != nil {
			if readErr != io.EOF && ctx.Err() == nil {
				log.Printf("[iptv-proxy] Upstream relay read ended for %s: %v", r.streamKey, readErr)
			}
			break
		}
	}

	r.hub.mu.Lock()
	delete(r.hub.relays, r.streamKey)
	r.hub.mu.Unlock()

	r.mu.Lock()
	r.active = false
	for sub := range r.subscribers {
		select {
		case <-sub.done:
		default:
			close(sub.done)
		}
	}
	r.mu.Unlock()
}
