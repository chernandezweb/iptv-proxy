package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

var serverStartTime = time.Now().UTC().Format(time.RFC3339Nano)

type AdminCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c *Config) getAdminCredentials() AdminCredentials {
	if c.adminAuthPath != "" {
		if data, err := ioutil.ReadFile(c.adminAuthPath); err == nil {
			var creds AdminCredentials
			if json.Unmarshal(data, &creds) == nil && creds.Username != "" && creds.Password != "" {
				return creds
			}
		}
	}
	user := c.User.String()
	pass := c.Password.String()
	if user == "" {
		user = "admin"
	}
	if pass == "" {
		pass = "admin"
	}
	return AdminCredentials{Username: user, Password: pass}
}

func (c *Config) adminRoutes(r *gin.RouterGroup) {
	admin := r.Group("/admin")
	admin.Use(func(ctx *gin.Context) {
		user, pass, hasAuth := ctx.Request.BasicAuth()
		if !hasAuth {
			ctx.Header("WWW-Authenticate", `Basic realm="IPTV Proxy Admin"`)
			ctx.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		// 1. Check dedicated admin credentials
		creds := c.getAdminCredentials()
		if user == creds.Username && pass == creds.Password {
			ctx.Set("admin_user", user)
			ctx.Next()
			return
		}

		// 2. Also allow any player user in userManager
		if c.userManager != nil {
			if u, ok := c.userManager.Authenticate(user, pass); ok && u.Enabled {
				ctx.Set("admin_user", u.Username)
				ctx.Next()
				return
			}
		}

		ctx.Header("WWW-Authenticate", `Basic realm="IPTV Proxy Admin"`)
		ctx.AbortWithStatus(http.StatusUnauthorized)
	})

	// Admin Authentication Management
	admin.GET("/api/admin-auth", c.adminGetAdminAuth)
	admin.POST("/api/admin-auth", c.adminSaveAdminAuth)

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
	admin.GET("/api/users", c.adminGetUsers)
	admin.POST("/api/users", c.adminSaveUsers)
	admin.DELETE("/api/users/:id", c.adminDeleteUser)
	admin.GET("/api/streams", c.adminGetStreams)
	admin.GET("/api/vpn-proxy", c.adminGetVpnProxy)
	admin.POST("/api/vpn-proxy", c.adminSaveVpnProxy)
	admin.POST("/api/vpn-proxy/test", c.adminTestVpnProxy)
	admin.GET("/api/version", c.adminGetVersion)
	admin.POST("/api/update", c.adminTriggerUpdate)
	admin.GET("/api/logs", c.adminGetLogs)

	// Static files from embedded FS
	adminHandler := func(ctx *gin.Context) {
		html, _ := webFS.ReadFile("web/admin.html")
		ctx.Data(http.StatusOK, "text/html; charset=utf-8", html)
	}
	admin.GET("", adminHandler)
	admin.GET("/", adminHandler)
	admin.HEAD("", adminHandler)
	admin.HEAD("/", adminHandler)
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
	if provs == nil {
		provs = []config.ProviderItem{}
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
				go func(resIdx int, tURL string, delayMs int) {
					defer urlWg.Done()
					if delayMs > 0 {
						time.Sleep(time.Duration(delayMs) * time.Millisecond)
					}
					start := time.Now()
					ref := p.Referer
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
				}(uIdx, targetURL, uIdx*200)
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

	// Invalidate category caches and metadata caches so new filters apply immediately
	clearAllClientCaches()
	if c.metadataCache != nil {
		c.metadataCache.Clear()
	}
	if c.xmltvCache != nil {
		c.xmltvCache.Clear()
	}
	xtreamM3uCacheLock.Lock()
	xtreamM3uCache = make(map[string]cacheMeta)
	xtreamM3uCacheLock.Unlock()

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

// AdminUserView provides user info along with live active connection stats.
type AdminUserView struct {
	config.UserItem
	ActiveConnections int               `json:"active_connections"`
	ActiveSlots       []config.UserSlot `json:"active_slots"`
}

func (c *Config) adminGetUsers(ctx *gin.Context) {
	if c.userManager == nil {
		ctx.JSON(http.StatusOK, gin.H{"users": []AdminUserView{}})
		return
	}

	users := c.userManager.GetUsers()
	allSlots := c.userManager.GetAllActiveSlots()
	result := make([]AdminUserView, len(users))

	for i, u := range users {
		slots := allSlots[u.Username]
		if slots == nil {
			slots = []config.UserSlot{}
		}
		result[i] = AdminUserView{
			UserItem:          u,
			ActiveConnections: len(slots),
			ActiveSlots:       slots,
		}
	}

	ctx.JSON(http.StatusOK, gin.H{
		"users":        result,
		"default_user": c.User.String(),
	})
}

func (c *Config) adminSaveUsers(ctx *gin.Context) {
	if c.userManager == nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "user manager not initialized"})
		return
	}

	bodyBytes, err := ioutil.ReadAll(ctx.Request.Body)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// Try single user
	var single config.UserItem
	if err := json.Unmarshal(bodyBytes, &single); err == nil && single.Username != "" {
		users := c.userManager.GetUsers()
		updated := false

		if single.ID != "" {
			for i, u := range users {
				if u.ID == single.ID {
					users[i].Username = single.Username
					if single.Password != "" {
						users[i].Password = single.Password
					}
					users[i].MaxConnections = single.MaxConnections
					users[i].Enabled = single.Enabled
					updated = true
					break
				}
			}
		}

		if !updated {
			for _, u := range users {
				if u.Username == single.Username {
					ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Username '%s' already exists", single.Username)})
					return
				}
			}
			if single.ID == "" {
				single.ID = fmt.Sprintf("user_%d", time.Now().UnixNano())
			}
			if single.CreatedAt.IsZero() {
				single.CreatedAt = time.Now()
			}
			users = append(users, single)
		}

		if err := c.userManager.Save(users); err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"status": "success", "message": "User saved successfully"})
		return
	}

	// Try list of users
	var list []config.UserItem
	if err := json.Unmarshal(bodyBytes, &list); err == nil {
		if err := c.userManager.Save(list); err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"status": "success", "message": "Users saved successfully"})
		return
	}

	ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON format"})
}

