# nspulse

Keeps the fastest DNS server set as the primary on Linux machines that use `systemd-resolved`.

## What it does

Every 5 minutes nspulse sends real DNS queries to each DNS server configured on the active interface and records the response times. After an hour it compares the results. If a candidate is faster than the incumbent by more than the margin (10% by default), it gets promoted to primary and nspulse moves it to the front of the `resolved` server list.

nspulse never adds or removes DNS servers. It only reorders servers that are already configured, and it keeps the incumbent when it is already the primary. Servers come from DHCP and from `resolvectl`; if DHCP discovery fails, the `resolved` list is used, so static configurations still work.

Measurements live in memory for the current hour. Restarting the daemon, changing the active interface, or changing the server set starts a fresh hour.

Only `nspulse daemon` changes DNS settings, `status` and `watch` read state and never switch anything. Only one daemon instance can run at a time; a second exits with an error.

Server entries keep their original form when reordered, including a `#server-name` (DoT) and an IPv6 `%zone`.

## Requirements

- Linux with systemd-resolved (`resolvectl` on PATH)
- Root for the daemon; `status` and `watch` run as any user
- Works with IPv4 and IPv6 servers, including IPv6-only hosts
- To build: Go 1.27+ and `upx` (optional; skip with `make build UPX=0`)

## Install

```sh
make install            # builds, installs binary + unit, reloads systemd (as root)
sudo systemctl enable --now nspulse
journalctl -u nspulse -f
```

Or install manually:

```sh
make build
sudo install -m 0755 out/nspulse /usr/local/bin/nspulse
sudo cp nspulse.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now nspulse
```

## Usage

Run the daemon in the foreground:

```sh
sudo nspulse daemon
```

Check on it:

```sh
nspulse status                 # one snapshot: hour progress + probable candidate
nspulse status --json          # raw JSON (same payload the daemon serves)
nspulse watch                  # live view, refreshes every 5s (see --every)
```

Example `status`:

```
nspulse  iface=eno1  round 2/12 (every 5m0s)  at 01:39:07
order:   *192.168.1.20   192.168.1.10
server        n  fail  median     p95  last-round
192.168.1.10  8     0  0.71ms  0.88ms      0.68ms
192.168.1.20  8     0  0.31ms  0.36ms      0.23ms
probable candidate: 192.168.1.20  (no switch needed)
```

`*` marks the incumbent, the server systemd-resolved is currently using. `at` is the time the last measurement round finished, and reads `(no rounds yet)` before the first round. `fail` counts queries that got no usable reply this hour. `last-round` shows `down` when a server answered nothing in the latest round. If the candidate beats the margin, the candidate line reads `-> WOULD SWITCH from <incumbent> (margin beaten)`.

## Daemon flags

| Flag         | Default                     | What it does                                                                                                 |
| ------------ | --------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `--interval` | `5m`                        | Time between measurement rounds.                                                                             |
| `--window`   | `1h`                        | Measurement window before a switching decision. Needs ≥ 2 rounds (`window` ÷ `interval`).                    |
| `--samples`  | `4`                         | A queries sent to each server per round, cycling `--domains`. Failed queries are dropped, not retried.       |
| `--timeout`  | `2s`                        | Per-query deadline. A query slower than this counts as failed.                                               |
| `--domains`  | `google.com,cloudflare.com` | Comma-separated names queried (A records), cycled across samples.                                            |
| `--socket`   | `/run/nspulse.sock`         | Unix socket the daemon serves status on for `status`/`watch`. Created mode 0666.                             |
| `--iface`    | `auto`                      | Interface to manage. `auto` follows the lowest-metric default route; set explicitly to pin one.              |
| `--margin`   | `0.10`                      | Candidate must beat the incumbent's hourly median by this fraction to trigger a switch. Anti-flap mechanism. |
| `--dry-run`  | off                         | Log what would be switched without calling `resolvectl`.                                                     |

Example with non-default flags:

```sh
sudo nspulse daemon --interval 5m --window 1h --samples 4 --timeout 2s \
  --socket /run/nspulse.sock --margin 0.10 --dry-run
```

## Build

```sh
make build          # release: static, stripped, UPX-packed into out/
make build UPX=0    # same, without packing
make debug          # build with debug symbols
make test           # or: go test ./...
make check          # gofmt + go vet + tests
```

The release binary is static and needs no libc. Skip UPX packing (`UPX=0`) if your environment flags packed binaries.
