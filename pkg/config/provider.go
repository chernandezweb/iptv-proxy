package config

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"sync"
)

// ProviderData contains upstream Xtream provider settings and backup URL list.
type ProviderData struct {
	XtreamBaseURL  string   `json:"xtream_base_url"`
	BackupURLs     []string `json:"backup_urls"`
	XtreamUser     string   `json:"xtream_user"`
	XtreamPassword string   `json:"xtream_password"`
	Referer        string   `json:"referer"`
}

// Provider manages persistent provider configuration.
type Provider struct {
	sync.RWMutex
	Data ProviderData
	Path string
}

// NewProvider initializes Provider settings from disk or defaults.
func NewProvider(path string, defaults ProviderData) *Provider {
	p := &Provider{
		Path: path,
		Data: defaults,
	}
	p.Load()
	return p
}

func cleanURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

func cleanURLList(urls []string) []string {
	var cleaned []string
	seen := make(map[string]bool)
	for _, raw := range urls {
		u := cleanURL(raw)
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
		p.Data.XtreamBaseURL = cleanURL(loaded.XtreamBaseURL)
	}
	if len(loaded.BackupURLs) > 0 {
		p.Data.BackupURLs = cleanURLList(loaded.BackupURLs)
	}
	if loaded.XtreamUser != "" {
		p.Data.XtreamUser = loaded.XtreamUser
	}
	if loaded.XtreamPassword != "" {
		p.Data.XtreamPassword = loaded.XtreamPassword
	}
	if loaded.Referer != "" {
		p.Data.Referer = cleanURL(loaded.Referer)
	} else if p.Data.XtreamBaseURL != "" {
		p.Data.Referer = p.Data.XtreamBaseURL
	}
	log.Println("[iptv-proxy] Loaded provider.json successfully")
}

// Save writes provider settings to disk.
func (p *Provider) Save(data ProviderData) error {
	p.Lock()
	defer p.Unlock()

	data.XtreamBaseURL = cleanURL(data.XtreamBaseURL)
	data.BackupURLs = cleanURLList(data.BackupURLs)
	if data.Referer == "" && data.XtreamBaseURL != "" {
		data.Referer = data.XtreamBaseURL
	} else {
		data.Referer = cleanURL(data.Referer)
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

// GetAllURLs returns active URL followed by unique backup URLs.
func (p *Provider) GetAllURLs() []string {
	p.RLock()
	defer p.RUnlock()

	var all []string
	seen := make(map[string]bool)

	active := cleanURL(p.Data.XtreamBaseURL)
	if active != "" {
		all = append(all, active)
		seen[active] = true
	}

	for _, b := range p.Data.BackupURLs {
		cleaned := cleanURL(b)
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

	newClean := cleanURL(newURL)
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
		cb := cleanURL(b)
		if cb != "" && !seen[cb] {
			newBackups = append(newBackups, cb)
			seen[cb] = true
		}
	}
	p.Data.BackupURLs = newBackups

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

	current := cleanURL(p.GetData().XtreamBaseURL)
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
