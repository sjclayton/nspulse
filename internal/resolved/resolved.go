// Package resolved reorders per-link DNS servers via resolvectl. It only
// ever reorders the known server set; it never adds or removes servers.
package resolved

import (
	"fmt"
	"os/exec"
	"strings"
)

// SetOrder makes candidate the first server on iface, preserving the
// relative order of the rest. It is a no-op (nil) when candidate already
// heads the list, and an error when candidate is absent from servers.
func SetOrder(iface string, servers []string, candidate string) error {
	if len(servers) == 0 {
		return fmt.Errorf("no servers to order")
	}
	found := false
	for _, s := range servers {
		if s == candidate {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("candidate %s not in server list", candidate)
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
