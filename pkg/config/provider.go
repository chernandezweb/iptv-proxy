package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"sync"
)

// DefaultUserAgent is the standard upstream User-Agent to avoid player fingerprinting and blocks.
const DefaultUserAgent = "IPTVSmartersPro"

// ProviderItem represents a distinct upstream Xtream IPTV provider subscription.
type ProviderItem struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	XtreamBaseURL  string   `json:"xtream_base_url"`
	BackupURLs     []string `json:"backup_urls"`
	XtreamUser     string   `json:"xtream_user"`
	XtreamPassword string   `json:"xtream_password"`
	Referer        string   `json:"referer"`
	UserAgent      string   `json:"user_agent"`
	CategoryPrefix string   `json:"category_prefix,omitempty"`
}

// ProviderData contains upstream Xtream provider settings and multi-provider list.
type ProviderData struct {
	XtreamBaseURL  string         `json:"xtream_base_url"`
	BackupURLs     []string       `json:"backup_urls"`
	XtreamUser     string         `json:"xtream_user"`
	XtreamPassword string         `json:"xtream_password"`
	Referer        string         `json:"referer"`
	UserAgent      string         `json:"user_agent"`
	Providers      []ProviderItem `json:"providers"`
}

// Provider manages persistent provider configuration.
type Provider struct {
	sync.RWMutex
	Data ProviderData
	Path string
}

// NewProvider initializes Provider settings from disk or defaults.
func NewProvider(path string, defaults ProviderData) *Provider {
	if defaults.UserAgent == "" {
		defaults.UserAgent = DefaultUserAgent
	}
	p := &Provider{
		Path: path,
		Data: defaults,
	}
	p.Load()
	return p
}

// CleanURL sanitizes provider URLs, ensuring a single valid http(s) scheme and stripping redundant slashes or duplicate schemes.
func CleanURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}

	scheme := "http://"
	if strings.HasPrefix(strings.ToLower(u), "https") {
		scheme = "https://"
	}

	// Repeatedly strip any leading protocol artifacts like "http://", "https://", "http:/", "http//", "http:", "http", etc.
	for {
		lower := strings.ToLower(u)
		if strings.HasPrefix(lower, "https://") {
			u = u[8:]
		} else if strings.HasPrefix(lower, "http://") {
			u = u[7:]
		} else if strings.HasPrefix(lower, "https:/") {
			u = u[7:]
		} else if strings.HasPrefix(lower, "http:/") {
			u = u[6:]
		} else if strings.HasPrefix(lower, "https:") {
			u = u[6:]
		} else if strings.HasPrefix(lower, "http:") {
			u = u[5:]
		} else if strings.HasPrefix(lower, "https//") {
			u = u[7:]
		} else if strings.HasPrefix(lower, "http//") {
			u = u[6:]
		} else if strings.HasPrefix(lower, "https/") {
			u = u[6:]
		} else if strings.HasPrefix(lower, "http/") {
			u = u[5:]
		} else if strings.HasPrefix(lower, "https") && len(u) > 5 && !strings.Contains(u[:6], ".") {
			u = u[5:]
		} else if strings.HasPrefix(lower, "http") && len(u) > 4 && !strings.Contains(u[:5], ".") {
			u = u[4:]
		} else if strings.HasPrefix(u, "/") {
			u = strings.TrimLeft(u, "/")
		} else if strings.HasPrefix(u, ":") {
			u = strings.TrimLeft(u, ":")
		} else {
			break
		}
	}

	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}

	return scheme + strings.TrimRight(u, "/")
}

// CleanURLList returns a deduplicated list of sanitized URLs.
func CleanURLList(urls []string) []string {
	var cleaned []string
	seen := make(map[string]bool)
	for _, raw := range urls {
		u := CleanURL(raw)
		if u != "" && !seen[u] {
			seen[u] = true
			cleaned = append(cleaned, u)
		}
	}
	return cleaned
}

