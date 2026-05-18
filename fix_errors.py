import sys

# 1. Update admin.go
file_path = 'pkg/server/admin.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

# Add config import
if '"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"' not in content:
    content = content.replace('"github.com/gin-gonic/gin"', '"github.com/gin-gonic/gin"\n\t"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"')

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

# 2. Update server.go
file_path = 'pkg/server/server.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

# Fix parameter name shadowing
content = content.replace("func NewServer(config *config.ProxyConfig) (*Config, error) {", "func NewServer(cfgData *config.ProxyConfig) (*Config, error) {")
content = content.replace("ProxyConfig:          config,", "ProxyConfig:          cfgData,")
content = content.replace("playlist:             &p,", "playlist:             &p,")
content = content.replace("endpointAntiColision: endpointAntiColision,", "endpointAntiColision: endpointAntiColision,")

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

# 3. Update xtreamHandles.go
file_path = 'pkg/server/xtreamHandles.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

content = content.replace("if isAllowedCategory(groupTitle, c.AllowedLiveCategories) {", "if c.ProxyConfig.Filters.IsAllowed(\"live\", groupTitle) {")
content = content.replace("if isAllowedCategory(groupTitle, c.AllowedVODCategories) {", "if c.ProxyConfig.Filters.IsAllowed(\"vod\", groupTitle) {")
content = content.replace("if isAllowedCategory(groupTitle, c.AllowedSeriesCategories) {", "if c.ProxyConfig.Filters.IsAllowed(\"series\", groupTitle) {")

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

print("Fixed compiler errors")
