package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"time"

	"golang.org/x/crypto/ssh"
)

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

func (m *Monitor) checkDevice(dev *Device) {
	online := ping(dev.IP)
	m.state.SetOnline(dev.IP, online)

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

	// Check allowed hours
	if !dev.Schedule.IsAllowedHour(now) {
		log.Printf("Device %s (%s): outside allowed hours, shutting down", dev.Name, dev.IP)
		m.halt(dev)
		return
	}

	// Check daily limit
	limit := dev.Schedule.LimitForDay(now.Weekday())
	if limit >= 0 {
		usage := m.state.GetUsageToday(dev.IP)
		if usage >= limit {
			log.Printf("Device %s (%s): daily limit reached (%d/%d min), shutting down", dev.Name, dev.IP, usage, limit)
			m.halt(dev)
			return
		}
	}
}

func ping(ip string) bool {
	cmd := exec.Command("ping", "-c", "1", "-W", "2", ip)
	err := cmd.Run()
	return err == nil
}

func (m *Monitor) halt(dev *Device) {
	var cmd string
	switch dev.OS {
	case "windows":
		cmd = "shutdown /s /t 0"
	default:
		cmd = "sudo shutdown -h now"
	}

	err := sshRun(dev, cmd)
	if err != nil {
		log.Printf("Failed to halt %s (%s): %v", dev.Name, dev.IP, err)
	}
}

func sshRun(dev *Device, command string) error {
	var authMethods []ssh.AuthMethod
	if dev.SSHKey != "" {
		signer, err := loadKeyFile(dev.SSHKey)
		if err != nil {
			return fmt.Errorf("loading ssh key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if dev.SSHPassword != "" {
		authMethods = append(authMethods, ssh.Password(dev.SSHPassword))
	}

	config := &ssh.ClientConfig{
		User:            dev.SSHUser,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(dev.IP, "22")
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	// shutdown commands typically kill the connection, so ignore EOF-like errors
	if err := session.Run(command); err != nil {
		// Connection drops are expected when shutting down
		if _, ok := err.(*ssh.ExitMissingError); ok {
			return nil
		}
		return fmt.Errorf("ssh run: %w", err)
	}
	return nil
}

func loadKeyFile(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}
