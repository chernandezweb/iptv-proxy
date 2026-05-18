package config

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"sync"
)

type FilterData struct {
	AllowedLiveCategories   []string `json:"allowed_live_categories"`
	AllowedVODCategories    []string `json:"allowed_vod_categories"`
	AllowedSeriesCategories []string `json:"allowed_series_categories"`
}

type Filters struct {
	sync.RWMutex
	Data FilterData
	Path string
}

func NewFilters(path string) *Filters {
	f := &Filters{
		Path: path,
		Data: FilterData{
			AllowedLiveCategories:   []string{},
			AllowedVODCategories:    []string{},
			AllowedSeriesCategories: []string{},
		},
	}
	f.Load()
	return f
}

func (f *Filters) Load() {
	f.Lock()
	defer f.Unlock()

	file, err := os.Open(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("[iptv-proxy] filters.json not found, using empty filters (allowing all)")
		} else {
			log.Printf("[iptv-proxy] error opening filters.json: %v", err)
		}
		return
	}
	defer file.Close()

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		log.Printf("[iptv-proxy] error reading filters.json: %v", err)
		return
	}

	if err := json.Unmarshal(bytes, &f.Data); err != nil {
		log.Printf("[iptv-proxy] error parsing filters.json: %v", err)
		return
	}
	log.Println("[iptv-proxy] Loaded filters.json successfully")
}

func (f *Filters) Save(data FilterData) error {
	f.Lock()
	defer f.Unlock()

	f.Data = data

	bytes, err := json.MarshalIndent(f.Data, "", "  ")
	if err != nil {
		return err
	}

	return ioutil.WriteFile(f.Path, bytes, 0644)
}

func (f *Filters) IsAllowed(categoryType string, categoryName string) bool {
	f.RLock()
	defer f.RUnlock()

	var allowedPrefixes []string
	switch categoryType {
	case "live":
		allowedPrefixes = f.Data.AllowedLiveCategories
	case "vod":
		allowedPrefixes = f.Data.AllowedVODCategories
	case "series":
		allowedPrefixes = f.Data.AllowedSeriesCategories
	}

	if len(allowedPrefixes) == 0 {
		return true
	}

	nameUpper := strings.ToUpper(categoryName)
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(nameUpper, strings.ToUpper(prefix)) {
			return true
		}
	}
	return false
}
