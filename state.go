package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type StateData struct {
	// Usage[deviceIP][dateString] = minutes used
	Usage        map[string]map[string]int `json:"usage"`
	DisabledUntil map[string]time.Time     `json:"disabled_until"`
	// Online tracks last ping result per device
	Online map[string]bool `json:"online"`
	// RouterBlocked tracks which devices have an active firewall rule on the router
	RouterBlocked map[string]bool `json:"router_blocked"`
}

type State struct {
	mu   sync.Mutex
	data StateData
	path string
}

func NewState(path string) *State {
	return &State{
		path: path,
		data: StateData{
			Usage:         make(map[string]map[string]int),
			DisabledUntil: make(map[string]time.Time),
			Online:        make(map[string]bool),
			RouterBlocked: make(map[string]bool),
		},
	}
}

func (s *State) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no state file yet, start fresh
		}
		return err
	}
	defer f.Close()

	if err := json.NewDecoder(f).Decode(&s.data); err != nil {
		return err
	}

	if s.data.Usage == nil {
		s.data.Usage = make(map[string]map[string]int)
	}
	if s.data.DisabledUntil == nil {
		s.data.DisabledUntil = make(map[string]time.Time)
	}
	if s.data.Online == nil {
		s.data.Online = make(map[string]bool)
	}
	if s.data.RouterBlocked == nil {
		s.data.RouterBlocked = make(map[string]bool)
	}
	s.pruneOldUsageLocked(7)
	return nil
}

func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *State) saveLocked() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func dateKey(t time.Time) string {
	return t.Format("2006-01-02")
}

// IncrementUsage adds 1 minute of usage for a device on today's date.
func (s *State) IncrementUsage(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	today := dateKey(time.Now())
	if s.data.Usage[ip] == nil {
		s.data.Usage[ip] = make(map[string]int)
	}
	s.data.Usage[ip][today]++
	s.saveLocked()
}

// GetUsageToday returns today's usage in minutes for a device.
func (s *State) GetUsageToday(ip string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	today := dateKey(time.Now())
	if s.data.Usage[ip] == nil {
		return 0
	}
	return s.data.Usage[ip][today]
}

// SetOnline sets the online status for a device.
func (s *State) SetOnline(ip string, online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Online[ip] = online
}

// IsOnline returns whether a device is currently online.
func (s *State) IsOnline(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Online[ip]
}

// SetRouterBlocked sets whether a device is currently blocked via router firewall rule.
func (s *State) SetRouterBlocked(ip string, blocked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.RouterBlocked[ip] = blocked
	s.saveLocked()
}

// IsRouterBlocked returns whether a device currently has an active router firewall rule.
func (s *State) IsRouterBlocked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.RouterBlocked[ip]
}

// DisableFor sets a device as "disabled" (no blocking) for the given duration.
func (s *State) DisableFor(ip string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.DisabledUntil[ip] = time.Now().Add(d)
	s.saveLocked()
}

// IsDisabled returns whether blocking is currently disabled for a device.
func (s *State) IsDisabled(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.data.DisabledUntil[ip]
	if !ok {
		return false
	}
	return time.Now().Before(until)
}

// GetDisabledUntil returns the time until which blocking is disabled, or zero time.
func (s *State) GetDisabledUntil(ip string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.DisabledUntil[ip]
}

// pruneOldUsageLocked removes usage entries older than keepDays. Must be called with mu held.
func (s *State) pruneOldUsageLocked(keepDays int) {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	for ip, dates := range s.data.Usage {
		for date := range dates {
			if date < cutoff {
				delete(dates, date)
			}
		}
		if len(dates) == 0 {
			delete(s.data.Usage, ip)
		}
	}
}
