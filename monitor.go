package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHTarget holds connection details for an SSH host.
type SSHTarget struct {
	IP       string
	Port     string // optional; defaults to "22"
	User     string
	Password string
	KeyPath  string
}

type Monitor struct {
	config  *Config
	state   *State
	Verbose bool
}

func NewMonitor(config *Config, state *State) *Monitor {
	return &Monitor{config: config, state: state}
}

// Run starts the monitoring loop. It blocks; call in a goroutine.
func (m *Monitor) Run(stop <-chan struct{}) {
	// Run immediately on start, then every 60 seconds
	m.tick()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.tick()
		case <-stop:
			return
		}
	}
}

func (m *Monitor) tick() {
	for i := range m.config.Devices {
		dev := &m.config.Devices[i]
		m.checkDevice(dev)
	}
}

// totalUsageToday returns the sum of today's usage across all configured devices (for root schedule).
func (m *Monitor) totalUsageToday() int {
	var total int
	for i := range m.config.Devices {
		total += m.state.GetUsageToday(m.config.Devices[i].IP)
	}
	return total
}

func (m *Monitor) checkDevice(dev *Device) {
	var online bool
	switch dev.DetectMethod {
	case "router_conntrack":
		// Only count usage when there's actual traffic (conntrack), not just "on network" (ARP).
		// E.g. TV in sleep = no conntrack = no usage; TV streaming = conntrack entries = usage.
		online = m.routerDetectActiveTraffic(dev)
	case "jellyfin":
		online = m.jellyfinDetectPlaying(dev)
	default:
		online = ping(dev.IP)
	}
	wasOnline := m.state.IsOnline(dev.IP)
	m.state.SetOnline(dev.IP, online)

	if m.Verbose {
		usage := m.state.GetUsageToday(dev.IP)
		if online != wasOnline {
			log.Printf("[tick] %s (%s): %s → %s [detect=%s, usage=%dm]", dev.Name, dev.IP, onOff(wasOnline), onOff(online), dev.DetectMethod, usage)
		} else if online {
			log.Printf("[tick] %s (%s): online [detect=%s, usage=%dm]", dev.Name, dev.IP, dev.DetectMethod, usage)
		}
	}

	// Unblock logic for router-blocked devices
	if dev.BlockMethod == "router" && m.state.IsRouterBlocked(dev.IP) {
		if m.shouldUnblock(dev) {
			log.Printf("Device %s (%s): unblocking on router", dev.Name, dev.IP)
			m.RouterUnblock(dev)
		} else if m.Verbose {
			log.Printf("[tick] %s (%s): still router-blocked", dev.Name, dev.IP)
		}
	}

	if !online {
		return
	}

	// Device is online — increment usage
	m.state.IncrementUsage(dev.IP)

	// Check if blocking is disabled
	if m.state.IsDisabled(dev.IP) {
		if m.Verbose {
			log.Printf("[tick] %s (%s): blocking disabled, skipping limit check", dev.Name, dev.IP)
		}
		return
	}

	now := time.Now()

	// Allowed hours: root schedule overrides if it has allowed_hours, else per-device
	schedForHours := &dev.Schedule
	if m.config.Schedule != nil && m.config.Schedule.AllowedHours != nil {
		schedForHours = m.config.Schedule
	}
	if !schedForHours.IsAllowedHour(now) {
		log.Printf("Device %s (%s): outside allowed hours, blocking", dev.Name, dev.IP)
		m.block(dev)
		return
	}

	// Daily limit: root schedule = shared pool across all devices; else per-device
	if m.config.Schedule != nil {
		rootLimit := m.config.Schedule.LimitForDay(now.Weekday())
		if rootLimit >= 0 {
			total := m.totalUsageToday()
			if total >= rootLimit {
				log.Printf("Device %s (%s): shared daily limit reached (%d/%d min), blocking", dev.Name, dev.IP, total, rootLimit)
				m.block(dev)
				return
			}
			return
		}
	}
	limit := dev.Schedule.LimitForDay(now.Weekday())
	if limit >= 0 {
		usage := m.state.GetUsageToday(dev.IP)
		if usage >= limit {
			log.Printf("Device %s (%s): daily limit reached (%d/%d min), blocking", dev.Name, dev.IP, usage, limit)
			m.block(dev)
			return
		}
	}
}

