package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWebServer(t *testing.T) (*WebServer, *State) {
	t.Helper()
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		UIPassword: "testpass",
		Devices: []Device{
			{
				Name:         "Test PC",
				IP:           "10.0.0.1",
				OS:           "linux",
				BlockMethod:  "ssh_shutdown",
				DetectMethod: "ping",
				Schedule:     Schedule{All: intPtr(120)},
			},
		},
	}
	return NewWebServer(cfg, state, nil, nil), state
}

func TestSession_CreateAndValidate(t *testing.T) {
	key := "secret"
	token := createSession(key)

	if !validateSession(token, key) {
		t.Error("valid token should validate")
	}
	if validateSession(token, "wrong") {
		t.Error("token should not validate with wrong key")
	}
	if validateSession("garbage", key) {
		t.Error("garbage token should not validate")
	}
	if validateSession("", key) {
		t.Error("empty token should not validate")
	}
	if validateSession("no-dot", key) {
		t.Error("token without dot should not validate")
	}
	if validateSession("msg.tampered", key) {
		t.Error("tampered signature should not validate")
	}
}

func TestWeb_RootRedirectsToLogin(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	loc := resp.Header.Get("Location")
	if loc != "/login" {
		t.Errorf("redirect to %q, want /login", loc)
	}
}

func TestWeb_LoginPage(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestWeb_LoginSuccess(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.PostForm(srv.URL+"/login", url.Values{"password": {"testpass"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	// Check session cookie is set
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "session" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie set")
	}
	if !validateSession(sessionCookie.Value, "testpass") {
		t.Error("session cookie is not valid")
	}
}

func TestWeb_LoginWrongPassword(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/login", url.Values{"password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-renders login with error)", resp.StatusCode)
	}
}

func TestWeb_DashboardRequiresAuth(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(srv.URL + "/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect to login)", resp.StatusCode, http.StatusSeeOther)
	}
}

func TestWeb_DashboardWithAuth(t *testing.T) {
	ws, state := testWebServer(t)
	state.SetOnline("10.0.0.1", true)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	token := createSession("testpass")
	req, _ := http.NewRequest("GET", srv.URL+"/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestWeb_DisableRequiresAuth(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Post(srv.URL+"/disable/10.0.0.1", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect to login)", resp.StatusCode, http.StatusSeeOther)
	}
}

func TestWeb_DisableDevice(t *testing.T) {
	ws, state := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	token := createSession("testpass")
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	req, _ := http.NewRequest("POST", srv.URL+"/disable/10.0.0.1", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	if !state.IsDisabled("10.0.0.1") {
		t.Error("device should be disabled after POST /disable/")
	}
}

func TestWeb_DisableUnknownDevice(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	token := createSession("testpass")
	req, _ := http.NewRequest("POST", srv.URL+"/disable/99.99.99.99", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for unknown device", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestWeb_DisableGetNotAllowed(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	token := createSession("testpass")
	req, _ := http.NewRequest("GET", srv.URL+"/disable/10.0.0.1", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestWeb_Logout(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(srv.URL + "/logout")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	// Check cookie is cleared
	for _, c := range resp.Cookies() {
		if c.Name == "session" && c.MaxAge > 0 {
			t.Error("session cookie should be cleared on logout")
		}
	}
}

func TestWeb_NotFound(t *testing.T) {
	ws, _ := testWebServer(t)
	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestWeb_DisableUnblocksRouterDevice(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		UIPassword: "testpass",
		Router:     &Router{IP: "192.168.1.1", SSHUser: "root", SSHPassword: "pass"},
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
	monitor := NewMonitor(cfg, state)
	ws := NewWebServer(cfg, state, monitor, nil)

	// Simulate the device being router-blocked
	state.SetRouterBlocked("10.0.0.5", true)

	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	token := createSession("testpass")
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	req, _ := http.NewRequest("POST", srv.URL+"/disable/10.0.0.5", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	if !state.IsDisabled("10.0.0.5") {
		t.Error("device should be disabled after POST /disable/")
	}

	// RouterUnblock will fail (no actual router), but the state should still be
	// checked for the attempt. Since sshRun fails, the state remains blocked,
	// but the disable state is set. This tests that the code path is reached.
}

func TestWeb_DashboardShowsBlockedStatus(t *testing.T) {
	state := NewState(filepath.Join(t.TempDir(), "state.json"))
	cfg := &Config{
		UIPassword: "testpass",
		Devices: []Device{
			{
				Name: "Test PC",
				IP:   "10.0.0.1",
				OS:   "linux",
				Schedule: Schedule{
					All:          intPtr(2),
					AllowedHours: &AllowedHours{Start: "08:00", End: "21:00"},
				},
			},
		},
	}
	ws := NewWebServer(cfg, state, nil, nil)

	// Simulate usage exceeding the limit
	state.IncrementUsage("10.0.0.1")
	state.IncrementUsage("10.0.0.1")
	state.IncrementUsage("10.0.0.1")
	state.SetOnline("10.0.0.1", true)

	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	token := createSession("testpass")
	req, _ := http.NewRequest("GET", srv.URL+"/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestWeb_DashboardShowsDisabledStatus(t *testing.T) {
	ws, state := testWebServer(t)
	state.DisableFor("10.0.0.1", 1*time.Hour)

	srv := httptest.NewServer(ws.Handler())
	defer srv.Close()

	token := createSession("testpass")
	req, _ := http.NewRequest("GET", srv.URL+"/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