// Load reads provider settings from disk if available.
func (p *Provider) Load() {
	p.Lock()
	defer p.Unlock()

	file, err := os.Open(p.Path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("[iptv-proxy] provider.json not found, using configuration from flags/environment")
		} else {
			log.Printf("[iptv-proxy] error opening provider.json: %v", err)
		}
		return
	}
	defer file.Close()

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		log.Printf("[iptv-proxy] error reading provider.json: %v", err)
		return
	}

	var loaded ProviderData
	if err := json.Unmarshal(bytes, &loaded); err != nil {
		log.Printf("[iptv-proxy] error parsing provider.json: %v", err)
		return
	}

	if loaded.XtreamBaseURL != "" {
		p.Data.XtreamBaseURL = CleanURL(loaded.XtreamBaseURL)
	}
	if len(loaded.BackupURLs) > 0 {
		p.Data.BackupURLs = CleanURLList(loaded.BackupURLs)
	}
	if loaded.XtreamUser != "" {
		p.Data.XtreamUser = loaded.XtreamUser
	}
	if loaded.XtreamPassword != "" {
		p.Data.XtreamPassword = loaded.XtreamPassword
	}
	if loaded.Referer != "" {
		p.Data.Referer = CleanURL(loaded.Referer)
	} else if p.Data.XtreamBaseURL != "" {
		p.Data.Referer = p.Data.XtreamBaseURL
	}
	if loaded.UserAgent != "" && !strings.Contains(loaded.UserAgent, "Chrome/128") {
		p.Data.UserAgent = loaded.UserAgent
	} else {
		p.Data.UserAgent = DefaultUserAgent
	}

	// Handle multi-provider initialization
	if len(loaded.Providers) == 0 && p.Data.XtreamBaseURL != "" {
		p.Data.Providers = []ProviderItem{
			{
				ID:             "provider_1",
				Name:           "Primary Provider",
				Enabled:        true,
				XtreamBaseURL:  p.Data.XtreamBaseURL,
				BackupURLs:     p.Data.BackupURLs,
				XtreamUser:     p.Data.XtreamUser,
				XtreamPassword: p.Data.XtreamPassword,
				Referer:        p.Data.Referer,
				UserAgent:      p.Data.UserAgent,
			},
		}
	} else if len(loaded.Providers) > 0 {
		p.Data.Providers = make([]ProviderItem, 0, len(loaded.Providers))
		for idx, prov := range loaded.Providers {
			if prov.ID == "" {
				prov.ID = fmt.Sprintf("provider_%d", idx+1)
			}
			if prov.Name == "" {
				prov.Name = fmt.Sprintf("Provider %d", idx+1)
			}
			prov.XtreamBaseURL = CleanURL(prov.XtreamBaseURL)
			prov.BackupURLs = CleanURLList(prov.BackupURLs)
			if prov.Referer == "" {
				prov.Referer = prov.XtreamBaseURL
			} else {
				prov.Referer = CleanURL(prov.Referer)
			}
			if prov.UserAgent == "" || strings.Contains(prov.UserAgent, "Chrome/128") {
				prov.UserAgent = DefaultUserAgent
			}
			p.Data.Providers = append(p.Data.Providers, prov)
		}
	}

	// Sync top-level active provider
	if len(p.Data.Providers) > 0 {
		active := p.Data.Providers[0]
		for _, prov := range p.Data.Providers {
			if prov.Enabled {
				active = prov
				break
			}
		}
		p.Data.XtreamBaseURL = active.XtreamBaseURL
		p.Data.BackupURLs = active.BackupURLs
		p.Data.XtreamUser = active.XtreamUser
		p.Data.XtreamPassword = active.XtreamPassword
		p.Data.Referer = active.Referer
		p.Data.UserAgent = active.UserAgent
	}

	log.Println("[iptv-proxy] Loaded provider.json successfully")
}

// Save writes provider settings to disk.
func (p *Provider) Save(data ProviderData) error {
	p.Lock()
	defer p.Unlock()

	for i := range data.Providers {
		if data.Providers[i].ID == "" {
			data.Providers[i].ID = fmt.Sprintf("provider_%d", i+1)
		}
		if data.Providers[i].Name == "" {
			data.Providers[i].Name = fmt.Sprintf("Provider %d", i+1)
		}
		data.Providers[i].XtreamBaseURL = CleanURL(data.Providers[i].XtreamBaseURL)
		data.Providers[i].BackupURLs = CleanURLList(data.Providers[i].BackupURLs)
		if data.Providers[i].Referer == "" {
			data.Providers[i].Referer = data.Providers[i].XtreamBaseURL
		} else {
			data.Providers[i].Referer = CleanURL(data.Providers[i].Referer)
		}
		if data.Providers[i].UserAgent == "" {
			data.Providers[i].UserAgent = DefaultUserAgent
		}
	}

	if len(data.Providers) == 0 && data.XtreamBaseURL != "" {
		data.XtreamBaseURL = CleanURL(data.XtreamBaseURL)
		data.BackupURLs = CleanURLList(data.BackupURLs)
		if data.Referer == "" {
			data.Referer = data.XtreamBaseURL
		} else {
			data.Referer = CleanURL(data.Referer)
		}
		if data.UserAgent == "" {
			data.UserAgent = DefaultUserAgent
		}
		data.Providers = []ProviderItem{
			{
				ID:             "provider_1",
				Name:           "Primary Provider",
				Enabled:        true,
				XtreamBaseURL:  data.XtreamBaseURL,
				BackupURLs:     data.BackupURLs,
				XtreamUser:     data.XtreamUser,
				XtreamPassword: data.XtreamPassword,
				Referer:        data.Referer,
				UserAgent:      data.UserAgent,
			},
		}
	}

	if len(data.Providers) > 0 {
		active := data.Providers[0]
		for _, prov := range data.Providers {
			if prov.Enabled {
				active = prov
				break
			}
		}
		data.XtreamBaseURL = active.XtreamBaseURL
		data.BackupURLs = active.BackupURLs
		data.XtreamUser = active.XtreamUser
		data.XtreamPassword = active.XtreamPassword
		data.Referer = active.Referer
		data.UserAgent = active.UserAgent
	}

	p.Data = data

	bytes, err := json.MarshalIndent(p.Data, "", "  ")
	if err != nil {
		return err
	}

	return ioutil.WriteFile(p.Path, bytes, 0644)
}

