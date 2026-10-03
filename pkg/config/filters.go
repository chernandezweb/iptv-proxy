package config

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
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
			log.Printf("[iptv-proxy] filters file not found at %s, using empty filters (allowing all)", f.Path)
		} else {
			log.Printf("[iptv-proxy] error opening filters file at %s: %v", f.Path, err)
		}
		return
	}
	defer file.Close()

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		log.Printf("[iptv-proxy] error reading filters file at %s: %v", f.Path, err)
		return
	}

	if err := json.Unmarshal(bytes, &f.Data); err != nil {
		log.Printf("[iptv-proxy] error parsing filters file at %s: %v", f.Path, err)
		return
	}
	log.Printf("[iptv-proxy] Loaded filters successfully from %s (%d live, %d vod, %d series allowed)",
		f.Path, len(f.Data.AllowedLiveCategories), len(f.Data.AllowedVODCategories), len(f.Data.AllowedSeriesCategories))
}

func (f *Filters) Save(data FilterData) error {
	f.Lock()
	defer f.Unlock()

	f.Data = data

	bytes, err := json.MarshalIndent(f.Data, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[iptv-proxy] error creating directory %s for filters: %v", dir, err)
	}

	err = ioutil.WriteFile(f.Path, bytes, 0644)
	if err != nil {
		log.Printf("[iptv-proxy] error writing filters file at %s: %v", f.Path, err)
		return err
	}
	log.Printf("[iptv-proxy] Saved filters to %s successfully (%d live, %d vod, %d series allowed)",
		f.Path, len(f.Data.AllowedLiveCategories), len(f.Data.AllowedVODCategories), len(f.Data.AllowedSeriesCategories))
	return nil
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