func (c *Config) adminDeleteUser(ctx *gin.Context) {
	if c.userManager == nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "user manager not initialized"})
		return
	}
	id := ctx.Param("id")
	if id == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	users := c.userManager.GetUsers()
	filtered := make([]config.UserItem, 0, len(users))
	found := false
	for _, u := range users {
		if u.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, u)
	}

	if !found {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if err := c.userManager.Save(filtered); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"status": "success", "message": "User deleted successfully"})
}

type ActiveStreamSession struct {
	Username  string    `json:"username"`
	IP        string    `json:"ip"`
	StreamID  string    `json:"stream_id"`
	StreamURL string    `json:"stream_url"`
	StartedAt time.Time `json:"started_at"`
	Duration  string    `json:"duration"`
}

func (c *Config) adminGetStreams(ctx *gin.Context) {
	var sessions []ActiveStreamSession
	if c.userManager != nil {
		allSlots := c.userManager.GetAllActiveSlots()
		for user, slots := range allSlots {
			for _, sl := range slots {
				dur := time.Since(sl.StartedAt).Truncate(time.Second).String()
				sessions = append(sessions, ActiveStreamSession{
					Username:  user,
					IP:        sl.IP,
					StreamID:  sl.StreamID,
					StreamURL: sl.StreamURL,
					StartedAt: sl.StartedAt,
					Duration:  dur,
				})
			}
		}
	}
	if sessions == nil {
		sessions = []ActiveStreamSession{}
	}

	ctx.JSON(http.StatusOK, gin.H{
		"streams":       sessions,
		"total_streams": len(sessions),
	})
}

func (c *Config) adminGetVpnProxy(ctx *gin.Context) {
	socks := c.ProxyConfig.Provider.GetSocksProxy()
	activeURL := socks.ProxyURL()
	ctx.JSON(http.StatusOK, gin.H{
		"settings":   socks,
		"active_url": activeURL,
		"active":     socks.Enabled && activeURL != "",
	})
}

func (c *Config) adminSaveVpnProxy(ctx *gin.Context) {
	var payload config.UpstreamProxySettings
	if err := ctx.BindJSON(&payload); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload: " + err.Error()})
		return
	}

	if err := c.ProxyConfig.Provider.SetSocksProxy(payload); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.ApplyProxySettings(payload)

	ctx.JSON(http.StatusOK, gin.H{
		"status":     "success",
		"message":    "VPN / SOCKS5 proxy settings saved and applied successfully",
		"settings":   payload,
		"active_url": payload.ProxyURL(),
		"active":     payload.Enabled && payload.ProxyURL() != "",
	})
}

