# ParentalParrot

```
    __
   (o o)   ParentalParrot
   / V \   Device usage monitor
  /(___)\
   ^   ^
```

A lightweight Go application that monitors devices on your network via ping, tracks daily usage time, and shuts them down via SSH when time limits are exceeded or they're outside the allowed schedule. A web UI allows temporarily disabling the blockage.

## Features

- Ping-based device monitoring (every 60 seconds)
- Per-day time limits with flexible scheduling (all days / weekday+weekend / individual days)
- Allowed hours window (e.g. 08:00-21:00)
- Automatic shutdown via SSH (Linux and Windows)
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

[[devices]]
name = "Kids PC"
ip = "192.168.1.100"
ssh_user = "admin"
ssh_password = "password"
# ssh_key = "/home/you/.ssh/id_ed25519"  # alternative to password
os = "linux"  # "linux" or "windows"

[devices.schedule]
# Use one of: "all", "weekday"/"weekend", or individual day names
all = 120  # minutes per day

[devices.schedule.allowed_hours]
start = "08:00"
end = "21:00"
```

### Schedule Priority

Per-day override > weekday/weekend > all. If nothing is set, the device has unlimited time.

### SSH Setup

The SSH user on the target device needs permission to run the shutdown command:

- **Linux**: `sudo shutdown -h now` — add a sudoers rule like `admin ALL=(ALL) NOPASSWD: /sbin/shutdown`
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
