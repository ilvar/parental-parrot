# ParentalParrot

Network-wide parental control daemon written in Go. Monitors device usage via ping or router ARP table, enforces daily time limits and allowed-hours schedules, and blocks devices either by SSH shutdown (Linux/macOS/Windows) or by adding iptables firewall rules on an OpenWRT router (phones/tablets/TVs).

## Build & Test

```bash
make build    # build binary
make test     # run all tests (go test -v -count=1 ./...)
make dist     # cross-compile for linux/darwin/windows amd64+arm64
```

## Project Structure

All source is in the root package (`package main`):

- `config.go` — Config/Device/Router/Schedule structs, YAML parsing, validation
- `state.go` — Persistent JSON state (usage, online status, disabled-until, router-blocked)
- `monitor.go` — Main loop: ping/conntrack detection, usage tracking, block/unblock dispatch, SSH operations
- `web.go` — HTTP dashboard with login, device status, disable button
- `main.go` — Entrypoint, wiring, signal handling

## Key Patterns

- `SSHTarget` struct decouples SSH connection details from device config — used for both device SSH and router SSH
- `BlockMethod` field dispatches between `ssh_shutdown` and `router` blocking
- `DetectMethod` field dispatches between `ping` and `router_conntrack` detection
- Router blocking is bidirectional: `routerBlock`/`RouterUnblock` add/remove iptables rules and track state in `RouterBlocked` map
- `shouldUnblock()` determines when to remove firewall rules (disable active, under limit, within allowed hours)
- State is persisted to JSON on every mutation (usage increment, block/unblock, disable)
- Web server holds a `*Monitor` reference to trigger immediate unblock on disable

## Testing

Tests use real structs with temp files — no mocks. SSH-dependent tests (block dispatch, router unblock) expect SSH failures and verify the code doesn't panic. Run `go vet ./...` before committing.
