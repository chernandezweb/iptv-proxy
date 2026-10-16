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
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
}

// EPGChannel holds channel details parsed from XMLTV.
type EPGChannel struct {
	ID           string   `json:"id"`
	DisplayNames []string `json:"display_names"`
	Icon         string   `json:"icon,omitempty"`
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

// EPGEnricher downloads, parses, indexes, and merges external XMLTV EPG sources.
// It auto-matches provider channels to EPG IDs and injects complete schedule data into xmltv.php.
type EPGEnricher struct {
	mu             sync.RWMutex
	filePath       string
	mappingsPath   string
	sources        []EPGSourceItem
	customMappings map[string]string // streamID / channelName -> target epgChannelID

	// Indexed EPG data
	channels     map[string]EPGChannel   // raw xmltv channelID -> EPGChannel
	channelIndex map[string]string       // normalizedKey -> canonical channelID
	programsMap  map[string][]EPGProgram // canonical channelID -> sorted slice of EPGProgram

	isRefreshing bool
	onUpdate     func()
}

func NewEPGEnricher(configPath, mappingsPath string) *EPGEnricher {
	e := &EPGEnricher{
		filePath:       configPath,
		mappingsPath:   mappingsPath,
		sources:        make([]EPGSourceItem, 0),
		customMappings: make(map[string]string),
		channels:       make(map[string]EPGChannel),
		channelIndex:   make(map[string]string),
		programsMap:    make(map[string][]EPGProgram),
	}
	e.loadConfig()
	e.loadCustomMappings()

	// Initial background refresh on startup
	go func() {
		time.Sleep(3 * time.Second)
		e.RefreshAll(false)
	}()

	// Periodic refresh every 12 hours
	go e.runPeriodicRefresher()
	return e
}

func (e *EPGEnricher) SetOnUpdate(fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onUpdate = fn
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

func (e *EPGEnricher) loadCustomMappings() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.mappingsPath == "" {
		return
	}
	data, err := ioutil.ReadFile(e.mappingsPath)
	if err != nil {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err == nil {
		e.customMappings = m
	}
}

func (e *EPGEnricher) saveCustomMappingsUnlocked() {
	if e.mappingsPath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(e.mappingsPath), 0755)
	data, err := json.MarshalIndent(e.customMappings, "", "  ")
	if err == nil {
		_ = ioutil.WriteFile(e.mappingsPath, data, 0644)
	}
}

func (e *EPGEnricher) GetCustomMappings() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make(map[string]string, len(e.customMappings))
	for k, v := range e.customMappings {
		res[k] = v
	}
	return res
}

