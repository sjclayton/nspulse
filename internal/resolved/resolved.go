// Package resolved switches per-link DNS servers via resolvectl. It only
// ever reorders the known set: the narrowing step temporarily drops the
// other servers, then the full set is restored.
package resolved

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sjclayton/nspulse/internal/discover"
	"github.com/sjclayton/nspulse/internal/execpath"
)

// How long to wait for resolved to adopt the narrowed, candidate-only list
// before restoring the full set, and how often to check. Variables so tests
// can skip the wait.
var (
	adoptLimit = time.Second
	adoptPoll  = 50 * time.Millisecond
)

// SetOrder switches the link from its incumbent to candidate, preserving the
// relative order of the rest. Writing a full list does not move resolved's
// Current DNS Server, so it first narrows the link to candidate alone, waits
// until resolved reports candidate as current (shortening the no-fallback
// window), then restores the full list with candidate first. It errors when
// candidate is absent from servers.
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
	if len(servers) == 1 {
		// Nothing to reorder. Leave the link untouched: a host with one
		// configured server must keep it even when it is down, and there
		// is no fallback to lose.
		return nil
	}
	ordered := []string{candidate}
	for _, s := range servers {
		if s != candidate {
			ordered = append(ordered, s)
		}
	}
	if err := set(iface, candidate); err != nil {
		return err
	}
	for deadline := time.Now().Add(adoptLimit); time.Now().Before(deadline); time.Sleep(adoptPoll) {
		if link, err := discover.LinkStatus(iface); err == nil && discover.ServerKey(link.Current) == discover.ServerKey(candidate) {
			break
		}
	}
	// One retry: a failed restore would leave the link narrowed to the
	// candidate alone until resolved re-applies the full set.
	if err := set(iface, ordered...); err != nil {
		if retry := set(iface, ordered...); retry != nil {
			return err
		}
	}
	return nil
}

func set(iface string, servers ...string) error {
	path, err := execpath.Lookup("resolvectl")
	if err != nil {
		return err
	}
	args := append([]string{"dns", iface}, servers...)
	out, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("resolvectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
