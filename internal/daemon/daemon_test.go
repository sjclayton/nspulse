package daemon

import (
	"testing"

	"github.com/shaun/nspulse/internal/discover"
)

func TestPrimaryOfPrefersInUseServer(t *testing.T) {
	ordered := []string{"192.168.1.10", "192.168.1.20"}
	// In-use server present in list but not head: trust it.
	link := discover.LinkDNS{Servers: ordered, Current: "192.168.1.20"}
	if got := primaryOf(link, ordered); got != "192.168.1.20" {
		t.Fatalf("got %q, want 192.168.1.20", got)
	}
	// Stale current (not in list): fall back to list head.
	link = discover.LinkDNS{Servers: ordered, Current: "10.0.0.1"}
	if got := primaryOf(link, ordered); got != "192.168.1.10" {
		t.Fatalf("got %q, want 192.168.1.10", got)
	}
	// Empty current: fall back to list head.
	link = discover.LinkDNS{Servers: ordered}
	if got := primaryOf(link, ordered); got != "192.168.1.10" {
		t.Fatalf("got %q, want 192.168.1.10", got)
	}
	if got := primaryOf(discover.LinkDNS{}, nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