func (e *EPGEnricher) SaveCustomMappings(mappings map[string]string) {
	e.mu.Lock()
	e.customMappings = mappings
	e.saveCustomMappingsUnlocked()
	updateFn := e.onUpdate
	e.mu.Unlock()

	if updateFn != nil {
		updateFn()
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

// GetEPGChannels returns all currently indexed external EPG channels (for dropdown mapping UI).
func (e *EPGEnricher) GetEPGChannels() []EPGChannel {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]EPGChannel, 0, len(e.channels))
	for _, ch := range e.channels {
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool {
		nameI := out[i].ID
		if len(out[i].DisplayNames) > 0 {
			nameI = out[i].DisplayNames[0]
		}
		nameJ := out[j].ID
		if len(out[j].DisplayNames) > 0 {
			nameJ = out[j].DisplayNames[0]
		}
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	return out
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
		updateFn := e.onUpdate
		e.mu.Unlock()

		if updateFn != nil {
			updateFn()
		}
	}()

	client := &http.Client{Timeout: 90 * time.Second}
	newProgMap := make(map[string][]EPGProgram)
	newChannels := make(map[string]EPGChannel)
	newIndex := make(map[string]string)

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

		cCount, pCount, errParse := parseXMLTVStream(reader, windowStart, windowEnd, newProgMap, newChannels, newIndex)
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
	e.channels = newChannels
	e.channelIndex = newIndex
	e.mu.Unlock()

	log.Printf("[iptv-proxy] External EPG index updated: total %d channels, %d schedule entries ready.", len(newChannels), len(newProgMap))
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

// NormalizeEPGChannelKey cleans channel names with word/token awareness so French & Quebec stations
// like Radio-Canada, Télé-Québec, Casa, Canal+, and RDS are never corrupted.
func NormalizeEPGChannelKey(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return ""
	}

	// 1. Remove accents (Latin/French)
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

	// 2. Strip bracketed country tags like [CA], (CA), [QC], (QC), |CA|, |QC|, [FR], (US)
	bracketPrefixes := []string{"[CA]", "(CA)", "[QC]", "(QC)", "|CA|", "|QC|", "[FR]", "(FR)", "[US]", "(US)", "[UK]", "(UK)"}
	for _, bp := range bracketPrefixes {
		norm = strings.ReplaceAll(norm, bp, " ")
	}

	// 3. Strip leading prefix before delimiter '|', ':', '-'
	for _, sep := range []string{"|", ":"} {
		if idx := strings.Index(norm, sep); idx >= 0 && idx < 20 {
			prefix := strings.TrimSpace(norm[:idx])
			// If prefix is short country/group code (CA, QC, FR, US, VIP, etc.), strip it
			if len(prefix) <= 6 {
				norm = strings.TrimSpace(norm[idx+1:])
			}
		}
	}

	// 4. Tokenize by separators (spaces, dots, underscores, dashes, slashes, brackets)
	words := strings.FieldsFunc(norm, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '_' || r == '-' || r == '.' || r == '/' || r == '(' || r == ')' || r == '[' || r == ']' || r == '|' || r == ':'
	})

	// Noise words to drop ONLY when appearing as whole tokens
	noiseTokens := map[string]bool{
		"FHD": true, "UHD": true, "4K": true, "HEVC": true, "H265": true, "1080P": true, "720P": true,
		"60FPS": true, "50FPS": true, "RAW": true, "HD": true, "SD": true, "BACKUP": true, "VIP": true,
		"LOCAL": true, "EAST": true, "WEST": true, "EST": true, "OUEST": true, "PLUS": false,
		"CA": true, "QC": true, "FR": true, "US": true, "CAN": true, "CANAL": false,
	}

	var filtered []string
	for _, w := range words {
		wClean := strings.TrimSpace(w)
		if wClean == "" {
			continue
		}
		if noiseTokens[wClean] {
			continue
		}
		filtered = append(filtered, wClean)
	}

	// Build combined clean alphanumeric key (keeping '+')
	var clean strings.Builder
	for _, w := range filtered {
		for _, ch := range w {
			if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '+' {
				clean.WriteRune(unicode.ToLower(ch))
			}
		}
	}
	res := clean.String()

	// 5. Canonical Quebec & French channel aliases
	switch {
	case strings.Contains(res, "radiocanada") || strings.Contains(res, "icitele") || strings.Contains(res, "cbft") || res == "src":
		return "radiocanada"
	case strings.Contains(res, "icirdi") || res == "rdi":
		return "rdi"
	case strings.Contains(res, "telequebec") || strings.Contains(res, "civq"):
		return "telequebec"
	case strings.Contains(res, "tvasports2") || res == "tvasports2":
		return "tvasports2"
	case strings.Contains(res, "tvasports") || res == "tvasports":
		return "tvasports1"
	case res == "tva" || strings.HasPrefix(res, "tvamontreal") || strings.HasPrefix(res, "tvaquebec") || strings.Contains(res, "cftm"):
		return "tva"
	case res == "rds" || res == "rds1" || strings.Contains(res, "reseaudessports"):
		return "rds1"
	case res == "rds2":
		return "rds2"
	case strings.Contains(res, "rdsinfo"):
		return "rdsinfo"
	case strings.Contains(res, "noovo") || strings.Contains(res, "cfjp") || strings.Contains(res, "vtele"):
		return "noovo"
	case strings.Contains(res, "lcn") || strings.Contains(res, "canalnouvelles"):
		return "lcn"
	case strings.Contains(res, "superecran4"):
		return "superecran4"
	case strings.Contains(res, "superecran3"):
		return "superecran3"
	case strings.Contains(res, "superecran2"):
		return "superecran2"
	case strings.Contains(res, "superecran"):
		return "superecran1"
	case strings.Contains(res, "cinepop"):
		return "cinepop"
	case strings.Contains(res, "canald"):
		return "canald"
	case strings.Contains(res, "canalvie"):
		return "canalvie"
	case strings.Contains(res, "casa"):
		return "casa"
	case strings.Contains(res, "ztele") || res == "canalz":
		return "ztele"
	case strings.Contains(res, "evasion"):
		return "evasion"
	case strings.Contains(res, "historia"):
		return "historia"
	case strings.Contains(res, "seriesplus") || strings.Contains(res, "series+"):
		return "seriesplus"
	case strings.Contains(res, "vrak"):
		return "vrak"
	case strings.Contains(res, "meteomedia"):
		return "meteomedia"
	case strings.Contains(res, "canal+") || strings.Contains(res, "canalplus"):
		return "canalplus"
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
			// Parse timezone offset (+0000, -0400, etc.)
			if len(s) >= 20 && (s[15] == '+' || s[15] == '-') {
				offsetStr := s[15:20]
				var sign int = 1
				if offsetStr[0] == '-' {
					sign = -1
				}
				var hours, mins int
				fmt.Sscanf(offsetStr[1:], "%02d%02d", &hours, &mins)
				zoneOffset := time.Duration(sign*(hours*3600+mins*60)) * time.Second
				t = t.Add(-zoneOffset)
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid xmltv time %q", s)
}

func parseXMLTVStream(
	r io.Reader,
	winStart, winEnd time.Time,
	pMap map[string][]EPGProgram,
	cMap map[string]EPGChannel,
	indexMap map[string]string,
) (int, int, error) {
	decoder := xml.NewDecoder(r)
	cCount := 0
	pCount := 0

	var curChanID string
	var curChanNames []string
	var curChanIcon string

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
				curChanIcon = ""
				for _, attr := range el.Attr {
					if attr.Name.Local == "id" {
						curChanID = attr.Value
					}
				}
			} else if el.Name.Local == "display-name" && curChanID != "" {
				var name string
				if errD := decoder.DecodeElement(&name, &el); errD == nil && name != "" {
					curChanNames = append(curChanNames, name)
				}
			} else if el.Name.Local == "icon" && curChanID != "" {
				for _, attr := range el.Attr {
					if attr.Name.Local == "src" {
						curChanIcon = attr.Value
					}
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
							if chanAttr != "" {
								pMap[chanAttr] = append(pMap[chanAttr], prog)
								// Also index under normalized chan ID
								normID := NormalizeEPGChannelKey(chanAttr)
								if normID != "" && normID != chanAttr {
									pMap[normID] = append(pMap[normID], prog)
								}
								pCount++
							}
						}
					}
				}
			}
		case xml.EndElement:
			if el.Name.Local == "channel" && curChanID != "" {
				cMap[curChanID] = EPGChannel{
					ID:           curChanID,
					DisplayNames: append([]string(nil), curChanNames...),
					Icon:         curChanIcon,
				}
				// Register in index
				rawKey := strings.ToLower(strings.TrimSpace(curChanID))
				indexMap[rawKey] = curChanID

				normID := NormalizeEPGChannelKey(curChanID)
				if normID != "" {
					indexMap[normID] = curChanID
				}
				for _, dName := range curChanNames {
					normName := NormalizeEPGChannelKey(dName)
					if normName != "" {
						indexMap[normName] = curChanID
					}
				}
				cCount++
				curChanID = ""
			}
		}
	}
	return cCount, pCount, nil
}

