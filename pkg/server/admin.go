package server

import (
	"embed"
	"net/http"

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

	// Static files from embedded FS
	admin.GET("/", func(ctx *gin.Context) {
		html, _ := webFS.ReadFile("web/admin.html")
		ctx.Data(http.StatusOK, "text/html; charset=utf-8", html)
	})
}

func (c *Config) adminGetCategories(ctx *gin.Context) {
	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	live, _ := client.GetLiveCategories()
	vod, _ := client.GetVideoOnDemandCategories()
	series, _ := client.GetSeriesCategories()

	c.ProxyConfig.Filters.RLock()
	defer c.ProxyConfig.Filters.RUnlock()

	ctx.JSON(http.StatusOK, gin.H{
		"live":    live,
		"vod":     vod,
		"series":  series,
		"filters": c.ProxyConfig.Filters.Data,
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
