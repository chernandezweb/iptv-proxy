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

package xtreamproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
	xtream "github.com/tellytv/go.xtream-codes"
)

const (
	getLiveCategories   = "get_live_categories"
	getLiveStreams      = "get_live_streams"
	getVodCategories    = "get_vod_categories"
	getVodStreams       = "get_vod_streams"
	getVodInfo          = "get_vod_info"
	getSeriesCategories = "get_series_categories"
	getSeries           = "get_series"
	getSerieInfo        = "get_series_info"
	getShortEPG         = "get_short_epg"
	getSimpleDataTable  = "get_simple_data_table"
)

type seriesCacheEntry struct {
	series    []xtream.SeriesInfo
	expiresAt time.Time
}

type seriesCatCacheEntry struct {
	categories []xtream.Category
	expiresAt  time.Time
}

type catCacheEntry struct {
	categories []xtream.Category
	expiresAt  time.Time
}

// Client represent an xtream client
type Client struct {
	*xtream.XtreamClient
	seriesCacheMu    sync.RWMutex
	seriesCache      seriesCacheEntry
	seriesCatCacheMu sync.RWMutex
	seriesCatCache   seriesCatCacheEntry
	vodCatCacheMu    sync.RWMutex
	vodCatCache      catCacheEntry
	liveCatCacheMu   sync.RWMutex
	liveCatCache     catCacheEntry
}

// ClearCategoryCache flushes cached categories and series
func (c *Client) ClearCategoryCache() {
	c.liveCatCacheMu.Lock()
	c.liveCatCache = catCacheEntry{}
	c.liveCatCacheMu.Unlock()

	c.vodCatCacheMu.Lock()
	c.vodCatCache = catCacheEntry{}
	c.vodCatCacheMu.Unlock()

	c.seriesCatCacheMu.Lock()
	c.seriesCatCache = seriesCatCacheEntry{}
	c.seriesCatCacheMu.Unlock()

	c.seriesCacheMu.Lock()
	c.seriesCache = seriesCacheEntry{}
	c.seriesCacheMu.Unlock()
}

func (c *Client) getCachedLiveCategories() ([]xtream.Category, error) {
	c.liveCatCacheMu.RLock()
	if len(c.liveCatCache.categories) > 0 && time.Now().Before(c.liveCatCache.expiresAt) {
		cats := c.liveCatCache.categories
		c.liveCatCacheMu.RUnlock()
		return cats, nil
	}
	c.liveCatCacheMu.RUnlock()

	var cats []xtream.Category
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		cats, err = c.GetLiveCategories()
		if err == nil && len(cats) > 0 {
			break
		}
		time.Sleep(time.Duration(attempt*200) * time.Millisecond)
	}

	if err == nil && len(cats) > 0 {
		c.liveCatCacheMu.Lock()
		c.liveCatCache = catCacheEntry{
			categories: cats,
			expiresAt:  time.Now().Add(15 * time.Minute),
		}
		c.liveCatCacheMu.Unlock()
	}
	return cats, err
}

func (c *Client) getCachedVodCategories() ([]xtream.Category, error) {
	c.vodCatCacheMu.RLock()
	if len(c.vodCatCache.categories) > 0 && time.Now().Before(c.vodCatCache.expiresAt) {
		cats := c.vodCatCache.categories
		c.vodCatCacheMu.RUnlock()
		return cats, nil
	}
	c.vodCatCacheMu.RUnlock()

	var cats []xtream.Category
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		cats, err = c.GetVideoOnDemandCategories()
		if err == nil && len(cats) > 0 {
			break
		}
		time.Sleep(time.Duration(attempt*200) * time.Millisecond)
	}

	if err == nil && len(cats) > 0 {
		c.vodCatCacheMu.Lock()
		c.vodCatCache = catCacheEntry{
			categories: cats,
			expiresAt:  time.Now().Add(15 * time.Minute),
		}
		c.vodCatCacheMu.Unlock()
	}
	return cats, err
}

// New new xtream client
func New(user, password, baseURL, userAgent string, referer ...string) (*Client, error) {
	ref := ""
	if len(referer) > 0 {
		ref = referer[0]
	}
	cli, err := xtream.NewClientWithConfig(context.Background(), user, password, baseURL, userAgent, ref)
	if err != nil {
		return nil, err
	}

	return &Client{XtreamClient: cli}, nil
}

