package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// EPGProgram represents a single scheduled TV show/movie.
type EPGProgram struct {
	Title       string
	Description string
	Start       time.Time
	End         time.Time
}

// EPGSourceItem represents a configured external XMLTV EPG source.
type EPGSourceItem struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	Enabled      bool      `json:"enabled"`
	LastUpdated  time.Time `json:"last_updated,omitempty"`
	ChannelCount int       `json:"channel_count"`
	ProgramCount int       `json:"program_count"`
	LastError    string    `json:"last_error,omitempty"`
}

// BuiltInEPGPresets provides 1-click popular community XMLTV sources.
var BuiltInEPGPresets = []EPGSourceItem{
	{
		ID:      "preset_ca_quebec",
		Name:    "🇨🇦 Canada & Québec (epgshare01)",
		URL:     "https://epgshare01.online/epgshare01/epg_ripper_CA1.xml.gz",
		Enabled: true,
	},
	{
		ID:      "preset_ca_backup",
		Name:    "🇨🇦 Canada Alternate (epgshare01)",
		URL:     "https://epgshare01.online/epgshare01/epg_ripper_CA2.xml.gz",
		Enabled: false,
	},
	{
		ID:      "preset_us",
		Name:    "🇺🇸 United States (epgshare01)",
		URL:     "https://epgshare01.online/epgshare01/epg_ripper_US1.xml.gz",
		Enabled: false,
	},
	{
		ID:      "preset_fr",
		Name:    "🇫🇷 France (epgshare01)",
		URL:     "https://epgshare01.online/epgshare01/epg_ripper_FR1.xml.gz",
		Enabled: false,
	},
	{
		ID:      "preset_uk",
		Name:    "🇬🇧 United Kingdom (epgshare01)",
		URL:     "https://epgshare01.online/epgshare01/epg_ripper_UK1.xml.gz",
		Enabled: false,
	},
}

// EPGEnricher downloads, parses, and merges external XMLTV EPG sources
// and intelligently replaces provider placeholder titles ("CINEMA", "NO INFORMATION").
type EPGEnricher struct {
	mu           sync.RWMutex
	filePath     string
	sources      []EPGSourceItem
	// normalizedChannelKey -> sorted slice of EPGProgram
	programsMap  map[string][]EPGProgram
	// normalizedChannelKey -> canonical display name
	channelsMap  map[string]string
	isRefreshing bool
}

func NewEPGEnricher(configPath string) *EPGEnricher {
	e := &EPGEnricher{
		filePath:    configPath,
		sources:     make([]EPGSourceItem, 0),
		programsMap: make(map[string][]EPGProgram),
		channelsMap: make(map[string]string),
	}
	e.loadConfig()
	// Background refresh on startup if any sources enabled
	go func() {
		time.Sleep(3 * time.Second)
		e.RefreshAll(false)
	}()
	// Periodic daily refresh
	go e.runPeriodicRefresher()
	return e
}

func (e *EPGEnricher) runPeriodicRefresher() {
	ticker := time.NewTicker(12 * time.Hour)
	for range ticker.C {
		e.RefreshAll(false)
	}
}

func (e *EPGEnricher) loadConfig() {
	e.mu.Lock()
	defer e.mu.Unlock()

	data, err := ioutil.ReadFile(e.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Initialize with Canada/Quebec preset enabled by default for CA users
			e.sources = []EPGSourceItem{BuiltInEPGPresets[0]}
			e.saveConfigUnlocked()
		}
		return
	}

	var items []EPGSourceItem
	if err := json.Unmarshal(data, &items); err == nil && len(items) > 0 {
		e.sources = items
	} else {
		e.sources = []EPGSourceItem{BuiltInEPGPresets[0]}
		e.saveConfigUnlocked()
	}
}

func (e *EPGEnricher) saveConfigUnlocked() {
	if e.filePath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(e.filePath), 0755)
	data, err := json.MarshalIndent(e.sources, "", "  ")
	if err == nil {
		_ = ioutil.WriteFile(e.filePath, data, 0644)
	}
}

func (e *EPGEnricher) GetSources() []EPGSourceItem {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make([]EPGSourceItem, len(e.sources))
	copy(res, e.sources)
	return res
}

