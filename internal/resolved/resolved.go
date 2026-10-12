// Package resolved reorders per-link DNS servers via resolvectl. It only
// ever reorders the known server set; it never adds or removes servers.
package resolved

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// CurrentOrder returns the per-link DNS server order resolvectl reports.
// Empty output means the link has no explicit DNS (uses global/fallback).
// Only valid IPs are returned, so header tokens can never leak in.
func CurrentOrder(iface string) ([]string, error) {
	out, err := exec.Command("resolvectl", "--no-pager", "dns", iface).Output()
	if err != nil {
		return nil, fmt.Errorf("resolvectl dns %s: %w", iface, err)
	}
	var servers []string
	for _, f := range strings.Fields(string(out)) {
		if net.ParseIP(f) != nil {
			servers = append(servers, f)
		}
	}
	return servers, nil
}

// SetOrder makes candidate the first server on iface, preserving the
// relative order of the rest. It is a no-op (nil) when candidate is
// already primary.
func SetOrder(iface string, servers []string, candidate string) error {
	if len(servers) == 0 {
		return fmt.Errorf("no servers to order")
	}
	if servers[0] == candidate {
		return nil
	}
	ordered := []string{candidate}
	for _, s := range servers {
		if s != candidate {
			ordered = append(ordered, s)
		}
	}
	args := append([]string{"dns", iface}, ordered...)
	out, err := exec.Command("resolvectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("resolvectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
