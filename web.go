package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"database/sql"
)

type WebServer struct {
	config  *Config
	state   *State
	monitor *Monitor
	db      *sql.DB
}

func NewWebServer(config *Config, state *State, monitor *Monitor, db *sql.DB) *WebServer {
	return &WebServer{config: config, state: state, monitor: monitor, db: db}
}

func (ws *WebServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleRoot)
	mux.HandleFunc("/login", ws.handleLogin)
	mux.HandleFunc("/dashboard", ws.handleDashboard)
	mux.HandleFunc("/disable/", ws.handleDisable)
	mux.HandleFunc("/settings", ws.handleSettings)
	mux.HandleFunc("/settings/test-router", ws.handleTestRouter)
	mux.HandleFunc("/settings/test-device/", ws.handleTestDevice)
	mux.HandleFunc("/logout", ws.handleLogout)
	return mux
}

func (ws *WebServer) isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("session")
	if err != nil {
		return false
	}
	return validateSession(cookie.Value, ws.config.UIPassword)
}

func (ws *WebServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if ws.isAuthenticated(r) {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (ws *WebServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		renderTemplate(w, loginTmpl, nil)
		return
	}

	if r.Method == http.MethodPost {
		password := r.FormValue("password")
		if password == ws.config.UIPassword {
			token := createSession(ws.config.UIPassword)
			http.SetCookie(w, &http.Cookie{
				Name:     "session",
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				MaxAge:   86400,
			})
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		renderTemplate(w, loginTmpl, map[string]any{"Error": "Invalid password"})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

type DeviceStatus struct {
	Name          string
	IP            string
	OS            string
	Online        bool
	UsageToday    int
	Limit         int
	SharedPool    bool // when true, limit is from root schedule (shared)
	Disabled      bool
	DisabledUntil string
	Blocked       bool
	UsagePercent  int
	UsageClass    string
}

// RootScheduleStatus is set when config has a root schedule with a daily limit (shared pool).
type RootScheduleStatus struct {
	TotalUsage int
	Limit      int
}

func (ws *WebServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if !ws.isAuthenticated(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	now := time.Now()
	var totalUsage int
	for _, dev := range ws.config.Devices {
		totalUsage += ws.state.GetUsageToday(dev.IP)
	}

	var rootStatus *RootScheduleStatus
	var rootUsagePercent int
	var rootUsageClass string
	if ws.config.Schedule != nil {
		rootLimit := ws.config.Schedule.LimitForDay(now.Weekday())
		if rootLimit >= 0 {
			rootStatus = &RootScheduleStatus{TotalUsage: totalUsage, Limit: rootLimit}
			if rootLimit > 0 {
				rootUsagePercent = totalUsage * 100 / rootLimit
				if rootUsagePercent > 100 {
					rootUsagePercent = 100
				}
				if totalUsage >= rootLimit {
					rootUsageClass = "over"
				} else if totalUsage > rootLimit/2 {
					rootUsageClass = "warn"
				} else {
					rootUsageClass = "ok"
				}
			}
		}
	}

	var devices []DeviceStatus
	for _, dev := range ws.config.Devices {
		usage := ws.state.GetUsageToday(dev.IP)
		limit := dev.Schedule.LimitForDay(now.Weekday())
		if rootStatus != nil {
			limit = -1 // per-device limit not used when root schedule defines shared pool
		}
		disabled := ws.state.IsDisabled(dev.IP)
		online := ws.state.IsOnline(dev.IP)

		schedForHours := &dev.Schedule
		if ws.config.Schedule != nil && ws.config.Schedule.AllowedHours != nil {
			schedForHours = ws.config.Schedule
		}
		blocked := false
		if !disabled {
			if !schedForHours.IsAllowedHour(now) {
				blocked = true
			} else if rootStatus != nil {
				blocked = totalUsage >= rootStatus.Limit
			} else if limit >= 0 && usage >= limit {
				blocked = true
			}
		}

		var disabledUntilStr string
		if disabled {
			disabledUntilStr = ws.state.GetDisabledUntil(dev.IP).Format("15:04")
		}

		usagePercent := 0
		usageClass := "ok"
		if rootStatus != nil && rootStatus.Limit > 0 {
			usagePercent = rootUsagePercent
			usageClass = rootUsageClass
		} else if limit > 0 {
			usagePercent = usage * 100 / limit
			if usagePercent > 100 {
				usagePercent = 100
			}
			if usage >= limit {
				usageClass = "over"
			} else if usage > limit/2 {
				usageClass = "warn"
			}
		}

		devices = append(devices, DeviceStatus{
			Name:          dev.Name,
			IP:            dev.IP,
			OS:            dev.OS,
			Online:        online,
			UsageToday:    usage,
			Limit:         limit,
			SharedPool:    rootStatus != nil,
			Disabled:      disabled,
			DisabledUntil: disabledUntilStr,
			Blocked:       blocked,
			UsagePercent:  usagePercent,
			UsageClass:    usageClass,
		})
	}

	renderTemplate(w, dashboardTmpl, map[string]any{
		"Devices":          devices,
		"RootSchedule":     rootStatus,
		"RootUsagePercent": rootUsagePercent,
		"RootUsageClass":   rootUsageClass,
	})
}

func (ws *WebServer) handleDisable(w http.ResponseWriter, r *http.Request) {
	if !ws.isAuthenticated(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip := strings.TrimPrefix(r.URL.Path, "/disable/")
	if ip == "" {
		http.Error(w, "Missing device IP", http.StatusBadRequest)
		return
	}

	var foundDev *Device
	for i := range ws.config.Devices {
		if ws.config.Devices[i].IP == ip {
			foundDev = &ws.config.Devices[i]
			break
		}
	}
	if foundDev == nil {
		http.Error(w, "Unknown device", http.StatusBadRequest)
		return
	}

	ws.state.DisableFor(ip, 1*time.Hour)
	log.Printf("Blocking disabled for 1 hour for device %s", ip)

	// Immediately unblock router-blocked devices
	if foundDev.BlockMethod == "router" && ws.state.IsRouterBlocked(ip) && ws.monitor != nil {
		ws.monitor.RouterUnblock(foundDev)
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (ws *WebServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	if !ws.isAuthenticated(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if ws.db == nil {
		http.Error(w, "Settings only available when using database", http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			renderTemplate(w, settingsTmpl, map[string]any{"Error": err.Error(), "Config": ws.config})
			return
		}
		cfg := ws.parseSettingsForm(r)
		if cfg.UIPassword == "" {
			cfg.UIPassword = ws.config.UIPassword
		}
		if err := ws.saveSettings(cfg); err != nil {
			renderTemplate(w, settingsTmpl, map[string]any{"Error": err.Error(), "Config": cfg})
			return
		}
		if err := ReloadConfigFromDB(ws.db, ws.config); err != nil {
			log.Printf("Reload config after save: %v", err)
		}
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
		return
	}

	saved := r.URL.Query().Get("saved") == "1"
	data := map[string]any{"Config": ws.config, "Saved": saved}
	if saved {
		data["Message"] = "Settings saved."
	}
	renderTemplate(w, settingsTmpl, data)
}

func (ws *WebServer) parseSettingsForm(r *http.Request) *Config {
	cfg := &Config{}
	cfg.UIPassword = strings.TrimSpace(r.FormValue("ui_password"))
	cfg.DefaultRouterBlock = r.FormValue("default_router_block") == "on"
	if v := r.FormValue("traffic_threshold"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 1 {
			cfg.TrafficThreshold = n
		}
	}
	if cfg.TrafficThreshold < 1 {
		cfg.TrafficThreshold = 1
	}

	// Root schedule
	if v := r.FormValue("root_all"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Schedule = &Schedule{}
			cfg.Schedule.All = &n
		}
	}
	if cfg.Schedule == nil && (r.FormValue("root_weekday") != "" || r.FormValue("root_weekend") != "") {
		cfg.Schedule = &Schedule{}
	}
	if cfg.Schedule != nil {
		if v := r.FormValue("root_weekday"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				cfg.Schedule.Weekday = &n
			}
		}
		if v := r.FormValue("root_weekend"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				cfg.Schedule.Weekend = &n
			}
		}
		as, ae := strings.TrimSpace(r.FormValue("root_allowed_start")), strings.TrimSpace(r.FormValue("root_allowed_end"))
		if as != "" || ae != "" {
			cfg.Schedule.AllowedHours = &AllowedHours{Start: as, End: ae}
		}
		if cfg.Schedule.All == nil && cfg.Schedule.Weekday == nil && cfg.Schedule.Weekend == nil && cfg.Schedule.AllowedHours == nil {
			cfg.Schedule = nil
		}
	}

	// Router
	rip := strings.TrimSpace(r.FormValue("router_ip"))
	if rip != "" {
		cfg.Router = &Router{
			IP:          rip,
			SSHUser:     strings.TrimSpace(r.FormValue("router_ssh_user")),
			SSHPassword: strings.TrimSpace(r.FormValue("router_ssh_password")),
			SSHKey:      strings.TrimSpace(r.FormValue("router_ssh_key")),
		}
	}

	// Devices (parallel slices)
	names := r.Form["device_name"]
	ips := r.Form["device_ip"]
	for i := 0; i < len(names) && i < len(ips); i++ {
		name := strings.TrimSpace(names[i])
		ip := strings.TrimSpace(ips[i])
		if name == "" && ip == "" {
			continue
		}
		if ip == "" {
			continue
		}
		d := Device{
			Name:         name,
			IP:           ip,
			SSHUser:      strings.TrimSpace(getFormIndex(r.Form["device_ssh_user"], i)),
			SSHPassword:  strings.TrimSpace(getFormIndex(r.Form["device_ssh_password"], i)),
			SSHKey:       strings.TrimSpace(getFormIndex(r.Form["device_ssh_key"], i)),
			OS:           strings.ToLower(strings.TrimSpace(getFormIndex(r.Form["device_os"], i))),
			MAC:          strings.TrimSpace(getFormIndex(r.Form["device_mac"], i)),
			BlockMethod:  strings.TrimSpace(getFormIndex(r.Form["device_block_method"], i)),
			DetectMethod: strings.TrimSpace(getFormIndex(r.Form["device_detect_method"], i)),
		}
		if d.OS == "" {
			d.OS = "linux"
		}
		if d.BlockMethod == "" {
			d.BlockMethod = "ssh_shutdown"
		}
		if d.DetectMethod == "" {
			d.DetectMethod = "ping"
		}
		// Per-device schedule
		if v := getFormIndex(r.Form["device_sched_all"], i); v != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
				d.Schedule.All = &n
			}
		}
		as := strings.TrimSpace(getFormIndex(r.Form["device_allowed_start"], i))
		ae := strings.TrimSpace(getFormIndex(r.Form["device_allowed_end"], i))
		if as != "" || ae != "" {
			d.Schedule.AllowedHours = &AllowedHours{Start: as, End: ae}
		}
		cfg.Devices = append(cfg.Devices, d)
	}
	return cfg
}

func getFormIndex(sl []string, i int) string {
	if i < len(sl) {
		return sl[i]
	}
	return ""
}

func (ws *WebServer) saveSettings(cfg *Config) error {
	hasRouter := cfg.Router != nil && cfg.Router.IP != ""
	for i := range cfg.Devices {
		if err := ValidateDevice(&cfg.Devices[i], hasRouter); err != nil {
			return err
		}
	}
	return SaveConfigToDB(ws.db, cfg)
}

func (ws *WebServer) writeTestResult(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (ws *WebServer) handleTestRouter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ws.isAuthenticated(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if ws.monitor == nil {
		ws.writeTestResult(w, fmt.Errorf("monitor not available"))
		return
	}
	err := ws.monitor.TestRouterConnection()
	ws.writeTestResult(w, err)
}

func (ws *WebServer) handleTestDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ws.isAuthenticated(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ip := strings.TrimPrefix(r.URL.Path, "/settings/test-device/")
	if ip == "" {
		ws.writeTestResult(w, fmt.Errorf("missing device IP"))
		return
	}
	if ws.monitor == nil {
		ws.writeTestResult(w, fmt.Errorf("monitor not available"))
		return
	}
	err := ws.monitor.TestDeviceConnection(ip)
	ws.writeTestResult(w, err)
}

func (ws *WebServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// Session management using HMAC

func createSession(key string) string {
	msg := "authenticated"
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return msg + "." + sig
}

func validateSession(token, key string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	msg, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(sig), []byte(expected))
}

func renderTemplate(w http.ResponseWriter, tmplStr string, data any) {
	tmpl, err := template.New("page").Funcs(template.FuncMap{
		"ge": func(a, b int) bool { return a >= b },
		"opt": func(p *int) int {
			if p != nil {
				return *p
			}
			return -1
		},
		"optS": func(s *Schedule) *int {
			if s != nil && s.All != nil {
				return s.All
			}
			return nil
		},
	}).Parse(tmplStr)
	if err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("Template parse error: %v", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Template execute error: %v", err)
	}
}

const loginTmpl = `<!DOCTYPE html>
<html>
<head>
    <title>ParentalParrot - Login</title>
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f0f2f5; display: flex; justify-content: center; align-items: center; min-height: 100vh; }
        .login-box { background: white; padding: 2rem; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); width: 100%; max-width: 400px; }
        h1 { text-align: center; margin-bottom: 1.5rem; color: #333; }
        input[type=password] { width: 100%; padding: 0.75rem; border: 1px solid #ddd; border-radius: 4px; font-size: 1rem; margin-bottom: 1rem; }
        button { width: 100%; padding: 0.75rem; background: #4CAF50; color: white; border: none; border-radius: 4px; font-size: 1rem; cursor: pointer; }
        button:hover { background: #45a049; }
        .error { color: #d32f2f; text-align: center; margin-bottom: 1rem; }
    </style>
</head>
<body>
    <div class="login-box">
        <h1>ParentalParrot</h1>
        {{if .}}{{if .Error}}<div class="error">{{.Error}}</div>{{end}}{{end}}
        <form method="POST" action="/login">
            <input type="password" name="password" placeholder="Password" autofocus required>
            <button type="submit">Log In</button>
        </form>
    </div>
</body>
</html>`

const dashboardTmpl = `<!DOCTYPE html>
<html>
<head>
    <title>ParentalParrot - Dashboard</title>
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta http-equiv="refresh" content="30">
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f0f2f5; padding: 1rem; }
        .header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 1.5rem; }
        h1 { color: #333; }
        .logout { color: #666; text-decoration: none; }
        .logout:hover { color: #333; }
        .devices { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 1rem; }
        .device { background: white; border-radius: 8px; padding: 1.5rem; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        .device-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 1rem; }
        .device-name { font-size: 1.2rem; font-weight: 600; color: #333; }
        .status { padding: 0.25rem 0.75rem; border-radius: 12px; font-size: 0.85rem; font-weight: 500; }
        .status.online { background: #e8f5e9; color: #2e7d32; }
        .status.offline { background: #fafafa; color: #999; }
        .info { margin-bottom: 0.5rem; color: #666; font-size: 0.9rem; }
        .info strong { color: #333; }
        .usage-bar { background: #e0e0e0; border-radius: 4px; height: 8px; margin: 0.5rem 0; overflow: hidden; }
        .usage-fill { height: 100%; border-radius: 4px; transition: width 0.3s; }
        .usage-fill.ok { background: #4CAF50; }
        .usage-fill.warn { background: #FF9800; }
        .usage-fill.over { background: #f44336; }
        .badge { display: inline-block; padding: 0.2rem 0.5rem; border-radius: 4px; font-size: 0.8rem; font-weight: 500; margin-right: 0.5rem; }
        .badge.blocked { background: #ffebee; color: #c62828; }
        .badge.disabled { background: #e3f2fd; color: #1565c0; }
        .btn { display: inline-block; padding: 0.5rem 1rem; background: #2196F3; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 0.9rem; text-decoration: none; }
        .btn:hover { background: #1976D2; }
        .btn:disabled { background: #bbb; cursor: not-allowed; }
    </style>
</head>
<body>
    <div class="header">
        <h1>ParentalParrot</h1>
        <span><a href="/dashboard" class="logout">Dashboard</a> | <a href="/settings" class="logout">Settings</a> | <a href="/logout" class="logout">Logout</a></span>
    </div>
    {{if .RootSchedule}}
    <div class="shared-pool" style="background:#e3f2fd; padding:0.75rem 1rem; border-radius:8px; margin-bottom:1rem;">
        <div class="info">Shared pool (all devices): <strong>{{.RootSchedule.TotalUsage}} min</strong> / {{.RootSchedule.Limit}} min per day</div>
        <div class="usage-bar" style="margin-top:0.5rem;">
            <div class="usage-fill {{.RootUsageClass}}" style="width: {{.RootUsagePercent}}%"></div>
        </div>
    </div>
    {{end}}
    <div class="devices">
        {{range .Devices}}
        <div class="device">
            <div class="device-header">
                <span class="device-name">{{.Name}}</span>
                <span class="status {{if .Online}}online{{else}}offline{{end}}">
                    {{if .Online}}Online{{else}}Offline{{end}}
                </span>
            </div>
            <div class="info">IP: <strong>{{.IP}}</strong> | OS: <strong>{{.OS}}</strong></div>
            <div class="info">
                Usage today: <strong>{{.UsageToday}} min</strong>
                {{if .SharedPool}}(shared pool){{else if ge .Limit 0}} / {{.Limit}} min{{else}} (unlimited){{end}}
            </div>
            {{if ge .Limit 0}}
            <div class="usage-bar">
                <div class="usage-fill {{.UsageClass}}" style="width: {{.UsagePercent}}%"></div>
            </div>
            {{end}}
            <div style="margin: 0.75rem 0;">
                {{if .Blocked}}<span class="badge blocked">Blocked</span>{{end}}
                {{if .Disabled}}<span class="badge disabled">Disabled until {{.DisabledUntil}}</span>{{end}}
            </div>
            <form method="POST" action="/disable/{{.IP}}" style="display:inline">
                <button type="submit" class="btn" {{if .Disabled}}disabled{{end}}>
                    {{if .Disabled}}Already Disabled{{else}}Disable for 1 Hour{{end}}
                </button>
            </form>
        </div>
        {{end}}
    </div>
</body>
</html>`

const settingsTmpl = `<!DOCTYPE html>
<html>
<head>
    <title>ParentalParrot - Settings</title>
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f0f2f5; padding: 1rem; max-width: 900px; margin: 0 auto; }
        .header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 1.5rem; }
        h1 { color: #333; }
        a { color: #2196F3; text-decoration: none; }
        a:hover { text-decoration: underline; }
        .nav { display: flex; gap: 1rem; }
        section { background: white; border-radius: 8px; padding: 1.25rem; margin-bottom: 1rem; box-shadow: 0 2px 10px rgba(0,0,0,0.08); }
        section h2 { font-size: 1rem; margin-bottom: 0.75rem; color: #555; border-bottom: 1px solid #eee; padding-bottom: 0.25rem; }
        .form-row { margin-bottom: 0.75rem; }
        .form-row label { display: inline-block; min-width: 120px; font-size: 0.9rem; color: #333; }
        .form-row input { padding: 0.4rem 0.5rem; border: 1px solid #ddd; border-radius: 4px; font-size: 0.9rem; }
        .form-row input[type=number] { width: 80px; }
        .form-row input.short { width: 80px; }
        .form-row input.medium { width: 180px; }
        .form-row input.long { width: 280px; }
        .error { color: #c62828; margin-bottom: 0.75rem; }
        .message { color: #2e7d32; margin-bottom: 0.75rem; }
        table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
        th, td { padding: 0.35rem 0.5rem; text-align: left; border-bottom: 1px solid #eee; }
        th { color: #666; font-weight: 600; }
        td input { width: 100%; padding: 0.3rem; border: 1px solid #ddd; border-radius: 3px; }
        td input.num { width: 50px; }
        .btn { padding: 0.5rem 1rem; background: #2196F3; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 0.9rem; }
        .btn:hover { background: #1976D2; }
        .btn-secondary { background: #757575; }
        .btn-secondary:hover { background: #616161; }
        .btn-danger { background: #c62828; }
        .btn-danger:hover { background: #b71c1c; }
        #devices-tbody tr.new-row td input { background: #f5f5f5; }
        .devices-table .device-row-2 td { padding-top: 0; border-top: none; }
        .devices-table tr.device-row-1 + tr.device-row-2 { border-bottom: 1px solid #e0e0e0; }
        .devices-table tr.device-row-1 td input.num { width: 55px; }
        .devices-table tr.block-router .ssh-creds-cell { display: none; }
        .devices-table tr.block-ssh .mac-cell { display: none; }
        .devices-table .field-label { display: block; font-size: 0.75rem; color: #666; margin-bottom: 0.2rem; }
    </style>
</head>
<body>
    <div class="header">
        <h1>ParentalParrot – Settings</h1>
        <span class="nav"><a href="/dashboard">Dashboard</a> | <a href="/settings">Settings</a> | <a href="/logout">Logout</a></span>
    </div>
    {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
    {{if .Message}}<div class="message">{{.Message}}</div>{{end}}

    <form method="POST" action="/settings">
        <section>
            <h2>General</h2>
            <div class="form-row">
                <label>UI password</label>
                <input type="password" name="ui_password" class="medium" placeholder="Leave blank to keep current; set new to change">
            </div>
        </section>

        <section>
            <h2>Root schedule (shared pool for all devices)</h2>
            <div class="form-row">
                <label>All (min/day)</label>
                <input type="number" name="root_all" class="short" min="0" placeholder="e.g. 120" value="{{if .Config.Schedule}}{{if .Config.Schedule.All}}{{.Config.Schedule.All}}{{end}}{{end}}">
                <span style="margin-left:0.5rem;color:#666">Leave empty for per-device limits only</span>
            </div>
            <div class="form-row">
                <label>Weekday (min)</label>
                <input type="number" name="root_weekday" class="short" min="0" value="{{if .Config.Schedule}}{{if .Config.Schedule.Weekday}}{{.Config.Schedule.Weekday}}{{end}}{{end}}">
                <label style="margin-left:1rem">Weekend (min)</label>
                <input type="number" name="root_weekend" class="short" min="0" value="{{if .Config.Schedule}}{{if .Config.Schedule.Weekend}}{{.Config.Schedule.Weekend}}{{end}}{{end}}">
            </div>
            <div class="form-row">
                <label>Allowed hours</label>
                <input type="text" name="root_allowed_start" class="short" placeholder="08:00" value="{{if .Config.Schedule}}{{if .Config.Schedule.AllowedHours}}{{.Config.Schedule.AllowedHours.Start}}{{end}}{{end}}">
                –
                <input type="text" name="root_allowed_end" class="short" placeholder="21:00" value="{{if .Config.Schedule}}{{if .Config.Schedule.AllowedHours}}{{.Config.Schedule.AllowedHours.End}}{{end}}{{end}}">
            </div>
        </section>

        <section>
            <h2>Router (for block_method=router / detect_method=router_conntrack)</h2>
            <div class="form-row">
                <label class="field-label">Default for new devices</label>
                <label style="display:flex;align-items:center;gap:0.5rem;font-weight:normal;">
                    <input type="checkbox" name="default_router_block" value="on" {{if .Config.DefaultRouterBlock}}checked{{end}}>
                    Enable router block (+ conntrack) as default for new devices
                </label>
            </div>
            <div class="form-row">
                <label>Traffic threshold (min conntrack entries)</label>
                <input type="number" name="traffic_threshold" class="short" min="1" value="{{if .Config.TrafficThreshold}}{{.Config.TrafficThreshold}}{{else}}1{{end}}" title="Device is considered active when it has at least this many conntrack entries">
            </div>
            <div class="form-row">
                <label>IP</label>
                <input type="text" name="router_ip" class="medium" placeholder="192.168.1.1" value="{{if .Config.Router}}{{.Config.Router.IP}}{{end}}">
            </div>
            <div class="form-row">
                <label>SSH user</label>
                <input type="text" name="router_ssh_user" class="medium" value="{{if .Config.Router}}{{.Config.Router.SSHUser}}{{end}}">
            </div>
            <div class="form-row">
                <label>SSH password</label>
                <input type="password" name="router_ssh_password" class="medium" placeholder="(optional if key set)" value="{{if .Config.Router}}{{.Config.Router.SSHPassword}}{{end}}">
            </div>
            <div class="form-row">
                <label>SSH key path</label>
                <input type="text" name="router_ssh_key" class="long" placeholder="/home/you/.ssh/id_ed25519" value="{{if .Config.Router}}{{.Config.Router.SSHKey}}{{end}}">
            </div>
            <p style="margin-top:0.5rem;font-size:0.85rem;color:#666">Save settings first, then test connection to the router (uses saved credentials).</p>
            <button type="button" class="btn btn-secondary" id="test-router-btn" style="margin-top:0.5rem">Test router connection</button>
            <span id="test-router-result" style="margin-left:0.5rem;font-size:0.9rem"></span>
        </section>

        <section>
            <h2>Devices</h2>
            <table class="devices-table">
                <tbody id="devices-tbody">
                    {{range .Config.Devices}}
                    <tr class="device-row-1 {{if eq .BlockMethod "router"}}block-router{{else}}block-ssh{{end}}" data-device-ip="{{.IP}}">
                        <td><label class="field-label">Name</label><input type="text" name="device_name" value="{{.Name}}" placeholder="Name"></td>
                        <td><label class="field-label">Block</label><select name="device_block_method"><option value="ssh_shutdown" {{if eq .BlockMethod "ssh_shutdown"}}selected{{end}}>ssh</option><option value="router" {{if eq .BlockMethod "router"}}selected{{end}}>router</option></select></td>
                        <td><label class="field-label">Detect</label><select name="device_detect_method"><option value="ping" {{if eq .DetectMethod "ping"}}selected{{end}}>ping</option><option value="router_conntrack" {{if eq .DetectMethod "router_conntrack"}}selected{{end}}>conntrack</option></select></td>
                        <td><label class="field-label">Limit (min)</label><input type="number" name="device_sched_all" class="num" min="0" placeholder="" value="{{if .Schedule.All}}{{.Schedule.All}}{{end}}"></td>
                        <td><label class="field-label">Hours</label><input type="text" name="device_allowed_start" placeholder="08:00" value="{{if .Schedule.AllowedHours}}{{.Schedule.AllowedHours.Start}}{{end}}" style="width:55px"> – <input type="text" name="device_allowed_end" placeholder="21:00" value="{{if .Schedule.AllowedHours}}{{.Schedule.AllowedHours.End}}{{end}}" style="width:55px"></td>
                        <td><label class="field-label">&nbsp;</label><button type="button" class="btn btn-danger" onclick="removeDevice(this)">Remove</button></td>
                        <td></td>
                    </tr>
                    <tr class="device-row-2 {{if eq .BlockMethod "router"}}block-router{{else}}block-ssh{{end}}" data-device-ip="{{.IP}}">
                        <td><label class="field-label">IP</label><input type="text" name="device_ip" value="{{.IP}}" placeholder="192.168.1.100"></td>
                        <td class="mac-cell"><label class="field-label">MAC</label><input type="text" name="device_mac" value="{{.MAC}}" placeholder="AA:BB:CC:DD:EE:FF"></td>
                        <td class="ssh-creds-cell"><label class="field-label">SSH user</label><input type="text" name="device_ssh_user" value="{{.SSHUser}}" placeholder="admin"></td>
                        <td class="ssh-creds-cell"><label class="field-label">SSH pass</label><input type="password" name="device_ssh_password" value="{{.SSHPassword}}" placeholder=""></td>
                        <td class="ssh-creds-cell"><label class="field-label">SSH key</label><input type="text" name="device_ssh_key" value="{{.SSHKey}}" placeholder=""></td>
                        <td class="ssh-creds-cell"><label class="field-label">Test SSH</label><button type="button" class="btn btn-secondary test-device-btn" data-ip="{{.IP}}">Test</button><span class="device-test-result" data-ip="{{.IP}}" style="margin-left:0.25rem;font-size:0.85rem"></span></td>
                        <td><label class="field-label">OS</label><input type="text" name="device_os" value="{{.OS}}" placeholder="linux" style="width:70px"></td>
                    </tr>
                    {{end}}
                    <tr class="new-row device-row-1 {{if .Config.DefaultRouterBlock}}block-router{{else}}block-ssh{{end}}">
                        <td><label class="field-label">Name</label><input type="text" name="device_name" placeholder="Name"></td>
                        <td><label class="field-label">Block</label><select name="device_block_method"><option value="ssh_shutdown" {{if not .Config.DefaultRouterBlock}}selected{{end}}>ssh</option><option value="router" {{if .Config.DefaultRouterBlock}}selected{{end}}>router</option></select></td>
                        <td><label class="field-label">Detect</label><select name="device_detect_method"><option value="ping" {{if not .Config.DefaultRouterBlock}}selected{{end}}>ping</option><option value="router_conntrack" {{if .Config.DefaultRouterBlock}}selected{{end}}>conntrack</option></select></td>
                        <td><label class="field-label">Limit (min)</label><input type="number" name="device_sched_all" class="num" min="0" placeholder=""></td>
                        <td><label class="field-label">Hours</label><input type="text" name="device_allowed_start" placeholder="08:00" style="width:55px"> – <input type="text" name="device_allowed_end" placeholder="21:00" style="width:55px"></td>
                        <td></td>
                        <td></td>
                    </tr>
                    <tr class="new-row device-row-2 {{if .Config.DefaultRouterBlock}}block-router{{else}}block-ssh{{end}}">
                        <td><label class="field-label">IP</label><input type="text" name="device_ip" placeholder="192.168.1.100"></td>
                        <td class="mac-cell"><label class="field-label">MAC</label><input type="text" name="device_mac" placeholder="AA:BB:CC:DD:EE:FF"></td>
                        <td class="ssh-creds-cell"><label class="field-label">SSH user</label><input type="text" name="device_ssh_user" placeholder="admin"></td>
                        <td class="ssh-creds-cell"><label class="field-label">SSH pass</label><input type="password" name="device_ssh_password" placeholder=""></td>
                        <td class="ssh-creds-cell"><label class="field-label">SSH key</label><input type="text" name="device_ssh_key" placeholder=""></td>
                        <td class="ssh-creds-cell"><label class="field-label">Test SSH</label><button type="button" class="btn btn-secondary test-device-btn" data-ip="">Test</button><span class="device-test-result" data-ip="" style="margin-left:0.25rem;font-size:0.85rem"></span></td>
                        <td><label class="field-label">OS</label><input type="text" name="device_os" placeholder="linux" style="width:70px"></td>
                    </tr>
                </tbody>
            </table>
            <p style="margin-top:0.5rem;font-size:0.85rem;color:#666">Empty IP = skip row. Remove = clear IP and save. Add = fill the last row and save. Test SSH uses saved credentials (save first).</p>
            <button type="button" class="btn btn-secondary" onclick="addRow()" style="margin-top:0.5rem">Add device row</button>
        </section>

        <div style="margin-top:1rem">
            <button type="submit" class="btn">Save all settings</button>
        </div>
    </form>

    <script>
    document.getElementById('devices-tbody').addEventListener('change', function(ev) {
        if (ev.target.name !== 'device_block_method') return;
        var row1 = ev.target.closest('tr');
        var row2 = row1 && row1.nextElementSibling;
        if (!row2 || !row2.classList.contains('device-row-2')) return;
        var isRouter = ev.target.value === 'router';
        row1.classList.remove('block-router', 'block-ssh');
        row1.classList.add(isRouter ? 'block-router' : 'block-ssh');
        row2.classList.remove('block-router', 'block-ssh');
        row2.classList.add(isRouter ? 'block-router' : 'block-ssh');
    });
    function addRow() {
        var tbody = document.getElementById('devices-tbody');
        var newRows = tbody.querySelectorAll('tr.new-row');
        var row1 = newRows[0], row2 = newRows[1];
        var c1 = row1.cloneNode(true), c2 = row2.cloneNode(true);
        c1.classList.remove('new-row'); c1.classList.add('device-row-1');
        c2.classList.remove('new-row'); c2.classList.add('device-row-2');
        c1.querySelectorAll('input').forEach(function(inp) { inp.value = ''; });
        c2.querySelectorAll('input').forEach(function(inp) { inp.value = ''; });
        c1.querySelectorAll('select').forEach(function(s) { s.selectedIndex = 0; });
        c2.querySelectorAll('input').forEach(function(inp) { inp.value = ''; });
        tbody.insertBefore(c2, row1);
        tbody.insertBefore(c1, c2);
    }
    function removeDevice(btn) {
        var row1 = btn.closest('tr');
        var next = row1.nextElementSibling;
        if (next && next.classList.contains('device-row-2')) {
            next.remove();
        }
        row1.remove();
    }
    document.getElementById('test-router-btn').addEventListener('click', function() {
        var btn = this;
        var result = document.getElementById('test-router-result');
        btn.disabled = true;
        result.textContent = 'Testing…';
        result.style.color = '';
        fetch('/settings/test-router', { method: 'POST', credentials: 'same-origin' })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.ok) {
                    result.textContent = 'OK';
                    result.style.color = '#2e7d32';
                } else {
                    result.textContent = data.error || 'Failed';
                    result.style.color = '#c62828';
                }
            })
            .catch(function(e) {
                result.textContent = 'Error: ' + e.message;
                result.style.color = '#c62828';
            })
            .finally(function() { btn.disabled = false; });
    });
    document.addEventListener('click', function(ev) {
        if (ev.target.classList.contains('test-device-btn')) {
            var ip = ev.target.getAttribute('data-ip');
            if (!ip) return;
            var btn = ev.target;
            var result = document.querySelector('.device-test-result[data-ip="' + ip + '"]');
            if (!result) return;
            btn.disabled = true;
            result.textContent = '…';
            result.style.color = '';
            fetch('/settings/test-device/' + encodeURIComponent(ip), { method: 'POST', credentials: 'same-origin' })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.ok) {
                        result.textContent = 'OK';
                        result.style.color = '#2e7d32';
                    } else {
                        result.textContent = data.error || 'Failed';
                        result.style.color = '#c62828';
                    }
                })
                .catch(function(e) {
                    result.textContent = 'Error: ' + e.message;
                    result.style.color = '#c62828';
                })
                .finally(function() { btn.disabled = false; });
        }
    });
    </script>
</body>
</html>`
