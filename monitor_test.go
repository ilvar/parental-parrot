package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMonitor_ShouldUnblock_DisabledDevice(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		Router: &Router{IP: "192.168.1.1", SSHUser: "root", SSHPassword: "pass"},
		Devices: []Device{
			{
				Name:         "Phone",
				IP:           "10.0.0.5",
				MAC:          "AA:BB:CC:DD:EE:FF",
				BlockMethod:  "router",
				DetectMethod: "ping",
				Schedule:     Schedule{All: intPtr(120)},
			},
		},
	}
	m := NewMonitor(cfg, state)

	// Device is disabled → should unblock
	state.DisableFor("10.0.0.5", 1*time.Hour)
	if !m.shouldUnblock(&cfg.Devices[0]) {
		t.Error("should unblock when device is disabled")
	}
}

func TestMonitor_ShouldUnblock_UnderLimit(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		Router: &Router{IP: "192.168.1.1", SSHUser: "root", SSHPassword: "pass"},
		Devices: []Device{
			{
				Name:         "Phone",
				IP:           "10.0.0.5",
				MAC:          "AA:BB:CC:DD:EE:FF",
				BlockMethod:  "router",
				DetectMethod: "ping",
				Schedule:     Schedule{All: intPtr(120)},
			},
		},
	}
	m := NewMonitor(cfg, state)

	// Under the limit and within allowed hours (no hours restriction) → should unblock
	if !m.shouldUnblock(&cfg.Devices[0]) {
		t.Error("should unblock when under daily limit and no hour restrictions")
	}
}

func TestMonitor_ShouldUnblock_OverLimit(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		Router: &Router{IP: "192.168.1.1", SSHUser: "root", SSHPassword: "pass"},
		Devices: []Device{
			{
				Name:         "Phone",
				IP:           "10.0.0.5",
				MAC:          "AA:BB:CC:DD:EE:FF",
				BlockMethod:  "router",
				DetectMethod: "ping",
				Schedule:     Schedule{All: intPtr(2)},
			},
		},
	}
	m := NewMonitor(cfg, state)

	// Exceed the limit
	state.IncrementUsage("10.0.0.5")
	state.IncrementUsage("10.0.0.5")
	state.IncrementUsage("10.0.0.5")

	if m.shouldUnblock(&cfg.Devices[0]) {
		t.Error("should not unblock when over daily limit")
	}
}

func TestMonitor_ShouldUnblock_Unlimited(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		Router: &Router{IP: "192.168.1.1", SSHUser: "root", SSHPassword: "pass"},
		Devices: []Device{
			{
				Name:         "Phone",
				IP:           "10.0.0.5",
				MAC:          "AA:BB:CC:DD:EE:FF",
				BlockMethod:  "router",
				DetectMethod: "ping",
				Schedule:     Schedule{}, // unlimited
			},
		},
	}
	m := NewMonitor(cfg, state)

	if !m.shouldUnblock(&cfg.Devices[0]) {
		t.Error("should unblock when schedule is unlimited")
	}
}

func TestMonitor_ShouldUnblock_RootSchedule(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		Router:   &Router{IP: "192.168.1.1", SSHUser: "root", SSHPassword: "pass"},
		Schedule: &Schedule{All: intPtr(5)}, // 5 min shared pool
		Devices: []Device{
			{Name: "PC", IP: "10.0.0.1", MAC: "AA:BB:CC:DD:EE:01", BlockMethod: "router", DetectMethod: "ping", Schedule: Schedule{All: intPtr(120)}},
			{Name: "Phone", IP: "10.0.0.5", MAC: "AA:BB:CC:DD:EE:FF", BlockMethod: "router", DetectMethod: "ping", Schedule: Schedule{All: intPtr(120)}},
		},
	}
	m := NewMonitor(cfg, state)

	// Total 3 min (PC 2, Phone 1) < 5 → should unblock
	state.IncrementUsage("10.0.0.1")
	state.IncrementUsage("10.0.0.1")
	state.IncrementUsage("10.0.0.5")
	if !m.shouldUnblock(&cfg.Devices[0]) || !m.shouldUnblock(&cfg.Devices[1]) {
		t.Error("should unblock when shared pool total under limit")
	}

	// Total 5 min (add 2 more) → at limit, should not unblock
	state.IncrementUsage("10.0.0.5")
	state.IncrementUsage("10.0.0.5")
	if m.shouldUnblock(&cfg.Devices[0]) || m.shouldUnblock(&cfg.Devices[1]) {
		t.Error("should not unblock when shared pool total at or over limit")
	}
}

func TestMonitor_BlockDispatch_DefaultSSHShutdown(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		Devices: []Device{
			{
				Name:         "PC",
				IP:           "127.0.0.1",
				SSHPort:      "1",
				SSHUser:      "user",
				SSHPassword:  "pass",
				OS:           "linux",
				BlockMethod:  "ssh_shutdown",
				DetectMethod: "ping",
				Schedule:     Schedule{All: intPtr(120)},
			},
		},
	}
	m := NewMonitor(cfg, state)

	// block() for ssh_shutdown will fail (no SSH server), but should not panic
	m.block(&cfg.Devices[0])
}

func TestMonitor_BlockDispatch_Router(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		Router: &Router{IP: "127.0.0.1", SSHPort: "1", SSHUser: "root", SSHPassword: "pass"},
		Devices: []Device{
			{
				Name:         "Phone",
				IP:           "10.0.0.5",
				MAC:          "AA:BB:CC:DD:EE:FF",
				BlockMethod:  "router",
				DetectMethod: "ping",
				Schedule:     Schedule{All: intPtr(120)},
			},
		},
	}
	m := NewMonitor(cfg, state)

	// routerBlock will fail (no SSH server), but should not panic
	m.block(&cfg.Devices[0])

	// Since SSH fails, state should NOT be set to blocked
	if state.IsRouterBlocked("10.0.0.5") {
		t.Error("should not be marked as router-blocked when SSH fails")
	}
}

func TestMonitor_RouterUnblock_NotBlocked(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		Router: &Router{IP: "192.168.1.1", SSHUser: "root", SSHPassword: "pass"},
		Devices: []Device{
			{
				Name:         "Phone",
				IP:           "10.0.0.5",
				MAC:          "AA:BB:CC:DD:EE:FF",
				BlockMethod:  "router",
				DetectMethod: "ping",
				Schedule:     Schedule{All: intPtr(120)},
			},
		},
	}
	m := NewMonitor(cfg, state)

	// RouterUnblock when not blocked should be a no-op (no panic, no SSH call)
	m.RouterUnblock(&cfg.Devices[0])
}

func TestSSHTarget_FromDevice(t *testing.T) {
	target := SSHTarget{
		IP:       "10.0.0.1",
		User:     "admin",
		Password: "secret",
		KeyPath:  "/path/to/key",
	}

	if target.IP != "10.0.0.1" {
		t.Errorf("IP = %q, want %q", target.IP, "10.0.0.1")
	}
	if target.User != "admin" {
		t.Errorf("User = %q, want %q", target.User, "admin")
	}
	if target.Password != "secret" {
		t.Errorf("Password = %q, want %q", target.Password, "secret")
	}
	if target.KeyPath != "/path/to/key" {
		t.Errorf("KeyPath = %q, want %q", target.KeyPath, "/path/to/key")
	}
}
