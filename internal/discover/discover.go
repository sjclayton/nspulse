// Package discover finds the active (default-route) interface, the DHCP
// DNS server set for it, and resolved's per-link DNS state (configured
// order plus the server actually in use).
package discover

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// defRoute is one default route from either address family.
type defRoute struct {
	iface  string
	metric int64
}

// DefaultIface returns the interface holding the default route (IPv4 or
// IPv6) with the lowest metric, parsed from /proc/net/route and
// /proc/net/ipv6_route (no external dependency). Either table may be
// absent (v4-only or v6-only host); it fails only when no usable default
// route exists in either family.
func DefaultIface() (string, error) {
	var routes []defRoute
	if fh, err := os.Open("/proc/net/route"); err == nil {
		routes = parseV4Routes(fh)
		fh.Close()
	}
	// A missing v6 table just means a v4-only kernel path, not fatal.
	if data, err := os.ReadFile("/proc/net/ipv6_route"); err == nil {
		routes = append(routes, parseV6Routes(string(data))...)
	}
	return pickDefault(routes)
}

// parseV4Routes extracts default routes from /proc/net/route content.
func parseV4Routes(r io.Reader) []defRoute {
	var out []defRoute
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[1] != "00000000" {
			continue // not a default route
		}
		m, err := strconv.ParseInt(fields[6], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, defRoute{fields[0], m})
	}
	return out
}

// parseV6Routes extracts default routes from /proc/net/ipv6_route
// content. Columns: dest(32 hex) plen src src_plen nexthop metric refcnt
// use flags iface. A default route has an all-zero dest with prefix length
// 00. Metric 0xffffffff means "no metric" (e.g. lo) and is skipped.
func parseV6Routes(s string) []defRoute {
	var out []defRoute
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if fields[0] != "00000000000000000000000000000000" || fields[1] != "00" {
			continue // not a default route
		}
		m, err := strconv.ParseInt(fields[5], 16, 64)
		if err != nil || m < 0 || m == 0xffffffff {
			continue
		}
		out = append(out, defRoute{fields[9], m})
	}
	return out
}

// pickDefault returns the lowest-metric non-loopback interface, comparing
// IPv4 and IPv6 routes on equal footing.
func pickDefault(routes []defRoute) (string, error) {
	best, bestMetric := "", int64(0)
	for _, r := range routes {
		if r.iface == "" || r.iface == "lo" {
			continue
		}
		if best == "" || r.metric < bestMetric {
			best, bestMetric = r.iface, r.metric
		}
	}
	if best == "" {
		return "", fmt.Errorf("no default route found (neither IPv4 nor IPv6)")
	}
	return best, nil
}

// LinkDNS is systemd-resolved's per-link DNS state: the configured server
// order plus the server actually in use.
type LinkDNS struct {
	Servers []string // configured "DNS Servers:" order, as listed
	Current string   // in-use "Current DNS Server:"; "" if resolved reports none
}

// LinkStatus returns resolved's per-link DNS state for iface via
// resolvectl status. Servers keeps the configured order; Current is the
// server actually handling queries, which can differ from Servers[0]
// (e.g. after a failure or an external reorder).
func LinkStatus(iface string) (LinkDNS, error) {
	out, err := exec.Command("resolvectl", "--no-pager", "status", iface).Output()
	if err != nil {
		return LinkDNS{}, fmt.Errorf("resolvectl status %s: %w", iface, err)
	}
	return parseLinkStatus(string(out))
}

// parseLinkStatus extracts the "DNS Servers:" list (plus any wrapped
// continuation lines) and the "Current DNS Server:" value from resolvectl
// status output, preserving order and each entry as written. Entries may
// carry a #server-name (DoT SNI) or %zone (link-local IPv6); both are kept
// so switching can write the config back unchanged. Note the singular
// "Current DNS Server:" must not be mistaken for the plural list header.
func parseLinkStatus(output string) (LinkDNS, error) {
	var link LinkDNS
	collecting := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "Current DNS Server:"); ok {
			if fields := strings.Fields(rest); len(fields) > 0 && validServer(fields[0]) {
				link.Current = fields[0]
			}
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "DNS Servers:"); ok {
			link.Servers = append(link.Servers, validServers(strings.Fields(rest))...)
			collecting = true
			continue
		}
		if collecting {
			fields := strings.Fields(trimmed)
			// All-server continuation line (e.g. wrapped "DNS Servers:")?
			if ips := validServers(fields); len(fields) > 0 && len(ips) == len(fields) {
				link.Servers = append(link.Servers, ips...)
				continue
			}
			collecting = false
		}
	}
	if len(link.Servers) == 0 {
		return LinkDNS{}, fmt.Errorf("no DNS servers listed")
	}
	link.Servers = dedupeKeepOrder(link.Servers)
	return link, nil
}

