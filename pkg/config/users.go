package config

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UserItem represents an authorized client account on the proxy.
type UserItem struct {
	ID             string    `json:"id"`
	Username       string    `json:"username"`
	Password       string    `json:"password"`
	MaxConnections int       `json:"max_connections"` // 0 = unlimited
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
}

// UserSlot represents an active streaming session for a user.
type UserSlot struct {
	ID           string             `json:"id"`
	IP           string             `json:"ip"`
	StreamID     string             `json:"stream_id"`
	StreamURL    string             `json:"stream_url"`
	StartedAt    time.Time          `json:"started_at"`
	LastActiveAt time.Time          `json:"last_active_at"`
	ActiveReqs   int                `json:"-"`
	Cancel       context.CancelFunc `json:"-"`
}

// UserSlots tracks active concurrent streaming slots for a user.
type UserSlots struct {
	mu    sync.Mutex
	slots []*UserSlot
}

const sessionGracePeriod = 20 * time.Second

func cleanStreamID(id string) string {
	id = strings.TrimSpace(id)
	for _, sfx := range []string{".ts", ".m3u8", ".mp4", ".mkv", ".avi", ".webm"} {
		id = strings.TrimSuffix(id, sfx)
	}
	return id
}

func isSameStream(idA, urlA, idB, urlB string) bool {
	cA := cleanStreamID(idA)
	cB := cleanStreamID(idB)
	if cA != "" && cB != "" && cA == cB {
		return true
	}
	if urlA != "" && urlB != "" {
		if urlA == urlB || filepath.Base(urlA) == filepath.Base(urlB) {
			return true
		}
	}
	return false
}

func (s *UserSlots) pruneExpiredUnlocked() {
	now := time.Now()
	var kept []*UserSlot
	for _, sl := range s.slots {
		if sl.ActiveReqs > 0 || now.Sub(sl.LastActiveAt) <= sessionGracePeriod {
			kept = append(kept, sl)
		} else {
			if sl.Cancel != nil {
				sl.Cancel()
			}
		}
	}
	s.slots = kept
}

// ActiveCount returns current active streaming connections.
func (s *UserSlots) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredUnlocked()
	return len(s.slots)
}

// Acquire attempts to claim a streaming slot. If max > 0 and limit is reached:
// - If the request is from the same IP continuing the same stream (VOD chunk / live reconnect), it reuses the session.
// - If from the same IP switching to a different stream, the old stream is replaced.
// - If from a different IP (concurrent limit reached), returns false.
func (s *UserSlots) Acquire(maxLimit int, clientIP string, streamID, streamURL string, cancel context.CancelFunc) (*UserSlot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneExpiredUnlocked()

	// 1. Check if same device is continuing to watch the same stream (VOD range chunk or live reconnect)
	for _, sl := range s.slots {
		if sl.IP == clientIP && isSameStream(sl.StreamID, sl.StreamURL, streamID, streamURL) {
			sl.LastActiveAt = time.Now()
			sl.ActiveReqs++
			if cancel != nil {
				sl.Cancel = cancel
			}
			if streamURL != "" {
				sl.StreamURL = streamURL
			}
			return sl, true
		}
	}

	// 2. Check if same device switched to a different stream (replace old slot)
	sameIPIdx := -1
	for i, sl := range s.slots {
		if sl.IP == clientIP {
			sameIPIdx = i
			break
		}
	}

	if sameIPIdx >= 0 {
		old := s.slots[sameIPIdx]
		if old.Cancel != nil {
			old.Cancel()
		}
		s.slots = append(s.slots[:sameIPIdx], s.slots[sameIPIdx+1:]...)
		log.Printf("[iptv-proxy] User stream switch: IP %s changed from %s to %s", clientIP, old.StreamID, streamID)
	} else if maxLimit > 0 && len(s.slots) >= maxLimit {
		// Limit exceeded by another device/IP
		return nil, false
	}

	// 3. Create new session
	now := time.Now()
	slot := &UserSlot{
		ID:           fmt.Sprintf("slot_%d", now.UnixNano()),
		IP:           clientIP,
		StreamID:     streamID,
		StreamURL:    streamURL,
		StartedAt:    now,
		LastActiveAt: now,
		ActiveReqs:   1,
		Cancel:       cancel,
	}
	s.slots = append(s.slots, slot)
	return slot, true
}

// Release marks an active stream request finished while preserving the session during grace period.
func (s *UserSlots) Release(slot *UserSlot) {
	if slot == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sl := range s.slots {
		if sl == slot || sl.ID == slot.ID {
			if sl.ActiveReqs > 0 {
				sl.ActiveReqs--
			}
			sl.LastActiveAt = time.Now()
			return
		}
	}
}

// GetActiveSlots returns a copy of current active slots.
func (s *UserSlots) GetActiveSlots() []UserSlot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredUnlocked()
	res := make([]UserSlot, len(s.slots))
	for i, sl := range s.slots {
		res[i] = *sl
	}
	return res
}

// UserManager manages persistent users and runtime connection slots.
type UserManager struct {
	sync.RWMutex
	Path  string
	Users []UserItem
	slots map[string]*UserSlots // keyed by username
}

