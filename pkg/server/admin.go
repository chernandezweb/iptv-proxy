package server

import (
	"embed"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
	xtreamapi "github.com/pierre-emmanuelJ/iptv-proxy/pkg/xtream-proxy"
)

//go:embed web/*
var webFS embed.FS

func (c *Config) adminRoutes(r *gin.RouterGroup) {
	admin := r.Group("/admin")
	admin.Use(gin.BasicAuth(gin.Accounts{c.User.String(): c.Password.String()}))

	// API endpoints
	admin.GET("/api/categories", c.adminGetCategories)
	admin.POST("/api/filters", c.adminSaveFilters)
	admin.GET("/api/providers", c.adminGetProviders)
	admin.POST("/api/providers", c.adminSaveProviders)
	admin.POST("/api/providers/test", c.adminTestProviders)
	admin.GET("/api/provider", c.adminGetProvider)
	admin.POST("/api/provider", c.adminSaveProvider)
	admin.POST("/api/provider/test", c.adminTestProvider)
	admin.POST("/api/provider/rotate", c.adminRotateProvider)

	// Static files from embedded FS
	admin.GET("/", func(ctx *gin.Context) {
		html, _ := webFS.ReadFile("web/admin.html")
		ctx.Data(http.StatusOK, "text/html; charset=utf-8", html)
	})
}

func (c *Config) adminGetCategories(ctx *gin.Context) {
	c.ProxyConfig.Filters.RLock()
	filtersData := c.ProxyConfig.Filters.Data
	c.ProxyConfig.Filters.RUnlock()

	enabled := c.GetEnabledProvidersOrFallback()
	if len(enabled) == 0 {
		ctx.JSON(http.StatusOK, gin.H{
			"live":       []interface{}{},
			"vod":        []interface{}{},
			"series":     []interface{}{},
			"filters":    filtersData,
			"warning":    "No IPTV providers configured.",
		})
		return
	}

	type CatItem struct {
		ID   string `json:"category_id"`
		Name string `json:"category_name"`
	}

	var allLive []CatItem
	var allVod []CatItem
	var allSeries []CatItem
	var warnings []string

	for provIdx, prov := range enabled {
		client, err := getClientForProvider(prov)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", prov.Name, err))
			continue
		}

		prefix := ""
		if len(enabled) > 1 {
			prefix = fmt.Sprintf("[%s] ", prov.Name)
		}

		if live, err := client.GetLiveCategories(); err == nil {
			for _, cat := range live {
				catID := fmt.Sprint(cat.ID)
				if provIdx > 0 {
					if num, errP := strconv.Atoi(catID); errP == nil {
						catID = strconv.Itoa((provIdx * 100000) + num)
					}
				}
				allLive = append(allLive, CatItem{
					ID:   catID,
					Name: prefix + cat.Name,
				})
			}
		}

		if vod, err := client.GetVideoOnDemandCategories(); err == nil {
			for _, cat := range vod {
				catID := fmt.Sprint(cat.ID)
				if provIdx > 0 {
					if num, errP := strconv.Atoi(catID); errP == nil {
						catID = strconv.Itoa((provIdx * 100000) + num)
					}
				}
				allVod = append(allVod, CatItem{
					ID:   catID,
					Name: prefix + cat.Name,
				})
			}
		}

		if series, err := client.GetSeriesCategories(); err == nil {
			for _, cat := range series {
				catID := fmt.Sprint(cat.ID)
				if provIdx > 0 {
					if num, errP := strconv.Atoi(catID); errP == nil {
						catID = strconv.Itoa((provIdx * 100000) + num)
					}
				}
				allSeries = append(allSeries, CatItem{
					ID:   catID,
					Name: prefix + cat.Name,
				})
			}
		}
	}

	res := gin.H{
		"live":       allLive,
		"vod":        allVod,
		"series":     allSeries,
		"filters":    filtersData,
		"active_url": c.XtreamBaseURL,
	}
	if len(warnings) > 0 {
		res["warnings"] = warnings
	}

	ctx.JSON(http.StatusOK, res)
}

func (c *Config) adminGetProviders(ctx *gin.Context) {
	provs := c.ProxyConfig.Provider.GetProviders()
	if len(provs) == 0 {
		provs = c.GetEnabledProvidersOrFallback()
	}
	ctx.JSON(http.StatusOK, gin.H{
		"providers": provs,
	})
}