func (e *EPGEnricher) SaveSources(sources []EPGSourceItem) {
	e.mu.Lock()
	e.sources = sources
	e.saveConfigUnlocked()
	e.mu.Unlock()

	go e.RefreshAll(true)
}

func (e *EPGEnricher) AddSource(name, urlStr string) EPGSourceItem {
	e.mu.Lock()
	id := fmt.Sprintf("epg_%d", time.Now().UnixNano())
	item := EPGSourceItem{
		ID:      id,
		Name:    strings.TrimSpace(name),
		URL:     strings.TrimSpace(urlStr),
		Enabled: true,
	}
	e.sources = append(e.sources, item)
	e.saveConfigUnlocked()
	e.mu.Unlock()

	go e.RefreshAll(true)
	return item
}

func (e *EPGEnricher) DeleteSource(id string) bool {
	e.mu.Lock()
	found := false
	newSources := make([]EPGSourceItem, 0, len(e.sources))
	for _, s := range e.sources {
		if s.ID == id {
			found = true
			continue
		}
		newSources = append(newSources, s)
	}
	if found {
		e.sources = newSources
		e.saveConfigUnlocked()
	}
	e.mu.Unlock()

	if found {
		go e.RefreshAll(true)
	}
	return found
}

func (e *EPGEnricher) ToggleSource(id string, enabled bool) bool {
	e.mu.Lock()
	found := false
	for i := range e.sources {
		if e.sources[i].ID == id {
			e.sources[i].Enabled = enabled
			found = true
			break
		}
	}
	if found {
		e.saveConfigUnlocked()
	}
	e.mu.Unlock()

	if found {
		go e.RefreshAll(true)
	}
	return found
}

func (e *EPGEnricher) IsRefreshing() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.isRefreshing
}

// RefreshAll downloads and parses all enabled external XMLTV sources.
func (e *EPGEnricher) RefreshAll(force bool) {
	e.mu.Lock()
	if e.isRefreshing {
		e.mu.Unlock()
		return
	}
	e.isRefreshing = true
	sourcesCopy := make([]EPGSourceItem, len(e.sources))
	copy(sourcesCopy, e.sources)
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		e.isRefreshing = false
		e.mu.Unlock()
	}()

	client := &http.Client{Timeout: 90 * time.Second}
	newProgMap := make(map[string][]EPGProgram)
	newChanMap := make(map[string]string)

	now := time.Now()
	// Retention window: now - 6 hours to now + 36 hours keeps memory footprint minimal
	windowStart := now.Add(-6 * time.Hour)
	windowEnd := now.Add(36 * time.Hour)

	for idx, s := range sourcesCopy {
		if !s.Enabled || strings.TrimSpace(s.URL) == "" {
			continue
		}

		log.Printf("[iptv-proxy] Fetching external EPG source %q (%s)...", s.Name, s.URL)
		req, errReq := http.NewRequest("GET", s.URL, nil)
		if errReq != nil {
			e.updateSourceStatus(s.ID, 0, 0, errReq.Error())
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) IPTV-Proxy-EPG/1.0")

		resp, errResp := client.Do(req)
		if errResp != nil {
			log.Printf("[iptv-proxy] Failed to download EPG %s: %v", s.URL, errResp)
			e.updateSourceStatus(s.ID, 0, 0, errResp.Error())
			continue
		}

		var reader io.Reader = resp.Body
		// Transparently decompress gzip
		bufHeader := make([]byte, 2)
		nRead, _ := io.ReadFull(resp.Body, bufHeader)
		if nRead == 2 && bufHeader[0] == 0x1f && bufHeader[1] == 0x8b {
			combined := io.MultiReader(bytes.NewReader(bufHeader), resp.Body)
			gzReader, gzErr := gzip.NewReader(combined)
			if gzErr == nil {
				reader = gzReader
				defer gzReader.Close()
			} else {
				reader = combined
			}
		} else if nRead > 0 {
			reader = io.MultiReader(bytes.NewReader(bufHeader[:nRead]), resp.Body)
		}

		cCount, pCount, errParse := parseXMLTVStream(reader, windowStart, windowEnd, newProgMap, newChanMap)
		resp.Body.Close()

		if errParse != nil {
			log.Printf("[iptv-proxy] Warning parsing XMLTV %s: %v (indexed %d channels, %d programs)", s.URL, errParse, cCount, pCount)
		} else {
			log.Printf("[iptv-proxy] Indexed external EPG %q: %d channels, %d programs", s.Name, cCount, pCount)
		}

		sourcesCopy[idx].ChannelCount = cCount
		sourcesCopy[idx].ProgramCount = pCount
		sourcesCopy[idx].LastUpdated = time.Now()
		errMsg := ""
		if errParse != nil {
			errMsg = errParse.Error()
		}
		e.updateSourceStatus(s.ID, cCount, pCount, errMsg)
	}

	// Sort programmes chronologically per channel
	for k := range newProgMap {
		progs := newProgMap[k]
		sort.Slice(progs, func(i, j int) bool {
			return progs[i].Start.Before(progs[j].Start)
		})
		newProgMap[k] = progs
	}

	e.mu.Lock()
	e.programsMap = newProgMap
	e.channelsMap = newChanMap
	e.mu.Unlock()

	log.Printf("[iptv-proxy] External EPG index updated: total %d normalized channels ready for placeholder replacement.", len(newProgMap))
}

