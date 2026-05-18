import os
import sys

# 1. Update config.go
config_file = 'pkg/config/config.go'
with open(config_file, 'r', encoding='utf-8') as f:
    config_content = f.read()

if 'AllowedLiveCategories' not in config_content:
    old_config = """	XMLTVCacheTTL        time.Duration
	M3UFileName          string"""
    new_config = """	XMLTVCacheTTL        time.Duration
	AllowedLiveCategories []string
	AllowedVODCategories  []string
	AllowedSeriesCategories []string
	M3UFileName          string"""
    config_content = config_content.replace(old_config, new_config)
    config_content = config_content.replace(old_config.replace('\n', '\r\n'), new_config)
    with open(config_file, 'w', encoding='utf-8') as f:
        f.write(config_content)


# 2. Update root.go
root_file = 'cmd/root.go'
with open(root_file, 'r', encoding='utf-8') as f:
    root_content = f.read()

if 'allowed-live-categories' not in root_content:
    old_root_flags = """	rootCmd.Flags().Duration("xmltv-cache-ttl", 30*time.Minute, "Cache duration for xmltv.php responses (set to 0 to disable)")"""
    new_root_flags = """	rootCmd.Flags().Duration("xmltv-cache-ttl", 30*time.Minute, "Cache duration for xmltv.php responses (set to 0 to disable)")
	rootCmd.Flags().StringSlice("allowed-live-categories", []string{}, "Comma-separated list of allowed live category prefixes (e.g. 'US|,CA|'). Empty means allow all.")
	rootCmd.Flags().StringSlice("allowed-vod-categories", []string{}, "Comma-separated list of allowed VOD category prefixes. Empty means allow all.")
	rootCmd.Flags().StringSlice("allowed-series-categories", []string{}, "Comma-separated list of allowed series category prefixes. Empty means allow all.")"""
    root_content = root_content.replace(old_root_flags, new_root_flags)
    root_content = root_content.replace(old_root_flags.replace('\n', '\r\n'), new_root_flags)

    old_root_conf = """			XMLTVCacheTTL:        viper.GetDuration("xmltv-cache-ttl"),
		}"""
    new_root_conf = """			XMLTVCacheTTL:        viper.GetDuration("xmltv-cache-ttl"),
			AllowedLiveCategories: viper.GetStringSlice("allowed-live-categories"),
			AllowedVODCategories:  viper.GetStringSlice("allowed-vod-categories"),
			AllowedSeriesCategories: viper.GetStringSlice("allowed-series-categories"),
		}"""
    root_content = root_content.replace(old_root_conf, new_root_conf)
    root_content = root_content.replace(old_root_conf.replace('\n', '\r\n'), new_root_conf)
    
    with open(root_file, 'w', encoding='utf-8') as f:
        f.write(root_content)


# 3. Update xtreamHandles.go
xtream_file = 'pkg/server/xtreamHandles.go'
with open(xtream_file, 'r', encoding='utf-8') as f:
    xtream_content = f.read()

if 'isAllowedCategory' not in xtream_content:
    # Insert helper function at the top after imports or somewhere safe, let's put it right before xtreamGenerateM3u
    old_generate_func = """func (c *Config) xtreamGenerateM3u(userAgent string, extension string) (*m3u.Playlist, error) {"""
    new_helper_and_generate = """func isAllowedCategory(categoryName string, allowedPrefixes []string) bool {
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

func (c *Config) xtreamGenerateM3u(userAgent string, extension string) (*m3u.Playlist, error) {"""
    xtream_content = xtream_content.replace(old_generate_func, new_helper_and_generate)
    xtream_content = xtream_content.replace(old_generate_func.replace('\n', '\r\n'), new_helper_and_generate)

    # Filter Live
    old_live = """	for _, category := range liveCat {
		live, err := client.GetLiveStreams(fmt.Sprint(category.ID))"""
    new_live = """	for _, category := range liveCat {
		if !isAllowedCategory(category.Name, c.AllowedLiveCategories) {
			continue
		}
		live, err := client.GetLiveStreams(fmt.Sprint(category.ID))"""
    xtream_content = xtream_content.replace(old_live, new_live)
    xtream_content = xtream_content.replace(old_live.replace('\n', '\r\n'), new_live)

    # Filter VOD
    old_vod = """	for _, category := range vodCat {
		vods, err := client.GetVideoOnDemandStreams(fmt.Sprint(category.ID))"""
    new_vod = """	for _, category := range vodCat {
		if !isAllowedCategory(category.Name, c.AllowedVODCategories) {
			continue
		}
		vods, err := client.GetVideoOnDemandStreams(fmt.Sprint(category.ID))"""
    xtream_content = xtream_content.replace(old_vod, new_vod)
    xtream_content = xtream_content.replace(old_vod.replace('\n', '\r\n'), new_vod)

    # Filter Series
    old_series = """	for _, category := range seriesCat {
		series, err := client.GetSeries(fmt.Sprint(category.ID))"""
    new_series = """	for _, category := range seriesCat {
		if !isAllowedCategory(category.Name, c.AllowedSeriesCategories) {
			continue
		}
		series, err := client.GetSeries(fmt.Sprint(category.ID))"""
    xtream_content = xtream_content.replace(old_series, new_series)
    xtream_content = xtream_content.replace(old_series.replace('\n', '\r\n'), new_series)

    # And we also need to filter in standard m3u generation inside xtreamGet and xtreamApiGet?
    # Actually, the user's provider is Xtream. The proxy's standard m3u `xtreamGet` just proxies `get.php`.
    # Wait, the `get.php` payload is the standard raw M3U. Filtering the raw M3U is trickier.
    # The `xtreamApiGet` is the one they are using because it builds the M3U from the API (`xtreamGenerateM3u`).
    # If they use `get.php`, we can also filter it using the m3u struct.
    # Let's add filtering for the raw M3U too, so it applies everywhere.
    
    # In xtreamGet:
    old_xtreamGet_m3uParse = """	playlist, err := m3u.Parse(m3uURL.String())
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	if err := c.cacheXtreamM3u(&playlist, m3uURL.String()); err != nil {"""
    
    new_xtreamGet_m3uParse = """	playlist, err := m3u.Parse(m3uURL.String())
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	
	// Apply filters to raw M3U
	var filteredTracks []m3u.Track
	for _, track := range playlist.Tracks {
		groupTitle := ""
		for _, tag := range track.Tags {
			if tag.Name == "group-title" {
				groupTitle = tag.Value
				break
			}
		}
		// Since we don't know if a raw M3U track is Live/VOD/Series easily, we'll use AllowedLiveCategories as the global filter for raw M3U
		if isAllowedCategory(groupTitle, c.AllowedLiveCategories) {
			filteredTracks = append(filteredTracks, track)
		}
	}
	playlist.Tracks = filteredTracks

	if err := c.cacheXtreamM3u(&playlist, m3uURL.String()); err != nil {"""

    # Note: xtreamGet is called twice in the logic (first block is the cache miss in bg routine, second is cache miss in fg)
    xtream_content = xtream_content.replace(old_xtreamGet_m3uParse, new_xtreamGet_m3uParse)
    xtream_content = xtream_content.replace(old_xtreamGet_m3uParse.replace('\n', '\r\n'), new_xtreamGet_m3uParse)
    
    with open(xtream_file, 'w', encoding='utf-8') as f:
        f.write(xtream_content)

print("Patch applied successfully")