// shouldUnblock returns true if a router-blocked device should be unblocked.
func (m *Monitor) shouldUnblock(dev *Device) bool {
	if m.state.IsDisabled(dev.IP) {
		return true
	}
	now := time.Now()
	schedForHours := &dev.Schedule
	if m.config.Schedule != nil && m.config.Schedule.AllowedHours != nil {
		schedForHours = m.config.Schedule
	}
	if !schedForHours.IsAllowedHour(now) {
		return false
	}
	if m.config.Schedule != nil {
		rootLimit := m.config.Schedule.LimitForDay(now.Weekday())
		if rootLimit >= 0 {
			return m.totalUsageToday() < rootLimit
		}
	}
	limit := dev.Schedule.LimitForDay(now.Weekday())
	if limit < 0 {
		return true // unlimited
	}
	return m.state.GetUsageToday(dev.IP) < limit
}

func (m *Monitor) block(dev *Device) {
	switch dev.BlockMethod {
	case "router":
		m.routerBlock(dev)
	default:
		m.sshShutdown(dev)
	}
}

func (m *Monitor) sshShutdown(dev *Device) {
	var cmd string
	switch dev.OS {
	case "windows":
		cmd = "shutdown /s /t 0"
	default:
		cmd = "sudo shutdown -h now"
	}

	target := SSHTarget{
		IP:       dev.IP,
		Port:     dev.SSHPort,
		User:     dev.SSHUser,
		Password: dev.SSHPassword,
		KeyPath:  dev.SSHKey,
	}
	err := sshRun(target, cmd)
	if err != nil {
		log.Printf("Failed to halt %s (%s): %v", dev.Name, dev.IP, err)
	}
}

func (m *Monitor) routerTarget() SSHTarget {
	r := m.config.Router
	return SSHTarget{
		IP:       r.IP,
		Port:     r.SSHPort,
		User:     r.SSHUser,
		Password: r.SSHPassword,
		KeyPath:  r.SSHKey,
	}
}

// CheckResult holds the result of a single router connectivity/config check.
type CheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fixed  bool   `json:"fixed,omitempty"`
}

