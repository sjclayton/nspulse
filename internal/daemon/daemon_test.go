package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjclayton/nspulse/internal/discover"
)

func TestIncumbentOfPrefersInUseServer(t *testing.T) {
	ordered := []string{"192.168.1.10", "192.168.1.20"}
	// In-use server present in list but not head: trust it.
	link := discover.LinkDNS{Servers: ordered, Current: "192.168.1.20"}
	if got := incumbentOf(link, ordered); got != "192.168.1.20" {
		t.Fatalf("got %q, want 192.168.1.20", got)
	}
	// Stale current (not in list): fall back to list head.
	link = discover.LinkDNS{Servers: ordered, Current: "10.0.0.1"}
	if got := incumbentOf(link, ordered); got != "192.168.1.10" {
		t.Fatalf("got %q, want 192.168.1.10", got)
	}
	// Empty current: fall back to list head.
	link = discover.LinkDNS{Servers: ordered}
	if got := incumbentOf(link, ordered); got != "192.168.1.10" {
		t.Fatalf("got %q, want 192.168.1.10", got)
	}
	if got := incumbentOf(discover.LinkDNS{}, nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Current DNS Server may be printed without the annotation while the list
// entry has one. Match by bare address and return the raw entry.
func TestIncumbentOfMatchesAnnotatedEntry(t *testing.T) {
	ordered := []string{"192.168.1.10#dns.example", "192.168.1.20"}
	link := discover.LinkDNS{Current: "192.168.1.10"}
	if got := incumbentOf(link, ordered); got != ordered[0] {
		t.Fatalf("got %q, want %q", got, ordered[0])
	}
}

func TestIntersectKeepOrderMatchesBareAddress(t *testing.T) {
	ordered := []string{"192.168.1.10#dns.example", "192.168.1.20", "10.0.0.1"}
	got := intersectKeepOrder(ordered, []string{"192.168.1.10", "192.168.1.20"})
	want := []string{"192.168.1.10#dns.example", "192.168.1.20"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Zero or negative durations used to panic (interval) or silently break
// every query (timeout); an out-of-range margin disables switching.
func TestValidateFlags(t *testing.T) {
	if err := validateFlags(5*time.Minute, time.Hour, 2*time.Second, 0.1); err != nil {
		t.Fatalf("valid flags rejected: %v", err)
	}
	cases := []struct {
		name                      string
		interval, window, timeout time.Duration
		margin                    float64
	}{
		{"zero interval", 0, time.Hour, time.Second, 0.1},
		{"negative interval", -time.Second, time.Hour, time.Second, 0.1},
		{"zero window", time.Minute, 0, time.Second, 0.1},
		{"zero timeout", time.Minute, time.Hour, 0, 0.1},
		{"negative margin", time.Minute, time.Hour, time.Second, -0.1},
		{"margin one", time.Minute, time.Hour, time.Second, 1},
	}
	for _, tc := range cases {
		if err := validateFlags(tc.interval, tc.window, tc.timeout, tc.margin); err == nil {
			t.Errorf("%s: want error", tc.name)
		}
	}
}

// A second daemon must be refused while the first holds the lock, and the
// lock must be reusable after the first exits.
func TestAcquireLockRefusesSecond(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nspulse.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := acquireLock(path); err == nil {
		second.Close()
		t.Fatal("second acquire succeeded, want error")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second acquire error = %q, want 'already running'", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireLock(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	third.Close()
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
