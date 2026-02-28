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
cp config.yaml my-config.yaml
vim my-config.yaml

# Run
./ParentalParrot -config my-config.yaml
```

Open http://localhost:8080 and log in with the password from your config.

## Configuration

```yaml
ui_password: "secret123"

# Optional root schedule: shared daily limit across ALL devices (e.g. 120 = 2 hours total per day).
# When set, per-device schedule limits are ignored; optional allowed_hours here apply to all devices.
# schedule:
#   all: 120
#   allowed_hours:
#     start: "08:00"
#     end: "21:00"

# Required for block_method = "router" or detect_method = "router_conntrack"
# router:
#   ip: "192.168.1.1"
#   ssh_user: "root"
#   ssh_key: "/home/you/.ssh/id_ed25519"
#   # ssh_password: "password"  # alternative to ssh_key

# A computer — shut down via SSH when limit is exceeded
devices:
  - name: "Kids PC"
    ip: "192.168.1.100"
    ssh_user: admin
    ssh_password: password
    os: linux  # "linux", "macos", or "windows"
    # block_method: ssh_shutdown  # default
    # detect_method: ping         # default
    schedule:
      all: 120  # minutes per day
      allowed_hours:
        start: "08:00"
        end: "21:00"

  # A phone — blocked at the router by MAC address
  - name: "Kid's Phone"
    ip: "192.168.1.150"
    mac: "AA:BB:CC:DD:EE:FF"
    block_method: router
    detect_method: router_conntrack
    schedule:
      weekday: 60
      weekend: 120
```

### Schedule Priority

Per-day override > weekday/weekend > all. If nothing is set, the device has unlimited time.

### Root schedule (shared pool)

Top-level `schedule` defines a **shared** daily limit across all devices. For example, `schedule.all: 120` means all devices together get 2 hours per day total. When a root schedule with a limit is set, per-device limits are ignored. Optional `allowed_hours` on the root schedule apply to every device. Use this when you want one family-wide time budget instead of per-device limits.

### SSH Setup

The SSH user on the target device needs permission to run the shutdown command:

- **Linux**: `sudo shutdown -h now` — add a sudoers rule like `admin ALL=(ALL) NOPASSWD: /sbin/shutdown`
- **macOS**: `sudo shutdown -h now` — enable Remote Login in System Settings > General > Sharing, and add the same sudoers rule as Linux
- **Windows**: `shutdown /s /t 0` — the SSH user needs to be an administrator

#### Adding admin / root SSH users

**Linux (computer to be shut down)**  
Create a dedicated user and allow it to run only `shutdown` via sudo:

```bash
# On the target Linux machine
sudo useradd -m -s /bin/bash parentalparrot   # or use an existing admin user
sudo passwd parentalparrot                    # set password if using password auth

# Allow that user to run shutdown without a password
echo 'parentalparrot ALL=(ALL) NOPASSWD: /sbin/shutdown' | sudo tee /etc/sudoers.d/parentalparrot
sudo chmod 440 /etc/sudoers.d/parentalparrot
```

Use `ssh_user = "parentalparrot"` (and `ssh_password` or `ssh_key`) in your device config.

**OpenWRT router**  
The router is usually accessed as `root`. Ensure SSH is enabled and use key-based auth when possible:

```bash
# On your machine: copy your public key to the router
ssh-copy-id root@192.168.1.1
```

In config, set `[router]` with `ssh_user = "root"` and `ssh_key` (or `ssh_password`).

**macOS**  
Enable Remote Login, then add a sudoers rule for the SSH user (same as Linux):

- System Settings → General → Sharing → Remote Login: On (allow your user or “All users”).
- On the Mac: `sudo visudo` and add a line like `youruser ALL=(ALL) NOPASSWD: /sbin/shutdown`.

**Windows**  
Create a dedicated user in the Administrators group so it can run `shutdown /s /t 0`. Enable OpenSSH server if needed (Settings → Apps → Optional features → Add OpenSSH Server).

Run in PowerShell as Administrator:

```powershell
# Create a dedicated local user (replace password with a strong one)
$Password = ConvertTo-SecureString "YourSecurePassword" -AsPlainText -Force
New-LocalUser -Name "parentalparrot" -Password $Password -FullName "ParentalParrot" -Description "SSH user for ParentalParrot shutdown"

# Add to Administrators group so shutdown works
Add-LocalGroupMember -Group "Administrators" -Member "parentalparrot"
```

Use `ssh_user = "parentalparrot"` and `ssh_password` in your device config.

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `config.yaml` | Path to config file |
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
sudo cp config.yaml /etc/parentalparrot/

# Install and start service
sudo cp parentalparrot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now parentalparrot

# Check logs
journalctl -u parentalparrot -f
```