// TestRouterConnection checks SSH connectivity and network config on the router.
// When m.Verbose is true (-v flag), each check is logged to the server log.
// It checks: SSH connection, br_netfilter module, bridge-nf-call-iptables, conntrack table.
// Missing br_netfilter / bridge-nf-call-iptables are fixed automatically.
func (m *Monitor) TestRouterConnection() ([]CheckResult, error) {
	if m.config.Router == nil || m.config.Router.IP == "" {
		return nil, fmt.Errorf("router not configured")
	}

	var checks []CheckResult
	target := m.routerTarget()
	anyFailed := false

	logCheck := func(c CheckResult) {
		if m.Verbose {
			status := "OK"
			if !c.OK {
				status = "FAIL"
			}
			if c.Fixed {
				status = "FIXED"
			}
			log.Printf("[router-test] %s: %s — %s", status, c.Name, c.Detail)
		}
	}

	// 1. SSH connection
	_, err := sshRunOutput(target, "echo ok")
	if err != nil {
		c := CheckResult{Name: "SSH connection", OK: false, Detail: err.Error()}
		logCheck(c)
		checks = append(checks, c)
		return checks, fmt.Errorf("SSH connection failed: %v", err)
	}
	c := CheckResult{Name: "SSH connection", OK: true, Detail: "connected to " + m.config.Router.IP}
	logCheck(c)
	checks = append(checks, c)

	// 2. br_netfilter module (needed for LAN-to-LAN conntrack)
	out, _ := sshRunOutput(target, "lsmod 2>/dev/null | grep -q br_netfilter && echo loaded || echo missing")
	if strings.TrimSpace(out) != "loaded" {
		_, err := sshRunOutput(target, "modprobe br_netfilter 2>&1")
		if err != nil {
			// modprobe failed — try installing kmod-br-netfilter (OpenWRT)
			c := CheckResult{Name: "br_netfilter module", OK: false, Detail: "modprobe failed, installing kmod-br-netfilter..."}
			logCheck(c)
			checks = append(checks, c)
			_, err = sshRunOutput(target, "opkg update >/dev/null 2>&1 && opkg install kmod-br-netfilter 2>&1")
			if err != nil {
				c = CheckResult{Name: "kmod-br-netfilter install", OK: false, Detail: "opkg install failed: " + err.Error()}
				logCheck(c)
				checks = append(checks, c)
				anyFailed = true
			} else {
				c = CheckResult{Name: "kmod-br-netfilter install", OK: true, Detail: "package installed", Fixed: true}
				logCheck(c)
				checks = append(checks, c)
				// Now try modprobe again
				_, err = sshRunOutput(target, "modprobe br_netfilter 2>&1")
				if err != nil {
					c = CheckResult{Name: "br_netfilter module", OK: false, Detail: "modprobe still failed after install: " + err.Error()}
					logCheck(c)
					checks = append(checks, c)
					anyFailed = true
				} else {
					c = CheckResult{Name: "br_netfilter module", OK: true, Detail: "loaded after package install", Fixed: true}
					logCheck(c)
					checks = append(checks, c)
				}
			}
		} else {
			c := CheckResult{Name: "br_netfilter module", OK: true, Detail: "loaded (was missing, fixed)", Fixed: true}
			logCheck(c)
			checks = append(checks, c)
		}
	} else {
		c := CheckResult{Name: "br_netfilter module", OK: true, Detail: "already loaded"}
		logCheck(c)
		checks = append(checks, c)
	}

	// 3. bridge-nf-call-iptables (makes bridged frames go through iptables/conntrack)
	out, _ = sshRunOutput(target, "cat /proc/sys/net/bridge/bridge-nf-call-iptables 2>/dev/null || echo missing")
	val := strings.TrimSpace(out)
	if val == "1" {
		c := CheckResult{Name: "bridge-nf-call-iptables", OK: true, Detail: "enabled"}
		logCheck(c)
		checks = append(checks, c)
	} else {
		_, err := sshRunOutput(target, "echo 1 > /proc/sys/net/bridge/bridge-nf-call-iptables 2>&1")
		if err != nil {
			c := CheckResult{Name: "bridge-nf-call-iptables", OK: false, Detail: fmt.Sprintf("was %q; failed to enable: %v", val, err)}
			logCheck(c)
			checks = append(checks, c)
			anyFailed = true
		} else {
			c := CheckResult{Name: "bridge-nf-call-iptables", OK: true, Detail: fmt.Sprintf("enabled (was %q, fixed)", val), Fixed: true}
			logCheck(c)
			checks = append(checks, c)
		}
	}

	// 4. conntrack table accessible
	out, _ = sshRunOutput(target, "test -f /proc/net/nf_conntrack && echo exists || echo missing")
	if strings.TrimSpace(out) == "exists" {
		c := CheckResult{Name: "conntrack table", OK: true, Detail: "/proc/net/nf_conntrack accessible"}
		logCheck(c)
		checks = append(checks, c)
	} else {
		c := CheckResult{Name: "conntrack table", OK: false, Detail: "/proc/net/nf_conntrack not found"}
		logCheck(c)
		checks = append(checks, c)
		anyFailed = true
	}

	if anyFailed {
		return checks, fmt.Errorf("some checks failed")
	}
	return checks, nil
}

// TestDeviceConnection runs a no-op SSH command on the device with the given IP. Returns nil on success.
func (m *Monitor) TestDeviceConnection(ip string) error {
	var dev *Device
	for i := range m.config.Devices {
		if m.config.Devices[i].IP == ip {
			dev = &m.config.Devices[i]
			break
		}
	}
	if dev == nil {
		return fmt.Errorf("device %s not found", ip)
	}
	target := SSHTarget{
		IP:       dev.IP,
		Port:     dev.SSHPort,
		User:     dev.SSHUser,
		Password: dev.SSHPassword,
		KeyPath:  dev.SSHKey,
	}
	return sshRun(target, "echo ok")
}