// ServerKey normalizes a resolvectl server entry for comparison by dropping
// an optional port, %zone, and #server-name. It returns "" when the entry
// holds no valid IP.
func ServerKey(entry string) string {
	addr := entry
	if i := strings.IndexByte(addr, '#'); i >= 0 {
		addr = addr[:i]
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	if i := strings.IndexByte(addr, '%'); i >= 0 {
		addr = addr[:i]
	}
	if net.ParseIP(addr) == nil {
		return ""
	}
	return addr
}

// validServer reports whether entry is an IP, optionally with a %zone,
// :port, and #server-name, as accepted by resolvectl dns.
func validServer(entry string) bool {
	return ServerKey(entry) != ""
}

func validServers(in []string) []string {
	var out []string
	for _, s := range in {
		if validServer(s) {
			out = append(out, s)
		}
	}
	return out
}

// DHCPServers returns the DHCP-provided DNS server set for iface. It tries
// NetworkManager first (present on most desktop/server installs), then
// systemd-networkd lease files, then ISC dhclient lease files.
func DHCPServers(iface string) ([]string, error) {
	if s, err := fromNMCLI(iface); err == nil && len(s) > 0 {
		return s, nil
	}
	if s, err := fromNetworkdLeases(iface); err == nil && len(s) > 0 {
		return s, nil
	}
	return fromDHClientLeases(iface)
}

// fromNMCLI reads IP4.DNS/IP6.DNS for iface via nmcli. Terse mode escapes
// ":" inside values by default, which would turn an IPv6 server into
// "2001\:db8\:\:1"; -e no keeps the addresses parseable.
func fromNMCLI(iface string) ([]string, error) {
	out, err := exec.Command("nmcli", "-t", "-e", "no", "dev", "show", iface).Output()
	if err != nil {
		return nil, err
	}
	var servers []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "IP4.DNS[") || strings.HasPrefix(line, "IP6.DNS[") {
			if _, v, ok := strings.Cut(line, ":"); ok && net.ParseIP(strings.TrimSpace(v)) != nil {
				servers = append(servers, strings.TrimSpace(v))
			}
		}
	}
	return dedupe(servers), nil
}

// networkdLeaseDir is a variable so tests can point at fixtures.
var networkdLeaseDir = "/run/systemd/netif/leases"

// fromNetworkdLeases parses DNS= lines from the lease of iface only.
// systemd names lease files by ifindex, which scopes the lookup exactly:
// another interface's lease (a LAN link, a VPN, a second default route)
// must never contribute DNS servers, even if it has one.
func fromNetworkdLeases(iface string) ([]string, error) {
	ni, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("find interface %s: %w", iface, err)
	}
	data, err := os.ReadFile(filepath.Join(networkdLeaseDir, strconv.Itoa(ni.Index)))
	if err != nil {
		return nil, fmt.Errorf("no networkd lease for %s: %w", iface, err)
	}
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "DNS="); ok {
			servers = append(servers, strings.Fields(rest)...)
		}
	}
	servers = filterIPs(servers)
	if len(servers) == 0 {
		return nil, fmt.Errorf("no DNS in networkd lease for %s", iface)
	}
	return dedupe(servers), nil
}

// fromDHClientLeases parses option domain-name-servers in dhclient leases.
func fromDHClientLeases(iface string) ([]string, error) {
	paths, _ := filepath.Glob("/var/lib/dhcp/dhclient*.leases")
	paths2, _ := filepath.Glob("/var/lib/NetworkManager/dhclient*.lease")
	paths = append(paths, paths2...)
	var servers []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if !strings.Contains(string(data), iface) {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if rest, ok := strings.CutPrefix(line, "option domain-name-servers"); ok {
				rest = strings.Trim(strings.TrimSpace(rest), ";")
				for _, ip := range strings.Split(rest, ",") {
					if net.ParseIP(strings.TrimSpace(ip)) != nil {
						servers = append(servers, strings.TrimSpace(ip))
					}
				}
			}
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no DHCP DNS found for %s", iface)
	}
	return dedupe(servers), nil
}

// filterIPs keeps valid IPs, stripping resolvectl "#suffix" annotations
// (e.g. "2001:db8::1#dns.example") before validating.
func filterIPs(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if h, _, ok := strings.Cut(s, "#"); ok {
			s = strings.TrimSpace(h)
		}
		if net.ParseIP(s) != nil {
			out = append(out, s)
		}
	}
	return out
}

func dedupe(in []string) []string {
	out := dedupeKeepOrder(in)
	sort.Strings(out)
	return out
}

// dedupeKeepOrder dedupes while preserving first-seen order. Use for
// resolved's configured server list, whose order is significant.
func dedupeKeepOrder(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// SameSet reports whether two server lists hold the same IPs ignoring order.
func SameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac, bc := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