func (c *Config) adminTestVpnProxy(ctx *gin.Context) {
	var payload config.UpstreamProxySettings
	if err := ctx.BindJSON(&payload); err != nil {
		payload = c.ProxyConfig.Provider.GetSocksProxy()
	}

	testURLStr := payload.ProxyURL()
	if !payload.Enabled || testURLStr == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"online":  false,
			"error":   "Proxy is disabled or missing host/URL",
			"latency": 0,
		})
		return
	}

	testURL, err := url.Parse(testURLStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"online":  false,
			"error":   "Invalid proxy URL: " + err.Error(),
			"latency": 0,
		})
		return
	}

	testClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(testURL),
			DialContext: (&net.Dialer{
				Timeout:   8 * time.Second,
				KeepAlive: 15 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: 8 * time.Second,
		},
		Timeout: 10 * time.Second,
	}

	start := time.Now()
	resp, err := testClient.Get("https://api.ipify.org?format=json")
	if err != nil {
		ctx.JSON(http.StatusOK, gin.H{
			"online":  false,
			"error":   "Connection failed: " + err.Error(),
			"latency": 0,
		})
		return
	}
	defer resp.Body.Close()
	latencyMs := time.Since(start).Milliseconds()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		ctx.JSON(http.StatusOK, gin.H{
			"online":  false,
			"error":   "Failed reading response: " + err.Error(),
			"latency": latencyMs,
		})
		return
	}

	var ipInfo struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &ipInfo); err != nil || ipInfo.IP == "" {
		ipInfo.IP = strings.TrimSpace(string(body))
	}

	ctx.JSON(http.StatusOK, gin.H{
		"online":   true,
		"ip":       ipInfo.IP,
		"latency":  latencyMs,
		"message":  fmt.Sprintf("Proxy connected successfully! Outbound IP: %s (%dms)", ipInfo.IP, latencyMs),
	})
}

type CommitSummary struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Date    string `json:"date"`
}

func getCurrentCommit() string {
	// 1. Check container root /app_commit.txt
	if data, err := ioutil.ReadFile("/app_commit.txt"); err == nil {
		s := strings.TrimSpace(string(data))
		if len(s) >= 7 && !strings.Contains(s, " ") && !strings.Contains(s, "ref:") {
			return s[:7]
		}
	}
	// 2. Check /data/commit.txt (synced runtime data)
	if data, err := ioutil.ReadFile("/data/commit.txt"); err == nil {
		s := strings.TrimSpace(string(data))
		if len(s) >= 7 && !strings.Contains(s, " ") && !strings.Contains(s, "ref:") {
			return s[:7]
		}
	}
	// 3. Check host-mounted repo paths
	for _, p := range []string{"/host/root/iptv-proxy", "/host/home/iptv-proxy"} {
		gitRef := filepath.Join(p, ".git", "refs", "heads", "master")
		if data, err := ioutil.ReadFile(gitRef); err == nil {
			s := strings.TrimSpace(string(data))
			if len(s) >= 7 {
				return s[:7]
			}
		}
	}
	return "c08aa74" // Fallback to current build
}

