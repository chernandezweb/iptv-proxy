import sys

file_path = 'cmd/root.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

helper = """func parseEnvSlice(envKey string) []string {
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
}

func initConfig() {"""

if 'parseEnvSlice' not in content:
    content = content.replace("func initConfig() {", helper)

old_conf = """			AllowedLiveCategories: viper.GetStringSlice("allowed-live-categories"),
			AllowedVODCategories:  viper.GetStringSlice("allowed-vod-categories"),
			AllowedSeriesCategories: viper.GetStringSlice("allowed-series-categories"),"""

new_conf = """			AllowedLiveCategories: parseEnvSlice("ALLOWED_LIVE_CATEGORIES"),
			AllowedVODCategories:  parseEnvSlice("ALLOWED_VOD_CATEGORIES"),
			AllowedSeriesCategories: parseEnvSlice("ALLOWED_SERIES_CATEGORIES"),"""

content = content.replace(old_conf, new_conf)
content = content.replace(old_conf.replace('\n', '\r\n'), new_conf)

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

print("Patched env parser")
