# NSPulse

Daemon that measures DNS server response times and keeps the fastest server first on a Linux machine managed by `systemd-resolved`.

## How it works

1. **Discover** the active interface on every round: the lowest-metric default route, checking both `/proc/net/route` (IPv4) and `/proc/net/ipv6_route` (IPv6) and comparing by metric. Works on IPv6-only hosts. If the interface changes mid-hour, the hour resets.
2. **DNS set from DHCP** (`nmcli dev show`, then systemd-networkd leases, then dhclient leases); **order from resolved** (`resolvectl status`). Only servers present in both lists are measured. NSPulse never adds servers of its own. If DHCP discovery fails entirely, it falls back to resolved's set so a statically-configured server is still measured. If the server set changes (e.g. DHCP renewal), the hour resets.
3. **Measure** every 5 minutes (default): 4 direct A queries per server per round, cycling the `--domains` list such as `google.com` and `cloudflare.com`, 2s per-query timeout. Measures real DNS response time over UDP port 53 (not ping), querying each server directly and bypassing the resolved cache. Any reply counts, including NXDOMAIN/SERVFAIL. The server answered, so its response time is recorded. A fully-timed-out round marks the server down for that round.
4. **Decide** after 1 hour (12 rounds at defaults): lowest hourly **median** wins; medians within 0.05 ms are treated as tied and the lowest p95 wins. An exact tie (median and p95 equal) keeps the incumbent. There is no evidence favoring a switch. A challenger dethrones the incumbent only if its median beats the incumbent's by a margin larger than `--margin` (default 10%), to avoid switching back and forth between servers of similar speed. A fully-down incumbent always loses to any healthy challenger; if every server is down for the whole hour, nothing switches.
5. **Switch** with `resolvectl dns <iface> <candidate first, rest in original order>`. No-op if the candidate is already primary. If the new primary is down, resolved falls back to the other server on its own. Only the `daemon` command switches; `status`/`watch` are read-only socket clients.

State is in memory only, served on a Unix socket (default `/run/nspulse.sock`, mode 0666 so unprivileged `status`/`watch` work). A daemon restart starts a fresh hour.

## Requirements

- Linux with `systemd-resolved` (`resolvectl` on PATH)
- Root for the daemon (`resolvectl dns` needs privilege); `status`/`watch` run as any user
- `nmcli` preferred for DHCP discovery (falls back to lease files without it)
- To build: Go 1.27+ and `upx` (or `make build UPX=0` to skip packing)

## Build

```sh
make build          # release: static, stripped, UPX-packed (~1.2 MB) into out/
make build UPX=0    # same, without packing (~3.5 MB)
make debug          # build with debug symbols
```

The release build is fully static (`CGO_ENABLED=0`, no libc needed) and
dependency-free: the DNS prober is stdlib-only, so the pre-pack binary is
~3.5 MB and UPX takes it to ~1.2 MB. Skip packing (`UPX=0`) if your
environment flags packed binaries.

Other targets: `make test`, `make check` (gofmt + vet + test), `make run [args]`, `make clean`, `make install` (binary + systemd unit, needs root).

## Run

Daemon (does the switching):

```sh
sudo nspulse daemon
```

## Daemon flags

| Flag         | Default                     | What it does                                                                                           |
| ------------ | --------------------------- | ------------------------------------------------------------------------------------------------------ |
| `--interval` | `5m`                        | Time between measurement rounds.                                                                       |
| `--window`   | `1h`                        | Measurement window before a switching decision. Needs ≥ 2 rounds (`window` ÷ `interval`).              |
| `--samples`  | `4`                         | A queries sent to each server per round, cycling `--domains`. Failed queries are dropped, not retried. |
| `--timeout`  | `2s`                        | Per-query deadline. A query slower than this counts as failed.                                         |
| `--domains`  | `google.com,cloudflare.com` | Comma-separated names queried (A records), cycled across samples.                                      |
| `--socket`   | `/run/nspulse.sock`         | Unix socket the daemon serves status on for `status`/`watch`. Created mode 0666.                       |
| `--iface`    | `auto`                      | Interface to manage. `auto` follows the lowest-metric default route; set explicitly to pin one.        |
| `--margin`   | `0.10`                      | Challenger must beat the incumbent's hourly median by this fraction to trigger a switch. Anti-flap.    |
| `--dry-run`  | off                         | Log what would be switched without calling `resolvectl`.                                               |

Example with non-defaults:

```sh
sudo nspulse daemon --interval 5m --window 1h --samples 4 --timeout 2s \
  --socket /run/nspulse.sock --margin 0.10 --dry-run
```

Read-only clients (never switch):

```sh
nspulse status                 # one snapshot: hour progress + probable candidate
nspulse status --json          # raw JSON (same payload the daemon serves)
nspulse watch                  # live view, refreshes every 5s (see --every)
```

Example `status` (mid-hour, incumbent `.20` still winning, no switch):

```
nspulse  iface=eno1  round 2/12 (every 5m0s)  at 01:39:07
order:   *192.168.1.20   192.168.1.10
server        n  fail  median     p95  last-round  dhcp?
192.168.1.10  8     0  0.71ms  0.88ms      0.68ms    yes
192.168.1.20  8     0  0.31ms  0.36ms      0.23ms    yes
probable candidate: 192.168.1.20  (no switch needed)
```

`*` marks the current primary. `fail` counts queries that produced no usable reply this hour (timeouts, dial/write failures). `last-round` shows `down` when a server answered nothing in the latest round. `dhcp?` confirms each listed server is in the DHCP set. Servers in the resolved order but absent from DHCP are excluded from measuring entirely. When the challenger beats the margin, the candidate line reads `-> WOULD SWITCH from <primary> (margin beaten)`.

## Install as a service

```sh
make install            # builds, installs binary + unit, reloads systemd (as root)
sudo systemctl enable --now nspulse
journalctl -u nspulse -f
```

Or manually:

```sh
make build
sudo install -m 0755 out/nspulse /usr/local/bin/nspulse
sudo cp nspulse.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now nspulse
```

## Tests

```sh
make test     # or: go test ./...
make check    # gofmt + go vet + tests
```