func (c *Config) adminSaveProviders(ctx *gin.Context) {
	var payload struct {
		Providers []config.ProviderItem `json:"providers"`
	}
	if err := ctx.BindJSON(&payload); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload: " + err.Error()})
		return
	}

	data := c.ProxyConfig.Provider.GetData()
	data.Providers = payload.Providers
	if err := c.ProxyConfig.Provider.Save(data); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Sync top-level active provider settings
	data = c.ProxyConfig.Provider.GetData()
	c.ProxyConfig.XtreamBaseURL = data.XtreamBaseURL
	c.ProxyConfig.BackupURLs = data.BackupURLs
	c.ProxyConfig.XtreamUser = config.CredentialString(data.XtreamUser)
	c.ProxyConfig.XtreamPassword = config.CredentialString(data.XtreamPassword)
	c.ProxyConfig.Referer = data.Referer
	c.ProxyConfig.UserAgent = data.UserAgent

	if data.XtreamBaseURL != "" {
		if u, err := url.Parse(data.XtreamBaseURL); err == nil {
			c.baseStreamURL = u
		}
	}

	// Clear caches
	if c.metadataCache != nil {
		c.metadataCache.Clear()
	}
	if c.xmltvCache != nil {
		c.xmltvCache.Clear()
	}
	xtreamM3uCacheLock.Lock()
	xtreamM3uCache = make(map[string]cacheMeta)
	xtreamM3uCacheLock.Unlock()

	ctx.JSON(http.StatusOK, gin.H{
		"status":    "success",
		"message":   "Providers saved and synced successfully",
		"providers": data.Providers,
	})
}

type providerSummaryResult struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Online    bool            `json:"online"`
	Results   []urlTestResult `json:"results"`
}

func (c *Config) adminTestProviders(ctx *gin.Context) {
	var payload struct {
		Providers []config.ProviderItem `json:"providers"`
	}
	_ = ctx.BindJSON(&payload)

	providersToTest := payload.Providers
	if len(providersToTest) == 0 {
		providersToTest = c.ProxyConfig.Provider.GetProviders()
	}
	if len(providersToTest) == 0 {
		providersToTest = c.GetEnabledProvidersOrFallback()
	}

	summaries := make([]providerSummaryResult, len(providersToTest))
	var wg sync.WaitGroup

	for i, prov := range providersToTest {
		wg.Add(1)
		go func(idx int, p config.ProviderItem) {
			defer wg.Done()

			var urlsToTest []string
			seen := make(map[string]bool)
			primaryClean := config.CleanURL(p.XtreamBaseURL)
			if primaryClean != "" {
				urlsToTest = append(urlsToTest, primaryClean)
				seen[primaryClean] = true
			}
			for _, b := range p.BackupURLs {
				cb := config.CleanURL(b)
				if cb != "" && !seen[cb] {
					urlsToTest = append(urlsToTest, cb)
					seen[cb] = true
				}
			}

			results := make([]urlTestResult, len(urlsToTest))
			var urlWg sync.WaitGroup
			for uIdx, targetURL := range urlsToTest {
				urlWg.Add(1)
				go func(resIdx int, tURL string) {
					defer urlWg.Done()
					start := time.Now()
					ref := p.Referer
					if ref == "" {
						ref = tURL
					}
					ua := p.UserAgent
					if ua == "" {
						ua = c.GetUpstreamUserAgent()
					}

					client, err := xtreamapi.New(p.XtreamUser, p.XtreamPassword, tURL, ua, ref)
					latency := time.Since(start).Milliseconds()

					if err != nil {
						results[resIdx] = urlTestResult{
							URL:       tURL,
							Online:    false,
							LatencyMs: latency,
							Message:   fmt.Sprintf("Failed: %v", err),
						}
						return
					}

					liveCats, err := client.GetLiveCategories()
					if err != nil {
						results[resIdx] = urlTestResult{
							URL:       tURL,
							Online:    false,
							LatencyMs: latency,
							Message:   fmt.Sprintf("Auth ok, category fetch error: %v", err),
						}
						return
					}

					expDateStr := ""
					if client.UserInfo.ExpDate != nil && !client.UserInfo.ExpDate.IsZero() {
						expDateStr = client.UserInfo.ExpDate.Time.Format("2006-01-02")
					}
					maxConnStr := fmt.Sprintf("%d", client.UserInfo.MaxConnections)

					results[resIdx] = urlTestResult{
						URL:             tURL,
						Online:          true,
						LatencyMs:       latency,
						CategoriesCount: len(liveCats),
						Message:         fmt.Sprintf("Online! %d live categories", len(liveCats)),
						Status:          client.UserInfo.Status,
						ExpDate:         expDateStr,
						MaxConnections:  maxConnStr,
						ServerURL:       client.ServerInfo.URL,
					}
				}(uIdx, targetURL)
			}
			urlWg.Wait()

			provOnline := false
			for _, r := range results {
				if r.Online {
					provOnline = true
					break
				}
			}

			summaries[idx] = providerSummaryResult{
				ID:      p.ID,
				Name:    p.Name,
				Online:  provOnline,
				Results: results,
			}
		}(i, prov)
	}

	wg.Wait()

	ctx.JSON(http.StatusOK, gin.H{
		"providers": summaries,
	})
}

