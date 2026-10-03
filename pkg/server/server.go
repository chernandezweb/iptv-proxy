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

	"github.com/gin-contrib/cors"
	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
	xtreamapi "github.com/pierre-emmanuelJ/iptv-proxy/pkg/xtream-proxy"
	uuid "github.com/satori/go.uuid"

	"github.com/gin-gonic/gin"
)

type LogRingBuffer struct {
	sync.Mutex
	lines []string
	max   int
}

var globalLogBuffer = &LogRingBuffer{max: 1000}

func (b *LogRingBuffer) Write(p []byte) (n int, err error) {
	b.Lock()
	defer b.Unlock()
	str := string(p)
	parts := strings.Split(str, "\n")
	for _, line := range parts {
		clean := strings.TrimRight(line, "\r")
		if clean != "" {
			b.lines = append(b.lines, clean)
			if len(b.lines) > b.max {
				b.lines = b.lines[len(b.lines)-b.max:]
			}
		}
	}
	return len(p), nil
}

func (b *LogRingBuffer) GetTail(n int) []string {
	b.Lock()
	defer b.Unlock()
	if n <= 0 || n > len(b.lines) {
		n = len(b.lines)
	}
	start := len(b.lines) - n
	res := make([]string, n)
	copy(res, b.lines[start:])
	return res
}

func init() {
	log.SetOutput(io.MultiWriter(os.Stderr, globalLogBuffer))
}

var defaultProxyfiedM3UPath = filepath.Join(os.TempDir(), uuid.NewV4().String()+".iptv-proxy.m3u")
var endpointAntiColision = strings.Split(uuid.NewV4().String(), "-")[0]

// StreamRoutingTarget maps a virtual stream ID to its upstream provider and original ID.
type StreamRoutingTarget struct {
	ProviderIndex int
	OriginalID    string
	StreamType    string // "live", "movie", "series"
}

// Config represent the server configuration
type Config struct {
	*config.ProxyConfig

	// M3U service part
	playlist *m3u.Playlist
	// this variable is set only for m3u proxy endpoints
	track *m3u.Track
	// path to the proxyfied m3u file
	proxyfiedM3UPath string

	endpointAntiColision string

	metadataCache *responseCache
	xmltvCache    *responseCache
	chunkCache    *chunkCache
	userManager   *config.UserManager
	httpClient    *http.Client
	baseStreamURL *url.URL

	streamRoutingMap  map[string]StreamRoutingTarget
	streamRoutingLock sync.RWMutex

	adminAuthPath string

	refreshing      map[string]bool
	refreshingMutex sync.Mutex
}