// ResolveChannelEPGID finds the best XMLTV channel ID for a stream using:
// 1. Manual user overrides (/data/epg_mappings.json)
// 2. Existing original epg_channel_id if already valid
// 3. Normalized fuzzy matching across all external XMLTV channels and display names
func (e *EPGEnricher) ResolveChannelEPGID(streamID, channelName, origEPGID string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 1. Custom overrides (by stream ID, exact name, or normalized name)
	if e.customMappings != nil {
		if target, ok := e.customMappings[streamID]; ok && target != "" {
			return target
		}
		if target, ok := e.customMappings[channelName]; ok && target != "" {
			return target
		}
		normName := NormalizeEPGChannelKey(channelName)
		if target, ok := e.customMappings[normName]; ok && target != "" {
			return target
		}
	}

	// 2. If origEPGID is already a known channel in our index, keep it
	if origEPGID != "" {
		if _, ok := e.channels[origEPGID]; ok {
			return origEPGID
		}
		if canonID, ok := e.channelIndex[strings.ToLower(origEPGID)]; ok {
			return canonID
		}
		if canonID, ok := e.channelIndex[NormalizeEPGChannelKey(origEPGID)]; ok {
			return canonID
		}
	}

	// 3. Match by normalized channel name
	normKey := NormalizeEPGChannelKey(channelName)
	if normKey != "" {
		if canonID, ok := e.channelIndex[normKey]; ok {
			return canonID
		}
	}

	// 4. Fallback to origEPGID if set, even if not in external index
	return origEPGID
}

