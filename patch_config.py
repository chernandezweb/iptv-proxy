import sys

# 1. Update filters.go package name
file_path = 'pkg/config/filters.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

content = content.replace("package server", "package config")
with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

# 2. Update config.go to include Filters struct
file_path = 'pkg/config/config.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

old_config = """	AllowedLiveCategories []string
	AllowedVODCategories  []string
	AllowedSeriesCategories []string"""
new_config = """	Filters *Filters"""
content = content.replace(old_config, new_config)
content = content.replace(old_config.replace('\n', '\r\n'), new_config)

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

# 3. Update server.go to remove Filters from server.Config and set it in ProxyConfig
file_path = 'pkg/server/server.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

old_server_config = """	refreshing      map[string]bool
	refreshingMutex sync.Mutex

	Filters *Filters
}"""
new_server_config = """	refreshing      map[string]bool
	refreshingMutex sync.Mutex
}"""
content = content.replace(old_server_config, new_server_config)
content = content.replace(old_server_config.replace('\n', '\r\n'), new_server_config)

old_server_init = """	cfg.metadataCache = newResponseCache(config.MetadataCacheTTL)
	cfg.xmltvCache = newResponseCache(config.XMLTVCacheTTL)
	cfg.httpClient = newUpstreamHTTPClient(cfg)
	cfg.Filters = NewFilters("filters.json")"""
new_server_init = """	cfg.metadataCache = newResponseCache(config.MetadataCacheTTL)
	cfg.xmltvCache = newResponseCache(config.XMLTVCacheTTL)
	cfg.httpClient = newUpstreamHTTPClient(cfg)
	cfg.ProxyConfig.Filters = config.NewFilters("filters.json")"""
content = content.replace(old_server_init, new_server_init)
content = content.replace(old_server_init.replace('\n', '\r\n'), new_server_init)

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

# 4. Update admin.go
file_path = 'pkg/server/admin.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

content = content.replace("c.Filters.", "c.ProxyConfig.Filters.")
content = content.replace("FilterData", "config.FilterData")

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

print("Patched config and server")
