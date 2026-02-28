# ParentalParrot

Network-wide parental control daemon written in Go. Monitors device usage via ping or router conntrack (active traffic only; not just “on network”), enforces daily time limits and allowed-hours schedules, and blocks devices either by SSH shutdown (Linux/macOS/Windows) or by adding iptables firewall rules on an OpenWRT router (phones/tablets/TVs).

## Build & Test

```bash
make build    # build binary
make test     # run all tests (go test -v -count=1 ./...)
make dist     # cross-compile for linux/darwin/windows amd64+arm64
```

## Project Structure

All source is in the root package (`package main`):

- `config.go` — Config/Device/Router/Schedule structs, YAML parsing (for seed), validation (ValidateDevice in db.go)
- `db.go` — SQLite schema, OpenDB/InitSchema, SeedFromYAMLIfEmpty, LoadConfigFromDB/SaveConfigToDB, state helpers (dbIncrementUsage, etc.)
- `state.go` — State: in-memory + either JSON file (NewState) or SQLite (NewStateDB); usage, online, disabled_until, router_blocked
- `monitor.go` — Main loop: ping/conntrack detection, usage tracking, block/unblock dispatch, SSH operations
- `web.go` — HTTP dashboard (login, device status, disable), Settings page (edit config: password, root schedule, router, devices)
- `main.go` — Entrypoint: -db, -seed flags; OpenDB, InitSchema, SeedFromYAMLIfEmpty, LoadConfigFromDB, NewStateDB, monitor, web

## Key Patterns

- **SQLite** is the main store: config (ui_password), root_schedule, router, devices, plus usage/disabled_until/online/router_blocked. Empty DB is seeded from config.example.yaml.
- `SSHTarget` struct decouples SSH connection details from device config — used for both device SSH and router SSH
- `BlockMethod` field dispatches between `ssh_shutdown` and `router` blocking
- `DetectMethod` field dispatches between `ping` and `router_conntrack` (conntrack = only count usage when router sees active traffic from device, e.g. streaming; TV in sleep = no usage)
- Router blocking is bidirectional: `routerBlock`/`RouterUnblock` add/remove iptables rules and track state in DB (or JSON when using NewState)
- `shouldUnblock()` determines when to remove firewall rules (disable active, under limit, within allowed hours)
- Web server holds db and *Config; on Settings save it calls SaveConfigToDB then ReloadConfigFromDB so the in-memory config (and monitor) see updates immediately

## Testing

Tests use real structs with temp files — no mocks. SSH-dependent tests (block dispatch, router unblock) expect SSH failures and verify the code doesn't panic. Run `go vet ./...` before committing.
