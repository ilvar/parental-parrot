package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Single writer recommended for sqlite; limit connections
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}
	return db, nil
}

func InitSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS config (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		ui_password TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS root_schedule (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		all_mins INTEGER,
		weekday INTEGER,
		weekend INTEGER,
		monday INTEGER, tuesday INTEGER, wednesday INTEGER, thursday INTEGER, friday INTEGER, saturday INTEGER, sunday INTEGER,
		allowed_start TEXT,
		allowed_end TEXT
	);
	CREATE TABLE IF NOT EXISTS router (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		ip TEXT NOT NULL DEFAULT '',
		ssh_port TEXT NOT NULL DEFAULT '',
		ssh_user TEXT NOT NULL DEFAULT '',
		ssh_password TEXT NOT NULL DEFAULT '',
		ssh_key TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS jellyfin (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		url TEXT NOT NULL DEFAULT '',
		api_key TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS devices (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		ip TEXT NOT NULL UNIQUE,
		ssh_port TEXT NOT NULL DEFAULT '',
		ssh_user TEXT NOT NULL DEFAULT '',
		ssh_password TEXT NOT NULL DEFAULT '',
		ssh_key TEXT NOT NULL DEFAULT '',
		os TEXT NOT NULL DEFAULT 'linux',
		mac TEXT NOT NULL DEFAULT '',
		block_method TEXT NOT NULL DEFAULT 'ssh_shutdown',
		detect_method TEXT NOT NULL DEFAULT 'ping',
		sched_all INTEGER, sched_weekday INTEGER, sched_weekend INTEGER,
		sched_monday INTEGER, sched_tuesday INTEGER, sched_wednesday INTEGER, sched_thursday INTEGER,
		sched_friday INTEGER, sched_saturday INTEGER, sched_sunday INTEGER,
		allowed_start TEXT, allowed_end TEXT
	);
	CREATE TABLE IF NOT EXISTS usage (
		device_ip TEXT NOT NULL,
		date TEXT NOT NULL,
		minutes INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (device_ip, date)
	);
	CREATE TABLE IF NOT EXISTS disabled_until (
		device_ip TEXT PRIMARY KEY,
		until_ts INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS online (
		device_ip TEXT PRIMARY KEY,
		online INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS router_blocked (
		device_ip TEXT PRIMARY KEY,
		blocked INTEGER NOT NULL DEFAULT 0
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	// Migrations: add columns (ignore if column already exists)
	_, _ = db.Exec("ALTER TABLE config ADD COLUMN default_router_block INTEGER DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE config ADD COLUMN traffic_threshold INTEGER DEFAULT 1")
	_, _ = db.Exec("ALTER TABLE router ADD COLUMN ssh_port TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE devices ADD COLUMN ssh_port TEXT NOT NULL DEFAULT ''")
	return nil
}

func SeedFromYAMLIfEmpty(db *sql.DB, yamlPath string) error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM devices").Scan(&n); err != nil {
		return fmt.Errorf("count devices: %w", err)
	}
	if n > 0 {
		return nil // already populated
	}
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("read seed yaml: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse seed yaml: %w", err)
	}
	return SaveConfigToDB(db, &cfg)
}

func LoadConfigFromDB(db *sql.DB) (*Config, error) {
	cfg := &Config{}

	// config
	var pw string
	var defaultRouter, trafficThreshold int
	err := db.QueryRow("SELECT ui_password, COALESCE(default_router_block, 0), COALESCE(traffic_threshold, 1) FROM config WHERE id = 1").Scan(&pw, &defaultRouter, &trafficThreshold)
	if err == sql.ErrNoRows {
		_, _ = db.Exec("INSERT INTO config (id, ui_password, default_router_block, traffic_threshold) VALUES (1, '', 0, 1)")
		pw = ""
		defaultRouter = 0
		trafficThreshold = 1
	} else if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.UIPassword = pw
	cfg.DefaultRouterBlock = defaultRouter != 0
	if trafficThreshold < 1 {
		trafficThreshold = 1
	}
	cfg.TrafficThreshold = trafficThreshold

	// root schedule
	var all, wd, we, mon, tue, wed, thu, fri, sat, sun sql.NullInt64
	var allowStart, allowEnd sql.NullString
	err = db.QueryRow(`SELECT all_mins, weekday, weekend, monday, tuesday, wednesday, thursday, friday, saturday, sunday, allowed_start, allowed_end FROM root_schedule WHERE id = 1`).Scan(
		&all, &wd, &we, &mon, &tue, &wed, &thu, &fri, &sat, &sun, &allowStart, &allowEnd)
	if err == nil {
		cfg.Schedule = &Schedule{}
		if all.Valid {
			x := int(all.Int64)
			cfg.Schedule.All = &x
		}
		if wd.Valid {
			x := int(wd.Int64)
			cfg.Schedule.Weekday = &x
		}
		if we.Valid {
			x := int(we.Int64)
			cfg.Schedule.Weekend = &x
		}
		if mon.Valid {
			x := int(mon.Int64)
			cfg.Schedule.Monday = &x
		}
		if tue.Valid {
			x := int(tue.Int64)
			cfg.Schedule.Tuesday = &x
		}
		if wed.Valid {
			x := int(wed.Int64)
			cfg.Schedule.Wednesday = &x
		}
		if thu.Valid {
			x := int(thu.Int64)
			cfg.Schedule.Thursday = &x
		}
		if fri.Valid {
			x := int(fri.Int64)
			cfg.Schedule.Friday = &x
		}
		if sat.Valid {
			x := int(sat.Int64)
			cfg.Schedule.Saturday = &x
		}
		if sun.Valid {
			x := int(sun.Int64)
			cfg.Schedule.Sunday = &x
		}
		if allowStart.Valid && allowEnd.Valid && (allowStart.String != "" || allowEnd.String != "") {
			cfg.Schedule.AllowedHours = &AllowedHours{Start: allowStart.String, End: allowEnd.String}
		}
		// check if schedule is effectively empty
		if cfg.Schedule.All == nil && cfg.Schedule.Weekday == nil && cfg.Schedule.Weekend == nil &&
			cfg.Schedule.Monday == nil && cfg.Schedule.Tuesday == nil && cfg.Schedule.Wednesday == nil &&
			cfg.Schedule.Thursday == nil && cfg.Schedule.Friday == nil && cfg.Schedule.Saturday == nil &&
			cfg.Schedule.Sunday == nil && cfg.Schedule.AllowedHours == nil {
			cfg.Schedule = nil
		}
	}

	// router
	var rip, rport, ruser, rpass, rkey string
	err = db.QueryRow("SELECT ip, ssh_port, ssh_user, ssh_password, ssh_key FROM router WHERE id = 1").Scan(&rip, &rport, &ruser, &rpass, &rkey)
	if err == nil && rip != "" {
		cfg.Router = &Router{IP: rip, SSHPort: rport, SSHUser: ruser, SSHPassword: rpass, SSHKey: rkey}
	}

	// jellyfin
	var jurl, jkey string
	err = db.QueryRow("SELECT url, api_key FROM jellyfin WHERE id = 1").Scan(&jurl, &jkey)
	if err == nil && jurl != "" {
		cfg.Jellyfin = &Jellyfin{URL: jurl, APIKey: jkey}
	}

	// devices
	rows, err := db.Query(`SELECT name, ip, ssh_port, ssh_user, ssh_password, ssh_key, os, mac, block_method, detect_method,
		sched_all, sched_weekday, sched_weekend, sched_monday, sched_tuesday, sched_wednesday, sched_thursday,
		sched_friday, sched_saturday, sched_sunday, allowed_start, allowed_end FROM devices ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("devices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d Device
		var os, mac, bm, dm string
		var sa, swd, swe, smon, stue, swed, sthu, sfri, ssat, ssun sql.NullInt64
		var as, ae sql.NullString
		err := rows.Scan(&d.Name, &d.IP, &d.SSHPort, &d.SSHUser, &d.SSHPassword, &d.SSHKey, &os, &mac, &bm, &dm,
			&sa, &swd, &swe, &smon, &stue, &swed, &sthu, &sfri, &ssat, &ssun, &as, &ae)
		if err != nil {
			return nil, fmt.Errorf("device row: %w", err)
		}
		d.OS = strings.ToLower(os)
		if d.OS == "" {
			d.OS = "linux"
		}
		d.MAC = mac
		d.BlockMethod = bm
		if d.BlockMethod == "" {
			d.BlockMethod = "ssh_shutdown"
		}
		d.DetectMethod = dm
		if d.DetectMethod == "" {
			d.DetectMethod = "ping"
		}
		d.Schedule = scheduleFromNulls(sa, swd, swe, smon, stue, swed, sthu, sfri, ssat, ssun, as, ae)
		cfg.Devices = append(cfg.Devices, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func scheduleFromNulls(sa, swd, swe, smon, stue, swed, sthu, sfri, ssat, ssun sql.NullInt64, as, ae sql.NullString) Schedule {
	s := Schedule{}
	if sa.Valid {
		x := int(sa.Int64)
		s.All = &x
	}
	if swd.Valid {
		x := int(swd.Int64)
		s.Weekday = &x
	}
	if swe.Valid {
		x := int(swe.Int64)
		s.Weekend = &x
	}
	if smon.Valid {
		x := int(smon.Int64)
		s.Monday = &x
	}
	if stue.Valid {
		x := int(stue.Int64)
		s.Tuesday = &x
	}
	if swed.Valid {
		x := int(swed.Int64)
		s.Wednesday = &x
	}
	if sthu.Valid {
		x := int(sthu.Int64)
		s.Thursday = &x
	}
	if sfri.Valid {
		x := int(sfri.Int64)
		s.Friday = &x
	}
	if ssat.Valid {
		x := int(ssat.Int64)
		s.Saturday = &x
	}
	if ssun.Valid {
		x := int(ssun.Int64)
		s.Sunday = &x
	}
	if as.Valid && ae.Valid && (as.String != "" || ae.String != "") {
		s.AllowedHours = &AllowedHours{Start: as.String, End: ae.String}
	}
	return s
}

func scheduleToNulls(s *Schedule) (all, wd, we, mon, tue, wed, thu, fri, sat, sun sql.NullInt64, allowStart, allowEnd sql.NullString) {
	if s == nil {
		return
	}
	if s.All != nil {
		all = sql.NullInt64{Int64: int64(*s.All), Valid: true}
	}
	if s.Weekday != nil {
		wd = sql.NullInt64{Int64: int64(*s.Weekday), Valid: true}
	}
	if s.Weekend != nil {
		we = sql.NullInt64{Int64: int64(*s.Weekend), Valid: true}
	}
	if s.Monday != nil {
		mon = sql.NullInt64{Int64: int64(*s.Monday), Valid: true}
	}
	if s.Tuesday != nil {
		tue = sql.NullInt64{Int64: int64(*s.Tuesday), Valid: true}
	}
	if s.Wednesday != nil {
		wed = sql.NullInt64{Int64: int64(*s.Wednesday), Valid: true}
	}
	if s.Thursday != nil {
		thu = sql.NullInt64{Int64: int64(*s.Thursday), Valid: true}
	}
	if s.Friday != nil {
		fri = sql.NullInt64{Int64: int64(*s.Friday), Valid: true}
	}
	if s.Saturday != nil {
		sat = sql.NullInt64{Int64: int64(*s.Saturday), Valid: true}
	}
	if s.Sunday != nil {
		sun = sql.NullInt64{Int64: int64(*s.Sunday), Valid: true}
	}
	if s.AllowedHours != nil {
		allowStart = sql.NullString{String: s.AllowedHours.Start, Valid: true}
		allowEnd = sql.NullString{String: s.AllowedHours.End, Valid: true}
	}
	return
}

func SaveConfigToDB(db *sql.DB, cfg *Config) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// config
	defaultRouter := 0
	if cfg.DefaultRouterBlock {
		defaultRouter = 1
	}
	thresh := cfg.TrafficThreshold
	if thresh < 1 {
		thresh = 1
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO config (id, ui_password, default_router_block, traffic_threshold) VALUES (1, ?, ?, ?)", cfg.UIPassword, defaultRouter, thresh)
	if err != nil {
		return err
	}

	// root schedule
	all, wd, we, mon, tue, wed, thu, fri, sat, sun, allowStart, allowEnd := scheduleToNulls(cfg.Schedule)
	_, err = tx.Exec(`INSERT OR REPLACE INTO root_schedule (id, all_mins, weekday, weekend, monday, tuesday, wednesday, thursday, friday, saturday, sunday, allowed_start, allowed_end)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		all, wd, we, mon, tue, wed, thu, fri, sat, sun, allowStart, allowEnd)
	if err != nil {
		return err
	}

	// router
	if cfg.Router != nil {
		_, err = tx.Exec("INSERT OR REPLACE INTO router (id, ip, ssh_port, ssh_user, ssh_password, ssh_key) VALUES (1, ?, ?, ?, ?, ?)",
			cfg.Router.IP, cfg.Router.SSHPort, cfg.Router.SSHUser, cfg.Router.SSHPassword, cfg.Router.SSHKey)
	} else {
		_, err = tx.Exec("INSERT OR REPLACE INTO router (id, ip, ssh_port, ssh_user, ssh_password, ssh_key) VALUES (1, '', '', '', '', '')")
	}
	if err != nil {
		return err
	}

	// jellyfin
	if cfg.Jellyfin != nil {
		_, err = tx.Exec("INSERT OR REPLACE INTO jellyfin (id, url, api_key) VALUES (1, ?, ?)",
			cfg.Jellyfin.URL, cfg.Jellyfin.APIKey)
	} else {
		_, err = tx.Exec("INSERT OR REPLACE INTO jellyfin (id, url, api_key) VALUES (1, '', '')")
	}
	if err != nil {
		return err
	}

	// devices: replace all
	_, err = tx.Exec("DELETE FROM devices")
	if err != nil {
		return err
	}
	for _, d := range cfg.Devices {
		all, wd, we, mon, tue, wed, thu, fri, sat, sun, allowStart, allowEnd := scheduleToNulls(&d.Schedule)
		_, err = tx.Exec(`INSERT INTO devices (name, ip, ssh_port, ssh_user, ssh_password, ssh_key, os, mac, block_method, detect_method,
			sched_all, sched_weekday, sched_weekend, sched_monday, sched_tuesday, sched_wednesday, sched_thursday,
			sched_friday, sched_saturday, sched_sunday, allowed_start, allowed_end)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.Name, d.IP, d.SSHPort, d.SSHUser, d.SSHPassword, d.SSHKey, d.OS, d.MAC, d.BlockMethod, d.DetectMethod,
			all, wd, we, mon, tue, wed, thu, fri, sat, sun, allowStart, allowEnd)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReloadConfigFromDB overwrites cfg with data from DB (for hot reload after UI save).
func ReloadConfigFromDB(db *sql.DB, cfg *Config) error {
	loaded, err := LoadConfigFromDB(db)
	if err != nil {
		return err
	}
	*cfg = *loaded
	return nil
}

func ValidateDevice(d *Device, hasRouter, hasJellyfin bool) error {
	d.OS = strings.ToLower(d.OS)
	if d.OS == "" {
		d.OS = "linux"
	}
	if d.OS != "linux" && d.OS != "windows" && d.OS != "macos" {
		return fmt.Errorf("device %q: unsupported OS %q (use linux, macos, or windows)", d.Name, d.OS)
	}
	if d.BlockMethod == "" {
		d.BlockMethod = "ssh_shutdown"
	}
	if d.BlockMethod != "ssh_shutdown" && d.BlockMethod != "router" {
		return fmt.Errorf("device %q: unsupported block_method %q", d.Name, d.BlockMethod)
	}
	if d.BlockMethod == "router" {
		if d.MAC == "" {
			return fmt.Errorf("device %q: mac is required when block_method is router", d.Name)
		}
		if !hasRouter {
			return fmt.Errorf("device %q: router section must be configured when block_method is router", d.Name)
		}
	}
	if d.DetectMethod == "" {
		d.DetectMethod = "ping"
	}
	if d.DetectMethod != "ping" && d.DetectMethod != "router_conntrack" && d.DetectMethod != "jellyfin" {
		return fmt.Errorf("device %q: unsupported detect_method %q", d.Name, d.DetectMethod)
	}
	if d.DetectMethod == "router_conntrack" && !hasRouter {
		return fmt.Errorf("device %q: router section must be configured when detect_method is router_conntrack", d.Name)
	}
	if d.DetectMethod == "jellyfin" && !hasJellyfin {
		return fmt.Errorf("device %q: jellyfin section must be configured when detect_method is jellyfin", d.Name)
	}
	return nil
}

// DB state helpers (used by State when db is set)
func dbIncrementUsage(db *sql.DB, ip string) error {
	today := time.Now().Format("2006-01-02")
	_, err := db.Exec("INSERT INTO usage (device_ip, date, minutes) VALUES (?, ?, 1) ON CONFLICT(device_ip, date) DO UPDATE SET minutes = minutes + 1", ip, today)
	return err
}

func dbGetUsageToday(db *sql.DB, ip string) int {
	today := time.Now().Format("2006-01-02")
	var min int
	err := db.QueryRow("SELECT COALESCE(minutes, 0) FROM usage WHERE device_ip = ? AND date = ?", ip, today).Scan(&min)
	if err != nil {
		return 0
	}
	return min
}

func dbSetOnline(db *sql.DB, ip string, online bool) error {
	v := 0
	if online {
		v = 1
	}
	_, err := db.Exec("INSERT OR REPLACE INTO online (device_ip, online) VALUES (?, ?)", ip, v)
	return err
}

func dbIsOnline(db *sql.DB, ip string) bool {
	var v int
	err := db.QueryRow("SELECT online FROM online WHERE device_ip = ?", ip).Scan(&v)
	return err == nil && v == 1
}

func dbSetRouterBlocked(db *sql.DB, ip string, blocked bool) error {
	v := 0
	if blocked {
		v = 1
	}
	_, err := db.Exec("INSERT OR REPLACE INTO router_blocked (device_ip, blocked) VALUES (?, ?)", ip, v)
	return err
}

func dbIsRouterBlocked(db *sql.DB, ip string) bool {
	var v int
	err := db.QueryRow("SELECT blocked FROM router_blocked WHERE device_ip = ?", ip).Scan(&v)
	return err == nil && v == 1
}

func dbDisableFor(db *sql.DB, ip string, until time.Time) error {
	_, err := db.Exec("INSERT OR REPLACE INTO disabled_until (device_ip, until_ts) VALUES (?, ?)", ip, until.Unix())
	return err
}

func dbIsDisabled(db *sql.DB, ip string) bool {
	var ts int64
	err := db.QueryRow("SELECT until_ts FROM disabled_until WHERE device_ip = ?", ip).Scan(&ts)
	if err != nil {
		return false
	}
	return time.Now().Unix() < ts
}

func dbGetDisabledUntil(db *sql.DB, ip string) time.Time {
	var ts int64
	err := db.QueryRow("SELECT until_ts FROM disabled_until WHERE device_ip = ?", ip).Scan(&ts)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

func dbPruneOldUsage(db *sql.DB, keepDays int) {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	db.Exec("DELETE FROM usage WHERE date < ?", cutoff)
}
