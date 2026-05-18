import sys

file_path = 'pkg/xtream-proxy/xtream-proxy.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

# Add strings import if missing
if '"strings"' not in content:
    content = content.replace('"strconv"', '"strconv"\n\t"strings"')

helper = """func isAllowedCategory(categoryName string, allowedPrefixes []string) bool {
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
}

// Action execute an xtream action."""

if 'func isAllowedCategory' not in content:
    content = content.replace("// Action execute an xtream action.", helper)

# Patch getLiveCategories
old_live_cat = """	case getLiveCategories:
		respBody, err = c.GetLiveCategories()"""
new_live_cat = """	case getLiveCategories:
		cats, err2 := c.GetLiveCategories()
		if err2 == nil {
			var filtered []xtream.Category
			for _, cat := range cats {
				if isAllowedCategory(cat.Name, config.AllowedLiveCategories) {
					filtered = append(filtered, cat)
				}
			}
			respBody = filtered
		}
		err = err2"""
if 'isAllowedCategory(cat.Name, config.AllowedLiveCategories)' not in content:
    content = content.replace(old_live_cat, new_live_cat)
    content = content.replace(old_live_cat.replace('\n', '\r\n'), new_live_cat)

# Patch getVodCategories
old_vod_cat = """	case getVodCategories:
		respBody, err = c.GetVideoOnDemandCategories()"""
new_vod_cat = """	case getVodCategories:
		cats, err2 := c.GetVideoOnDemandCategories()
		if err2 == nil {
			var filtered []xtream.Category
			for _, cat := range cats {
				if isAllowedCategory(cat.Name, config.AllowedVODCategories) {
					filtered = append(filtered, cat)
				}
			}
			respBody = filtered
		}
		err = err2"""
if 'isAllowedCategory(cat.Name, config.AllowedVODCategories)' not in content:
    content = content.replace(old_vod_cat, new_vod_cat)
    content = content.replace(old_vod_cat.replace('\n', '\r\n'), new_vod_cat)

# Patch getSeriesCategories
old_series_cat = """	case getSeriesCategories:
		log.Printf("[xtream-proxy] Getting series categories...")
		respBody, err = c.GetSeriesCategories()
		if err == nil {
			if categories, ok := respBody.([]xtream.Category); ok {"""
new_series_cat = """	case getSeriesCategories:
		log.Printf("[xtream-proxy] Getting series categories...")
		cats, err2 := c.GetSeriesCategories()
		err = err2
		if err == nil {
			var filtered []xtream.Category
			for _, cat := range cats {
				if isAllowedCategory(cat.Name, config.AllowedSeriesCategories) {
					filtered = append(filtered, cat)
				}
			}
			respBody = filtered
			if categories, ok := respBody.([]xtream.Category); ok {"""
if 'isAllowedCategory(cat.Name, config.AllowedSeriesCategories)' not in content:
    content = content.replace(old_series_cat, new_series_cat)
    content = content.replace(old_series_cat.replace('\n', '\r\n'), new_series_cat)

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

print("Patched xtream-proxy.go")
