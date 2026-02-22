# ParentalParrot

```
    ,///
   (o  >    ParentalParrot
    \_\>    Device usage monitor
```

## The Problem

Existing parental control tools work great for individual device types but none of them cover the full picture. Apple Screen Time only manages iPhones and iPads. Google Family Link handles Android devices. Router-level controls can restrict internet access but have no visibility into local usage. Desktop tools only see the computer they're installed on. If your household has a mix of PCs, phones, tablets, and smart TVs, you end up juggling multiple apps with separate limits — and kids quickly learn that when the PC is locked, the tablet still works.

ParentalParrot takes a different approach: it runs on your network as a single service and enforces unified time limits across all screen devices, regardless of OS or form factor. Computers are shut down via SSH. Phones, tablets, and TVs — devices that can't be shut down remotely — are blocked at the network level by SSHing into your OpenWRT router and adding iptables firewall rules. One config file, one dashboard, one set of rules for everything.

## Features

- Ping-based device monitoring (every 60 seconds)
- Per-day time limits with flexible scheduling (all days / weekday+weekend / individual days)
- Allowed hours window (e.g. 08:00-21:00)
- Two blocking methods:
  - **SSH shutdown** for computers (Linux, macOS, and Windows)
  - **Router firewall rules** for phones, tablets, and TVs via OpenWRT (iptables MAC-based blocking)
- Two detection methods:
  - **Ping** for devices that respond to ICMP
  - **Router ARP table** for devices that don't (many phones/tablets drop pings)
- Automatic unblocking when limits reset, allowed hours resume, or blocking is manually disabled
- SSH key and password authentication
- Web dashboard with login, usage bars, and "Disable for 1 hour" button
- Persistent state across restarts (JSON file, auto-prunes entries older than 7 days)
- Graceful shutdown on SIGINT/SIGTERM

## Quick Start

```bash
# Build
make build

# Edit config
cp config.toml my-config.toml
vim my-config.toml

# Run
./ParentalParrot -config my-config.toml
```

Open http://localhost:8080 and log in with the password from your config.

## Configuration

```toml
ui_password = "secret123"

# Required for block_method = "router" or detect_method = "router_conntrack"
[router]
ip = "192.168.1.1"
ssh_user = "root"
ssh_key = "/home/you/.ssh/id_ed25519"
# ssh_password = "password"  # alternative to ssh_key

# A computer — shut down via SSH when limit is exceeded
[[devices]]
name = "Kids PC"
ip = "192.168.1.100"
ssh_user = "admin"
ssh_password = "password"
os = "linux"  # "linux", "macos", or "windows"
# block_method = "ssh_shutdown"  # default
# detect_method = "ping"         # default

[devices.schedule]
all = 120  # minutes per day

[devices.schedule.allowed_hours]
start = "08:00"
end = "21:00"

# A phone — blocked at the router by MAC address
[[devices]]
name = "Kid's Phone"
ip = "192.168.1.150"
mac = "AA:BB:CC:DD:EE:FF"
block_method = "router"
detect_method = "router_conntrack"

[devices.schedule]
weekday = 60
weekend = 120
```

### Schedule Priority

Per-day override > weekday/weekend > all. If nothing is set, the device has unlimited time.

### SSH Setup

The SSH user on the target device needs permission to run the shutdown command:

- **Linux**: `sudo shutdown -h now` — add a sudoers rule like `admin ALL=(ALL) NOPASSWD: /sbin/shutdown`
- **macOS**: `sudo shutdown -h now` — enable Remote Login in System Settings > General > Sharing, and add the same sudoers rule as Linux
- **Windows**: `shutdown /s /t 0` — the SSH user needs to be an administrator

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `config.toml` | Path to config file |
| `-state` | `state.json` | Path to state file |
| `-listen` | `:8080` | Web UI listen address |

## Cross-Platform Build

```bash
# Build for all platforms (linux/darwin amd64+arm64, windows amd64)
make dist

# Binaries are placed in dist/
ls dist/
```

## Install as systemd Service

```bash
# Copy binary and config
sudo cp ParentalParrot /usr/local/bin/
sudo mkdir -p /etc/parentalparrot /var/lib/parentalparrot
sudo cp config.toml /etc/parentalparrot/

# Install and start service
sudo cp parentalparrot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now parentalparrot

# Check logs
journalctl -u parentalparrot -f
```