type login struct {
	UserInfo   xtream.UserInfo   `json:"user_info"`
	ServerInfo xtream.ServerInfo `json:"server_info"`
}

// Login xtream login
func (c *Client) login(proxyUser, proxyPassword, proxyURL string, proxyPort int, protocol string) (login, error) {
	cleanURL := strings.TrimPrefix(proxyURL, "http://")
	cleanURL = strings.TrimPrefix(cleanURL, "https://")
	if h, _, err := net.SplitHostPort(cleanURL); err == nil {
		cleanURL = h
	}

	req := login{
		UserInfo: xtream.UserInfo{
			Username:             proxyUser,
			Password:             proxyPassword,
			Message:              c.UserInfo.Message,
			Auth:                 c.UserInfo.Auth,
			Status:               c.UserInfo.Status,
			ExpDate:              c.UserInfo.ExpDate,
			IsTrial:              c.UserInfo.IsTrial,
			ActiveConnections:    c.UserInfo.ActiveConnections,
			CreatedAt:            c.UserInfo.CreatedAt,
			MaxConnections:       c.UserInfo.MaxConnections,
			AllowedOutputFormats: c.UserInfo.AllowedOutputFormats,
		},
		ServerInfo: xtream.ServerInfo{
			URL:          cleanURL,
			Port:         xtream.FlexInt(proxyPort),
			HTTPSPort:    xtream.FlexInt(proxyPort),
			Protocol:     protocol,
			RTMPPort:     xtream.FlexInt(proxyPort),
			Timezone:     c.ServerInfo.Timezone,
			TimestampNow: c.ServerInfo.TimestampNow,
			TimeNow:      c.ServerInfo.TimeNow,
		},
	}

	return req, nil
}