func (c *Config) adminGetVersion(ctx *gin.Context) {
	client := &http.Client{Timeout: 6 * time.Second}
	req, _ := http.NewRequestWithContext(ctx.Request.Context(), "GET", "https://api.github.com/repos/chernandezweb/iptv-proxy/commits?per_page=15", nil)
	req.Header.Set("User-Agent", "iptv-proxy")

	type ghCommitItem struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}

	var ghList []ghCommitItem
	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(&ghList)
	}

	currentCommit := getCurrentCommit()
	latestCommit := currentCommit
	latestMessage := ""
	latestDate := ""
	var pendingCommits []CommitSummary

	if len(ghList) > 0 {
		latestSHA := ghList[0].SHA
		if len(latestSHA) >= 7 {
			latestCommit = latestSHA[:7]
		}
		latestMessage = strings.Split(ghList[0].Commit.Message, "\n")[0]
		latestDate = ghList[0].Commit.Author.Date

		// Check if currentCommit matches any commit in the list
		foundIdx := -1
		for idx, item := range ghList {
			short := item.SHA
			if len(short) >= 7 {
				short = short[:7]
			}
			if strings.HasPrefix(item.SHA, currentCommit) || strings.HasPrefix(currentCommit, short) {
				foundIdx = idx
				break
			}
		}

		if foundIdx == 0 {
			// Already on latest commit
			pendingCommits = []CommitSummary{}
		} else if foundIdx > 0 {
			// Found currentCommit; collect all pending commits ahead of it
			for i := 0; i < foundIdx; i++ {
				cSHA := ghList[i].SHA
				if len(cSHA) >= 7 {
					cSHA = cSHA[:7]
				}
				msg := strings.Split(ghList[i].Commit.Message, "\n")[0]
				pendingCommits = append(pendingCommits, CommitSummary{
					SHA:     cSHA,
					Message: msg,
					Date:    ghList[i].Commit.Author.Date,
				})
			}
		} else {
			// Current commit is older than last 15 commits; list all of them
			for _, item := range ghList {
				cSHA := item.SHA
				if len(cSHA) >= 7 {
					cSHA = cSHA[:7]
				}
				msg := strings.Split(item.Commit.Message, "\n")[0]
				pendingCommits = append(pendingCommits, CommitSummary{
					SHA:     cSHA,
					Message: msg,
					Date:    item.Commit.Author.Date,
				})
			}
		}
	}

	isUpToDate := currentCommit != "" && latestCommit != "" && currentCommit == latestCommit

	ctx.JSON(http.StatusOK, gin.H{
		"boot_time":       serverStartTime,
		"current_commit":  currentCommit,
		"latest_commit":   latestCommit,
		"latest_message":  latestMessage,
		"latest_date":     latestDate,
		"pending_commits": pendingCommits,
		"behind_count":    len(pendingCommits),
		"is_up_to_date":   isUpToDate,
		"update_command":  "cd ~/iptv-proxy && git pull origin master && docker compose up -d --build",
		"docker_socket":   dockerSocketExists(),
	})
}

func dockerSocketExists() bool {
	_, err := os.Stat("/var/run/docker.sock")
	return err == nil
}

// getDockerAPIBase dynamically negotiates the Docker API version via /version (defaulting to v1.44+)
func getDockerAPIBase(client *http.Client) string {
	resp, err := client.Get("http://localhost/version")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var ver struct {
				ApiVersion string `json:"ApiVersion"`
			}
			if json.NewDecoder(resp.Body).Decode(&ver) == nil && ver.ApiVersion != "" {
				return "http://localhost/v" + ver.ApiVersion
			}
		}
	}
	return "http://localhost/v1.44"
}

