package server

import (
	"embed"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	admin.GET("/api/provider", c.adminGetProvider)
	admin.POST("/api/provider", c.adminSaveProvider)
	admin.POST("/api/provider/test", c.adminTestProvider)

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

	if c.XtreamBaseURL == "" || c.XtreamUser.String() == "" {
		ctx.JSON(http.StatusOK, gin.H{
			"live":    []interface{}{},
			"vod":     []interface{}{},
			"series":  []interface{}{},
			"filters": filtersData,
			"warning": "Provider settings are not configured. Please configure them in the Provider Settings tab.",
		})
		return
	}

	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent(), c.Referer)
	if err != nil {
		ctx.JSON(http.StatusOK, gin.H{
			"live":    []interface{}{},
			"vod":     []interface{}{},
			"series":  []interface{}{},
			"filters": filtersData,
			"error":   err.Error(),
		})
		return
	}

	live, _ := client.GetLiveCategories()
	vod, _ := client.GetVideoOnDemandCategories()
	series, _ := client.GetSeriesCategories()

	ctx.JSON(http.StatusOK, gin.H{
		"live":    live,
		"vod":     vod,
		"series":  series,
		"filters": filtersData,
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
		"xtream_base_url":  data.XtreamBaseURL,
		"xtream_user":     data.XtreamUser,
		"xtream_password": data.XtreamPassword,
		"referer":         data.Referer,
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
	payload.Referer = strings.TrimRight(strings.TrimSpace(payload.Referer), "/")

	if err := c.ProxyConfig.Provider.Save(payload); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.ProxyConfig.XtreamBaseURL = payload.XtreamBaseURL
	c.ProxyConfig.XtreamUser = config.CredentialString(payload.XtreamUser)
	c.ProxyConfig.XtreamPassword = config.CredentialString(payload.XtreamPassword)
	c.ProxyConfig.Referer = payload.Referer

	if payload.XtreamBaseURL != "" {
		if u, err := url.Parse(payload.XtreamBaseURL); err == nil {
			c.baseStreamURL = u
		}
	}

	// Clear metadata and xmltv caches so fresh data from the updated provider is loaded
	if c.metadataCache != nil {
		c.metadataCache.Clear()
	}
	if c.xmltvCache != nil {
		c.xmltvCache.Clear()
	}

	ctx.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Provider settings saved successfully",
	})
}

func (c *Config) adminTestProvider(ctx *gin.Context) {
	var payload config.ProviderData
	if err := ctx.BindJSON(&payload); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload"})
		return
	}

	baseURL := strings.TrimRight(strings.TrimSpace(payload.XtreamBaseURL), "/")
	user := strings.TrimSpace(payload.XtreamUser)
	pass := strings.TrimSpace(payload.XtreamPassword)
	ref := strings.TrimRight(strings.TrimSpace(payload.Referer), "/")

	if baseURL == "" || user == "" || pass == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Base URL, username, and password are required"})
		return
	}

	client, err := xtreamapi.New(user, pass, baseURL, ctx.Request.UserAgent(), ref)
	if err != nil {
		ctx.JSON(http.StatusOK, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	liveCats, err := client.GetLiveCategories()
	if err != nil {
		ctx.JSON(http.StatusOK, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Authentication successful, but category check failed: %v", err),
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"success":            true,
		"message":            fmt.Sprintf("Connected successfully! Found %d live categories.", len(liveCats)),
		"status":             client.UserInfo.Status,
		"exp_date":           client.UserInfo.ExpDate,
		"max_connections":    client.UserInfo.MaxConnections,
		"active_connections": client.UserInfo.ActiveConnections,
		"server_url":         client.ServerInfo.URL,
	})
}