// NewServer initialize a new server configuration
func NewServer(cfgData *config.ProxyConfig) (*Config, error) {
	var p m3u.Playlist
	if cfgData.RemoteURL.String() != "" {
		var err error
		p, err = m3u.Parse(cfgData.RemoteURL.String())
		if err != nil {
			return nil, err
		}
	}

	if trimmedCustomId := strings.Trim(cfgData.CustomId, "/"); trimmedCustomId != "" {
		endpointAntiColision = trimmedCustomId
	}

	var baseURL *url.URL
	if cfgData.XtreamBaseURL != "" {
		var err error
		baseURL, err = url.Parse(cfgData.XtreamBaseURL)
		if err != nil {
			return nil, err
		}
	}

	cfg := &Config{
		ProxyConfig:          cfgData,
		playlist:             &p,
		track:                nil,
		proxyfiedM3UPath:     defaultProxyfiedM3UPath,
		endpointAntiColision: endpointAntiColision,
		baseStreamURL:        baseURL,
		refreshing:           make(map[string]bool),
	}
	cfg.metadataCache = newResponseCache(cfgData.MetadataCacheTTL)
	cfg.xmltvCache = newResponseCache(cfgData.XMLTVCacheTTL)
	cfg.chunkCache = newChunkCache(15 * time.Second)
	cfg.streamRoutingMap = make(map[string]StreamRoutingTarget)
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		if fi, err := os.Stat("/data"); err == nil && fi.IsDir() {
			dataDir = "/data"
		} else {
			dataDir = "."
		}
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Printf("[iptv-proxy] Warning: failed to ensure dataDir %s: %v", dataDir, err)
	}
	filtersPath := filepath.Join(dataDir, "filters.json")
	providerPath := filepath.Join(dataDir, "provider.json")
	usersPath := filepath.Join(dataDir, "users.json")
	adminAuthPath := filepath.Join(dataDir, "admin.json")
	cfg.adminAuthPath = adminAuthPath
	log.Printf("[iptv-proxy] Storage directory configured: %s (filters: %s, provider: %s, users: %s, admin: %s)", dataDir, filtersPath, providerPath, usersPath, adminAuthPath)
	cfg.ProxyConfig.Filters = config.NewFilters(filtersPath)
	cfg.ProxyConfig.Provider = config.NewProvider(providerPath, config.ProviderData{
		XtreamBaseURL:  cfgData.XtreamBaseURL,
		XtreamUser:     string(cfgData.XtreamUser),
		XtreamPassword: string(cfgData.XtreamPassword),
		Referer:        cfgData.Referer,
	})
	cfg.userManager = config.NewUserManager(usersPath, cfgData.User.String(), cfgData.Password.String())
	cfg.ProxyConfig.UserManager = cfg.userManager

	// Apply SOCKS / VPN proxy if configured in provider.json
	socks := cfg.ProxyConfig.Provider.GetSocksProxy()
	if socks.Enabled && socks.ProxyURL() != "" {
		pURL := socks.ProxyURL()
		os.Setenv("ALL_PROXY", pURL)
		os.Setenv("HTTP_PROXY", pURL)
		os.Setenv("HTTPS_PROXY", pURL)
		log.Printf("[iptv-proxy] Upstream VPN/SOCKS5 proxy active: %s://%s:%d", socks.Type, socks.Host, socks.Port)
	}
	cfg.httpClient = newUpstreamHTTPClient(cfg)

	provData := cfg.ProxyConfig.Provider.GetData()
	cfg.ProxyConfig.XtreamBaseURL = provData.XtreamBaseURL
	cfg.ProxyConfig.BackupURLs = provData.BackupURLs
	cfg.ProxyConfig.XtreamUser = config.CredentialString(provData.XtreamUser)

	// Auto-detect public IP if Hostname is not explicitly set
	if cfgData.HostConfig != nil && (cfgData.HostConfig.Hostname == "" || cfgData.HostConfig.Hostname == "0.0.0.0") {
		go func() {
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Get("https://api.ipify.org")
			if err == nil && resp.StatusCode == http.StatusOK {
				body, _ := ioutil.ReadAll(resp.Body)
				resp.Body.Close()
				ip := strings.TrimSpace(string(body))
				if ip != "" && net.ParseIP(ip) != nil {
					cfgData.HostConfig.Hostname = ip
					log.Printf("[iptv-proxy] Auto-detected public VPS IP: %s", ip)
				}
			}
		}()
	}
	cfg.ProxyConfig.XtreamPassword = config.CredentialString(provData.XtreamPassword)
	cfg.ProxyConfig.Referer = provData.Referer
	cfg.ProxyConfig.UserAgent = provData.UserAgent
	if provData.XtreamBaseURL != "" {
		if u, err := url.Parse(provData.XtreamBaseURL); err == nil {
			cfg.baseStreamURL = u
		}
	}

	return cfg, nil
}