func (m *Monitor) routerBlock(dev *Device) {
	if m.state.IsRouterBlocked(dev.IP) {
		return // already blocked
	}
	cmd := fmt.Sprintf("iptables -I FORWARD -m mac --mac-source %s -j DROP", dev.MAC)
	err := sshRun(m.routerTarget(), cmd)
	if err != nil {
		log.Printf("Failed to router-block %s (%s): %v", dev.Name, dev.IP, err)
		return
	}
	m.state.SetRouterBlocked(dev.IP, true)
	log.Printf("Router-blocked %s (%s) MAC=%s", dev.Name, dev.IP, dev.MAC)
}

func (m *Monitor) RouterUnblock(dev *Device) {
	if !m.state.IsRouterBlocked(dev.IP) {
		return // not blocked
	}
	cmd := fmt.Sprintf("iptables -D FORWARD -m mac --mac-source %s -j DROP", dev.MAC)
	err := sshRun(m.routerTarget(), cmd)
	if err != nil {
		log.Printf("Failed to router-unblock %s (%s): %v", dev.Name, dev.IP, err)
		return
	}
	m.state.SetRouterBlocked(dev.IP, false)
	log.Printf("Router-unblocked %s (%s) MAC=%s", dev.Name, dev.IP, dev.MAC)
}

// routerDetectOnline checks if a device is online by querying the router's ARP table.
func (m *Monitor) routerDetectOnline(dev *Device) bool {
	cmd := "cat /proc/net/arp"
	output, err := sshRunOutput(m.routerTarget(), cmd)
	if err != nil {
		log.Printf("Failed to query router ARP table for %s: %v", dev.Name, err)
		return false
	}

	macUpper := strings.ToUpper(dev.MAC)
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(strings.ToUpper(line), macUpper) {
			// Check for the 0x2 flag (reachable)
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "0x2" {
				return true
			}
		}
	}
	return false
}

// routerDetectActiveTraffic checks if the device has at least TrafficThreshold active
// connections in the router's conntrack table. Only then do we count usage.
func (m *Monitor) routerDetectActiveTraffic(dev *Device) bool {
	thresh := m.config.TrafficThreshold
	if thresh < 1 {
		thresh = 1
	}
	// Match src=IP or dst=IP so we catch both outgoing and incoming traffic
	// (e.g. LAN streaming where the device is the destination, not the source)
	cmd := fmt.Sprintf("grep -cE '(src|dst)=%s ' /proc/net/nf_conntrack 2>/dev/null || echo 0", dev.IP)
	output, err := sshRunOutput(m.routerTarget(), cmd)
	if err != nil {
		log.Printf("Failed to query router conntrack for %s: %v", dev.Name, err)
		return false
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(output), "%d", &count); err != nil {
		return false
	}
	if m.Verbose {
		log.Printf("[conntrack] %s (%s): %d entries (threshold=%d, active=%v)", dev.Name, dev.IP, count, thresh, count >= thresh)
	}
	return count >= thresh
}

// jellyfinDetectPlaying checks if the device has an active playback session on Jellyfin.
func (m *Monitor) jellyfinDetectPlaying(dev *Device) bool {
	if m.config.Jellyfin == nil || m.config.Jellyfin.URL == "" {
		return false
	}
	jf := m.config.Jellyfin
	url := strings.TrimRight(jf.URL, "/") + "/Sessions?activeWithinSeconds=60"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("Jellyfin request error for %s: %v", dev.Name, err)
		return false
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Token="%s"`, jf.APIKey))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Jellyfin HTTP error for %s: %v", dev.Name, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Jellyfin returned status %d for %s", resp.StatusCode, dev.Name)
		return false
	}

	var sessions []struct {
		RemoteEndPoint string `json:"RemoteEndPoint"`
		NowPlayingItem *struct {
			Name string `json:"Name"`
		} `json:"NowPlayingItem"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		log.Printf("Jellyfin JSON decode error for %s: %v", dev.Name, err)
		return false
	}

	for _, s := range sessions {
		if s.NowPlayingItem != nil {
			if m.Verbose {
				log.Printf("[jellyfin] %s: playing %q (endpoint=%s)", dev.Name, s.NowPlayingItem.Name, s.RemoteEndPoint)
			}
			return true
		}
	}
	if m.Verbose {
		log.Printf("[jellyfin] %s: %d sessions, none playing", dev.Name, len(sessions))
	}
	return false
}

