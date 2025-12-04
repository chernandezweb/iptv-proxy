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
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"

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

// Client represent an xtream client
type Client struct {
	*xtream.XtreamClient
}

// New new xtream client
func New(user, password, baseURL, userAgent string) (*Client, error) {
	cli, err := xtream.NewClientWithUserAgent(context.Background(), user, password, baseURL, userAgent)
	if err != nil {
		return nil, err
	}

	return &Client{cli}, nil
}

type login struct {
	UserInfo   xtream.UserInfo   `json:"user_info"`
	ServerInfo xtream.ServerInfo `json:"server_info"`
}

// Login xtream login
func (c *Client) login(proxyUser, proxyPassword, proxyURL string, proxyPort int, protocol string) (login, error) {
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
			URL:          proxyURL,
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
		respBody, err = c.GetLiveCategories()
	case getLiveStreams:
		categoryID := ""
		if len(q["category_id"]) > 0 {
			categoryID = q["category_id"][0]
		}
		respBody, err = c.GetLiveStreams(categoryID)
	case getVodCategories:
		respBody, err = c.GetVideoOnDemandCategories()
	case getVodStreams:
		categoryID := ""
		if len(q["category_id"]) > 0 {
			categoryID = q["category_id"][0]
		}
		respBody, err = c.GetVideoOnDemandStreams(categoryID)
	case getVodInfo:
		httpcode, err = validateParams(q, "vod_id")
		if err != nil {
			return
		}
		respBody, err = c.GetVideoOnDemandInfo(q["vod_id"][0])
		if err != nil {
			log.Printf("[xtream-proxy] Error getting VOD info for vod_id %s: %v", q["vod_id"][0], err)
		}
	case getSeriesCategories:
		log.Printf("[xtream-proxy] Getting series categories...")
		respBody, err = c.GetSeriesCategories()
		if err == nil {
			if categories, ok := respBody.([]xtream.Category); ok {
				log.Printf("[xtream-proxy] Found %d series categories", len(categories))
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
			log.Printf("[xtream-proxy] No category specified, falling back to category-by-category approach...")
			categories, err := c.GetSeriesCategories()
			if err != nil {
				log.Printf("[xtream-proxy] Error getting series categories: %v", err)
				return nil, http.StatusInternalServerError, err
			}

			var allSeries []xtream.SeriesInfo
			var mu sync.Mutex
			successCount := 0
			errorCount := 0

			// Bounded concurrency to fetch categories in parallel and speed up total time
			var wg sync.WaitGroup
			sem := make(chan struct{}, 10) // concurrency limit

			for _, category := range categories {
				wg.Add(1)
				sem <- struct{}{}
				go func(cat xtream.Category) {
					defer wg.Done()
					defer func() { <-sem }()

					categorySeries, err := c.GetSeries(fmt.Sprint(cat.ID))
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						errorCount++
						log.Printf("[xtream-proxy] Error getting series for category %d (%s): %v", cat.ID, cat.Name, err)
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
			respBody = allSeries
		} else {
			// Category specified, try to get series for that specific category
			log.Printf("[xtream-proxy] Getting series for specific category: %s", categoryID)
			respBody, err = c.GetSeries(categoryID)
			if err != nil {
				log.Printf("[xtream-proxy] Error getting series for category %s: %v", categoryID, err)
			} else {
				if series, ok := respBody.([]xtream.SeriesInfo); ok {
					log.Printf("[xtream-proxy] Found %d series in category %s", len(series), categoryID)
				}
			}
		}
	case getSerieInfo:
		httpcode, err = validateParams(q, "series_id")
		if err != nil {
			return
		}
		respBody, err = c.GetSeriesInfo(q["series_id"][0])
		if err != nil {
			log.Printf("[xtream-proxy] Error getting series info for series_id %s: %v", q["series_id"][0], err)
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
		respBody, err = c.login(config.User.String(), config.Password.String(), protocol+"://"+config.HostConfig.Hostname, config.AdvertisedPort, protocol)
	}

	return
}

func validateParams(u url.Values, params ...string) (int, error) {
	for _, p := range params {
		if len(u[p]) < 1 {
			return http.StatusBadRequest, fmt.Errorf("missing %q", p)
		}

	}

	return 0, nil
}
