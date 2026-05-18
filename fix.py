import sys
import re

# Fix server.go
with open('pkg/server/server.go', 'r', encoding='utf-8') as f:
    content = f.read()

if '"sync"' not in content:
    content = content.replace(
        '"strings"\n\t"time"',
        '"strings"\n\t"sync"\n\t"time"'
    )

with open('pkg/server/server.go', 'w', encoding='utf-8') as f:
    f.write(content)

# Fix xtreamHandles.go
with open('pkg/server/xtreamHandles.go', 'r', encoding='utf-8') as f:
    content = f.read()

# I want to replace `userAgent` back to `ctx.Request.UserAgent()` in xtreamPlayerAPI, xtreamXMLTV, etc.
# The only place where `userAgent` should be used as `userAgent` in `xtreamapi.New` is inside `xtreamGenerateM3u`
# Let's replace ALL instances of `userAgent` back to `ctx.Request.UserAgent()`, and then manually fix `xtreamGenerateM3u`

content = content.replace(
    'client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, userAgent)',
    'client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())'
)

# Now fix xtreamGenerateM3u
old_generate = """func (c *Config) xtreamGenerateM3u(userAgent string, extension string) (*m3u.Playlist, error) {
	log.Printf("[iptv-proxy] xtreamGenerateM3u called with extension: %s", extension)

	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())"""
new_generate = """func (c *Config) xtreamGenerateM3u(userAgent string, extension string) (*m3u.Playlist, error) {
	log.Printf("[iptv-proxy] xtreamGenerateM3u called with extension: %s", extension)

	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, userAgent)"""
content = content.replace(old_generate, new_generate)
content = content.replace(old_generate.replace('\n', '\r\n'), new_generate)

# Also fix the background goroutine in xtreamXMLTV
# It currently has: `client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())`
# But it runs in a goroutine where ctx might be unsafe. Wait, we can define userAgent locally.
old_xmltv_bg = """		if isExpired {
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
					client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())"""
new_xmltv_bg = """		if isExpired {
			c.refreshingMutex.Lock()
			if !c.refreshing[cacheKey] {
				c.refreshing[cacheKey] = true
				c.refreshingMutex.Unlock()
				userAgent := ctx.Request.UserAgent()

				go func() {
					defer func() {
						c.refreshingMutex.Lock()
						c.refreshing[cacheKey] = false
						c.refreshingMutex.Unlock()
					}()

					log.Printf("[iptv-proxy] Background XMLTV refresh starting...")
					client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, userAgent)"""
content = content.replace(old_xmltv_bg, new_xmltv_bg)
content = content.replace(old_xmltv_bg.replace('\n', '\r\n'), new_xmltv_bg)


with open('pkg/server/xtreamHandles.go', 'w', encoding='utf-8') as f:
    f.write(content)
print("Done")