func (c *Config) adminTriggerUpdate(ctx *gin.Context) {
	if !dockerSocketExists() {
		ctx.JSON(http.StatusOK, gin.H{
			"supported": false,
			"error":     "Docker socket (/var/run/docker.sock) is not mounted into container. Add '/var/run/docker.sock:/var/run/docker.sock' to volumes in docker-compose.yml to enable 1-click update.",
			"command":   "cd ~/iptv-proxy && git pull origin master && docker compose up -d --build",
		})
		return
	}

	tr := &http.Transport{
		DialContext: func(ctx context.Context, proto, addr string) (net.Conn, error) {
			return net.Dial("unix", "/var/run/docker.sock")
		},
	}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	apiBase := getDockerAPIBase(client)

	// Clean up any stale updater container from earlier runs
	delReq, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/containers/iptv_proxy_updater?force=true", apiBase), nil)
	if delResp, err := client.Do(delReq); err == nil {
		delResp.Body.Close()
	}

	// Discover host repo path and current container image by inspecting running container
	var repoCandidates []string
	var imageToUse string

	containerCandidates := []string{"iptv-proxy"}
	if h, err := os.Hostname(); err == nil && h != "" {
		containerCandidates = append([]string{h}, containerCandidates...)
	}

	for _, name := range containerCandidates {
		inspResp, err := client.Get(fmt.Sprintf("%s/containers/%s/json", apiBase, name))
		if err == nil && inspResp.StatusCode == http.StatusOK {
			var insp struct {
				Image  string `json:"Image"`
				Config struct {
					Image string `json:"Image"`
				} `json:"Config"`
				Mounts []struct {
					Source      string `json:"Source"`
					Destination string `json:"Destination"`
				} `json:"Mounts"`
			}
			if json.NewDecoder(inspResp.Body).Decode(&insp) == nil {
				if insp.Config.Image != "" {
					imageToUse = insp.Config.Image
				} else if insp.Image != "" {
					imageToUse = insp.Image
				}
				for _, m := range insp.Mounts {
					if m.Destination == "/data" && m.Source != "" {
						dir := filepath.Dir(m.Source)
						if dir != "" && dir != "/" {
							repoCandidates = append(repoCandidates, dir)
						}
						break
					}
				}
			}
			inspResp.Body.Close()
			if imageToUse != "" {
				break
			}
		}
		if inspResp != nil {
			inspResp.Body.Close()
		}
	}

	if imageToUse == "" {
		imageToUse = "alpine:latest"
	}

	repoCandidates = append(repoCandidates,
		"/root/iptv-proxy",
		"~/iptv-proxy",
		"/home/*/iptv-proxy",
		"/var/www/iptv-proxy",
		"/opt/iptv-proxy",
	)

	searchPaths := strings.Join(repoCandidates, " ")

	script := fmt.Sprintf(`export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin:$PATH
for p in %s; do
  if [ -d "$p/.git" ]; then
    cd "$p" || continue
    git config --global --add safe.directory "$p" 2>/dev/null || true
    git fetch origin master 2>/dev/null || true
    git reset --hard origin/master 2>/dev/null || git pull origin master || true
    if [ -d "$p/data" ]; then
      git rev-parse HEAD > "$p/data/commit.txt" 2>/dev/null || true
    fi
    if command -v docker >/dev/null 2>&1; then
      docker compose up -d --build || docker-compose up -d --build
    elif command -v docker-compose >/dev/null 2>&1; then
      docker-compose up -d --build
    fi
    exit 0
  fi
done
exit 1`, searchPaths)

	createReq := map[string]interface{}{
		"Image":      imageToUse,
		"Entrypoint": []string{"sh", "-c"},
		"Cmd": []string{
			fmt.Sprintf("chroot /host sh -c %s", strconv.Quote(script)),
		},
		"HostConfig": map[string]interface{}{
			"Binds": []string{
				"/:/host:rw",
				"/var/run/docker.sock:/var/run/docker.sock",
			},
			"Privileged": true,
			"AutoRemove": true,
		},
	}

	bodyBytes, _ := json.Marshal(createReq)
	resp, err := client.Post(fmt.Sprintf("%s/containers/create?name=iptv_proxy_updater", apiBase), "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"supported": false,
			"error":     "Failed to contact Docker socket: " + err.Error(),
			"command":   "cd ~/iptv-proxy && git pull origin master && docker compose up -d --build",
		})
		return
	}
	defer resp.Body.Close()

	// If image is not present in local cache, pull it and retry
	if resp.StatusCode == http.StatusNotFound {
		pullURL := fmt.Sprintf("%s/images/create?fromImage=alpine&tag=latest", apiBase)
		if strings.Contains(imageToUse, ":") {
			parts := strings.SplitN(imageToUse, ":", 2)
			pullURL = fmt.Sprintf("%s/images/create?fromImage=%s&tag=%s", apiBase, url.QueryEscape(parts[0]), url.QueryEscape(parts[1]))
		}
		pullResp, pErr := client.Post(pullURL, "application/json", nil)
		if pErr == nil {
			io.Copy(ioutil.Discard, pullResp.Body)
			pullResp.Body.Close()
			resp2, err2 := client.Post(fmt.Sprintf("%s/containers/create?name=iptv_proxy_updater", apiBase), "application/json", bytes.NewReader(bodyBytes))
			if err2 == nil {
				resp = resp2
				defer resp.Body.Close()
			}
		}
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := ioutil.ReadAll(resp.Body)
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"supported": false,
			"error":     fmt.Sprintf("Docker returned HTTP %d: %s", resp.StatusCode, string(respBody)),
			"command":   "cd ~/iptv-proxy && git pull origin master && docker compose up -d --build",
		})
		return
	}

	var createResp struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil || createResp.ID == "" {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"supported": false,
			"error":     "Failed parsing container ID from Docker response",
			"command":   "cd ~/iptv-proxy && git pull origin master && docker compose up -d --build",
		})
		return
	}

	// Start the updater container
	startResp, err := client.Post(fmt.Sprintf("%s/containers/%s/start", apiBase, createResp.ID), "application/json", nil)
	if err != nil || (startResp.StatusCode != http.StatusOK && startResp.StatusCode != http.StatusNoContent) {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"supported": false,
			"error":     "Created updater container but failed to start it",
			"command":   "cd ~/iptv-proxy && git pull origin master && docker compose up -d --build",
		})
		return
	}
	defer startResp.Body.Close()

	ctx.JSON(http.StatusOK, gin.H{
		"supported": true,
		"status":    "started",
		"message":   "1-Click update started successfully! Pulling latest code and rebuilding container...",
	})
}