// NewUserManager initializes the user manager and loads users from disk.
func NewUserManager(path string, defaultUser, defaultPassword string) *UserManager {
	m := &UserManager{
		Path:  path,
		Users: []UserItem{},
		slots: make(map[string]*UserSlots),
	}
	m.Load(defaultUser, defaultPassword)
	return m
}

// getUserSlots returns or initializes UserSlots for a username.
func (m *UserManager) getUserSlots(username string) *UserSlots {
	m.Lock()
	defer m.Unlock()
	if s, ok := m.slots[username]; ok {
		return s
	}
	s := &UserSlots{}
	m.slots[username] = s
	return s
}

// Load loads users from users.json, falling back to defaultUser if empty.
func (m *UserManager) Load(defaultUser, defaultPassword string) {
	m.Lock()
	defer m.Unlock()

	file, err := os.Open(m.Path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[iptv-proxy] users file not found at %s, initializing default account %q", m.Path, defaultUser)
		} else {
			log.Printf("[iptv-proxy] error opening users file at %s: %v", m.Path, err)
		}
		if defaultUser != "" {
			m.Users = []UserItem{
				{
					ID:             "user_default",
					Username:       defaultUser,
					Password:       defaultPassword,
					MaxConnections: 0, // unlimited default
					Enabled:        true,
					CreatedAt:      time.Now(),
				},
			}
			go m.saveUnlocked()
		}
		return
	}
	defer file.Close()

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		log.Printf("[iptv-proxy] error reading users file at %s: %v", m.Path, err)
		return
	}

	var users []UserItem
	if err := json.Unmarshal(bytes, &users); err != nil {
		log.Printf("[iptv-proxy] error parsing users file at %s: %v", m.Path, err)
		return
	}

	if len(users) == 0 && defaultUser != "" {
		users = append(users, UserItem{
			ID:             "user_default",
			Username:       defaultUser,
			Password:       defaultPassword,
			MaxConnections: 0,
			Enabled:        true,
			CreatedAt:      time.Now(),
		})
		m.Users = users
		go m.saveUnlocked()
		return
	}

	m.Users = users
	log.Printf("[iptv-proxy] Loaded %d users successfully from %s", len(m.Users), m.Path)
}

func (m *UserManager) saveUnlocked() error {
	dir := filepath.Dir(m.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[iptv-proxy] error creating directory %s for users: %v", dir, err)
	}

	bytes, err := json.MarshalIndent(m.Users, "", "  ")
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(m.Path, bytes, 0644)
	if err != nil {
		log.Printf("[iptv-proxy] error saving users to %s: %v", m.Path, err)
		return err
	}
	log.Printf("[iptv-proxy] Saved %d users to %s successfully", len(m.Users), m.Path)
	return nil
}

// Save writes users list to disk.
func (m *UserManager) Save(users []UserItem) error {
	m.Lock()
	defer m.Unlock()
	m.Users = users
	return m.saveUnlocked()
}

// Authenticate checks username and password against active users.
func (m *UserManager) Authenticate(username, password string) (*UserItem, bool) {
	m.RLock()
	defer m.RUnlock()

	for _, u := range m.Users {
		if !u.Enabled {
			continue
		}
		userMatch := subtle.ConstantTimeCompare([]byte(u.Username), []byte(username)) == 1
		passMatch := subtle.ConstantTimeCompare([]byte(u.Password), []byte(password)) == 1
		if userMatch && passMatch {
			copyU := u
			return &copyU, true
		}
	}
	return nil, false
}

// GetUserByUsername finds a user by username.
func (m *UserManager) GetUserByUsername(username string) (*UserItem, bool) {
	m.RLock()
	defer m.RUnlock()
	for _, u := range m.Users {
		if u.Username == username {
			copyU := u
			return &copyU, true
		}
	}
	return nil, false
}

// GetUsers returns all users.
func (m *UserManager) GetUsers() []UserItem {
	m.RLock()
	defer m.RUnlock()
	res := make([]UserItem, len(m.Users))
	copy(res, m.Users)
	return res
}

// AcquireSlot claims a stream slot for a user, enforcing MaxConnections.
func (m *UserManager) AcquireSlot(username, clientIP string, streamID, streamURL string, cancel context.CancelFunc) (*UserSlot, bool) {
	u, ok := m.GetUserByUsername(username)
	if !ok || !u.Enabled {
		return nil, false
	}
	slots := m.getUserSlots(username)
	return slots.Acquire(u.MaxConnections, clientIP, streamID, streamURL, cancel)
}

// ReleaseSlot releases a stream slot.
func (m *UserManager) ReleaseSlot(username string, slot *UserSlot) {
	slots := m.getUserSlots(username)
	slots.Release(slot)
}

// ActiveConnections returns the number of active streams for a user.
func (m *UserManager) ActiveConnections(username string) int {
	slots := m.getUserSlots(username)
	return slots.ActiveCount()
}

// GetAllActiveSlots returns all active streaming sessions across all users.
func (m *UserManager) GetAllActiveSlots() map[string][]UserSlot {
	m.RLock()
	defer m.RUnlock()

	all := make(map[string][]UserSlot)
	for username, s := range m.slots {
		slots := s.GetActiveSlots()
		if len(slots) > 0 {
			all[username] = slots
		}
	}
	return all
}