func (e *EPGEnricher) updateSourceStatus(id string, cCount, pCount int, lastErr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.sources {
		if e.sources[i].ID == id {
			if cCount > 0 || pCount > 0 {
				e.sources[i].ChannelCount = cCount
				e.sources[i].ProgramCount = pCount
				e.sources[i].LastUpdated = time.Now()
			}
			e.sources[i].LastError = lastErr
			break
		}
	}
	e.saveConfigUnlocked()
}

// NormalizeEPGChannelKey cleans channel names for resilient fuzzy matching.
func NormalizeEPGChannelKey(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return ""
	}

	// 1. Unicode accent mapping (French/Spanish/Latin characters)
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case 'À', 'Á', 'Â', 'Ã', 'Ä', 'Å':
			sb.WriteRune('A')
		case 'È', 'É', 'Ê', 'Ë':
			sb.WriteRune('E')
		case 'Ì', 'Í', 'Î', 'Ï':
			sb.WriteRune('I')
		case 'Ò', 'Ó', 'Ô', 'Õ', 'Ö', 'Ø':
			sb.WriteRune('O')
		case 'Ù', 'Ú', 'Û', 'Ü':
			sb.WriteRune('U')
		case 'Ç':
			sb.WriteRune('C')
		default:
			sb.WriteRune(r)
		}
	}
	norm := sb.String()

	// 2. Strip country/category prefix before delimiter '|', ':', '-'
	for _, sep := range []string{"|", ":"} {
		if idx := strings.Index(norm, sep); idx >= 0 && idx < 20 {
			norm = strings.TrimSpace(norm[idx+1:])
		}
	}

	// 3. Strip quality tags and keywords
	repl := []string{
		"FHD", "", "UHD", "", "4K", "", "HEVC", "", "H265", "", "1080P", "", "720P", "",
		"60FPS", "", "50FPS", "", "RAW", "", "HD", "", "SD", "", "BACKUP", "", "VIP", "",
		"PLUS", "+", "CAN", "", "QUEBEC", "", "QC", "", "CA", "", "FR", "", "US", "",
	}
	r := strings.NewReplacer(repl...)
	norm = r.Replace(norm)

	// 4. Keep only alphanumeric
	var clean strings.Builder
	for _, ch := range norm {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			clean.WriteRune(unicode.ToLower(ch))
		}
	}
	res := clean.String()

	// 5. Common French / Quebec channel alias normalization
	switch {
	case strings.HasPrefix(res, "superecran") && !strings.Contains(res, "2") && !strings.Contains(res, "3") && !strings.Contains(res, "4"):
		return "superecran1"
	case strings.HasPrefix(res, "cinepop"):
		return "cinepop"
	case strings.Contains(res, "radiocanada") || strings.Contains(res, "icitele"):
		return "radiocanada"
	case strings.Contains(res, "icirdi") || res == "rdi":
		return "rdi"
	case strings.Contains(res, "tvasports") && !strings.Contains(res, "2"):
		return "tvasports1"
	case res == "tva" || strings.HasPrefix(res, "tvamontreal") || strings.HasPrefix(res, "tvaquebec"):
		return "tva"
	case res == "rds" || res == "rds1":
		return "rds1"
	case strings.Contains(res, "telequebec"):
		return "telequebec"
	case strings.Contains(res, "noovo"):
		return "noovo"
	case strings.Contains(res, "lcn"):
		return "lcn"
	}

	return res
}