func (c *Config) adminGetAdminAuth(ctx *gin.Context) {
	creds := c.getAdminCredentials()
	ctx.JSON(http.StatusOK, gin.H{
		"username": creds.Username,
	})
}

func (c *Config) adminSaveAdminAuth(ctx *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Username) == "" || strings.TrimSpace(req.Password) == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Username and password cannot be empty"})
		return
	}

	creds := AdminCredentials{
		Username: strings.TrimSpace(req.Username),
		Password: strings.TrimSpace(req.Password),
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed encoding admin credentials"})
		return
	}

	if err := ioutil.WriteFile(c.adminAuthPath, data, 0600); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed writing admin credentials: " + err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"success":  true,
		"username": creds.Username,
		"message":  "Admin credentials updated successfully! Use your new username and password next time you log in.",
	})
}

func (c *Config) adminGetLogs(ctx *gin.Context) {
	tailStr := ctx.DefaultQuery("tail", "100")
	tail, err := strconv.Atoi(tailStr)
	if err != nil || tail <= 0 {
		tail = 100
	}
	if tail > 1000 {
		tail = 1000
	}

	if dockerSocketExists() {
		tr := &http.Transport{
			DialContext: func(ctx context.Context, proto, addr string) (net.Conn, error) {
				return net.Dial("unix", "/var/run/docker.sock")
			},
		}
		client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
		apiBase := getDockerAPIBase(client)

		candidates := []string{"iptv-proxy"}
		if h, err := os.Hostname(); err == nil && h != "" {
			candidates = append([]string{h}, candidates...)
		}

		for _, name := range candidates {
			u := fmt.Sprintf("%s/containers/%s/logs?stdout=true&stderr=true&tail=%d&timestamps=false", apiBase, name, tail)
			resp, err := client.Get(u)
			if err == nil && resp.StatusCode == http.StatusOK {
				body, _ := ioutil.ReadAll(resp.Body)
				resp.Body.Close()
				parsed := parseDockerLogs(body)
				lines := strings.Split(strings.TrimSpace(parsed), "\n")
				ctx.JSON(http.StatusOK, gin.H{
					"source": "docker",
					"tail":   tail,
					"lines":  lines,
					"raw":    parsed,
				})
				return
			}
			if resp != nil {
				resp.Body.Close()
			}
		}
	}

	// Fallback to internal in-memory ring buffer
	lines := globalLogBuffer.GetTail(tail)
	ctx.JSON(http.StatusOK, gin.H{
		"source": "memory",
		"tail":   tail,
		"lines":  lines,
		"raw":    strings.Join(lines, "\n"),
	})
}

func parseDockerLogs(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var out bytes.Buffer
	r := bytes.NewReader(body)
	hdr := make([]byte, 8)
	demuxed := false

	for {
		if r.Len() < 8 {
			break
		}
		_, err := io.ReadFull(r, hdr)
		if err != nil {
			break
		}
		// Docker stream multiplex header byte 0: 1=stdout, 2=stderr
		if (hdr[0] == 1 || hdr[0] == 2) && hdr[1] == 0 && hdr[2] == 0 && hdr[3] == 0 {
			demuxed = true
			size := binary.BigEndian.Uint32(hdr[4:8])
			if int(size) > r.Len() {
				io.Copy(&out, r)
				break
			}
			chunk := make([]byte, size)
			r.Read(chunk)
			out.Write(chunk)
		} else {
			break
		}
	}

	if demuxed && out.Len() > 0 {
		return out.String()
	}

	var clean bytes.Buffer
	for _, b := range body {
		if b >= 32 || b == '\n' || b == '\t' || b == '\r' || b == 27 {
			clean.WriteByte(b)
		}
	}
	return clean.String()
}



