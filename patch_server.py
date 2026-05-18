import sys

file_path = 'pkg/server/server.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

old_config_struct = """	refreshing      map[string]bool
	refreshingMutex sync.Mutex
}"""

new_config_struct = """	refreshing      map[string]bool
	refreshingMutex sync.Mutex

	Filters *Filters
}"""
content = content.replace(old_config_struct, new_config_struct)
content = content.replace(old_config_struct.replace('\n', '\r\n'), new_config_struct)

old_init = """	cfg.metadataCache = newResponseCache(config.MetadataCacheTTL)
	cfg.xmltvCache = newResponseCache(config.XMLTVCacheTTL)
	cfg.httpClient = newUpstreamHTTPClient(cfg)

	return cfg, nil"""
new_init = """	cfg.metadataCache = newResponseCache(config.MetadataCacheTTL)
	cfg.xmltvCache = newResponseCache(config.XMLTVCacheTTL)
	cfg.httpClient = newUpstreamHTTPClient(cfg)
	cfg.Filters = NewFilters("filters.json")

	return cfg, nil"""
content = content.replace(old_init, new_init)
content = content.replace(old_init.replace('\n', '\r\n'), new_init)

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)
print("Patched server.go")
