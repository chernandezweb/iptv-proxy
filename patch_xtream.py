import sys

# 1. Update pkg/server/xtreamHandles.go
file_path = 'pkg/server/xtreamHandles.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

# Remove isAllowedCategory function from xtreamHandles.go
isAllowedFunc = """func isAllowedCategory(categoryName string, allowedPrefixes []string) bool {
	if len(allowedPrefixes) == 0 {
		return true // Allow all if not configured
	}
	nameUpper := strings.ToUpper(categoryName)
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(nameUpper, strings.ToUpper(prefix)) {
			return true
		}
	}
	return false
}"""
content = content.replace(isAllowedFunc, "")
content = content.replace(isAllowedFunc.replace('\n', '\r\n'), "")

# Replace usage in xtreamHandles.go
content = content.replace("!isAllowedCategory(category.Name, c.AllowedLiveCategories)", "!c.ProxyConfig.Filters.IsAllowed(\"live\", category.Name)")
content = content.replace("!isAllowedCategory(category.Name, c.AllowedVODCategories)", "!c.ProxyConfig.Filters.IsAllowed(\"vod\", category.Name)")
content = content.replace("!isAllowedCategory(category.Name, c.AllowedSeriesCategories)", "!c.ProxyConfig.Filters.IsAllowed(\"series\", category.Name)")

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

# 2. Update pkg/xtream-proxy/xtream-proxy.go
file_path = 'pkg/xtream-proxy/xtream-proxy.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

# Remove isAllowedCategory function from xtream-proxy.go
content = content.replace(isAllowedFunc, "")
content = content.replace(isAllowedFunc.replace('\n', '\r\n'), "")

# Replace usage in xtream-proxy.go
content = content.replace("isAllowedCategory(cat.Name, config.AllowedLiveCategories)", "config.Filters.IsAllowed(\"live\", cat.Name)")
content = content.replace("isAllowedCategory(cat.Name, config.AllowedVODCategories)", "config.Filters.IsAllowed(\"vod\", cat.Name)")
content = content.replace("isAllowedCategory(cat.Name, config.AllowedSeriesCategories)", "config.Filters.IsAllowed(\"series\", cat.Name)")

# Also in getLiveStreams check, we need to check len of filters. Wait, GetLiveCategories for streams filter:
old_len_check = "if len(config.AllowedLiveCategories) > 0 {"
new_len_check = "if len(config.Filters.Data.AllowedLiveCategories) > 0 {"
content = content.replace(old_len_check, new_len_check)

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

# 3. Update cmd/root.go to remove flags and init
file_path = 'cmd/root.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

# Remove the allowed categories from struct
old_flags_struct = """			AllowedLiveCategories: parseEnvSlice("ALLOWED_LIVE_CATEGORIES"),
			AllowedVODCategories:  parseEnvSlice("ALLOWED_VOD_CATEGORIES"),
			AllowedSeriesCategories: parseEnvSlice("ALLOWED_SERIES_CATEGORIES"),"""
content = content.replace(old_flags_struct, "")
content = content.replace(old_flags_struct.replace('\n', '\r\n'), "")

# Remove parseEnvSlice
parseEnvFunc = """func parseEnvSlice(envKey string) []string {
	val := os.Getenv(envKey)
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	var res []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			res = append(res, p)
		}
	}
	return res
}"""
content = content.replace(parseEnvFunc, "")
content = content.replace(parseEnvFunc.replace('\n', '\r\n'), "")

# Remove string slices from flags
old_flags_reg = """	rootCmd.Flags().StringSlice("allowed-live-categories", []string{}, "Comma-separated list of allowed live category prefixes (e.g. 'US|,CA|'). Empty means allow all.")
	rootCmd.Flags().StringSlice("allowed-vod-categories", []string{}, "Comma-separated list of allowed VOD category prefixes. Empty means allow all.")
	rootCmd.Flags().StringSlice("allowed-series-categories", []string{}, "Comma-separated list of allowed series category prefixes. Empty means allow all.")"""
content = content.replace(old_flags_reg, "")
content = content.replace(old_flags_reg.replace('\n', '\r\n'), "")

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

print("Patched xtream files and root")