// LookupProgrammes returns upcoming programmes matching channel name, streamID, or epgID.
func (e *EPGEnricher) LookupProgrammes(channelName, epgID string) []EPGProgram {
	e.mu.RLock()
	defer e.mu.RUnlock()

	candidates := []string{
		epgID,
		NormalizeEPGChannelKey(epgID),
		NormalizeEPGChannelKey(channelName),
	}

	for _, k := range candidates {
		if k == "" {
			continue
		}
		if progs, ok := e.programsMap[k]; ok && len(progs) > 0 {
			return progs
		}
		if canonID, ok := e.channelIndex[k]; ok {
			if progs, ok2 := e.programsMap[canonID]; ok2 && len(progs) > 0 {
				return progs
			}
		}
	}
	return nil
}

// EnrichXtreamEPGListings enriches Xtream Codes player_api JSON epg_listings.
func (e *EPGEnricher) EnrichXtreamEPGListings(channelName, epgID string, rawListings []interface{}) []interface{} {
	progs := e.LookupProgrammes(channelName, epgID)
	if len(progs) == 0 {
		return rawListings
	}

	now := time.Now()
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
			if len(out) >= 12 {
				break
			}
		}
		return out
	}

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
			startUnixStr := fmt.Sprint(listingMap["start_timestamp"])
			var startSec int64
			fmt.Sscanf(startUnixStr, "%d", &startSec)
			slotTime := now
			if startSec > 0 {
				slotTime = time.Unix(startSec, 0)
			}

			for _, p := range progs {
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

// EnrichXMLTV creates or enriches the complete XMLTV guide requested by TiviMate & other players.
// It injects full <channel> and <programme> schedules for all matched live streams and external channels.
func (e *EPGEnricher) EnrichXMLTV(rawXML []byte, liveChannels []StreamRoutingTarget) []byte {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<!DOCTYPE tv SYSTEM "xmltv.dtd">` + "\n")
	buf.WriteString(`<tv generator-info-name="iptv-proxy-enricher">` + "\n")

	emittedChannels := make(map[string]bool)
	channelsToPrograms := make(map[string][]EPGProgram)

	// 1. For every live channel provided by the proxy, emit XMLTV <channel> elements
	for _, target := range liveChannels {
		if target.StreamType != "live" || target.Name == "" {
			continue
		}

		streamID := target.OriginalID
		resolvedEPG := target.EPGChannelID
		if resolvedEPG == "" {
			resolvedEPG = e.ResolveChannelEPGID(streamID, target.Name, "")
		}

		// Channel IDs to register for this live stream in TiviMate
		aliasIDs := []string{streamID}
		if resolvedEPG != "" && resolvedEPG != streamID {
			aliasIDs = append(aliasIDs, resolvedEPG)
		}

		// Find programs from our indexed external sources
		var progs []EPGProgram
		if resolvedEPG != "" {
			if p, ok := e.programsMap[resolvedEPG]; ok && len(p) > 0 {
				progs = p
			} else if canonID, ok2 := e.channelIndex[NormalizeEPGChannelKey(resolvedEPG)]; ok2 {
				if p2, ok3 := e.programsMap[canonID]; ok3 && len(p2) > 0 {
					progs = p2
				}
			}
		}
		if len(progs) == 0 {
			normName := NormalizeEPGChannelKey(target.Name)
			if canonID, ok := e.channelIndex[normName]; ok {
				progs = e.programsMap[canonID]
			}
		}

		for _, cid := range aliasIDs {
			if cid == "" || emittedChannels[cid] {
				continue
			}
			emittedChannels[cid] = true

			buf.WriteString(fmt.Sprintf(`  <channel id="%s">`+"\n", xmlEscape(cid)))
			buf.WriteString(fmt.Sprintf(`    <display-name>%s</display-name>`+"\n", xmlEscape(target.Name)))
			if resolvedEPG != "" && resolvedEPG != target.Name {
				buf.WriteString(fmt.Sprintf(`    <display-name>%s</display-name>`+"\n", xmlEscape(resolvedEPG)))
			}
			buf.WriteString(`  </channel>` + "\n")

			if len(progs) > 0 {
				channelsToPrograms[cid] = progs
			}
		}
	}

	// 2. Also emit external XMLTV channels that might not have been matched to a proxy channel
	for cid, ch := range e.channels {
		if emittedChannels[cid] {
			continue
		}
		emittedChannels[cid] = true
		buf.WriteString(fmt.Sprintf(`  <channel id="%s">`+"\n", xmlEscape(cid)))
		for _, dn := range ch.DisplayNames {
			buf.WriteString(fmt.Sprintf(`    <display-name>%s</display-name>`+"\n", xmlEscape(dn)))
		}
		if ch.Icon != "" {
			buf.WriteString(fmt.Sprintf(`    <icon src="%s" />`+"\n", xmlEscape(ch.Icon)))
		}
		buf.WriteString(`  </channel>` + "\n")

		if progs, ok := e.programsMap[cid]; ok && len(progs) > 0 {
			channelsToPrograms[cid] = progs
		}
	}

	// 3. Emit <programme> elements for all channels
	for cid, progs := range channelsToPrograms {
		for _, p := range progs {
			startStr := p.Start.UTC().Format("20060102150405 +0000")
			stopStr := p.End.UTC().Format("20060102150405 +0000")
			buf.WriteString(fmt.Sprintf(`  <programme start="%s" stop="%s" channel="%s">`+"\n", startStr, stopStr, xmlEscape(cid)))
			buf.WriteString(fmt.Sprintf(`    <title lang="fr">%s</title>`+"\n", xmlEscape(p.Title)))
			if p.Description != "" {
				buf.WriteString(fmt.Sprintf(`    <desc lang="fr">%s</desc>`+"\n", xmlEscape(p.Description)))
			}
			buf.WriteString(`  </programme>` + "\n")
		}
	}

	buf.WriteString("</tv>\n")
	return buf.Bytes()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
