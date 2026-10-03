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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
	xtreamapi "github.com/pierre-emmanuelJ/iptv-proxy/pkg/xtream-proxy"
	uuid "github.com/satori/go.uuid"
)

var (
	provClientCache     = map[string]*xtreamapi.Client{}
	provClientCacheLock = sync.RWMutex{}
)

func invalidateClientForProvider(prov config.ProviderItem) {
	cacheKey := prov.XtreamUser + ":" + prov.XtreamPassword + "@" + prov.XtreamBaseURL
	provClientCacheLock.Lock()
	delete(provClientCache, cacheKey)
	provClientCacheLock.Unlock()
}

func clearAllClientCaches() {
	provClientCacheLock.RLock()
	defer provClientCacheLock.RUnlock()
	for _, cli := range provClientCache {
		if cli != nil {
			cli.ClearCategoryCache()
		}
	}
}

func getClientForProvider(prov config.ProviderItem) (*xtreamapi.Client, error) {
	cacheKey := prov.XtreamUser + ":" + prov.XtreamPassword + "@" + prov.XtreamBaseURL
	provClientCacheLock.RLock()
	if cli, ok := provClientCache[cacheKey]; ok && cli != nil {
		provClientCacheLock.RUnlock()
		return cli, nil
	}
	provClientCacheLock.RUnlock()

	urls := []string{prov.XtreamBaseURL}
	urls = append(urls, prov.BackupURLs...)
	var lastErr error
	for _, u := range urls {
		uClean := config.CleanURL(u)
		if uClean == "" {
			continue
		}
		ref := prov.Referer
		ua := prov.UserAgent
		if ua == "" {
			ua = config.DefaultUserAgent
		}
		cli, err := xtreamapi.New(prov.XtreamUser, prov.XtreamPassword, uClean, ua, ref)
		if err == nil {
			provClientCacheLock.Lock()
			provClientCache[cacheKey] = cli
			provClientCacheLock.Unlock()
			return cli, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

type cacheMeta struct {
	string
	time.Time
}

var hlsChannelsRedirectURL map[string]url.URL = map[string]url.URL{}
var hlsChannelsRedirectURLLock = sync.RWMutex{}

// XXX Use key/value storage e.g: etcd, redis...
// and remove that dirty globals
var xtreamM3uCache map[string]cacheMeta = map[string]cacheMeta{}
var xtreamM3uCacheLock = sync.RWMutex{}

func (c *Config) cacheXtreamM3u(playlist *m3u.Playlist, cacheName string) error {
	xtreamM3uCacheLock.Lock()
	defer xtreamM3uCacheLock.Unlock()

	tmp := *c
	tmp.playlist = playlist

	path := filepath.Join(os.TempDir(), uuid.NewV4().String()+".iptv-proxy.m3u")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := tmp.marshallInto(f, true); err != nil {
		return err
	}
	xtreamM3uCache[cacheName] = cacheMeta{path, time.Now()}

	return nil
}



func (c *Config) xtreamGenerateM3u(userAgent string, extension string) (*m3u.Playlist, error) {
	log.Printf("[iptv-proxy] xtreamGenerateM3u called with extension: %s", extension)

	enabled := c.GetEnabledProvidersOrFallback()

	var playlist = new(m3u.Playlist)
	playlist.Tracks = make([]m3u.Track, 0)

	var livePrefix string
	if extension != "" {
		livePrefix = "live/"
	}
	var ext string
	if extension != "" {
		ext = "." + extension
	}

	for provIdx, prov := range enabled {
		client, err := getClientForProvider(prov)
		if err != nil {
			log.Printf("[iptv-proxy] xtreamGenerateM3u: Error connecting to provider %s (%s): %v", prov.Name, prov.XtreamBaseURL, err)
			continue
		}

		// 1. Live Categories & Streams
		liveCat, err := client.GetLiveCategories()
		if err == nil {
			for _, category := range liveCat {
				catDisplayName := category.Name
				if len(enabled) > 1 {
					catDisplayName = fmt.Sprintf("[%s] %s", prov.Name, category.Name)
				}
				if !c.ProxyConfig.Filters.IsAllowed("live", category.Name) && !c.ProxyConfig.Filters.IsAllowed("live", catDisplayName) {
					continue
				}

				live, err := client.GetLiveStreams(fmt.Sprint(category.ID))
				if err != nil {
					continue
				}

				for _, stream := range live {
					track := m3u.Track{Name: stream.Name, Length: -1, URI: "", Tags: nil}
					if stream.EPGChannelID != "" {
						track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-id", Value: stream.EPGChannelID})
					}
					if stream.Name != "" {
						track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-name", Value: stream.Name})
					}
					if stream.Icon != "" {
						track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-logo", Value: stream.Icon})
					}
					track.Tags = append(track.Tags, m3u.Tag{Name: "group-title", Value: catDisplayName})

					origID := fmt.Sprint(stream.ID)
					var virtualID string
					origNum, numErr := strconv.Atoi(origID)
					if numErr == nil && provIdx > 0 {
						virtualID = fmt.Sprintf("%d", (provIdx*1000000)+origNum)
					} else if provIdx > 0 {
						virtualID = fmt.Sprintf("p%d_%s", provIdx, origID)
					} else {
						virtualID = origID
					}

					c.RegisterStreamTarget(virtualID, StreamRoutingTarget{
						ProviderIndex: provIdx,
						OriginalID:    origID,
						StreamType:    "live",
					})

					track.URI = fmt.Sprintf("%s/%s%s/%s/%s%s", prov.XtreamBaseURL, livePrefix, prov.XtreamUser, prov.XtreamPassword, virtualID, ext)
					playlist.Tracks = append(playlist.Tracks, track)
				}
			}
		}

		// 2. VOD Categories & Streams
		vodCat, err := client.GetVideoOnDemandCategories()
		if err == nil {
			for _, category := range vodCat {
				catDisplayName := category.Name
				if len(enabled) > 1 {
					catDisplayName = fmt.Sprintf("[%s] %s", prov.Name, category.Name)
				}
				if !c.ProxyConfig.Filters.IsAllowed("vod", category.Name) && !c.ProxyConfig.Filters.IsAllowed("vod", catDisplayName) {
					continue
				}

				vods, err := client.GetVideoOnDemandStreams(fmt.Sprint(category.ID))
				if err != nil {
					continue
				}

				for _, vod := range vods {
					track := m3u.Track{Name: vod.Name, Length: -1, URI: "", Tags: nil}
					if vod.Name != "" {
						track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-name", Value: vod.Name})
					}
					if vod.Icon != "" {
						track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-logo", Value: vod.Icon})
					}
					track.Tags = append(track.Tags, m3u.Tag{Name: "group-title", Value: catDisplayName})

					origID := fmt.Sprint(vod.ID)
					var virtualID string
					origNum, numErr := strconv.Atoi(origID)
					if numErr == nil && provIdx > 0 {
						virtualID = fmt.Sprintf("%d", (provIdx*1000000)+origNum)
					} else if provIdx > 0 {
						virtualID = fmt.Sprintf("p%d_%s", provIdx, origID)
					} else {
						virtualID = origID
					}

					c.RegisterStreamTarget(virtualID, StreamRoutingTarget{
						ProviderIndex: provIdx,
						OriginalID:    origID,
						StreamType:    "movie",
					})

					track.URI = fmt.Sprintf("%s/movie/%s/%s/%s%s", prov.XtreamBaseURL, prov.XtreamUser, prov.XtreamPassword, virtualID, ext)
					playlist.Tracks = append(playlist.Tracks, track)
				}
			}
		}

		// 3. Series Categories & Streams
		seriesCat, err := client.GetSeriesCategories()
		if err == nil {
			for _, category := range seriesCat {
				catDisplayName := category.Name
				if len(enabled) > 1 {
					catDisplayName = fmt.Sprintf("[%s] %s", prov.Name, category.Name)
				}
				if !c.ProxyConfig.Filters.IsAllowed("series", category.Name) && !c.ProxyConfig.Filters.IsAllowed("series", catDisplayName) {
					continue
				}

				seriesList, err := client.GetSeries(fmt.Sprint(category.ID))
				if err != nil {
					continue
				}

				for _, serie := range seriesList {
					track := m3u.Track{Name: serie.Name, Length: -1, URI: "", Tags: nil}
					if serie.Name != "" {
						track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-name", Value: serie.Name})
					}
					if serie.Cover != "" {
						track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-logo", Value: serie.Cover})
					}
					track.Tags = append(track.Tags, m3u.Tag{Name: "group-title", Value: catDisplayName})

					origID := fmt.Sprint(serie.SeriesID)
					var virtualID string
					origNum, numErr := strconv.Atoi(origID)
					if numErr == nil && provIdx > 0 {
						virtualID = fmt.Sprintf("%d", (provIdx*1000000)+origNum)
					} else if provIdx > 0 {
						virtualID = fmt.Sprintf("p%d_%s", provIdx, origID)
					} else {
						virtualID = origID
					}

					c.RegisterStreamTarget(virtualID, StreamRoutingTarget{
						ProviderIndex: provIdx,
						OriginalID:    origID,
						StreamType:    "series",
					})

					track.URI = fmt.Sprintf("%s/series/%s/%s/%s%s", prov.XtreamBaseURL, prov.XtreamUser, prov.XtreamPassword, virtualID, ext)
					playlist.Tracks = append(playlist.Tracks, track)
				}
			}
		}
	}

	log.Printf("[iptv-proxy] Aggregated total tracks in playlist: %d", len(playlist.Tracks))
	return playlist, nil
}

func (c *Config) xtreamGetAuto(ctx *gin.Context) {
	newQuery := ctx.Request.URL.Query()
	q := c.RemoteURL.Query()
	for k, v := range q {
		if k == "username" || k == "password" {
			continue
		}

		newQuery.Add(k, strings.Join(v, ","))
	}
	ctx.Request.URL.RawQuery = newQuery.Encode()

	c.xtreamGet(ctx)
}

func (c *Config) xtreamGet(ctx *gin.Context) {
	rawURL := fmt.Sprintf("%s/get.php?username=%s&password=%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword)

	q := ctx.Request.URL.Query()

	for k, v := range q {
		if k == "username" || k == "password" {
			continue
		}

		rawURL = fmt.Sprintf("%s&%s=%s", rawURL, k, strings.Join(v, ","))
	}

	m3uURL, err := url.Parse(rawURL)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	xtreamM3uCacheLock.RLock()
	meta, ok := xtreamM3uCache[m3uURL.String()]
	d := time.Since(meta.Time)
	isExpired := d.Hours() >= float64(c.M3UCacheExpiration)
	xtreamM3uCacheLock.RUnlock()

	if ok {
		ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
		xtreamM3uCacheLock.RLock()
		path := xtreamM3uCache[m3uURL.String()].string
		xtreamM3uCacheLock.RUnlock()
		ctx.Header("Content-Type", "application/octet-stream")
		ctx.File(path)

		if isExpired {
			c.refreshingMutex.Lock()
			if !c.refreshing[m3uURL.String()] {
				c.refreshing[m3uURL.String()] = true
				c.refreshingMutex.Unlock()

				go func() {
					defer func() {
						c.refreshingMutex.Lock()
						c.refreshing[m3uURL.String()] = false
						c.refreshingMutex.Unlock()
					}()

					log.Printf("[iptv-proxy] Background M3U refresh starting...")
					playlist, err := m3u.Parse(m3uURL.String())
					if err == nil {
						c.cacheXtreamM3u(&playlist, m3uURL.String())
						log.Printf("[iptv-proxy] Background M3U refresh completed successfully.")
					} else {
						log.Printf("[iptv-proxy] Background M3U refresh failed: %v", err)
					}
				}()
			} else {
				c.refreshingMutex.Unlock()
			}
		}
		return
	}

	log.Printf("[iptv-proxy] %v | %s | xtream cache m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
	playlist, err := m3u.Parse(m3uURL.String())
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	
	// Apply filters to raw M3U
	var filteredTracks []m3u.Track
	for _, track := range playlist.Tracks {
		groupTitle := ""
		for _, tag := range track.Tags {
			if tag.Name == "group-title" {
				groupTitle = tag.Value
				break
			}
		}
		// Since we don't know if a raw M3U track is Live/VOD/Series easily, we'll use AllowedLiveCategories as the global filter for raw M3U
		if c.ProxyConfig.Filters.IsAllowed("live", groupTitle) {
			filteredTracks = append(filteredTracks, track)
		}
	}
	playlist.Tracks = filteredTracks

	if err := c.cacheXtreamM3u(&playlist, m3uURL.String()); err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	path := xtreamM3uCache[m3uURL.String()].string
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(path)
}

func (c *Config) xtreamApiGet(ctx *gin.Context) {
	log.Printf("[iptv-proxy] xtreamApiGet called")
	const (
		apiGet = "apiget"
	)

	var (
		extension = ctx.Query("output")
		cacheName = apiGet + extension
	)
	log.Printf("[iptv-proxy] Extension: %s, CacheName: %s", extension, cacheName)

	xtreamM3uCacheLock.RLock()
	meta, ok := xtreamM3uCache[cacheName]
	d := time.Since(meta.Time)
	isExpired := d.Hours() >= float64(c.M3UCacheExpiration)
	xtreamM3uCacheLock.RUnlock()

	userAgent := ctx.Request.UserAgent()

	if ok {
		ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
		xtreamM3uCacheLock.RLock()
		path := xtreamM3uCache[cacheName].string
		xtreamM3uCacheLock.RUnlock()
		ctx.Header("Content-Type", "application/octet-stream")
		ctx.File(path)

		if isExpired {
			c.refreshingMutex.Lock()
			if !c.refreshing[cacheName] {
				c.refreshing[cacheName] = true
				c.refreshingMutex.Unlock()

				go func() {
					defer func() {
						c.refreshingMutex.Lock()
						c.refreshing[cacheName] = false
						c.refreshingMutex.Unlock()
					}()

					log.Printf("[iptv-proxy] Background API M3U refresh starting...")
					playlist, err := c.xtreamGenerateM3u(userAgent, extension)
					if err == nil {
						c.cacheXtreamM3u(playlist, cacheName)
						log.Printf("[iptv-proxy] Background API M3U refresh completed successfully.")
					} else {
						log.Printf("[iptv-proxy] Background API M3U refresh failed: %v", err)
					}
				}()
			} else {
				c.refreshingMutex.Unlock()
			}
		}
		return
	}

	log.Printf("[iptv-proxy] %v | %s | xtream cache API m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
	playlist, err := c.xtreamGenerateM3u(userAgent, extension)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	if err := c.cacheXtreamM3u(playlist, cacheName); err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	path := xtreamM3uCache[cacheName].string
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(path)

}

func (c *Config) xtreamPlayerAPIGET(ctx *gin.Context) {
	c.xtreamPlayerAPI(ctx, ctx.Request.URL.Query())
}

func (c *Config) xtreamPlayerAPIPOST(ctx *gin.Context) {
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

	c.xtreamPlayerAPI(ctx, q)
}

func (c *Config) xtreamPlayerAPI(ctx *gin.Context, q url.Values) {
	var action string
	if len(q["action"]) > 0 {
		action = q["action"][0]
	}

	cacheKey, cacheable := c.metadataCacheKey(action, q)
	if cacheable {
		if entry, ok, isExpired := c.metadataCache.Get(cacheKey); ok {
			if !isExpired {
				log.Printf("[iptv-proxy] %v | %s |Action\t%s (cache hit)\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP(), action)
				ctx.Data(http.StatusOK, entry.contentType, entry.payload)
				return
			}
			// Stale cache hit: serve immediately so client never times out or drops connection, and refresh in background
			log.Printf("[iptv-proxy] %v | %s |Action\t%s (stale cache hit, serving immediately)\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP(), action)
			ctx.Data(http.StatusOK, entry.contentType, entry.payload)
			go c.revalidatePlayerAPI(action, q, cacheKey)
			return
		}

		if c.metadataInFlight != nil {
			waited, done := c.metadataInFlight.Start(cacheKey)
			if waited {
				if entry, ok, _ := c.metadataCache.Get(cacheKey); ok {
					log.Printf("[iptv-proxy] %v | %s |Action\t%s (in-flight cache hit)\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP(), action)
					ctx.Data(http.StatusOK, entry.contentType, entry.payload)
					return
				}
			} else {
				defer done()
			}
		}
	}

	resp, httpcode, err := c.executePlayerAPI(action, q)
	if err != nil {
		ctx.AbortWithError(httpcode, err)
		return
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err)
		return
	}

	if action == "" {
		var loginMap map[string]interface{}
		if errM := json.Unmarshal(payload, &loginMap); errM == nil {
			if userInfo, ok := loginMap["user_info"].(map[string]interface{}); ok {
				if authUserVal, exists := ctx.Get("auth_user"); exists {
					if u, ok := authUserVal.(*config.UserItem); ok {
						userInfo["username"] = u.Username
						userInfo["password"] = u.Password
						if u.MaxConnections > 0 {
							userInfo["max_connections"] = strconv.Itoa(u.MaxConnections)
						}
						if c.userManager != nil {
							userInfo["active_cons"] = strconv.Itoa(c.userManager.ActiveConnections(u.Username))
						}
					}
				}
			}
			if serverInfo, ok := loginMap["server_info"].(map[string]interface{}); ok {
				// Automatically use the host header from the client request (e.g. VPS IP or domain)
				reqHost := ctx.Request.Host
				if h, p, errH := net.SplitHostPort(reqHost); errH == nil {
					serverInfo["url"] = h
					if pInt, errP := strconv.Atoi(p); errP == nil {
						serverInfo["port"] = pInt
						serverInfo["https_port"] = pInt
						serverInfo["rtmp_port"] = pInt
					}
				} else if reqHost != "" {
					serverInfo["url"] = reqHost
				}
			}
			if updated, errM := json.Marshal(loginMap); errM == nil {
				payload = updated
			}
		}
	}

	log.Printf("[iptv-proxy] %v | %s |Action\t%s\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP(), action)

	if cacheable {
		c.metadataCache.Set(cacheKey, payload, "application/json")
	}

	ctx.Data(http.StatusOK, "application/json", payload)
}

func (c *Config) revalidatePlayerAPI(action string, q url.Values, cacheKey string) {
	if c.metadataInFlight != nil {
		waited, done := c.metadataInFlight.Start(cacheKey)
		if waited {
			return
		}
		defer done()
	}

	resp, _, err := c.executePlayerAPI(action, q)
	if err != nil {
		return
	}
	payload, errM := json.Marshal(resp)
	if errM != nil {
		return
	}
	c.metadataCache.Set(cacheKey, payload, "application/json")
	log.Printf("[iptv-proxy] Background revalidation completed for %s\n", action)
}

func (c *Config) executePlayerAPI(action string, q url.Values) (interface{}, int, error) {
	enabled := c.GetEnabledProvidersOrFallback()
	var resp interface{}
	var httpcode int = http.StatusOK
	var err error

	switch action {
	case "get_live_categories", "get_vod_categories", "get_series_categories":
		filterType := "live"
		if action == "get_vod_categories" {
			filterType = "vod"
		} else if action == "get_series_categories" {
			filterType = "series"
		}

		var aggregated []map[string]interface{}
		for provIdx, prov := range enabled {
			client, errClient := getClientForProvider(prov)
			if errClient != nil {
				log.Printf("[iptv-proxy] Warning: failed to connect to provider %s: %v", prov.Name, errClient)
				continue
			}

			subResp, _, errAction := client.Action(c.ProxyConfig, action, q)
			if errAction != nil {
				log.Printf("[iptv-proxy] Warning: provider %s action %s error: %v", prov.Name, action, errAction)
				continue
			}

			b, errM := json.Marshal(subResp)
			if errM != nil {
				continue
			}
			var list []map[string]interface{}
			if errU := json.Unmarshal(b, &list); errU != nil {
				continue
			}

			for _, cat := range list {
				origName := fmt.Sprint(cat["category_name"])
				displayName := origName
				if len(enabled) > 1 {
					displayName = fmt.Sprintf("[%s] %s", prov.Name, origName)
					cat["category_name"] = displayName
				}

				if !c.ProxyConfig.Filters.IsAllowed(filterType, origName) && !c.ProxyConfig.Filters.IsAllowed(filterType, displayName) {
					continue
				}

				if provIdx > 0 {
					origIDStr := fmt.Sprint(cat["category_id"])
					if num, errParse := strconv.Atoi(origIDStr); errParse == nil {
						cat["category_id"] = strconv.Itoa((provIdx * 100000) + num)
					} else {
						cat["category_id"] = fmt.Sprintf("p%d_%s", provIdx, origIDStr)
					}
				}
				aggregated = append(aggregated, cat)
			}
		}
		resp = aggregated

	case "get_live_streams", "get_vod_streams", "get_series":
		streamType := "live"
		idKey := "stream_id"
		if action == "get_vod_streams" {
			streamType = "movie"
		} else if action == "get_series" {
			streamType = "series"
			idKey = "series_id"
		}

		catParam := ""
		if len(q["category_id"]) > 0 {
			catParam = q["category_id"][0]
		}

		if catParam != "" {
			provIdx := 0
			origCatID := catParam
			if num, errParse := strconv.Atoi(catParam); errParse == nil && num >= 100000 {
				provIdx = num / 100000
				origCatID = strconv.Itoa(num % 100000)
			} else if strings.HasPrefix(catParam, "p") && strings.Contains(catParam, "_") {
				parts := strings.SplitN(catParam, "_", 2)
				if idx, errParse := strconv.Atoi(parts[0][1:]); errParse == nil {
					provIdx = idx
					origCatID = parts[1]
				}
			}

			if provIdx < 0 || provIdx >= len(enabled) {
				provIdx = 0
			}
			targetProv := enabled[provIdx]

			client, errClient := getClientForProvider(targetProv)
			if errClient != nil {
				return nil, http.StatusBadGateway, errClient
			}

			subQ := url.Values{}
			for k, v := range q {
				subQ[k] = v
			}
			subQ.Set("category_id", origCatID)

			subResp, code, errAction := client.Action(c.ProxyConfig, action, subQ)
			if errAction != nil {
				return nil, code, errAction
			}

			if len(enabled) == 1 && provIdx == 0 {
				resp = subResp
			} else {
				b, _ := json.Marshal(subResp)
				var list []map[string]interface{}
				if errU := json.Unmarshal(b, &list); errU == nil {
					batchTargets := make(map[string]StreamRoutingTarget, len(list))
					for _, item := range list {
						rawID := fmt.Sprint(item[idKey])
						var virtualID string
						origNum, numErr := strconv.Atoi(rawID)
						if numErr == nil && provIdx > 0 {
							vNum := (provIdx * 1000000) + origNum
							virtualID = strconv.Itoa(vNum)
							item[idKey] = vNum
						} else if provIdx > 0 {
							virtualID = fmt.Sprintf("p%d_%s", provIdx, rawID)
							item[idKey] = virtualID
						} else {
							virtualID = rawID
						}
						item["category_id"] = catParam

						batchTargets[virtualID] = StreamRoutingTarget{
							ProviderIndex: provIdx,
							OriginalID:    rawID,
							StreamType:    streamType,
						}
					}
					c.RegisterStreamTargetsBatch(batchTargets)
					resp = list
				} else {
					resp = subResp
				}
			}
		} else {
			if len(enabled) == 1 {
				client, errClient := getClientForProvider(enabled[0])
				if errClient != nil {
					return nil, http.StatusBadGateway, errClient
				}
				subResp, code, errAction := client.Action(c.ProxyConfig, action, q)
				if errAction != nil {
					return nil, code, errAction
				}
				resp = subResp
			} else {
				var aggregated []map[string]interface{}
				batchTargets := make(map[string]StreamRoutingTarget)
				for provIdx, prov := range enabled {
					client, errClient := getClientForProvider(prov)
					if errClient != nil {
						continue
					}

					subResp, _, errAction := client.Action(c.ProxyConfig, action, q)
					if errAction != nil {
						continue
					}

					b, _ := json.Marshal(subResp)
					var list []map[string]interface{}
					if errU := json.Unmarshal(b, &list); errU != nil {
						continue
					}

					for _, item := range list {
						rawID := fmt.Sprint(item[idKey])
						var virtualID string
						origNum, numErr := strconv.Atoi(rawID)
						if numErr == nil && provIdx > 0 {
							vNum := (provIdx * 1000000) + origNum
							virtualID = strconv.Itoa(vNum)
							item[idKey] = vNum
						} else if provIdx > 0 {
							virtualID = fmt.Sprintf("p%d_%s", provIdx, rawID)
							item[idKey] = virtualID
						} else {
							virtualID = rawID
						}

						if provIdx > 0 {
							origCatStr := fmt.Sprint(item["category_id"])
							if origCatNum, errParse := strconv.Atoi(origCatStr); errParse == nil {
								item["category_id"] = strconv.Itoa((provIdx * 100000) + origCatNum)
							} else {
								item["category_id"] = fmt.Sprintf("p%d_%s", provIdx, origCatStr)
							}
						}

						batchTargets[virtualID] = StreamRoutingTarget{
							ProviderIndex: provIdx,
							OriginalID:    rawID,
							StreamType:    streamType,
						}
						aggregated = append(aggregated, item)
					}
				}
				c.RegisterStreamTargetsBatch(batchTargets)
				resp = aggregated
			}
		}

	case "get_vod_info":
		vodID := ""
		if len(q["vod_id"]) > 0 {
			vodID = q["vod_id"][0]
		}
		targetProv, origID := c.ResolveTargetStream("movie", vodID)
		client, errClient := getClientForProvider(targetProv)
		if errClient != nil {
			return nil, http.StatusBadGateway, errClient
		}
		subQ := url.Values{}
		for k, v := range q {
			subQ[k] = v
		}
		subQ.Set("vod_id", origID)
		resp, httpcode, err = client.Action(c.ProxyConfig, action, subQ)
		if err != nil {
			return nil, httpcode, err
		}

	case "get_series_info":
		seriesID := ""
		if len(q["series_id"]) > 0 {
			seriesID = q["series_id"][0]
		}
		targetProv, origID := c.ResolveTargetStream("series", seriesID)
		client, errClient := getClientForProvider(targetProv)
		if errClient != nil {
			return nil, http.StatusBadGateway, errClient
		}
		subQ := url.Values{}
		for k, v := range q {
			subQ[k] = v
		}
		subQ.Set("series_id", origID)
		resp, httpcode, err = client.Action(c.ProxyConfig, action, subQ)
		if err != nil {
			return nil, httpcode, err
		}

		provIdx := c.FindProviderIndex(targetProv)
		b, _ := json.Marshal(resp)
		var seriesData map[string]interface{}
		if errU := json.Unmarshal(b, &seriesData); errU == nil {
			if episodes, ok := seriesData["episodes"].(map[string]interface{}); ok {
				batchTargets := make(map[string]StreamRoutingTarget)
				for _, epList := range episodes {
					if epSlice, ok := epList.([]interface{}); ok {
						for _, epRaw := range epSlice {
							if epMap, ok := epRaw.(map[string]interface{}); ok {
								if epIDVal, exists := epMap["id"]; exists {
									epIDStr := fmt.Sprint(epIDVal)
									batchTargets[epIDStr] = StreamRoutingTarget{
										ProviderIndex: provIdx,
										OriginalID:    epIDStr,
										StreamType:    "series",
									}
								}
							}
						}
					}
				}
				c.RegisterStreamTargetsBatch(batchTargets)
			}
		}

	case "get_short_epg", "get_simple_data_table":
		streamID := ""
		if len(q["stream_id"]) > 0 {
			streamID = q["stream_id"][0]
		}
		targetProv, origID := c.ResolveTargetStream("live", streamID)
		client, errClient := getClientForProvider(targetProv)
		if errClient != nil {
			return nil, http.StatusBadGateway, errClient
		}
		subQ := url.Values{}
		for k, v := range q {
			subQ[k] = v
		}
		subQ.Set("stream_id", origID)
		resp, httpcode, err = client.Action(c.ProxyConfig, action, subQ)
		if err != nil {
			if httpcode == http.StatusUnauthorized || httpcode == http.StatusForbidden {
				invalidateClientForProvider(targetProv)
			}
			return nil, httpcode, err
		}

	default:
		client, errClient := getClientForProvider(enabled[0])
		if errClient != nil {
			return nil, http.StatusBadGateway, errClient
		}
		resp, httpcode, err = client.Action(c.ProxyConfig, action, q)
		if err != nil {
			if httpcode == http.StatusUnauthorized || httpcode == http.StatusForbidden {
				invalidateClientForProvider(enabled[0])
			}
			return nil, httpcode, err
		}
	}

	return resp, httpcode, err
}

func (c *Config) xtreamXMLTV(ctx *gin.Context) {
	cacheKey := "xmltv_cache_key"
	entry, ok, isExpired := c.xmltvCache.Get(cacheKey)

	if ok {
		log.Printf("[iptv-proxy] %v | %s | xmltv.php cache hit (expired: %v)\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP(), isExpired)
		ctx.Data(http.StatusOK, entry.contentType, entry.payload)

		if isExpired {
			c.refreshingMutex.Lock()
			if !c.refreshing[cacheKey] {
				c.refreshing[cacheKey] = true
				c.refreshingMutex.Unlock()

				go func() {
					defer func() {
						c.refreshingMutex.Lock()
						c.refreshing[cacheKey] = false
						c.refreshingMutex.Unlock()
					}()

					log.Printf("[iptv-proxy] Background XMLTV refresh starting...")
					client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, c.GetUpstreamUserAgent(), c.Referer)
					if err == nil {
						resp, err := client.GetXMLTV()
						if err == nil {
							c.xmltvCache.Set(cacheKey, resp, "application/xml")
							log.Printf("[iptv-proxy] Background XMLTV refresh completed successfully.")
						} else {
							log.Printf("[iptv-proxy] Background XMLTV refresh failed: %v", err)
						}
					} else {
						log.Printf("[iptv-proxy] Background XMLTV refresh failed to init client: %v", err)
					}
				}()
			} else {
				c.refreshingMutex.Unlock()
			}
		}
		return
	}

	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, c.GetUpstreamUserAgent(), c.Referer)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	resp, err := client.GetXMLTV()
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xmltvCache.Set(cacheKey, resp, "application/xml")
	ctx.Data(http.StatusOK, "application/xml", resp)
}

func (c *Config) authenticateStreamUser(ctx *gin.Context, streamID string, streamType string) (func(), bool) {
	userParam := ctx.Param("user")
	passParam := ctx.Param("password")

	var authenticatedUser *config.UserItem
	if c.userManager != nil {
		if u, ok := c.userManager.Authenticate(userParam, passParam); ok {
			authenticatedUser = u
		}
	}
	if authenticatedUser == nil && (c.User.String() == userParam && c.Password.String() == passParam) {
		authenticatedUser = &config.UserItem{
			Username:       userParam,
			Password:       passParam,
			MaxConnections: 0,
			Enabled:        true,
		}
	}

	if authenticatedUser == nil {
		log.Printf("[iptv-proxy] Unauthorized stream request from %s for user %q", ctx.ClientIP(), userParam)
		ctx.AbortWithStatus(http.StatusUnauthorized)
		return nil, false
	}

	streamCtx, cancel := context.WithCancel(ctx.Request.Context())
	var slot *config.UserSlot
	var allowed bool = true
	if c.userManager != nil {
		slot, allowed = c.userManager.AcquireSlot(authenticatedUser.Username, ctx.ClientIP(), streamID, streamType, cancel)
	}

	if !allowed {
		log.Printf("[iptv-proxy] 403 Forbidden: User %q exceeded max connection limit (%d allowed) from IP %s",
			authenticatedUser.Username, authenticatedUser.MaxConnections, ctx.ClientIP())
		cancel()
		ctx.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": fmt.Sprintf("Max concurrent stream connections reached (%d)", authenticatedUser.MaxConnections),
		})
		return nil, false
	}

	ctx.Request = ctx.Request.WithContext(streamCtx)

	cleanup := func() {
		cancel()
		if c.userManager != nil && slot != nil {
			c.userManager.ReleaseSlot(authenticatedUser.Username, slot)
		}
	}
	return cleanup, true
}

func (c *Config) xtreamStreamHandler(ctx *gin.Context) {
	id := ctx.Param("id")
	cleanup, ok := c.authenticateStreamUser(ctx, id, "auto")
	if !ok {
		return
	}
	defer cleanup()

	targetProv, origID := c.ResolveTargetStream("live", id)
	rpURL, err := url.Parse(fmt.Sprintf("%s/%s/%s/%s", targetProv.XtreamBaseURL, targetProv.XtreamUser, targetProv.XtreamPassword, origID))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamStreamLive(ctx *gin.Context) {
	id := ctx.Param("id")
	cleanup, ok := c.authenticateStreamUser(ctx, id, "live")
	if !ok {
		return
	}
	defer cleanup()

	targetProv, origID := c.ResolveTargetStream("live", id)
	rpURL, err := url.Parse(fmt.Sprintf("%s/live/%s/%s/%s", targetProv.XtreamBaseURL, targetProv.XtreamUser, targetProv.XtreamPassword, origID))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamStreamPlay(ctx *gin.Context) {
	token := ctx.Param("token")
	t := ctx.Param("type")
	rpURL, err := url.Parse(fmt.Sprintf("%s/play/%s/%s", c.XtreamBaseURL, token, t))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamStreamTimeshift(ctx *gin.Context) {
	duration := ctx.Param("duration")
	start := ctx.Param("start")
	id := ctx.Param("id")
	cleanup, ok := c.authenticateStreamUser(ctx, id, "timeshift")
	if !ok {
		return
	}
	defer cleanup()

	targetProv, origID := c.ResolveTargetStream("live", id)
	rpURL, err := url.Parse(fmt.Sprintf("%s/timeshift/%s/%s/%s/%s/%s", targetProv.XtreamBaseURL, targetProv.XtreamUser, targetProv.XtreamPassword, duration, start, origID))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.stream(ctx, rpURL)
}

func (c *Config) xtreamStreamMovie(ctx *gin.Context) {
	id := ctx.Param("id")
	cleanup, ok := c.authenticateStreamUser(ctx, id, "movie")
	if !ok {
		return
	}
	defer cleanup()

	targetProv, origID := c.ResolveTargetStream("movie", id)
	rpURL, err := url.Parse(fmt.Sprintf("%s/movie/%s/%s/%s", targetProv.XtreamBaseURL, targetProv.XtreamUser, targetProv.XtreamPassword, origID))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamStreamSeries(ctx *gin.Context) {
	id := ctx.Param("id")
	cleanup, ok := c.authenticateStreamUser(ctx, id, "series")
	if !ok {
		return
	}
	defer cleanup()

	targetProv, origID := c.ResolveTargetStream("series", id)
	rpURL, err := url.Parse(fmt.Sprintf("%s/series/%s/%s/%s", targetProv.XtreamBaseURL, targetProv.XtreamUser, targetProv.XtreamPassword, origID))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamHlsStream(ctx *gin.Context) {
	chunk := ctx.Param("chunk")
	s := strings.Split(chunk, "_")
	if len(s) != 2 {
		ctx.AbortWithError( // nolint: errcheck
			http.StatusInternalServerError,
			errors.New("HSL malformed chunk"),
		)
		return
	}
	channel := s[0]

	url, err := getHlsRedirectURL(channel)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	req, err := url.Parse(
		fmt.Sprintf(
			"%s://%s/hls/%s/%s",
			url.Scheme,
			url.Host,
			ctx.Param("token"),
			ctx.Param("chunk"),
		),
	)

	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, req)
}

func (c *Config) xtreamHlsrStream(ctx *gin.Context) {
	channel := ctx.Param("channel")

	url, err := getHlsRedirectURL(channel)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	req, err := url.Parse(
		fmt.Sprintf(
			"%s://%s/hlsr/%s/%s/%s/%s/%s/%s",
			url.Scheme,
			url.Host,
			ctx.Param("token"),
			c.XtreamUser,
			c.XtreamPassword,
			ctx.Param("channel"),
			ctx.Param("hash"),
			ctx.Param("chunk"),
		),
	)

	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, req)
}

func (c *Config) metadataCacheKey(action string, q url.Values) (string, bool) {
	if c == nil || c.metadataCache == nil || c.MetadataCacheTTL <= 0 {
		return "", false
	}

	switch action {
	case "get_live_categories", "get_vod_categories", "get_series_categories",
		"get_live_streams", "get_vod_streams", "get_vod_info", "get_series",
		"get_series_info", "get_short_epg", "get_simple_data_table":
		return action + "|" + canonicalizeQuery(q), true
	default:
		return "", false
	}
}

func canonicalizeQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}

	keys := make([]string, 0, len(q))
	for k := range q {
		if k == "username" || k == "password" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	first := true
	for _, k := range keys {
		values := append([]string(nil), q[k]...)
		sort.Strings(values)
		for _, v := range values {
			if !first {
				b.WriteByte('&')
			}
			first = false
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
		}
	}

	return b.String()
}

func getHlsRedirectURL(channel string) (*url.URL, error) {
	hlsChannelsRedirectURLLock.RLock()
	defer hlsChannelsRedirectURLLock.RUnlock()

	url, ok := hlsChannelsRedirectURL[channel+".m3u8"]
	if !ok {
		url, ok = hlsChannelsRedirectURL[channel]
	}
	if !ok {
		return nil, errors.New("HLS redirect url not found")
	}

	return &url, nil
}

func (c *Config) hlsXtreamStream(ctx *gin.Context, oriURL *url.URL) {
	client := &http.Client{
		Transport: c.httpClient.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequest("GET", oriURL.String(), nil)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	mergeHttpHeader(req.Header, ctx.Request.Header)
	req.Header.Set("User-Agent", c.GetUpstreamUserAgent())
	if c.Referer != "" {
		req.Header.Set("Referer", c.Referer)
	} else {
		req.Header.Del("Referer")
	}
	cleanUpstreamHeaders(req.Header)

	resp, err := c.doRequestWithProxyRetry(client, req)
	if (err != nil || (resp != nil && resp.StatusCode >= 400)) && !isProxyError(err) {
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
		if len(backupURLs) > 1 {
			for _, bURL := range backupURLs {
				parsedB, errB := url.Parse(bURL)
				if errB != nil || parsedB.Host == req.URL.Host {
					continue
				}
				bReq := req.Clone(ctx.Request.Context())
				bReq.URL.Scheme = parsedB.Scheme
				bReq.URL.Host = parsedB.Host
				bReq.Host = parsedB.Host
				if c.Referer != "" {
					bReq.Header.Set("Referer", c.Referer)
				} else {
					bReq.Header.Del("Referer")
				}

				bResp, bErr := c.doRequestWithProxyRetry(client, bReq)
				if bErr == nil && bResp.StatusCode < 400 {
					if resp != nil && resp.Body != nil {
						resp.Body.Close()
					}
					resp = bResp
					err = nil
					c.RotateToURL(bURL)
					break
				}
				if bResp != nil && bResp.Body != nil {
					bResp.Body.Close()
				}
			}
		}
	}
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	if resp.StatusCode == http.StatusOK {
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}
		body := c.rewriteM3U8Body(string(b), ctx, oriURL)
		mergeHttpHeader(ctx.Writer.Header(), resp.Header)
		ctx.Header("Access-Control-Allow-Origin", "*")
		ctx.Header("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		ctx.Header("Access-Control-Allow-Headers", "*")
		ctx.Data(http.StatusOK, resp.Header.Get("Content-Type"), []byte(body))
		return
	}

	if resp.StatusCode == http.StatusFound {
		location, err := resp.Location()
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}
		id := ctx.Param("id")
		baseID := strings.TrimSuffix(id, ".m3u8")
		hlsChannelsRedirectURLLock.Lock()
		hlsChannelsRedirectURL[id] = *location
		hlsChannelsRedirectURL[baseID] = *location
		hlsChannelsRedirectURL[baseID+".m3u8"] = *location
		hlsChannelsRedirectURLLock.Unlock()

		chunkKey := location.String()
		isPlaylist := strings.Contains(chunkKey, ".m3u8")

		// Check HLS chunk RAM cache for non-playlist segments (.ts)
		if c.chunkCache != nil && !isPlaylist {
			if cachedData, cType, found := c.chunkCache.Get(chunkKey); found {
				ctx.Header("Access-Control-Allow-Origin", "*")
				ctx.Data(http.StatusOK, cType, cachedData)
				return
			}
		}

		hlsReq, err := http.NewRequest("GET", location.String(), nil)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}

		mergeHttpHeader(hlsReq.Header, ctx.Request.Header)
		hlsReq.Header.Set("User-Agent", c.GetUpstreamUserAgent())
		if c.Referer != "" {
			hlsReq.Header.Set("Referer", c.Referer)
		}
		cleanUpstreamHeaders(hlsReq.Header)

		hlsResp, err := c.doRequestWithProxyRetry(client, hlsReq)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}
		defer hlsResp.Body.Close()

		b, err := ioutil.ReadAll(hlsResp.Body)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}

		if isPlaylist {
			body := c.rewriteM3U8Body(string(b), ctx, location)
			mergeHttpHeader(ctx.Writer.Header(), hlsResp.Header)
			ctx.Header("Access-Control-Allow-Origin", "*")
			ctx.Header("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			ctx.Header("Access-Control-Allow-Headers", "*")
			ctx.Data(http.StatusOK, hlsResp.Header.Get("Content-Type"), []byte(body))
			return
		}

		// Cache .ts chunk in RAM for 15s to serve other viewers watching same stream
		if c.chunkCache != nil {
			c.chunkCache.Set(chunkKey, b, hlsResp.Header.Get("Content-Type"))
		}

		mergeHttpHeader(ctx.Writer.Header(), hlsResp.Header)
		ctx.Header("Access-Control-Allow-Origin", "*")
		ctx.Header("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		ctx.Header("Access-Control-Allow-Headers", "*")
		ctx.Data(http.StatusOK, hlsResp.Header.Get("Content-Type"), b)
		return
	}

	ctx.Status(resp.StatusCode)
}

func (c *Config) rewriteM3U8Body(body string, ctx *gin.Context, location *url.URL) string {
	body = strings.ReplaceAll(body, "/"+c.XtreamUser.String()+"/"+c.XtreamPassword.String()+"/", "/"+c.User.String()+"/"+c.Password.String()+"/")
	if c.ProxyConfig != nil && c.ProxyConfig.Provider != nil {
		for _, prov := range c.ProxyConfig.Provider.GetProviders() {
			if prov.XtreamUser != "" && prov.XtreamPassword != "" {
				body = strings.ReplaceAll(body, "/"+prov.XtreamUser+"/"+prov.XtreamPassword+"/", "/"+c.User.String()+"/"+c.Password.String()+"/")
			}
		}
	}

	proxyScheme := "http"
	if ctx.Request.TLS != nil || ctx.GetHeader("X-Forwarded-Proto") == "https" {
		proxyScheme = "https"
	}
	proxyBase := proxyScheme + "://" + ctx.Request.Host

	if location != nil && location.Host != "" {
		body = strings.ReplaceAll(body, location.Scheme+"://"+location.Host, proxyBase)
		body = strings.ReplaceAll(body, "http://"+location.Host, proxyBase)
		body = strings.ReplaceAll(body, "https://"+location.Host, proxyBase)
	}

	for _, u := range c.GetAllProviderURLs() {
		if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
			body = strings.ReplaceAll(body, parsed.Scheme+"://"+parsed.Host, proxyBase)
			body = strings.ReplaceAll(body, "http://"+parsed.Host, proxyBase)
			body = strings.ReplaceAll(body, "https://"+parsed.Host, proxyBase)
		}
	}

	return body
}
