import sys
import re

with open('pkg/server/xtreamHandles.go', 'r', encoding='utf-8') as f:
    content = f.read()

# 1. Update xtreamGenerateM3u signature
content = content.replace(
    'func (c *Config) xtreamGenerateM3u(ctx *gin.Context, extension string) (*m3u.Playlist, error) {',
    'func (c *Config) xtreamGenerateM3u(userAgent string, extension string) (*m3u.Playlist, error) {'
)
content = content.replace(
    'client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())',
    'client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, userAgent)'
)

# 2. Update xtreamXMLTV
old_xmltv = """func (c *Config) xtreamXMLTV(ctx *gin.Context) {
	if entry, ok := c.xmltvCache.Get(ctx.Request.URL.RawQuery); ok {
		log.Printf("[iptv-proxy] %v | %s | xmltv.php cache hit\\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		ctx.Data(http.StatusOK, entry.contentType, entry.payload)
		return
	}

	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	resp, err := client.GetXMLTV()
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.xmltvCache.Set(ctx.Request.URL.RawQuery, resp, "application/xml")
	ctx.Data(http.StatusOK, "application/xml", resp)
}"""
new_xmltv = """func (c *Config) xtreamXMLTV(ctx *gin.Context) {
	cacheKey := "xmltv_cache_key"
	entry, ok, isExpired := c.xmltvCache.Get(cacheKey)

	if ok {
		log.Printf("[iptv-proxy] %v | %s | xmltv.php cache hit (expired: %v)\\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP(), isExpired)
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
					client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())
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

	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())
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
}"""

# Handle potential \r\n vs \n differences
content = content.replace(old_xmltv, new_xmltv)
content = content.replace(old_xmltv.replace('\n', '\r\n'), new_xmltv)

# 3. Update xtreamGet
old_xtreamGet = """	xtreamM3uCacheLock.RLock()
	meta, ok := xtreamM3uCache[m3uURL.String()]
	d := time.Since(meta.Time)
	if !ok || d.Hours() >= float64(c.M3UCacheExpiration) {
		log.Printf("[iptv-proxy] %v | %s | xtream cache m3u file\\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		xtreamM3uCacheLock.RUnlock()
		playlist, err := m3u.Parse(m3uURL.String())
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(&playlist, m3uURL.String()); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}
	} else {
		xtreamM3uCacheLock.RUnlock()
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	path := xtreamM3uCache[m3uURL.String()].string
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(path)"""
new_xtreamGet = """	xtreamM3uCacheLock.RLock()
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

	log.Printf("[iptv-proxy] %v | %s | xtream cache m3u file\\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
	playlist, err := m3u.Parse(m3uURL.String())
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	if err := c.cacheXtreamM3u(&playlist, m3uURL.String()); err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	path := xtreamM3uCache[m3uURL.String()].string
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(path)"""

content = content.replace(old_xtreamGet, new_xtreamGet)
content = content.replace(old_xtreamGet.replace('\n', '\r\n'), new_xtreamGet)


# 4. Update xtreamApiGet
old_xtreamApiGet = """	xtreamM3uCacheLock.RLock()
	meta, ok := xtreamM3uCache[cacheName]
	d := time.Since(meta.Time)
	if !ok || d.Hours() >= float64(c.M3UCacheExpiration) {
		log.Printf("[iptv-proxy] %v | %s | xtream cache API m3u file\\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		xtreamM3uCacheLock.RUnlock()
		playlist, err := c.xtreamGenerateM3u(ctx, extension)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(playlist, cacheName); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
			return
		}
	} else {
		xtreamM3uCacheLock.RUnlock()
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	path := xtreamM3uCache[cacheName].string
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(path)"""
new_xtreamApiGet = """	xtreamM3uCacheLock.RLock()
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

	log.Printf("[iptv-proxy] %v | %s | xtream cache API m3u file\\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
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

	ctx.File(path)"""

content = content.replace(old_xtreamApiGet, new_xtreamApiGet)
content = content.replace(old_xtreamApiGet.replace('\n', '\r\n'), new_xtreamApiGet)


with open('pkg/server/xtreamHandles.go', 'w', encoding='utf-8') as f:
    f.write(content)
print("Done")