// ApplyProxySettings updates the active outbound SOCKS5/VPN proxy transport in real time.
func (c *Config) ApplyProxySettings(socks config.UpstreamProxySettings) {
	if socks.Enabled && socks.ProxyURL() != "" {
		pURL := socks.ProxyURL()
		os.Setenv("ALL_PROXY", pURL)
		os.Setenv("HTTP_PROXY", pURL)
		os.Setenv("HTTPS_PROXY", pURL)
		log.Printf("[iptv-proxy] Upstream VPN/SOCKS5 proxy active: %s://%s:%d", socks.Type, socks.Host, socks.Port)
	} else {
		os.Unsetenv("ALL_PROXY")
		os.Unsetenv("HTTP_PROXY")
		os.Unsetenv("HTTPS_PROXY")
		log.Println("[iptv-proxy] Upstream VPN/SOCKS5 proxy disabled (direct connection)")
	}
	c.httpClient = newUpstreamHTTPClient(c)
	provClientCacheLock.Lock()
	provClientCache = make(map[string]*xtreamapi.Client)
	provClientCacheLock.Unlock()
}

// Serve the iptv-proxy api
func (c *Config) Serve() error {
	if err := c.playlistInitialization(); err != nil {
		return err
	}

	router := gin.Default()
	router.Use(cors.Default())
	group := router.Group("/")
	c.routes(group)
	c.adminRoutes(group)

	// Add a message to indicate the server is ready
	log.Printf("[iptv-proxy] Server is ready and listening on :%d", c.HostConfig.Port)

	return router.Run(fmt.Sprintf(":%d", c.HostConfig.Port))
}

