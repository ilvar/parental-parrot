package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func intPtr(v int) *int { return &v }

func TestSchedule_LimitForDay_All(t *testing.T) {
	s := Schedule{All: intPtr(120)}
	for day := time.Sunday; day <= time.Saturday; day++ {
		if got := s.LimitForDay(day); got != 120 {
			t.Errorf("LimitForDay(%v) = %d, want 120", day, got)
		}
	}
}

func TestSchedule_LimitForDay_WeekdayWeekend(t *testing.T) {
	s := Schedule{Weekday: intPtr(60), Weekend: intPtr(180)}

	weekdays := []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	for _, day := range weekdays {
		if got := s.LimitForDay(day); got != 60 {
			t.Errorf("LimitForDay(%v) = %d, want 60", day, got)
		}
	}

	weekends := []time.Weekday{time.Saturday, time.Sunday}
	for _, day := range weekends {
		if got := s.LimitForDay(day); got != 180 {
			t.Errorf("LimitForDay(%v) = %d, want 180", day, got)
		}
	}
}

func TestSchedule_LimitForDay_PerDayOverride(t *testing.T) {
	s := Schedule{
		All:     intPtr(120),
		Weekday: intPtr(60),
		Monday:  intPtr(30),
	}

	if got := s.LimitForDay(time.Monday); got != 30 {
		t.Errorf("Monday = %d, want 30 (per-day override)", got)
	}
	if got := s.LimitForDay(time.Tuesday); got != 60 {
		t.Errorf("Tuesday = %d, want 60 (weekday fallback)", got)
	}
	if got := s.LimitForDay(time.Saturday); got != 120 {
		t.Errorf("Saturday = %d, want 120 (all fallback)", got)
	}
}

func TestSchedule_LimitForDay_Unlimited(t *testing.T) {
	s := Schedule{}
	if got := s.LimitForDay(time.Monday); got != -1 {
		t.Errorf("LimitForDay(Monday) = %d, want -1 (unlimited)", got)
	}
}

func TestSchedule_IsAllowedHour(t *testing.T) {
	s := Schedule{
		AllowedHours: &AllowedHours{Start: "08:00", End: "21:00"},
	}

	tests := []struct {
		name string
		time string
		want bool
	}{
		{"before start", "07:59", false},
		{"at start", "08:00", true},
		{"midday", "12:00", true},
		{"just before end", "20:59", true},
		{"at end", "21:00", false},
		{"after end", "22:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := time.Parse("15:04", tt.time)
			if got := s.IsAllowedHour(parsed); got != tt.want {
				t.Errorf("IsAllowedHour(%s) = %v, want %v", tt.time, got, tt.want)
			}
		})
	}
}

func TestSchedule_IsAllowedHour_NoRestriction(t *testing.T) {
	s := Schedule{}
	now := time.Now()
	if !s.IsAllowedHour(now) {
		t.Error("IsAllowedHour should return true when no allowed_hours configured")
	}
}

func TestSchedule_IsAllowedHour_EmptyFields(t *testing.T) {
	s := Schedule{AllowedHours: &AllowedHours{}}
	now := time.Now()
	if !s.IsAllowedHour(now) {
		t.Error("IsAllowedHour should return true when start/end are empty")
	}
}

func TestLoadConfig(t *testing.T) {
	content := `
ui_password = "testpass"

[[devices]]
name = "Test PC"
ip = "10.0.0.1"
ssh_user = "user"
ssh_password = "pass"
os = "linux"

[devices.schedule]
weekday = 60
weekend = 120

[devices.schedule.allowed_hours]
start = "09:00"
end = "20:00"

[[devices]]
name = "Win PC"
ip = "10.0.0.2"
ssh_user = "admin"
ssh_password = "pass2"
os = "Windows"

[devices.schedule]
all = 90
`
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.UIPassword != "testpass" {
		t.Errorf("UIPassword = %q, want %q", cfg.UIPassword, "testpass")
	}
	if len(cfg.Devices) != 2 {
		t.Fatalf("len(Devices) = %d, want 2", len(cfg.Devices))
	}

	d0 := cfg.Devices[0]
	if d0.Name != "Test PC" || d0.IP != "10.0.0.1" || d0.OS != "linux" {
		t.Errorf("Device 0: got %+v", d0)
	}
	if d0.Schedule.LimitForDay(time.Monday) != 60 {
		t.Errorf("Device 0 Monday limit = %d, want 60", d0.Schedule.LimitForDay(time.Monday))
	}
	if d0.Schedule.LimitForDay(time.Sunday) != 120 {
		t.Errorf("Device 0 Sunday limit = %d, want 120", d0.Schedule.LimitForDay(time.Sunday))
	}

	d1 := cfg.Devices[1]
	if d1.OS != "windows" {
		t.Errorf("Device 1 OS = %q, want %q (should be lowercased)", d1.OS, "windows")
	}
	if d1.Schedule.LimitForDay(time.Wednesday) != 90 {
		t.Errorf("Device 1 Wednesday limit = %d, want 90", d1.Schedule.LimitForDay(time.Wednesday))
	}
}

func TestLoadConfig_DefaultOS(t *testing.T) {
	content := `
ui_password = "x"
[[devices]]
name = "D"
ip = "1.2.3.4"
ssh_user = "u"
ssh_password = "p"

[devices.schedule]
all = 60
`
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Devices[0].OS != "linux" {
		t.Errorf("OS = %q, want %q (default)", cfg.Devices[0].OS, "linux")
	}
}

func TestLoadConfig_InvalidOS(t *testing.T) {
	content := `
ui_password = "x"
[[devices]]
name = "D"
ip = "1.2.3.4"
ssh_user = "u"
ssh_password = "p"
os = "freebsd"

[devices.schedule]
all = 60
`
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(content), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unsupported OS, got nil")
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_SSHKey(t *testing.T) {
	content := `
ui_password = "x"
[[devices]]
name = "D"
ip = "1.2.3.4"
ssh_user = "u"
ssh_key = "/home/user/.ssh/id_ed25519"

[devices.schedule]
all = 60
`
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Devices[0].SSHKey != "/home/user/.ssh/id_ed25519" {
		t.Errorf("SSHKey = %q, want path", cfg.Devices[0].SSHKey)
	}
	if cfg.Devices[0].SSHPassword != "" {
		t.Errorf("SSHPassword = %q, want empty", cfg.Devices[0].SSHPassword)
	}
}
