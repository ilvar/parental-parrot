package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	UIPassword         string    `yaml:"ui_password"`
	DefaultRouterBlock bool      `yaml:"default_router_block"` // when true, new devices default to block_method=router + detect_method=router_conntrack
	TrafficThreshold   int       `yaml:"traffic_threshold"`    // min conntrack entries to consider device "active" (default 1)
	Router             *Router   `yaml:"router"`
	Jellyfin           *Jellyfin `yaml:"jellyfin"`
	Devices            []Device  `yaml:"devices"`
	// Schedule is an optional root schedule: when set, schedule.all (and weekday/weekend/per-day)
	// define a shared daily limit across all devices (e.g. schedule.all: 120 = 2 hours total per day).
	Schedule *Schedule `yaml:"schedule"`
}

type Jellyfin struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

type Router struct {
	IP          string `yaml:"ip"`
	SSHPort     string `yaml:"ssh_port"` // optional; defaults to "22"
	SSHUser     string `yaml:"ssh_user"`
	SSHPassword string `yaml:"ssh_password"`
	SSHKey      string `yaml:"ssh_key"`
}

type Device struct {
	Name         string   `yaml:"name"`
	IP           string   `yaml:"ip"`
	SSHPort      string   `yaml:"ssh_port"` // optional; defaults to "22"
	SSHUser      string   `yaml:"ssh_user"`
	SSHPassword  string   `yaml:"ssh_password"`
	SSHKey       string   `yaml:"ssh_key"` // path to private key file
	OS           string   `yaml:"os"`
	MAC          string   `yaml:"mac"`
	BlockMethod  string   `yaml:"block_method"`  // "ssh_shutdown" (default) or "router"
	DetectMethod string   `yaml:"detect_method"` // "ping" (default) or "router_conntrack"
	Schedule     Schedule `yaml:"schedule"`
}

type AllowedHours struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

type Schedule struct {
	All       *int `yaml:"all"`
	Weekday   *int `yaml:"weekday"`
	Weekend   *int `yaml:"weekend"`
	Monday    *int `yaml:"monday"`
	Tuesday   *int `yaml:"tuesday"`
	Wednesday *int `yaml:"wednesday"`
	Thursday  *int `yaml:"thursday"`
	Friday    *int `yaml:"friday"`
	Saturday  *int `yaml:"saturday"`
	Sunday    *int `yaml:"sunday"`

	AllowedHours *AllowedHours `yaml:"allowed_hours"`
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
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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
		if d.DetectMethod != "ping" && d.DetectMethod != "router_conntrack" && d.DetectMethod != "jellyfin" {
			return nil, fmt.Errorf("device %q: unsupported detect_method %q (use ping, router_conntrack, or jellyfin)", d.Name, d.DetectMethod)
		}
		if d.DetectMethod == "router_conntrack" && cfg.Router == nil {
			return nil, fmt.Errorf("device %q: [router] section must be configured when detect_method is \"router_conntrack\"", d.Name)
		}
		if d.DetectMethod == "jellyfin" && cfg.Jellyfin == nil {
			return nil, fmt.Errorf("device %q: [jellyfin] section must be configured when detect_method is \"jellyfin\"", d.Name)
		}

		cfg.Devices[i] = d
	}

	return &cfg, nil
}
