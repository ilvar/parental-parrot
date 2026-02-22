package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestState_NewState(t *testing.T) {
	s := NewState("/tmp/test.json")
	if s.GetUsageToday("1.2.3.4") != 0 {
		t.Error("new state should have zero usage")
	}
	if s.IsOnline("1.2.3.4") {
		t.Error("new state should have devices offline")
	}
	if s.IsDisabled("1.2.3.4") {
		t.Error("new state should not have devices disabled")
	}
}

func TestState_IncrementAndGetUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewState(path)

	ip := "10.0.0.1"
	if got := s.GetUsageToday(ip); got != 0 {
		t.Errorf("initial usage = %d, want 0", got)
	}

	s.IncrementUsage(ip)
	if got := s.GetUsageToday(ip); got != 1 {
		t.Errorf("after 1 increment = %d, want 1", got)
	}

	s.IncrementUsage(ip)
	s.IncrementUsage(ip)
	if got := s.GetUsageToday(ip); got != 3 {
		t.Errorf("after 3 increments = %d, want 3", got)
	}
}

func TestState_MultipleDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewState(path)

	s.IncrementUsage("10.0.0.1")
	s.IncrementUsage("10.0.0.1")
	s.IncrementUsage("10.0.0.2")

	if got := s.GetUsageToday("10.0.0.1"); got != 2 {
		t.Errorf("device 1 usage = %d, want 2", got)
	}
	if got := s.GetUsageToday("10.0.0.2"); got != 1 {
		t.Errorf("device 2 usage = %d, want 1", got)
	}
}

func TestState_OnlineStatus(t *testing.T) {
	s := NewState(filepath.Join(t.TempDir(), "state.json"))
	ip := "10.0.0.1"

	if s.IsOnline(ip) {
		t.Error("should be offline initially")
	}

	s.SetOnline(ip, true)
	if !s.IsOnline(ip) {
		t.Error("should be online after SetOnline(true)")
	}

	s.SetOnline(ip, false)
	if s.IsOnline(ip) {
		t.Error("should be offline after SetOnline(false)")
	}
}

func TestState_DisableFor(t *testing.T) {
	s := NewState(filepath.Join(t.TempDir(), "state.json"))
	ip := "10.0.0.1"

	if s.IsDisabled(ip) {
		t.Error("should not be disabled initially")
	}

	s.DisableFor(ip, 1*time.Hour)
	if !s.IsDisabled(ip) {
		t.Error("should be disabled after DisableFor")
	}

	until := s.GetDisabledUntil(ip)
	if until.IsZero() {
		t.Error("DisabledUntil should not be zero")
	}
	if time.Until(until) < 59*time.Minute {
		t.Error("DisabledUntil should be roughly 1 hour from now")
	}
}

func TestState_DisableExpires(t *testing.T) {
	s := NewState(filepath.Join(t.TempDir(), "state.json"))
	ip := "10.0.0.1"

	// Disable for a tiny duration that will expire immediately
	s.DisableFor(ip, 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	if s.IsDisabled(ip) {
		t.Error("should no longer be disabled after expiry")
	}
}

func TestState_SaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// Create state, add data, save
	s1 := NewState(path)
	s1.IncrementUsage("10.0.0.1")
	s1.IncrementUsage("10.0.0.1")
	s1.SetOnline("10.0.0.1", true)
	s1.DisableFor("10.0.0.1", 1*time.Hour)

	// Load into new state
	s2 := NewState(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := s2.GetUsageToday("10.0.0.1"); got != 2 {
		t.Errorf("loaded usage = %d, want 2", got)
	}
}

func TestState_LoadNonexistent(t *testing.T) {
	s := NewState(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err := s.Load(); err != nil {
		t.Errorf("Load nonexistent should not error, got: %v", err)
	}
}

func TestState_SaveCreatesValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewState(path)
	s.IncrementUsage("10.0.0.1")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("state.json is not valid JSON: %v", err)
	}

	if _, ok := parsed["usage"]; !ok {
		t.Error("state.json missing 'usage' key")
	}
}

func TestState_PruneOldUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewState(path)

	// Manually insert old usage entries
	ip := "10.0.0.1"
	s.mu.Lock()
	s.data.Usage[ip] = map[string]int{
		"2020-01-01": 50, // very old
		"2020-06-15": 30, // old
		dateKey(time.Now()): 10, // today
	}
	s.saveLocked()
	s.mu.Unlock()

	// Reload — pruning happens on Load
	s2 := NewState(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	s2.mu.Lock()
	defer s2.mu.Unlock()

	dates := s2.data.Usage[ip]
	if _, ok := dates["2020-01-01"]; ok {
		t.Error("old entry 2020-01-01 should have been pruned")
	}
	if _, ok := dates["2020-06-15"]; ok {
		t.Error("old entry 2020-06-15 should have been pruned")
	}

	today := dateKey(time.Now())
	if dates[today] != 10 {
		t.Errorf("today's usage = %d, want 10 (should be preserved)", dates[today])
	}
}

func TestState_PruneRemovesEmptyDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewState(path)

	// Insert only old data for a device
	s.mu.Lock()
	s.data.Usage["10.0.0.99"] = map[string]int{
		"2020-01-01": 50,
	}
	s.saveLocked()
	s.mu.Unlock()

	s2 := NewState(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	s2.mu.Lock()
	defer s2.mu.Unlock()

	if _, ok := s2.data.Usage["10.0.0.99"]; ok {
		t.Error("device with only old entries should be pruned entirely")
	}
}
