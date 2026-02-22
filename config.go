package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	UIPassword string   `toml:"ui_password"`
	Router     *Router  `toml:"router"`
	Devices    []Device `toml:"devices"`
}

type Router struct {
	IP          string `toml:"ip"`
	SSHUser     string `toml:"ssh_user"`
	SSHPassword string `toml:"ssh_password"`
	SSHKey      string `toml:"ssh_key"`
}

type Device struct {
	Name         string   `toml:"name"`
	IP           string   `toml:"ip"`
	SSHUser      string   `toml:"ssh_user"`
	SSHPassword  string   `toml:"ssh_password"`
	SSHKey       string   `toml:"ssh_key"` // path to private key file
	OS           string   `toml:"os"`
	MAC          string   `toml:"mac"`
	BlockMethod  string   `toml:"block_method"`  // "ssh_shutdown" (default) or "router"
	DetectMethod string   `toml:"detect_method"` // "ping" (default) or "router_conntrack"
	Schedule     Schedule `toml:"schedule"`
}

type AllowedHours struct {
	Start string `toml:"start"`
	End   string `toml:"end"`
}

type Schedule struct {
	All      *int `toml:"all"`
	Weekday  *int `toml:"weekday"`
	Weekend  *int `toml:"weekend"`
	Monday   *int `toml:"monday"`
	Tuesday  *int `toml:"tuesday"`
	Wednesday *int `toml:"wednesday"`
	Thursday *int `toml:"thursday"`
	Friday   *int `toml:"friday"`
	Saturday *int `toml:"saturday"`
	Sunday   *int `toml:"sunday"`

	AllowedHours *AllowedHours `toml:"allowed_hours"`
}

// LimitForDay returns the daily time limit in minutes for the given weekday.
// Returns -1 if no limit is configured (unlimited).
func (s *Schedule) LimitForDay(day time.Weekday) int {
	// Check per-day override first
	var perDay *int
	switch day {
	case time.Monday:
		perDay = s.Monday
	case time.Tuesday:
		perDay = s.Tuesday
	case time.Wednesday:
		perDay = s.Wednesday
	case time.Thursday:
		perDay = s.Thursday
	case time.Friday:
		perDay = s.Friday
	case time.Saturday:
		perDay = s.Saturday
	case time.Sunday:
		perDay = s.Sunday
	}
	if perDay != nil {
		return *perDay
	}

	// Check weekday/weekend
	isWeekend := day == time.Saturday || day == time.Sunday
	if isWeekend && s.Weekend != nil {
		return *s.Weekend
	}
	if !isWeekend && s.Weekday != nil {
		return *s.Weekday
	}

	// Check "all"
	if s.All != nil {
		return *s.All
	}

	return -1 // unlimited
}

// IsAllowedHour checks whether the given time falls within the allowed hours.
// Returns true if no allowed_hours are configured.
func (s *Schedule) IsAllowedHour(t time.Time) bool {
	if s.AllowedHours == nil {
		return true
	}
	if s.AllowedHours.Start == "" || s.AllowedHours.End == "" {
		return true
	}

	now := t.Format("15:04")
	start := s.AllowedHours.Start
	end := s.AllowedHours.End

	return now >= start && now < end
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	for i, d := range cfg.Devices {
		d.OS = strings.ToLower(d.OS)
		if d.OS == "" {
			d.OS = "linux"
		}
		if d.OS != "linux" && d.OS != "windows" && d.OS != "macos" {
			return nil, fmt.Errorf("device %q: unsupported OS %q (use linux, macos, or windows)", d.Name, d.OS)
		}

		if d.BlockMethod == "" {
			d.BlockMethod = "ssh_shutdown"
		}
		if d.BlockMethod != "ssh_shutdown" && d.BlockMethod != "router" {
			return nil, fmt.Errorf("device %q: unsupported block_method %q (use ssh_shutdown or router)", d.Name, d.BlockMethod)
		}
		if d.BlockMethod == "router" {
			if d.MAC == "" {
				return nil, fmt.Errorf("device %q: mac is required when block_method is \"router\"", d.Name)
			}
			if cfg.Router == nil {
				return nil, fmt.Errorf("device %q: [router] section must be configured when block_method is \"router\"", d.Name)
			}
		}

		if d.DetectMethod == "" {
			d.DetectMethod = "ping"
		}
		if d.DetectMethod != "ping" && d.DetectMethod != "router_conntrack" {
			return nil, fmt.Errorf("device %q: unsupported detect_method %q (use ping or router_conntrack)", d.Name, d.DetectMethod)
		}
		if d.DetectMethod == "router_conntrack" && cfg.Router == nil {
			return nil, fmt.Errorf("device %q: [router] section must be configured when detect_method is \"router_conntrack\"", d.Name)
		}

		cfg.Devices[i] = d
	}

	return &cfg, nil
}
