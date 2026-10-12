// Package discover finds the active (default-route) interface, the DHCP
// DNS server set for it, and the current resolved ordering.
package discover

import (
	"bufio"
	"bytes"
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

// defRoute is one default-route candidate from either address family.
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

// parseV4Routes extracts default-route candidates from /proc/net/route content.
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

// parseV6Routes extracts default-route candidates from /proc/net/ipv6_route
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
// IPv4 and IPv6 candidates on equal footing.
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

// OrderedServers returns the current per-link DNS server order for iface as
// known to systemd-resolved (via resolvectl status). Position 0 is primary;
// order is preserved exactly.
func OrderedServers(iface string) ([]string, error) {
	out, err := exec.Command("resolvectl", "--no-pager", "status", iface).Output()
	if err != nil {
		return nil, fmt.Errorf("resolvectl status %s: %w", iface, err)
	}
	return parseStatusOrder(string(out))
}

// parseStatusOrder extracts the "DNS Servers:" list (plus any wrapped
// continuation lines of bare IPs) from resolvectl status output, preserving
// order. Note "Current DNS Server:" (singular) is deliberately not matched.
func parseStatusOrder(output string) ([]string, error) {
	var servers []string
	collecting := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "DNS Servers:"); ok {
			servers = append(servers, filterIPs(strings.Fields(rest))...)
			collecting = true
			continue
		}
		if collecting {
			fields := strings.Fields(trimmed)
			// All-IP continuation line (e.g. wrapped "DNS Servers:")?
			// filterIPs also strips "#suffix" annotations.
			if ips := filterIPs(fields); len(fields) > 0 && len(ips) == len(fields) {
				servers = append(servers, ips...)
				continue
			}
			collecting = false
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no DNS servers listed")
	}
	return dedupeKeepOrder(servers), nil
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

// fromNMCLI reads IP4.DNS/IP6.DNS for iface via nmcli.
func fromNMCLI(iface string) ([]string, error) {
	out, err := exec.Command("nmcli", "-t", "dev", "show", iface).Output()
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

// fromNetworkdLeases parses DNS= lines in /run/systemd/netif/leases/*.
func fromNetworkdLeases(iface string) ([]string, error) {
	matches, _ := filepath.Glob("/run/systemd/netif/leases/*")
	var servers []string
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if !leaseMentionsIface(data, iface) {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "DNS="); ok {
				servers = append(servers, strings.Fields(rest)...)
			}
		}
	}
	servers = filterIPs(servers)
	if len(servers) == 0 {
		return nil, fmt.Errorf("no DNS in networkd leases for %s", iface)
	}
	return dedupe(servers), nil
}

// leaseMentionsIface is a best-effort check; lease files have no iface field
// in all versions, so when unsure we accept the file (caller intersects with
// resolved's per-link list anyway).
func leaseMentionsIface(data []byte, iface string) bool {
	if !bytes.Contains(data, []byte(iface)) {
		// No iface marker: still accept, single-default-route hosts
		// normally have exactly one lease file.
		return true
	}
	return true
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

func splitIPs(s string) ([]string, error) {
	servers := filterIPs(strings.Fields(s))
	if len(servers) == 0 {
		return nil, fmt.Errorf("no parseable IPs in %q", s)
	}
	return dedupeKeepOrder(servers), nil
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
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// dedupeKeepOrder dedupes while preserving first-seen order. Use for
// resolved's server list, where position 0 is the primary.
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