// Action execute an xtream action.
func (c *Client) Action(config *config.ProxyConfig, action string, q url.Values) (respBody interface{}, httpcode int, err error) {
	log.Printf("[xtream-proxy] Action called: '%s' with params: %v", action, q)
	protocol := "http"
	if config.HTTPS {
		protocol = "https"
	}

	switch action {
	case getLiveCategories:
		cats, err2 := c.getCachedLiveCategories()
		if err2 == nil {
			var filtered []xtream.Category
			for _, cat := range cats {
				if config.Filters.IsAllowed("live", cat.Name) {
					filtered = append(filtered, cat)
				}
			}
			respBody = filtered
		}
		err = err2
	case getLiveStreams:
		categoryID := ""
		if len(q["category_id"]) > 0 {
			categoryID = q["category_id"][0]
		}

		cats, _ := c.getCachedLiveCategories()
		allowedCatIDs := make(map[int64]bool)
		hasFilter := false
		if len(cats) > 0 {
			for _, cat := range cats {
				if config.Filters.IsAllowed("live", cat.Name) {
					allowedCatIDs[int64(cat.ID)] = true
				} else {
					hasFilter = true
				}
			}
		}

		if categoryID != "" && hasFilter {
			catInt, convErr := strconv.ParseInt(categoryID, 10, 64)
			if convErr == nil && !allowedCatIDs[catInt] {
				respBody = []xtream.Stream{}
				break
			}
		}

		streams, err2 := c.GetLiveStreams(categoryID)
		if err2 == nil {
			if hasFilter && categoryID == "" {
				filtered := make([]xtream.Stream, 0, len(streams))
				for _, stream := range streams {
					if allowedCatIDs[int64(stream.CategoryID)] {
						filtered = append(filtered, stream)
					}
				}
				respBody = filtered
			} else {
				respBody = streams
			}
		}
		err = err2
	case getVodCategories:
		cats, err2 := c.getCachedVodCategories()
		if err2 == nil {
			var filtered []xtream.Category
			for _, cat := range cats {
				if config.Filters.IsAllowed("vod", cat.Name) {
					filtered = append(filtered, cat)
				}
			}
			respBody = filtered
		}
		err = err2
	case getVodStreams:
		categoryID := ""
		if len(q["category_id"]) > 0 {
			categoryID = q["category_id"][0]
		}

		cats, _ := c.getCachedVodCategories()
		allowedCatIDs := make(map[int64]bool)
		hasFilter := false
		if len(cats) > 0 {
			for _, cat := range cats {
				if config.Filters.IsAllowed("vod", cat.Name) {
					allowedCatIDs[int64(cat.ID)] = true
				} else {
					hasFilter = true
				}
			}
		}

		if categoryID != "" && hasFilter {
			catInt, convErr := strconv.ParseInt(categoryID, 10, 64)
			if convErr == nil && !allowedCatIDs[catInt] {
				respBody = []xtream.Stream{}
				break
			}
		}

		streams, err2 := c.GetVideoOnDemandStreams(categoryID)
		if err2 == nil {
			if hasFilter && categoryID == "" {
				filtered := make([]xtream.Stream, 0, len(streams))
				for _, stream := range streams {
					if allowedCatIDs[int64(stream.CategoryID)] {
						filtered = append(filtered, stream)
					}
				}
				respBody = filtered
			} else {
				respBody = streams
			}
		}
		err = err2
	case getVodInfo:
		httpcode, err = validateParams(q, "vod_id")
		if err != nil {
			return
		}
		vodID := q["vod_id"][0]
		respBody, err = c.GetVideoOnDemandInfo(vodID)
		if err != nil {
			log.Printf("[xtream-proxy] Error getting VOD info for vod_id %s: %v", vodID, err)

			// Tolerant fallback: perform a raw HTTP GET to the player_api.php and return
			// the parsed JSON when typed unmarshal in the library fails (often due
			// to mixed number/string types for numeric fields).
			rawURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_vod_info&vod_id=%s", c.BaseURL, c.Username, c.Password, url.QueryEscape(vodID))
			req, rerr := http.NewRequest("GET", rawURL, nil)
			if rerr != nil {
				log.Printf("[xtream-proxy] Error creating raw request for VOD info: %v", rerr)
				// return original error
				return
			}
			req.Header.Set("User-Agent", c.UserAgent)
			req = req.WithContext(c.Context)

			resp, derr := c.HTTP.Do(req)
			if derr != nil {
				log.Printf("[xtream-proxy] Error performing raw request for VOD info: %v", derr)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode > 399 {
				log.Printf("[xtream-proxy] Raw VOD info request returned status %d", resp.StatusCode)
				return
			}

			body, rerr := ioutil.ReadAll(resp.Body)
			if rerr != nil {
				log.Printf("[xtream-proxy] Error reading raw VOD info response: %v", rerr)
				return
			}

			var generic interface{}
			if jerr := json.Unmarshal(body, &generic); jerr != nil {
				log.Printf("[xtream-proxy] Error unmarshalling raw VOD info JSON: %v", jerr)
				return
			}

			log.Printf("[xtream-proxy] Returning tolerant raw VOD info for vod_id %s", vodID)
			respBody = generic
			// clear the original error since we succeeded with the fallback
			err = nil
		}
	case getSeriesCategories:
		log.Printf("[xtream-proxy] Getting series categories...")
		c.seriesCatCacheMu.RLock()
		if len(c.seriesCatCache.categories) > 0 && time.Now().Before(c.seriesCatCache.expiresAt) {
			cachedCats := c.seriesCatCache.categories
			c.seriesCatCacheMu.RUnlock()
			var filtered []xtream.Category
			for _, cat := range cachedCats {
				if config.Filters.IsAllowed("series", cat.Name) {
					filtered = append(filtered, cat)
				}
			}
			respBody = filtered
			if categories, ok := respBody.([]xtream.Category); ok {
				log.Printf("[xtream-proxy] Found %d series categories (cached)", len(categories))
			}
			break
		}
		c.seriesCatCacheMu.RUnlock()

		var cats []xtream.Category
		var err2 error
		for attempt := 1; attempt <= 3; attempt++ {
			cats, err2 = c.GetSeriesCategories()
			if err2 == nil {
				break
			}
			log.Printf("[xtream-proxy] Attempt %d to get series categories failed: %v, retrying...", attempt, err2)
			time.Sleep(time.Duration(attempt*300) * time.Millisecond)
		}
		err = err2
		if err == nil {
			c.seriesCatCacheMu.Lock()
			c.seriesCatCache = seriesCatCacheEntry{
				categories: cats,
				expiresAt:  time.Now().Add(15 * time.Minute),
			}
			c.seriesCatCacheMu.Unlock()

			var filtered []xtream.Category
			for _, cat := range cats {
				if config.Filters.IsAllowed("series", cat.Name) {
					filtered = append(filtered, cat)
				}
			}
			respBody = filtered
			if categories, ok := respBody.([]xtream.Category); ok {
				log.Printf("[xtream-proxy] Found %d series categories (%d total upstream)", len(categories), len(cats))
			}
		}
	case getSeries:
		categoryID := ""
		if len(q["category_id"]) > 0 {
			categoryID = q["category_id"][0]
		}
		log.Printf("[xtream-proxy] Getting series for category: '%s'", categoryID)

		// If no category_id is provided, get series from all categories
		if categoryID == "" {
			c.seriesCacheMu.RLock()
			if len(c.seriesCache.series) > 0 && time.Now().Before(c.seriesCache.expiresAt) {
				cachedSeries := c.seriesCache.series
				c.seriesCacheMu.RUnlock()
				log.Printf("[xtream-proxy] Returning %d series from cache", len(cachedSeries))
				respBody = cachedSeries
				break
			}
			c.seriesCacheMu.RUnlock()

			// 1. First attempt: Try getting all series in a single bulk request
			log.Printf("[xtream-proxy] No category specified, attempting single bulk get_series call...")
			bulkSeries, bulkErr := c.GetSeries("")
			if bulkErr == nil && len(bulkSeries) > 0 {
				log.Printf("[xtream-proxy] Bulk get_series succeeded: %d series returned", len(bulkSeries))
				// Filter bulk series by allowed categories if filtering is configured
				filteredSeries := bulkSeries
				cats, catErr := c.GetSeriesCategories()
				if catErr == nil && len(cats) > 0 {
					allowedCatIDs := make(map[int]bool)
					hasFilter := false
					for _, cat := range cats {
						if config.Filters.IsAllowed("series", cat.Name) {
							allowedCatIDs[int(cat.ID)] = true
						} else {
							hasFilter = true
						}
					}
					if hasFilter {
						var kept []xtream.SeriesInfo
						for _, s := range bulkSeries {
							if s.CategoryID != nil && allowedCatIDs[int(*s.CategoryID)] {
								kept = append(kept, s)
							}
						}
						filteredSeries = kept
						log.Printf("[xtream-proxy] Filtered bulk series: kept %d of %d based on allowed categories", len(filteredSeries), len(bulkSeries))
					}
				}

				c.seriesCacheMu.Lock()
				c.seriesCache = seriesCacheEntry{
					series:    filteredSeries,
					expiresAt: time.Now().Add(15 * time.Minute),
				}
				c.seriesCacheMu.Unlock()

				respBody = filteredSeries
				break
			}

			// 2. Fallback to category-by-category approach if bulk fails or returns empty
			log.Printf("[xtream-proxy] Bulk get_series returned 0 or failed (%v), falling back to category-by-category...", bulkErr)
			var categories []xtream.Category
			for attempt := 1; attempt <= 3; attempt++ {
				categories, err = c.GetSeriesCategories()
				if err == nil {
					break
				}
				log.Printf("[xtream-proxy] Attempt %d to get series categories failed: %v, retrying...", attempt, err)
				time.Sleep(time.Duration(attempt*300) * time.Millisecond)
			}
			if err != nil {
				log.Printf("[xtream-proxy] Error getting series categories: %v", err)
				return nil, http.StatusInternalServerError, err
			}

			// Filter categories FIRST: only fetch series for allowed categories!
			var targetCategories []xtream.Category
			for _, cat := range categories {
				if config.Filters.IsAllowed("series", cat.Name) {
					targetCategories = append(targetCategories, cat)
				}
			}
			log.Printf("[xtream-proxy] Fetching series for %d allowed categories (out of %d total upstream categories)", len(targetCategories), len(categories))

			var allSeries []xtream.SeriesInfo
			var mu sync.Mutex
			successCount := 0
			errorCount := 0

			// Concurrency limit set to 4 to protect SOCKS proxy from connection/auth exhaustion
			var wg sync.WaitGroup
			sem := make(chan struct{}, 4)

			for _, category := range targetCategories {
				wg.Add(1)
				sem <- struct{}{}
				go func(cat xtream.Category) {
					defer wg.Done()
					defer func() { <-sem }()

					var categorySeries []xtream.SeriesInfo
					var catErr error
					for attempt := 1; attempt <= 3; attempt++ {
						categorySeries, catErr = c.GetSeries(fmt.Sprint(cat.ID))
						if catErr == nil {
							break
						}
						log.Printf("[xtream-proxy] Attempt %d failed for category %d (%s): %v, retrying...", attempt, cat.ID, cat.Name, catErr)
						time.Sleep(time.Duration(attempt*400) * time.Millisecond)
					}

					mu.Lock()
					defer mu.Unlock()
					if catErr != nil {
						errorCount++
						log.Printf("[xtream-proxy] Error getting series for category %d (%s) after 3 attempts: %v", cat.ID, cat.Name, catErr)
						return
					}
					if len(categorySeries) > 0 {
						allSeries = append(allSeries, categorySeries...)
						successCount++
						log.Printf("[xtream-proxy] Added %d series from category: %s", len(categorySeries), cat.Name)
					} else {
						log.Printf("[xtream-proxy] No series found in category: %s", cat.Name)
					}
				}(category)
			}
			wg.Wait()
			close(sem)

			log.Printf("[xtream-proxy] Series loading complete: %d categories successful, %d failed, %d total series", successCount, errorCount, len(allSeries))
			if len(allSeries) > 0 {
				c.seriesCacheMu.Lock()
				c.seriesCache = seriesCacheEntry{
					series:    allSeries,
					expiresAt: time.Now().Add(15 * time.Minute),
				}
				c.seriesCacheMu.Unlock()
			}
			respBody = allSeries
		} else {
			// Category specified, verify if allowed before fetching
			log.Printf("[xtream-proxy] Getting series for specific category: %s", categoryID)
			cats, _ := c.GetSeriesCategories()
			if len(cats) > 0 {
				catInt, convErr := strconv.Atoi(categoryID)
				if convErr == nil {
					isAllowed := true
					hasFilter := false
					for _, cat := range cats {
						if !config.Filters.IsAllowed("series", cat.Name) {
							hasFilter = true
						}
						if int(cat.ID) == catInt {
							isAllowed = config.Filters.IsAllowed("series", cat.Name)
						}
					}
					if hasFilter && !isAllowed {
						respBody = []xtream.SeriesInfo{}
						break
					}
				}
			}

			var specificSeries []xtream.SeriesInfo
			var specificErr error
			for attempt := 1; attempt <= 3; attempt++ {
				specificSeries, specificErr = c.GetSeries(categoryID)
				if specificErr == nil {
					break
				}
				log.Printf("[xtream-proxy] Attempt %d failed for category %s: %v, retrying...", attempt, categoryID, specificErr)
				time.Sleep(time.Duration(attempt*400) * time.Millisecond)
			}
			err = specificErr
			if err != nil {
				log.Printf("[xtream-proxy] Error getting series for category %s after 3 attempts: %v", categoryID, err)
			} else {
				log.Printf("[xtream-proxy] Found %d series in category %s", len(specificSeries), categoryID)
				respBody = specificSeries
			}
		}
	case getSerieInfo:
		httpcode, err = validateParams(q, "series_id")
		if err != nil {
			return
		}
		seriesID := q["series_id"][0]

		respBody, err = c.fetchRawSeriesInfo(seriesID)
		if err != nil {
			log.Printf("[xtream-proxy] Error fetching raw series info for series_id %s: %v", seriesID, err)
			httpcode = http.StatusBadGateway
			return
		}
	case getShortEPG:
		limit := 0

		httpcode, err = validateParams(q, "stream_id")
		if err != nil {
			return
		}
		if len(q["limit"]) > 0 && q["limit"][0] != "" {
			limit, err = strconv.Atoi(q["limit"][0])
			if err != nil {
				log.Printf("[xtream-proxy] Error parsing limit '%s': %v", q["limit"][0], err)
				httpcode = http.StatusInternalServerError
				return
			}
		}
		respBody, err = c.GetShortEPG(q["stream_id"][0], limit)
	case getSimpleDataTable:
		httpcode, err = validateParams(q, "stream_id")
		if err != nil {
			return
		}
		respBody, err = c.GetEPG(q["stream_id"][0])
	default:
		respBody, err = c.login(config.User.String(), config.Password.String(), config.HostConfig.Hostname, config.AdvertisedPort, protocol)
	}

	return
}

func (c *Client) fetchRawSeriesInfo(seriesID string) (interface{}, error) {
	rawURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_series_info&series_id=%s", c.BaseURL, c.Username, c.Password, url.QueryEscape(seriesID))
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req = req.WithContext(c.Context)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 399 {
		return nil, fmt.Errorf("raw series info request returned status %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var generic interface{}
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, err
	}

	return generic, nil
}

func validateParams(u url.Values, params ...string) (int, error) {
	for _, p := range params {
		if len(u[p]) < 1 {
			return http.StatusBadRequest, fmt.Errorf("missing %q", p)
		}

	}

	return 0, nil
}