// TestJellyfinConnection checks Jellyfin connectivity and authentication.
func (m *Monitor) TestJellyfinConnection() ([]CheckResult, error) {
	if m.config.Jellyfin == nil || m.config.Jellyfin.URL == "" {
		return nil, fmt.Errorf("jellyfin not configured")
	}

	var checks []CheckResult
	jf := m.config.Jellyfin
	client := &http.Client{Timeout: 5 * time.Second}

	logCheck := func(c CheckResult) {
		if m.Verbose {
			status := "OK"
			if !c.OK {
				status = "FAIL"
			}
			log.Printf("[jellyfin-test] %s: %s — %s", status, c.Name, c.Detail)
		}
	}

	// 1. HTTP connectivity (unauthenticated endpoint)
	url1 := strings.TrimRight(jf.URL, "/") + "/System/Info/Public"
	resp, err := client.Get(url1)
	if err != nil {
		c := CheckResult{Name: "HTTP connectivity", OK: false, Detail: err.Error()}
		logCheck(c)
		checks = append(checks, c)
		return checks, fmt.Errorf("HTTP connectivity failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c := CheckResult{Name: "HTTP connectivity", OK: false, Detail: fmt.Sprintf("status %d", resp.StatusCode)}
		logCheck(c)
		checks = append(checks, c)
		return checks, fmt.Errorf("HTTP connectivity failed: status %d", resp.StatusCode)
	}
	c := CheckResult{Name: "HTTP connectivity", OK: true, Detail: "connected to " + jf.URL}
	logCheck(c)
	checks = append(checks, c)

	// 2. Auth check (authenticated endpoint)
	url2 := strings.TrimRight(jf.URL, "/") + "/Sessions?activeWithinSeconds=60"
	req, err := http.NewRequest("GET", url2, nil)
	if err != nil {
		c := CheckResult{Name: "API authentication", OK: false, Detail: err.Error()}
		logCheck(c)
		checks = append(checks, c)
		return checks, fmt.Errorf("auth check failed: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Token="%s"`, jf.APIKey))
	resp, err = client.Do(req)
	if err != nil {
		c := CheckResult{Name: "API authentication", OK: false, Detail: err.Error()}
		logCheck(c)
		checks = append(checks, c)
		return checks, fmt.Errorf("auth check failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c := CheckResult{Name: "API authentication", OK: false, Detail: fmt.Sprintf("status %d (check API key)", resp.StatusCode)}
		logCheck(c)
		checks = append(checks, c)
		return checks, fmt.Errorf("auth check failed: status %d", resp.StatusCode)
	}
	c = CheckResult{Name: "API authentication", OK: true, Detail: "API key valid"}
	logCheck(c)
	checks = append(checks, c)

	return checks, nil
}

func onOff(b bool) string {
	if b {
		return "online"
	}
	return "offline"
}

func ping(ip string) bool {
	cmd := exec.Command("ping", "-c", "1", "-W", "2", ip)
	err := cmd.Run()
	return err == nil
}

func sshRun(target SSHTarget, command string) error {
	_, err := sshRunOutput(target, command)
	return err
}

func sshRunOutput(target SSHTarget, command string) (string, error) {
	var authMethods []ssh.AuthMethod
	if target.KeyPath != "" {
		signer, err := loadKeyFile(target.KeyPath)
		if err != nil {
			return "", fmt.Errorf("loading ssh key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if target.Password != "" {
		authMethods = append(authMethods, ssh.Password(target.Password))
	}

	config := &ssh.ClientConfig{
		User:            target.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	port := target.Port
	if port == "" {
		port = "22"
	}
	addr := net.JoinHostPort(target.IP, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return "", fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		// Connection drops are expected when shutting down
		if _, ok := err.(*ssh.ExitMissingError); ok {
			return string(output), nil
		}
		return string(output), fmt.Errorf("ssh run: %w", err)
	}
	return string(output), nil
}

func loadKeyFile(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}