func (c *Config) playlistInitialization() error {
	if len(c.playlist.Tracks) == 0 {
		return nil
	}

	f, err := os.Create(c.proxyfiedM3UPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return c.marshallInto(f, false)
}

// MarshallInto a *bufio.Writer a Playlist.
func (c *Config) marshallInto(into *os.File, xtream bool) error {
	filteredTrack := make([]m3u.Track, 0, len(c.playlist.Tracks))

	ret := 0
	into.WriteString("#EXTM3U\n") // nolint: errcheck
	for i, track := range c.playlist.Tracks {
		var buffer bytes.Buffer

		buffer.WriteString("#EXTINF:")                       // nolint: errcheck
		buffer.WriteString(fmt.Sprintf("%d ", track.Length)) // nolint: errcheck
		for i := range track.Tags {
			if i == len(track.Tags)-1 {
				buffer.WriteString(fmt.Sprintf("%s=%q", track.Tags[i].Name, track.Tags[i].Value)) // nolint: errcheck
				continue
			}
			buffer.WriteString(fmt.Sprintf("%s=%q ", track.Tags[i].Name, track.Tags[i].Value)) // nolint: errcheck
		}

		uri, err := c.replaceURL(track.URI, i-ret, xtream)
		if err != nil {
			ret++
			log.Printf("ERROR: track: %s: %s", track.Name, err)
			continue
		}

		into.WriteString(fmt.Sprintf("%s, %s\n%s\n", buffer.String(), track.Name, uri)) // nolint: errcheck

		filteredTrack = append(filteredTrack, track)
	}
	c.playlist.Tracks = filteredTrack

	return into.Sync()
}

// ReplaceURL replace original playlist url by proxy url
func (c *Config) replaceURL(uri string, trackIndex int, xtream bool) (string, error) {
	oriURL, err := url.Parse(uri)
	if err != nil {
		return "", err
	}

	protocol := "http"
	if c.HTTPS {
		protocol = "https"
	}

	customEnd := strings.Trim(c.CustomEndpoint, "/")
	if customEnd != "" {
		customEnd = fmt.Sprintf("/%s", customEnd)
	}

	uriPath := oriURL.EscapedPath()
	if xtream {
		uriPath = strings.ReplaceAll(uriPath, c.XtreamUser.PathEscape(), c.User.PathEscape())
		uriPath = strings.ReplaceAll(uriPath, c.XtreamPassword.PathEscape(), c.Password.PathEscape())
		if c.ProxyConfig != nil && c.ProxyConfig.Provider != nil {
			for _, prov := range c.ProxyConfig.Provider.GetProviders() {
				if prov.XtreamUser != "" {
					uriPath = strings.ReplaceAll(uriPath, url.PathEscape(prov.XtreamUser), c.User.PathEscape())
				}
				if prov.XtreamPassword != "" {
					uriPath = strings.ReplaceAll(uriPath, url.PathEscape(prov.XtreamPassword), c.Password.PathEscape())
				}
			}
		}
	} else {
		uriPath = path.Join("/", c.endpointAntiColision, c.User.PathEscape(), c.Password.PathEscape(), fmt.Sprintf("%d", trackIndex), path.Base(uriPath))
	}

	basicAuth := oriURL.User.String()
	if basicAuth != "" {
		basicAuth += "@"
	}

	newURI := fmt.Sprintf(
		"%s://%s%s:%d%s%s",
		protocol,
		basicAuth,
		c.HostConfig.Hostname,
		c.AdvertisedPort,
		customEnd,
		uriPath,
	)

	newURL, err := url.Parse(newURI)
	if err != nil {
		return "", err
	}

	return newURL.String(), nil
}

func newUpstreamHTTPClient(cfg *Config) *http.Client {
	proxyFunc := http.ProxyFromEnvironment
	for _, envKey := range []string{"ALL_PROXY", "all_proxy", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if val := os.Getenv(envKey); val != "" {
			if u, err := url.Parse(val); err == nil {
				proxyFunc = http.ProxyURL(u)
				break
			}
		}
	}

	transport := &http.Transport{
		Proxy: proxyFunc,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   128,
		MaxConnsPerHost:       256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   0,
	}
}

// GetUpstreamUserAgent returns configured masquerade User-Agent or default Chrome UA.
func (c *Config) GetUpstreamUserAgent() string {
	if c != nil && c.ProxyConfig != nil && c.ProxyConfig.UserAgent != "" {
		return c.ProxyConfig.UserAgent
	}
	return config.DefaultUserAgent
}

// GetAllProviderURLs returns all configured URLs (active URL followed by backup URLs).
func (c *Config) GetAllProviderURLs() []string {
	if c.ProxyConfig != nil && c.ProxyConfig.Provider != nil {
		return c.ProxyConfig.Provider.GetAllURLs()
	}
	if c.XtreamBaseURL != "" {
		return []string{c.XtreamBaseURL}
	}
	return nil
}

// RotateToURL switches the active provider URL to newURL, updates referer, baseStreamURL, and flushes caches.
func (c *Config) RotateToURL(newURL string) {
	newClean := strings.TrimRight(strings.TrimSpace(newURL), "/")
	if newClean == "" || newClean == c.XtreamBaseURL {
		return
	}

	log.Printf("[iptv-proxy] Failover triggered: switching provider base URL from %q to %q", c.XtreamBaseURL, newClean)
	c.XtreamBaseURL = newClean
	c.Referer = newClean
	c.ProxyConfig.XtreamBaseURL = newClean
	c.ProxyConfig.Referer = newClean

	if u, err := url.Parse(newClean); err == nil {
		c.baseStreamURL = u
	}

	if c.ProxyConfig.Provider != nil {
		if err := c.ProxyConfig.Provider.SetActiveURL(newClean); err != nil {
			log.Printf("[iptv-proxy] Error persisting rotated active URL: %v", err)
		}
		c.ProxyConfig.BackupURLs = c.ProxyConfig.Provider.GetData().BackupURLs
	}

	if c.metadataCache != nil {
		c.metadataCache.Clear()
	}
	if c.xmltvCache != nil {
		c.xmltvCache.Clear()
	}
	if c.chunkCache != nil {
		c.chunkCache.Clear()
	}
}

// ReplaceBaseURL replaces scheme and host of a given target URL with the newBaseURL.
func (c *Config) ReplaceBaseURL(targetURL *url.URL, newBaseURL string) (*url.URL, error) {
	parsedBase, err := url.Parse(strings.TrimRight(newBaseURL, "/"))
	if err != nil {
		return nil, err
	}

	res := *targetURL
	res.Scheme = parsedBase.Scheme
	res.Host = parsedBase.Host
	return &res, nil
}

// RegisterStreamTarget saves the mapping between a virtual stream ID and its upstream target.
func (c *Config) RegisterStreamTarget(virtualID string, target StreamRoutingTarget) {
	if c == nil {
		return
	}
	c.streamRoutingLock.Lock()
	if c.streamRoutingMap == nil {
		c.streamRoutingMap = make(map[string]StreamRoutingTarget)
	}
	c.streamRoutingMap[virtualID] = target
	c.streamRoutingLock.Unlock()
}

// ResolveTargetStream resolves which upstream provider and original stream ID a client request belongs to.
func (c *Config) ResolveTargetStream(streamType string, rawID string) (config.ProviderItem, string) {
	var enabled []config.ProviderItem
	if c != nil && c.ProxyConfig != nil && c.ProxyConfig.Provider != nil {
		enabled = c.ProxyConfig.Provider.GetEnabledProviders()
	}

	if len(enabled) == 0 {
		return config.ProviderItem{
			XtreamBaseURL:  c.XtreamBaseURL,
			XtreamUser:     string(c.XtreamUser),
			XtreamPassword: string(c.XtreamPassword),
			Referer:        c.Referer,
			UserAgent:      c.GetUpstreamUserAgent(),
		}, rawID
	}

	ext := path.Ext(rawID)
	base := strings.TrimSuffix(rawID, ext)

	// 1. Check in-memory map
	c.streamRoutingLock.RLock()
	if target, found := c.streamRoutingMap[base]; found {
		c.streamRoutingLock.RUnlock()
		if target.ProviderIndex >= 0 && target.ProviderIndex < len(enabled) {
			return enabled[target.ProviderIndex], target.OriginalID + ext
		}
	} else {
		c.streamRoutingLock.RUnlock()
	}

	// 2. Check alphanumeric prefix format: p<index>_<id>
	if strings.HasPrefix(base, "p") && strings.Contains(base, "_") {
		parts := strings.SplitN(base, "_", 2)
		if idx, err := strconv.Atoi(parts[0][1:]); err == nil && idx >= 0 && idx < len(enabled) {
			return enabled[idx], parts[1] + ext
		}
	}

	// 3. Check numeric deterministic offset: (providerIndex * 1,000,000) + id
	if num, err := strconv.Atoi(base); err == nil {
		idx := num / 1000000
		origNum := num % 1000000
		if idx >= 0 && idx < len(enabled) {
			return enabled[idx], fmt.Sprintf("%d%s", origNum, ext)
		}
	}

	// Default to first enabled provider
	return enabled[0], rawID
}

// FindProviderIndex returns the index of a given provider in the enabled providers slice.
func (c *Config) FindProviderIndex(prov config.ProviderItem) int {
	if c == nil || c.ProxyConfig == nil || c.ProxyConfig.Provider == nil {
		return 0
	}
	enabled := c.ProxyConfig.Provider.GetEnabledProviders()
	for i, p := range enabled {
		if p.ID == prov.ID || (p.XtreamBaseURL == prov.XtreamBaseURL && p.XtreamUser == prov.XtreamUser) {
			return i
		}
	}
	return 0
}

// GetEnabledProvidersOrFallback returns enabled providers, falling back to legacy single provider fields if empty.
func (c *Config) GetEnabledProvidersOrFallback() []config.ProviderItem {
	var enabled []config.ProviderItem
	if c != nil && c.ProxyConfig != nil && c.ProxyConfig.Provider != nil {
		enabled = c.ProxyConfig.Provider.GetEnabledProviders()
	}
	if len(enabled) == 0 {
		if c == nil || (c.XtreamBaseURL == "" && string(c.XtreamUser) == "") {
			return nil
		}
		return []config.ProviderItem{
			{
				ID:             "provider_1",
				Name:           "Primary Provider",
				Enabled:        true,
				XtreamBaseURL:  c.XtreamBaseURL,
				BackupURLs:     c.BackupURLs,
				XtreamUser:     string(c.XtreamUser),
				XtreamPassword: string(c.XtreamPassword),
				Referer:        c.Referer,
				UserAgent:      c.GetUpstreamUserAgent(),
			},
		}
	}
	return enabled
}