func (c *Config) adminSaveFilters(ctx *gin.Context) {
	var payload config.FilterData
	if err := ctx.BindJSON(&payload); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload"})
		return
	}

	if err := c.ProxyConfig.Filters.Save(payload); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (c *Config) adminGetProvider(ctx *gin.Context) {
	data := c.ProxyConfig.Provider.GetData()
	ctx.JSON(http.StatusOK, gin.H{
		"xtream_base_url": data.XtreamBaseURL,
		"backup_urls":     data.BackupURLs,
		"xtream_user":     data.XtreamUser,
		"xtream_password": data.XtreamPassword,
		"referer":         data.Referer,
		"user_agent":      data.UserAgent,
	})
}

func (c *Config) adminSaveProvider(ctx *gin.Context) {
	var payload config.ProviderData
	if err := ctx.BindJSON(&payload); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload"})
		return
	}

	payload.XtreamBaseURL = strings.TrimRight(strings.TrimSpace(payload.XtreamBaseURL), "/")
	payload.XtreamUser = strings.TrimSpace(payload.XtreamUser)
	payload.XtreamPassword = strings.TrimSpace(payload.XtreamPassword)

	// Clean backup URLs
	var cleanedBackups []string
	seen := make(map[string]bool)
	seen[payload.XtreamBaseURL] = true

	for _, u := range payload.BackupURLs {
		cu := strings.TrimRight(strings.TrimSpace(u), "/")
		if cu != "" && !seen[cu] {
			seen[cu] = true
			cleanedBackups = append(cleanedBackups, cu)
		}
	}
	payload.BackupURLs = cleanedBackups

	// Automatically use provider URL as referer if referer is empty
	payload.Referer = strings.TrimRight(strings.TrimSpace(payload.Referer), "/")
	if payload.Referer == "" && payload.XtreamBaseURL != "" {
		payload.Referer = payload.XtreamBaseURL
	}

	payload.UserAgent = strings.TrimSpace(payload.UserAgent)
	if payload.UserAgent == "" {
		payload.UserAgent = config.DefaultUserAgent
	}

	if len(payload.Providers) == 0 {
		existing := c.ProxyConfig.Provider.GetProviders()
		if len(existing) > 0 {
			existing[0].XtreamBaseURL = payload.XtreamBaseURL
			existing[0].BackupURLs = payload.BackupURLs
			existing[0].XtreamUser = payload.XtreamUser
			existing[0].XtreamPassword = payload.XtreamPassword
			existing[0].Referer = payload.Referer
			existing[0].UserAgent = payload.UserAgent
			payload.Providers = existing
		}
	}

	if err := c.ProxyConfig.Provider.Save(payload); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.ProxyConfig.XtreamBaseURL = payload.XtreamBaseURL
	c.ProxyConfig.BackupURLs = payload.BackupURLs
	c.ProxyConfig.XtreamUser = config.CredentialString(payload.XtreamUser)
	c.ProxyConfig.XtreamPassword = config.CredentialString(payload.XtreamPassword)
	c.ProxyConfig.Referer = payload.Referer
	c.ProxyConfig.UserAgent = payload.UserAgent

	if payload.XtreamBaseURL != "" {
		if u, err := url.Parse(payload.XtreamBaseURL); err == nil {
			c.baseStreamURL = u
		}
	}

	// Clear metadata and xmltv caches
	if c.metadataCache != nil {
		c.metadataCache.Clear()
	}
	if c.xmltvCache != nil {
		c.xmltvCache.Clear()
	}

	ctx.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Provider settings and backup URLs saved successfully",
	})
}

type urlTestResult struct {
	URL             string `json:"url"`
	Online          bool   `json:"online"`
	LatencyMs       int64  `json:"latency_ms"`
	Message         string `json:"message"`
	CategoriesCount int    `json:"categories_count,omitempty"`
	Status          string `json:"status,omitempty"`
	ExpDate         string `json:"exp_date,omitempty"`
	MaxConnections  string `json:"max_connections,omitempty"`
	ServerURL       string `json:"server_url,omitempty"`
}

