/*
 * Iptv-Proxy is a project to proxyfie an m3u file and to proxyfie an Xtream iptv service (client API).
 * Copyright (C) 2020  Pierre-Emmanuel Jacquier
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func (c *Config) getM3U(ctx *gin.Context) {
	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	ctx.Header("Content-Type", "application/octet-stream")

	authUser := c.User.String()
	authPass := c.Password.String()
	if val, exists := ctx.Get("auth_user"); exists {
		if u, ok := val.(*config.UserItem); ok {
			authUser = u.Username
			authPass = u.Password
		}
	} else if u := ctx.Query("username"); u != "" {
		authUser = u
		authPass = ctx.Query("password")
	}

	if authUser == c.User.String() && authPass == c.Password.String() {
		ctx.File(c.proxyfiedM3UPath)
		return
	}

	data, err := ioutil.ReadFile(c.proxyfiedM3UPath)
	if err != nil {
		ctx.File(c.proxyfiedM3UPath)
		return
	}
	content := string(data)
	content = strings.ReplaceAll(content, "/"+c.User.PathEscape()+"/"+c.Password.PathEscape()+"/", "/"+url.PathEscape(authUser)+"/"+url.PathEscape(authPass)+"/")
	ctx.Data(http.StatusOK, "application/octet-stream", []byte(content))
}

func (c *Config) reverseProxy(ctx *gin.Context) {
	rpURL, err := url.Parse(c.track.URI)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.stream(ctx, rpURL)
}

func (c *Config) m3u8ReverseProxy(ctx *gin.Context) {
	id := ctx.Param("id")

	rpURL, err := url.Parse(strings.ReplaceAll(c.track.URI, path.Base(c.track.URI), id))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.stream(ctx, rpURL)
}

func (c *Config) stream(ctx *gin.Context, oriURL *url.URL) {
	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	requestRangeHeader := ctx.Request.Header.Get("Range")
	forwardRange := requestRangeHeader != ""

	// Check if this stream can be multiplexed through the shared stream hub (live broadcasts).
	// When enabled, multiple viewers watching the same live channel share 1 upstream connection.
	isLiveBroadcast := !forwardRange && (strings.Contains(oriURL.Path, ".ts") || strings.Contains(ctx.Request.URL.Path, "/live/") || (!strings.Contains(oriURL.Path, "/movie/") && !strings.Contains(oriURL.Path, "/series/")))
	if c.streamHub != nil && isLiveBroadcast && c.IsSharedStreamEnabled() {
		if c.streamHub.TryPlaySharedStream(ctx, c, client, oriURL) {
			return
		}
	}

	resp, err := c.forwardStreamRequest(ctx, client, oriURL, forwardRange)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	logUpstreamStatus(oriURL, resp, forwardRange && requestRangeHeader != "", "initial")

	if forwardRange && shouldRetryWithoutRange(resp) {
		log.Printf("[iptv-proxy] Upstream %s returned 206 with zero/invalid payload (Range=%q); retrying without Range header", oriURL.String(), ctx.Request.Header.Get("Range"))
		resp.Body.Close()
		resp, err = c.forwardStreamRequest(ctx, client, oriURL, false)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}
		logUpstreamStatus(oriURL, resp, false, "retry-no-range")
	}
	defer resp.Body.Close()

	// If upstream returned an error status, log headers and a small portion
	// of the response body to aid debugging (don't consume large bodies).
	if resp.StatusCode >= 400 {
		// copy headers
		hdrs := make(map[string][]string)
		for k, v := range resp.Header {
			hdrs[k] = v
		}
		log.Printf("[iptv-proxy] Upstream %s returned status %d; headers=%v", oriURL.String(), resp.StatusCode, hdrs)

		// read up to 32KB of the body for logging/capture
		lr := io.LimitReader(resp.Body, 32*1024)
		b, _ := ioutil.ReadAll(lr)
		if len(b) > 0 {
			log.Printf("[iptv-proxy] Upstream error body (truncated): %s", string(b))
			if resp.StatusCode == 509 {
				if capturePath, err := captureErrorBody(resp.StatusCode, b); err != nil {
					log.Printf("[iptv-proxy] Unable to persist 509 body: %v", err)
				} else {
					log.Printf("[iptv-proxy] Persisted 509 body to %s", capturePath)
				}
			}
		}
		// Reset body reader so we can still stream the full response to the client.
		// Note: we can't rewind the original resp.Body. Instead, for error statuses
		// we'll return the truncated body we read above and set the status.
		mergeHttpHeader(ctx.Writer.Header(), resp.Header)
		ctx.Status(resp.StatusCode)
		if len(b) > 0 {
			ctx.Writer.Write(b) // nolint: errcheck
		}
		return
	}

	// For HLS .ts chunks, cache in memory to serve other devices watching the same channel.
	// Only cache individual discrete chunks (e.g. 414069_1085.ts), NEVER infinite live MPEG-TS streams (e.g. 414066.ts).
	chunkName := path.Base(oriURL.Path)
	if c.chunkCache != nil && strings.HasSuffix(chunkName, ".ts") && strings.Contains(chunkName, "_") && resp.StatusCode == http.StatusOK {
		var cacheBuf bytes.Buffer
		tee := io.TeeReader(resp.Body, &cacheBuf)
		mergeHttpHeader(ctx.Writer.Header(), resp.Header)
		ctx.Status(resp.StatusCode)
		bufPtr := streamBufferPool.Get().(*[]byte)
		defer streamBufferPool.Put(bufPtr)
		ctx.Stream(func(w io.Writer) bool {
			io.CopyBuffer(w, tee, *bufPtr)
			return false
		})
		if cacheBuf.Len() > 0 && cacheBuf.Len() < 10*1024*1024 {
			c.chunkCache.Set(chunkName, cacheBuf.Bytes(), resp.Header.Get("Content-Type"))
		}
		return
	}

	mergeHttpHeader(ctx.Writer.Header(), resp.Header)
	ctx.Status(resp.StatusCode)
	bufPtr := streamBufferPool.Get().(*[]byte)
	defer streamBufferPool.Put(bufPtr)
	ctx.Stream(func(w io.Writer) bool {
		io.CopyBuffer(w, resp.Body, *bufPtr) // nolint: errcheck
		return false
	})
}

func (c *Config) forwardStreamRequest(ctx *gin.Context, client *http.Client, oriURL *url.URL, forwardRange bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx.Request.Context(), "GET", oriURL.String(), nil)
	if err != nil {
		return nil, err
	}

	mergeHttpHeader(req.Header, ctx.Request.Header)

	// User-Agent: use original client UA if enabled, otherwise use configured masquerade UA
	if c.IsPassOriginalUserAgent() && ctx.Request.Header.Get("User-Agent") != "" {
		req.Header.Set("User-Agent", ctx.Request.Header.Get("User-Agent"))
	} else {
		req.Header.Set("User-Agent", c.GetUpstreamUserAgent())
	}

	if c.Referer != "" {
		req.Header.Set("Referer", c.Referer)
	} else {
		req.Header.Del("Referer")
	}

	// Anti-IP leak: strip all proxy and client IP headers so provider only sees VPS IP
	cleanUpstreamHeaders(req.Header)

	if forwardRange && ctx.Request.Header.Get("Range") != "" {
		req.Header.Set("Range", ctx.Request.Header.Get("Range"))
	} else {
		req.Header.Del("Range")
		req.Header.Del("If-Range")
	}

	resp, err := c.doRequestWithProxyRetry(client, req)
	if err == nil && resp.StatusCode < 400 {
		return resp, nil
	}

	// If the primary request failed (network/timeout error) or returned 5xx server error,
	// automatically failover across configured backup URLs.
	// NOTE: Only attempt failover across backup URLs if the error is NOT caused by our local SOCKS/proxy.
	// Outbound SOCKS authentication/handshake issues are local proxy issues, not provider outages.
	backupURLs := c.GetAllProviderURLs()
	if c.ProxyConfig != nil && c.ProxyConfig.Provider != nil {
		for _, prov := range c.ProxyConfig.Provider.GetProviders() {
			if strings.Contains(prov.XtreamBaseURL, req.URL.Host) {
				for _, b := range prov.BackupURLs {
					if cb := config.CleanURL(b); cb != "" {
						backupURLs = append(backupURLs, cb)
					}
				}
				break
			}
		}
	}
	if !isProxyError(err) && len(backupURLs) > 1 {
		currentHost := req.URL.Host
		for _, bURL := range backupURLs {
			parsedBackup, errP := url.Parse(bURL)
			if errP != nil || parsedBackup.Host == currentHost {
				continue
			}

			backupReq := req.Clone(ctx.Request.Context())
			backupReq.URL.Scheme = parsedBackup.Scheme
			backupReq.URL.Host = parsedBackup.Host
			backupReq.Host = parsedBackup.Host
			backupReq.Header.Set("Referer", bURL)

			log.Printf("[iptv-proxy] Upstream %s failed/down (err=%v); attempting failover to backup URL %s...", currentHost, err, bURL)
			bResp, bErr := c.doRequestWithProxyRetry(client, backupReq)
			if bErr == nil && bResp.StatusCode < 400 {
				if resp != nil && resp.Body != nil {
					resp.Body.Close()
				}
				log.Printf("[iptv-proxy] Failover to %s successful! Auto-rotating active provider URL.", bURL)
				c.RotateToURL(bURL)
				return bResp, nil
			}
			if bResp != nil && bResp.Body != nil {
				bResp.Body.Close()
			}
		}
	}

	if err == nil {
		return resp, nil
	}

	if fallbackReq := c.retryRequestWithBaseHost(ctx, req, err); fallbackReq != nil {
		log.Printf("[iptv-proxy] DNS lookup failed for %s; retrying against base host %s", req.URL.String(), fallbackReq.URL.Host)
		return c.doRequestWithProxyRetry(client, fallbackReq)
	}

	return nil, err
}

func shouldRetryWithoutRange(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusPartialContent {
		return false
	}

	if resp.ContentLength == 0 {
		return true
	}

	if resp.ContentLength < 0 {
		if cl := strings.TrimSpace(resp.Header.Get("Content-Length")); cl != "" {
			if v, err := strconv.ParseInt(cl, 10, 64); err == nil {
				return v == 0
			}
		}
		return false
	}

	return false
}

func logUpstreamStatus(oriURL *url.URL, resp *http.Response, usedRange bool, attempt string) {
	if resp == nil {
		return
	}

	contentRange := resp.Header.Get("Content-Range")
	contentLength := resp.ContentLength
	if contentLength < 0 {
		if cl := strings.TrimSpace(resp.Header.Get("Content-Length")); cl != "" {
			if v, err := strconv.ParseInt(cl, 10, 64); err == nil {
				contentLength = v
			}
		}
	}

	log.Printf(
		"[iptv-proxy] Upstream %s (%s) status=%d usedRange=%t contentLength=%d contentRange=%q",
		oriURL.String(),
		attempt,
		resp.StatusCode,
		usedRange,
		contentLength,
		contentRange,
	)
}

func (c *Config) xtreamStream(ctx *gin.Context, oriURL *url.URL) {
	id := ctx.Param("id")
	if strings.HasSuffix(id, ".m3u8") {
		c.hlsXtreamStream(ctx, oriURL)
		return
	}

	c.stream(ctx, oriURL)
}

type values []string

func (vs values) contains(s string) bool {
	for _, v := range vs {
		if v == s {
			return true
		}
	}

	return false
}

func mergeHttpHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			if values(dst.Values(k)).contains(v) {
				continue
			}
			dst.Add(k, v)
		}
	}
}

func (c *Config) retryRequestWithBaseHost(ctx *gin.Context, originalReq *http.Request, err error) *http.Request {
	if c == nil || c.baseStreamURL == nil || originalReq == nil {
		return nil
	}

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return nil
	}

	var dnsErr *net.DNSError
	if !errors.As(urlErr.Err, &dnsErr) || !dnsErr.IsNotFound {
		return nil
	}

	fallback := originalReq.Clone(ctx.Request.Context())
	if fallback.URL == nil {
		return nil
	}

	newURL := *fallback.URL
	newURL.Host = c.baseStreamURL.Host
	newURL.Scheme = c.baseStreamURL.Scheme
	fallback.URL = &newURL
	fallback.Host = c.baseStreamURL.Host

	return fallback
}

// authRequest handle auth credentials
type authRequest struct {
	Username string `form:"username" binding:"required"`
	Password string `form:"password" binding:"required"`
}

func (c *Config) authenticate(ctx *gin.Context) {
	var authReq authRequest
	if err := ctx.Bind(&authReq); err != nil {
		ctx.AbortWithError(http.StatusBadRequest, err) // nolint: errcheck
		return
	}

	valid := false
	if c.userManager != nil {
		if u, ok := c.userManager.Authenticate(authReq.Username, authReq.Password); ok {
			valid = true
			ctx.Set("auth_user", u)
		}
	}
	if !valid && c.ProxyConfig.User.String() == authReq.Username && c.ProxyConfig.Password.String() == authReq.Password {
		valid = true
		ctx.Set("auth_user", &config.UserItem{
			Username:       authReq.Username,
			Password:       authReq.Password,
			MaxConnections: 0,
			Enabled:        true,
		})
	}

	if !valid {
		ctx.AbortWithStatus(http.StatusUnauthorized)
	}
}

func (c *Config) appAuthenticate(ctx *gin.Context) {
	contents, err := ioutil.ReadAll(ctx.Request.Body)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	q, err := url.ParseQuery(string(contents))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	if len(q["username"]) == 0 || len(q["password"]) == 0 {
		ctx.AbortWithError(http.StatusBadRequest, fmt.Errorf("bad body url query parameters")) // nolint: errcheck
		return
	}
	log.Printf("[iptv-proxy] %v | %s |App Auth\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())

	reqUser := q["username"][0]
	reqPass := q["password"][0]
	valid := false
	if c.userManager != nil {
		if u, ok := c.userManager.Authenticate(reqUser, reqPass); ok {
			valid = true
			ctx.Set("auth_user", u)
		}
	}
	if !valid && c.ProxyConfig.User.String() == reqUser && c.ProxyConfig.Password.String() == reqPass {
		valid = true
		ctx.Set("auth_user", &config.UserItem{
			Username:       reqUser,
			Password:       reqPass,
			MaxConnections: 0,
			Enabled:        true,
		})
	}

	if !valid {
		ctx.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	ctx.Request.Body = ioutil.NopCloser(bytes.NewReader(contents))
}

func captureErrorBody(status int, body []byte) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("empty error body")
	}

	filename := fmt.Sprintf("iptv-proxy-%d-%d.body", status, time.Now().UnixNano())
	path := filepath.Join(os.TempDir(), filename)

	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}

	return path, nil
}

var streamBufferPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 128*1024) // 128KB buffer for high-throughput streaming
		return &b
	},
}

// cleanUpstreamHeaders strips all client and proxy IP forwarding headers to prevent
// upstream IPTV providers from detecting that 12+ devices across different networks
// are using the same account.
func cleanUpstreamHeaders(h http.Header) {
	h.Del("X-Forwarded-For")
	h.Del("X-Real-IP")
	h.Del("X-Client-IP")
	h.Del("CF-Connecting-IP")
	h.Del("True-Client-IP")
	h.Del("Client-IP")
	h.Del("X-Forwarded-Proto")
	h.Del("X-Forwarded-Host")
	h.Del("X-Forwarded-Port")
	h.Del("X-Forwarded-Server")
	h.Del("Forwarded")
	h.Del("Via")

	// Strip browser and cross-origin headers to mimic native IPTV apps
	h.Del("Origin")
	h.Del("Sec-Fetch-Site")
	h.Del("Sec-Fetch-Mode")
	h.Del("Sec-Fetch-Dest")
	h.Del("Sec-Ch-Ua")
	h.Del("Sec-Ch-Ua-Mobile")
	h.Del("Sec-Ch-Ua-Platform")

	// Strip client authentication headers
	h.Del("Authorization")
	h.Del("Proxy-Authorization")
}

