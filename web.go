package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

type WebServer struct {
	config *Config
	state  *State
}

func NewWebServer(config *Config, state *State) *WebServer {
	return &WebServer{config: config, state: state}
}

func (ws *WebServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleRoot)
	mux.HandleFunc("/login", ws.handleLogin)
	mux.HandleFunc("/dashboard", ws.handleDashboard)
	mux.HandleFunc("/disable/", ws.handleDisable)
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
	Disabled      bool
	DisabledUntil string
	Blocked       bool
	UsagePercent  int
	UsageClass    string
}

func (ws *WebServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if !ws.isAuthenticated(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	now := time.Now()
	var devices []DeviceStatus
	for _, dev := range ws.config.Devices {
		usage := ws.state.GetUsageToday(dev.IP)
		limit := dev.Schedule.LimitForDay(now.Weekday())
		disabled := ws.state.IsDisabled(dev.IP)
		online := ws.state.IsOnline(dev.IP)

		blocked := false
		if !disabled {
			if !dev.Schedule.IsAllowedHour(now) {
				blocked = true
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
		if limit > 0 {
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
			Disabled:      disabled,
			DisabledUntil: disabledUntilStr,
			Blocked:       blocked,
			UsagePercent:  usagePercent,
			UsageClass:    usageClass,
		})
	}

	renderTemplate(w, dashboardTmpl, map[string]any{
		"Devices": devices,
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

	found := false
	for _, dev := range ws.config.Devices {
		if dev.IP == ip {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "Unknown device", http.StatusBadRequest)
		return
	}

	ws.state.DisableFor(ip, 1*time.Hour)
	log.Printf("Blocking disabled for 1 hour for device %s", ip)

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
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
        <a href="/logout" class="logout">Logout</a>
    </div>
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
                {{if ge .Limit 0}} / {{.Limit}} min{{else}} (unlimited){{end}}
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
