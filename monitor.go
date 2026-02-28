package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHTarget holds connection details for an SSH host.
type SSHTarget struct {
	IP       string
	User     string
	Password string
	KeyPath  string
}

type Monitor struct {
	config *Config
	state  *State
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
	default:
		online = ping(dev.IP)
	}
	m.state.SetOnline(dev.IP, online)

	// Unblock logic for router-blocked devices
	if dev.BlockMethod == "router" && m.state.IsRouterBlocked(dev.IP) {
		if m.shouldUnblock(dev) {
			log.Printf("Device %s (%s): unblocking on router", dev.Name, dev.IP)
			m.RouterUnblock(dev)
		}
	}

	if !online {
		return
	}

	// Device is online — increment usage
	m.state.IncrementUsage(dev.IP)

	// Check if blocking is disabled
	if m.state.IsDisabled(dev.IP) {
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
		User:     r.SSHUser,
		Password: r.SSHPassword,
		KeyPath:  r.SSHKey,
	}
}

// TestRouterConnection runs a no-op SSH command on the router. Returns nil on success.
func (m *Monitor) TestRouterConnection() error {
	if m.config.Router == nil || m.config.Router.IP == "" {
		return fmt.Errorf("router not configured")
	}
	return sshRun(m.routerTarget(), "echo ok")
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
	// Match src=IP (space) so we don't match a prefix; count matches
	cmd := fmt.Sprintf("grep -cF 'src=%s ' /proc/net/nf_conntrack 2>/dev/null || echo 0", dev.IP)
	output, err := sshRunOutput(m.routerTarget(), cmd)
	if err != nil {
		log.Printf("Failed to query router conntrack for %s: %v", dev.Name, err)
		return false
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(output), "%d", &count); err != nil {
		return false
	}
	return count >= thresh
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

	addr := net.JoinHostPort(target.IP, "22")
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