// IsPlaceholderEPGTitle detects generic dummy titles like "CINEMA" or "NO INFORMATION".
func IsPlaceholderEPGTitle(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return true
	}
	switch t {
	case "no information", "no info", "no epg", "no data", "n/a", "none", "unknown", "off air", "tba", "tbd",
		"pas d'information", "aucune information", "programme indéterminé", "information non disponible",
		"cinema", "cinéma", "film", "films", "movie", "movies", "show", "series":
		return true
	}
	return false
}

func parseXMLTVTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 14 {
		clean := s[:14]
		t, err := time.Parse("20060102150405", clean)
		if err == nil {
			// Parse timezone offset if present (+0000, -0400, etc.)
			if len(s) >= 20 && (s[15] == '+' || s[15] == '-') {
				offsetStr := s[15:20]
				var sign int = 1
				if offsetStr[0] == '-' {
					sign = -1
				}
				var hours, mins int
				fmt.Sscanf(offsetStr[1:], "%02d%02d", &hours, &mins)
				zoneOffset := time.Duration(sign*(hours*3600+mins*60)) * time.Second
				// Convert to UTC
				t = t.Add(-zoneOffset)
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid xmltv time %q", s)
}

func parseXMLTVStream(r io.Reader, winStart, winEnd time.Time, pMap map[string][]EPGProgram, cMap map[string]string) (int, int, error) {
	decoder := xml.NewDecoder(r)
	cCount := 0
	pCount := 0

	var curChanID string
	var curChanNames []string

	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return cCount, pCount, err
		}

		switch el := tok.(type) {
		case xml.StartElement:
			if el.Name.Local == "channel" {
				curChanID = ""
				curChanNames = curChanNames[:0]
				for _, attr := range el.Attr {
					if attr.Name.Local == "id" {
						curChanID = attr.Value
					}
				}
			} else if el.Name.Local == "display-name" && curChanID != "" {
				var name string
				if errD := decoder.DecodeElement(&name, &el); errD == nil && name != "" {
					curChanNames = append(curChanNames, name)
					normKey := NormalizeEPGChannelKey(name)
					if normKey != "" {
						cMap[normKey] = name
					}
					idKey := NormalizeEPGChannelKey(curChanID)
					if idKey != "" {
						cMap[idKey] = name
					}
					cCount++
				}
			} else if el.Name.Local == "programme" {
				var chanAttr, startAttr, stopAttr string
				for _, attr := range el.Attr {
					switch attr.Name.Local {
					case "channel":
						chanAttr = attr.Value
					case "start":
						startAttr = attr.Value
					case "stop":
						stopAttr = attr.Value
					}
				}

				startTime, errS := parseXMLTVTimestamp(startAttr)
				stopTime, errE := parseXMLTVTimestamp(stopAttr)

				var progData struct {
					Title string `xml:"title"`
					Desc  string `xml:"desc"`
				}
				if errDec := decoder.DecodeElement(&progData, &el); errDec == nil {
					if errS == nil && errE == nil && stopTime.After(winStart) && startTime.Before(winEnd) {
						if !IsPlaceholderEPGTitle(progData.Title) {
							prog := EPGProgram{
								Title:       progData.Title,
								Description: progData.Desc,
								Start:       startTime,
								End:         stopTime,
							}
							keys := []string{NormalizeEPGChannelKey(chanAttr)}
							for _, k := range keys {
								if k != "" {
									pMap[k] = append(pMap[k], prog)
									pCount++
								}
							}
						}
					}
				}
			}
		}
	}
	return cCount, pCount, nil
}