func (c *Config) adminTestProvider(ctx *gin.Context) {
	var payload config.ProviderData
	if err := ctx.BindJSON(&payload); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload"})
		return
	}

	primaryURL := config.CleanURL(payload.XtreamBaseURL)
	user := strings.TrimSpace(payload.XtreamUser)
	pass := strings.TrimSpace(payload.XtreamPassword)

	if primaryURL == "" || user == "" || pass == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Provider URL, username, and password are required"})
		return
	}

	// Gather unique list of all URLs to test (Primary + Backups)
	var urlsToTest []string
	seen := make(map[string]bool)

	if primaryURL != "" {
		urlsToTest = append(urlsToTest, primaryURL)
		seen[primaryURL] = true
	}
	for _, b := range payload.BackupURLs {
		cb := config.CleanURL(b)
		if cb != "" && !seen[cb] {
			urlsToTest = append(urlsToTest, cb)
			seen[cb] = true
		}
	}

	results := make([]urlTestResult, len(urlsToTest))
	var wg sync.WaitGroup

	for i, testURL := range urlsToTest {
		wg.Add(1)
		go func(idx int, targetURL string) {
			defer wg.Done()

			start := time.Now()
			ref := targetURL
			if payload.Referer != "" && payload.Referer != payload.XtreamBaseURL {
				ref = payload.Referer
			}

			testUA := strings.TrimSpace(payload.UserAgent)
			if testUA == "" {
				testUA = c.GetUpstreamUserAgent()
			}

			client, err := xtreamapi.New(user, pass, targetURL, testUA, ref)
			latency := time.Since(start).Milliseconds()

			if err != nil {
				results[idx] = urlTestResult{
					URL:       targetURL,
					Online:    false,
					LatencyMs: latency,
					Message:   fmt.Sprintf("Connection failed: %v", err),
				}
				return
			}

			liveCats, err := client.GetLiveCategories()
			if err != nil {
				results[idx] = urlTestResult{
					URL:       targetURL,
					Online:    false,
					LatencyMs: latency,
					Message:   fmt.Sprintf("Auth ok, but category fetch failed: %v", err),
				}
				return
			}

			expDateStr := ""
			if client.UserInfo.ExpDate != nil && !client.UserInfo.ExpDate.IsZero() {
				expDateStr = client.UserInfo.ExpDate.Time.Format("2006-01-02")
			}
			maxConnStr := fmt.Sprintf("%d", client.UserInfo.MaxConnections)

			results[idx] = urlTestResult{
				URL:             targetURL,
				Online:          true,
				LatencyMs:       latency,
				CategoriesCount: len(liveCats),
				Message:         fmt.Sprintf("Online! Found %d live categories.", len(liveCats)),
				Status:          client.UserInfo.Status,
				ExpDate:         expDateStr,
				MaxConnections:  maxConnStr,
				ServerURL:       client.ServerInfo.URL,
			}
		}(i, testURL)
	}

	wg.Wait()

	// Overall success if at least primary or one URL works
	anyOnline := false
	for _, r := range results {
		if r.Online {
			anyOnline = true
			break
		}
	}

	ctx.JSON(http.StatusOK, gin.H{
		"success":    anyOnline,
		"active_url": primaryURL,
		"results":    results,
	})
}

func (c *Config) adminRotateProvider(ctx *gin.Context) {
	var body struct {
		URL string `json:"url"`
	}
	ctx.BindJSON(&body)

	target := config.CleanURL(body.URL)
	if target != "" {
		c.RotateToURL(target)
		ctx.JSON(http.StatusOK, gin.H{
			"status":     "success",
			"active_url": c.XtreamBaseURL,
			"message":    fmt.Sprintf("Switched active provider to %s", c.XtreamBaseURL),
		})
		return
	}

	// If no specific URL requested, rotate to next
	nextURL, rotated := c.ProxyConfig.Provider.RotateToNext()
	if !rotated {
		ctx.JSON(http.StatusOK, gin.H{
			"status":     "warning",
			"active_url": c.XtreamBaseURL,
			"message":    "No other backup URLs configured in pool",
		})
		return
	}

	c.RotateToURL(nextURL)
	ctx.JSON(http.StatusOK, gin.H{
		"status":     "success",
		"active_url": c.XtreamBaseURL,
		"message":    fmt.Sprintf("Rotated active provider to %s", c.XtreamBaseURL),
	})
}