// GetData returns the current provider settings thread-safely.
func (p *Provider) GetData() ProviderData {
	p.RLock()
	defer p.RUnlock()
	return p.Data
}

// GetProviders returns a copy of all configured providers.
func (p *Provider) GetProviders() []ProviderItem {
	p.RLock()
	defer p.RUnlock()
	res := make([]ProviderItem, len(p.Data.Providers))
	copy(res, p.Data.Providers)
	return res
}

// GetEnabledProviders returns a slice of only enabled providers.
func (p *Provider) GetEnabledProviders() []ProviderItem {
	p.RLock()
	defer p.RUnlock()
	var res []ProviderItem
	for _, prov := range p.Data.Providers {
		if prov.Enabled {
			res = append(res, prov)
		}
	}
	return res
}

// GetEnabledProviderByIndex returns the enabled provider at a given index.
func (p *Provider) GetEnabledProviderByIndex(idx int) (ProviderItem, bool) {
	enabled := p.GetEnabledProviders()
	if idx < 0 || idx >= len(enabled) {
		return ProviderItem{}, false
	}
	return enabled[idx], true
}

// GetAllURLs returns active URL followed by unique backup URLs.
func (p *Provider) GetAllURLs() []string {
	p.RLock()
	defer p.RUnlock()

	var all []string
	seen := make(map[string]bool)

	active := CleanURL(p.Data.XtreamBaseURL)
	if active != "" {
		all = append(all, active)
		seen[active] = true
	}

	for _, b := range p.Data.BackupURLs {
		cleaned := CleanURL(b)
		if cleaned != "" && !seen[cleaned] {
			all = append(all, cleaned)
			seen[cleaned] = true
		}
	}

	return all
}

// SetActiveURL updates the primary active URL and rotates old active into backup list.
func (p *Provider) SetActiveURL(newURL string) error {
	p.Lock()
	defer p.Unlock()

	newClean := CleanURL(newURL)
	if newClean == "" || newClean == p.Data.XtreamBaseURL {
		return nil
	}

	oldActive := p.Data.XtreamBaseURL
	p.Data.XtreamBaseURL = newClean

	// Automatically update referer to match active URL
	p.Data.Referer = newClean

	// Rebuild backup URLs so old active is retained in pool
	var newBackups []string
	seen := make(map[string]bool)
	seen[newClean] = true

	if oldActive != "" && !seen[oldActive] {
		newBackups = append(newBackups, oldActive)
		seen[oldActive] = true
	}

	for _, b := range p.Data.BackupURLs {
		cb := CleanURL(b)
		if cb != "" && !seen[cb] {
			newBackups = append(newBackups, cb)
			seen[cb] = true
		}
	}
	p.Data.BackupURLs = newBackups

	// Sync with active provider item in multi-provider list so all URLs remain in pool
	for i := range p.Data.Providers {
		if p.Data.Providers[i].Enabled {
			p.Data.Providers[i].XtreamBaseURL = newClean
			p.Data.Providers[i].BackupURLs = newBackups
			p.Data.Providers[i].Referer = newClean
			break
		}
	}

	bytes, err := json.MarshalIndent(p.Data, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(p.Path, bytes, 0644)
}

// RotateToNext rotates the active URL to the next available backup in the pool.
func (p *Provider) RotateToNext() (string, bool) {
	all := p.GetAllURLs()
	if len(all) <= 1 {
		return p.GetData().XtreamBaseURL, false
	}

	current := CleanURL(p.GetData().XtreamBaseURL)
	nextURL := all[1] // Default to second URL
	for i, u := range all {
		if u == current && i+1 < len(all) {
			nextURL = all[i+1]
			break
		}
	}

	if err := p.SetActiveURL(nextURL); err != nil {
		log.Printf("[iptv-proxy] Error persisting rotated URL: %v", err)
	}
	return nextURL, true
}
