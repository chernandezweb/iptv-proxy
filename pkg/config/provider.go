package config

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"sync"
)

// ProviderData contains upstream Xtream provider settings.
type ProviderData struct {
	XtreamBaseURL  string `json:"xtream_base_url"`
	XtreamUser     string `json:"xtream_user"`
	XtreamPassword string `json:"xtream_password"`
	Referer        string `json:"referer"`
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
		p.Data.XtreamBaseURL = loaded.XtreamBaseURL
	}
	if loaded.XtreamUser != "" {
		p.Data.XtreamUser = loaded.XtreamUser
	}
	if loaded.XtreamPassword != "" {
		p.Data.XtreamPassword = loaded.XtreamPassword
	}
	if loaded.Referer != "" {
		p.Data.Referer = loaded.Referer
	}
	log.Println("[iptv-proxy] Loaded provider.json successfully")
}

// Save writes provider settings to disk.
func (p *Provider) Save(data ProviderData) error {
	p.Lock()
	defer p.Unlock()

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