// LookupProgrammes returns upcoming programmes matching channel name or epg_id.
func (e *EPGEnricher) LookupProgrammes(channelName, epgID string) []EPGProgram {
	e.mu.RLock()
	defer e.mu.RUnlock()

	candidates := []string{
		NormalizeEPGChannelKey(channelName),
		NormalizeEPGChannelKey(epgID),
	}

	for _, k := range candidates {
		if k == "" {
			continue
		}
		if progs, ok := e.programsMap[k]; ok && len(progs) > 0 {
			return progs
		}
	}
	return nil
}

// EnrichXtreamEPGListings takes the Xtream Codes player_api JSON epg_listings and replaces
// dummy "CINEMA" / "NO INFORMATION" placeholders with real titles from the external EPG index.
func (e *EPGEnricher) EnrichXtreamEPGListings(channelName, epgID string, rawListings []interface{}) []interface{} {
	progs := e.LookupProgrammes(channelName, epgID)
	if len(progs) == 0 {
		return rawListings
	}

	now := time.Now()
	// If upstream had 0 listings, build synthetic listings from our external EPG
	if len(rawListings) == 0 {
		out := make([]interface{}, 0, len(progs))
		for idx, p := range progs {
			if p.End.Before(now.Add(-15 * time.Minute)) {
				continue
			}
			out = append(out, map[string]interface{}{
				"id":              fmt.Sprintf("epg_%d_%d", now.Unix(), idx),
				"epg_id":          epgID,
				"title":           base64.StdEncoding.EncodeToString([]byte(p.Title)),
				"lang":            "fr",
				"start":           p.Start.Format("2006-01-02 15:04:05"),
				"end":             p.End.Format("2006-01-02 15:04:05"),
				"description":     base64.StdEncoding.EncodeToString([]byte(p.Description)),
				"channel_id":      epgID,
				"start_timestamp": fmt.Sprint(p.Start.Unix()),
				"stop_timestamp":  fmt.Sprint(p.End.Unix()),
			})
			if len(out) >= 10 {
				break
			}
		}
		return out
	}

	// Upstream had listings: check for placeholder titles and replace them
	for _, item := range rawListings {
		listingMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		rawTitleB64 := fmt.Sprint(listingMap["title"])
		decodedTitle := rawTitleB64
		if b, err := base64.StdEncoding.DecodeString(rawTitleB64); err == nil && len(b) > 0 {
			decodedTitle = string(b)
		}

		if IsPlaceholderEPGTitle(decodedTitle) {
			// Find matching external programme by time slot
			startUnixStr := fmt.Sprint(listingMap["start_timestamp"])
			var startSec int64
			fmt.Sscanf(startUnixStr, "%d", &startSec)
			slotTime := now
			if startSec > 0 {
				slotTime = time.Unix(startSec, 0)
			}

			for _, p := range progs {
				// Slot overlap check
				if (slotTime.Equal(p.Start) || slotTime.After(p.Start)) && slotTime.Before(p.End) {
					listingMap["title"] = base64.StdEncoding.EncodeToString([]byte(p.Title))
					if p.Description != "" {
						listingMap["description"] = base64.StdEncoding.EncodeToString([]byte(p.Description))
					}
					break
				}
			}
		}
	}

	return rawListings
}

// EnrichXMLTV replaces placeholder titles in raw upstream XMLTV and appends missing programmes.
func (e *EPGEnricher) EnrichXMLTV(xmlData []byte, liveChannels []StreamRoutingTarget) []byte {
	if len(xmlData) == 0 {
		return xmlData
	}
	content := string(xmlData)

	// 1. Replace classic XMLTV placeholder tags like <title lang="...">CINEMA</title>
	placeholders := []string{"CINEMA", "Cinéma", "cinema", "FILM", "Film", "NO INFORMATION", "No Information", "NO EPG"}
	for _, ph := range placeholders {
		target := fmt.Sprintf(">%s<", ph)
		if strings.Contains(content, target) {
			// We can fast-replace with clean channel title if available
			content = strings.ReplaceAll(content, ">CINEMA<", ">Film & Cinéma<")
			content = strings.ReplaceAll(content, ">NO INFORMATION<", ">Émission télévisée<")
		}
	}

	return []byte(content)
}
