package daemon

import (
	"testing"
	"time"

	"github.com/sjclayton/nspulse/internal/discover"
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

// Snapshot time is the last round's completion time, not fetch time, so
// status shows when measuring happened. Before round 1 it stays zero.
func TestSnapshotTimeIsRoundTime(t *testing.T) {
	st := &state{
		cfg:       Config{Samples: 4, Interval: 5 * time.Minute, Window: time.Hour},
		samples:   map[string][]float64{},
		roundMeds: map[string]float64{},
		roundOK:   map[string]bool{},
	}
	if got := st.snapshot(); !got.Time.IsZero() {
		t.Fatalf("pre-round snapshot time = %v, want zero", got.Time)
	}
	then := time.Now().Add(-time.Hour).Round(0)
	st.measuredAt = then
	st.round = 3
	if got := st.snapshot(); !got.Time.Equal(then) {
		t.Fatalf("snapshot time = %v, want round time %v", got.Time, then)
	}
	// A fresh hour clears the stamp until the next round completes.
	st.reset()
	if got := st.snapshot(); !got.Time.IsZero() || got.Round != 0 {
		t.Fatalf("post-reset snapshot = time %v round %d, want zero time round 0", got.Time, got.Round)
	}
}
