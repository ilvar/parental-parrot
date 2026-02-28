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
  - **Router conntrack** for devices that don't (phones, tablets, TVs): usage is counted only when the router's conntrack table shows **active traffic** from the device (e.g. streaming). A device merely connected (e.g. TV in sleep) is not counted until it actually sends traffic.
- Automatic unblocking when limits reset, allowed hours resume, or blocking is manually disabled
- SSH key and password authentication
- Web dashboard with login, usage bars, and "Disable for 1 hour" button
- **Settings UI** — edit all config (password, root schedule, router, devices) in the browser; no YAML editing
- **SQLite** — config and state live in one database; if the DB is empty on first run, it is seeded from `config.example.yaml`
- Graceful shutdown on SIGINT/SIGTERM

## Quick Start

```bash
# Build
make build

# Run (creates parentalparrot.db if missing; seeds from config.example.yaml when DB is empty)
./ParentalParrot -db parentalparrot.db -seed config.example.yaml
```

Open http://localhost:8080, log in (default password from example: `secret123`), then go to **Settings** to change the UI password, root schedule, router, and devices. No config file to edit by hand.

## Docker

### Using Docker Compose (recommended)

```bash
# Build and start (detached)
docker compose up -d

# View logs
docker compose logs -f

# Stop
docker compose down
```

The database is stored in a named volume `parentalparrot-data`. To reset everything, remove the volume:

```bash
docker compose down -v
docker compose up -d
```

### Using Docker only

```bash
# Build the image
docker build -t parentalparrot .

# Run (creates a volume for the DB)
docker run -d -p 8080:8080 -v parentalparrot-data:/data --name parentalparrot parentalparrot

# Reset UI password (e.g. if you forgot it)
docker run --rm -v parentalparrot-data:/data parentalparrot -db /data/parentalparrot.db -reset-password yournewpassword

# Stop and remove container (volume keeps data)
docker stop parentalparrot && docker rm parentalparrot
```

In both cases, open http://localhost:8080 (or https://localhost:8080 if you use a reverse proxy). First run seeds the DB from the bundled `config.example.yaml`; change the password and all settings in the **Settings** page.

## Configuration (stored in DB; optional example YAML for seed)

The app uses SQLite as the main store. On first run with an empty DB, it loads `config.example.yaml` to populate config and devices. After that, change everything from the **Settings** page in the UI. The example YAML is for reference and initial seed only:

```yaml
ui_password: "secret123"

# Optional root schedule: shared daily limit across ALL devices (e.g. 120 = 2 hours total per day).
# When set, per-device schedule limits are ignored; optional allowed_hours here apply to all devices.
# schedule:
#   all: 120
#   allowed_hours:
#     start: "08:00"
#     end: "21:00"

# Required for block_method = "router" or detect_method = "router_conntrack".
# For router_conntrack the router must have /proc/net/nf_conntrack (standard on OpenWRT).
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
| `-db` | `parentalparrot.db` | Path to SQLite database |
| `-seed` | `config.example.yaml` | YAML file to seed DB when empty |
| `-reset-password` | (none) | Set to a new password to update UI password in DB and exit (e.g. `-reset-password=newpass`) |
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
# Copy binary and (optional) example config for first-time seed
sudo cp ParentalParrot /usr/local/bin/
sudo mkdir -p /etc/parentalparrot /var/lib/parentalparrot
sudo cp config.example.yaml /etc/parentalparrot/

# Install and start service (uses /var/lib/parentalparrot/parentalparrot.db by default)
sudo cp parentalparrot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now parentalparrot

# Check logs
journalctl -u parentalparrot -f
```
