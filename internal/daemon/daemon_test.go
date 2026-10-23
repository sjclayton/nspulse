package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjclayton/nspulse/internal/discover"
	"github.com/sjclayton/nspulse/internal/execpath"
)

// useSearchDir makes dir the first fixed helper directory for this test and
// restores the search list afterwards, so stubs are found without PATH.
func useSearchDir(t *testing.T, dir string) {
	t.Helper()
	old := execpath.SearchDirs
	execpath.SearchDirs = append([]string{dir}, old...)
	t.Cleanup(func() { execpath.SearchDirs = old })
}

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

// startTestDNS replies to any UDP query with a matching ID and QR set, which
// is all measure.Probe needs to consider the server reachable.
func startTestDNS(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil || n < 4 {
				return
			}
			resp := []byte{buf[0], buf[1], 0x81, 0x80, 0, 0, 0, 0, 0, 0, 0, 0}
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

// closedUDPAddr returns a localhost address with nothing listening.
func closedUDPAddr(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	return addr
}

// stubResolvectl puts a fake resolvectl on PATH that records calls and
// reports current as Current DNS Server. The optional servers form the DNS
// Servers line (default 10.0.0.1 10.0.0.2).
func stubResolvectl(t *testing.T, current string, servers ...string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	list := "10.0.0.1 10.0.0.2"
	if len(servers) > 0 {
		list = strings.Join(servers, " ")
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"if [ \"$2\" = status ]; then\n" +
		"  printf 'Current DNS Server: " + current + "\\n       DNS Servers: " + list + "\\n'\n" +
		"fi\n"
	if err := os.WriteFile(filepath.Join(dir, "resolvectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useSearchDir(t, dir)
	return logPath
}

// stubNmcli puts a fake nmcli on PATH that reports servers as IP4.DNS.
func stubNmcli(t *testing.T, servers ...string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n"
	for i, s := range servers {
		script += fmt.Sprintf("printf 'IP4.DNS[%d]:%s\\n'\n", i+1, s)
	}
	script += "exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "nmcli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useSearchDir(t, dir)
}

// stubNmcliFailing makes DHCP discovery fall through to networkd/dhclient.
func stubNmcliFailing(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nmcli"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	useSearchDir(t, dir)
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func hasCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

func decisionState(servers []string, incumbent string, samples map[string][]float64) *state {
	return &state{
		iface:     "eno1",
		servers:   servers,
		incumbent: incumbent,
		samples:   samples,
		roundMeds: map[string]float64{},
		roundOK:   map[string]bool{},
		cfg: Config{
			Domains: []string{"example.com"},
			Timeout: 200 * time.Millisecond,
			Margin:  0.10,
		},
	}
}

func TestDecideSwitchesWhenCandidateConfirmed(t *testing.T) {
	candidate := startTestDNS(t)
	const incumbent = "192.0.2.1"
	logPath := stubResolvectl(t, candidate)
	st := decisionState([]string{incumbent, candidate}, incumbent, map[string][]float64{
		incumbent: {50, 50, 50, 50},
		candidate: {1, 1, 1, 1},
	})

	st.decide(12)

	if st.incumbent != candidate {
		t.Fatalf("incumbent = %q, want candidate %q", st.incumbent, candidate)
	}
	if len(st.servers) == 0 || st.servers[0] != candidate {
		t.Fatalf("servers = %v, want candidate first", st.servers)
	}
	if st.lastErr != "" {
		t.Fatalf("lastErr = %q, want empty", st.lastErr)
	}
	if !strings.Contains(st.lastSw, "primary 192.0.2.1 -> candidate "+candidate) {
		t.Fatalf("lastSw = %q, want confirmed switch", st.lastSw)
	}
	calls := readCalls(t, logPath)
	if !hasCall(calls, "dns eno1 "+candidate) {
		t.Fatalf("calls = %v, want narrow to candidate", calls)
	}
	if !hasCall(calls, "dns eno1 "+candidate+" "+incumbent) {
		t.Fatalf("calls = %v, want full restore", calls)
	}
}

func TestDecideSkipsUnreachableCandidate(t *testing.T) {
	candidate := closedUDPAddr(t)
	const incumbent = "192.0.2.1"
	logPath := stubResolvectl(t, candidate)
	st := decisionState([]string{incumbent, candidate}, incumbent, map[string][]float64{
		incumbent: {50, 50, 50, 50},
		candidate: {1, 1, 1, 1},
	})

	st.decide(12)

	if !strings.Contains(st.lastErr, "unreachable") {
		t.Fatalf("lastErr = %q, want unreachable", st.lastErr)
	}
	if st.incumbent != incumbent {
		t.Fatalf("incumbent = %q, want unchanged %q", st.incumbent, incumbent)
	}
	if calls := readCalls(t, logPath); len(calls) != 0 {
		t.Fatalf("calls = %v, want no resolvectl write for a dead candidate", calls)
	}
}

// Dry-run must not claim a switch to a candidate that a real run would
// reject for being unreachable.
func TestDecideDryRunStillProbes(t *testing.T) {
	candidate := closedUDPAddr(t)
	const incumbent = "192.0.2.1"
	logPath := stubResolvectl(t, candidate)
	st := decisionState([]string{incumbent, candidate}, incumbent, map[string][]float64{
		incumbent: {50, 50, 50, 50},
		candidate: {1, 1, 1, 1},
	})
	st.cfg.DryRun = true

	st.decide(12)

	if !strings.Contains(st.lastErr, "unreachable") {
		t.Fatalf("lastErr = %q, want unreachable", st.lastErr)
	}
	if st.lastSw != "" {
		t.Fatalf("lastSw = %q, want no would-switch message", st.lastSw)
	}
	if calls := readCalls(t, logPath); len(calls) != 0 {
		t.Fatalf("calls = %v, want no resolvectl write", calls)
	}
}

func TestDecideKeepsIncumbentWhenSwitchNotConfirmed(t *testing.T) {
	candidate := startTestDNS(t)
	const incumbent = "192.0.2.1"
	logPath := stubResolvectl(t, incumbent)
	st := decisionState([]string{incumbent, candidate}, incumbent, map[string][]float64{
		incumbent: {50, 50, 50, 50},
		candidate: {1, 1, 1, 1},
	})

	st.decide(12)

	if !strings.Contains(st.lastErr, "not confirmed") {
		t.Fatalf("lastErr = %q, want not confirmed", st.lastErr)
	}
	if st.incumbent != incumbent {
		t.Fatalf("incumbent = %q, want unchanged %q", st.incumbent, incumbent)
	}
	if len(st.servers) == 0 || st.servers[0] != candidate {
		t.Fatalf("servers = %v, want restored order with candidate first", st.servers)
	}
	if !hasCall(readCalls(t, logPath), "dns eno1 "+candidate+" "+incumbent) {
		t.Fatal("want full restore after failed adoption")
	}
}

// A failed SetOrder must be recorded, with the incumbent kept and no
// dry-run success message.
func TestDecideRecordsSwitchError(t *testing.T) {
	candidate := startTestDNS(t)
	const incumbent = "192.0.2.1"
	logPath := stubResolvectlDNSError(t, candidate)
	st := decisionState([]string{incumbent, candidate}, incumbent, map[string][]float64{
		incumbent: {50, 50, 50, 50},
		candidate: {1, 1, 1, 1},
	})

	st.decide(12)

	if !strings.Contains(st.lastErr, "resolvectl dns") {
		t.Fatalf("lastErr = %q, want resolvectl failure", st.lastErr)
	}
	if st.incumbent != incumbent {
		t.Fatalf("incumbent = %q, want unchanged %q", st.incumbent, incumbent)
	}
	if st.lastSw != "" {
		t.Fatalf("lastSw = %q, want empty", st.lastSw)
	}
	if !hasCall(readCalls(t, logPath), "dns eno1 "+candidate) {
		t.Fatal("want an attempted narrow to the candidate")
	}
}

// stubResolvectlDNSError reports status normally but fails every dns write.
func stubResolvectlDNSError(t *testing.T, current string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"if [ \"$2\" = status ]; then\n" +
		"  printf 'Current DNS Server: " + current + "\\n       DNS Servers: " + current + "\\n'\n" +
		"elif [ \"$1\" = dns ]; then\n" +
		"  exit 1\n" +
		"fi\n"
	if err := os.WriteFile(filepath.Join(dir, "resolvectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useSearchDir(t, dir)
	return logPath
}

// A host with one configured server must never narrow or rewrite it, even
// when that server is unhealthy.
func TestDecideSingleDownServerLeftAlone(t *testing.T) {
	const server = "192.0.2.1"
	logPath := stubResolvectl(t, server, server)
	st := decisionState([]string{server}, server, map[string][]float64{})

	st.decide(12)

	if !strings.Contains(st.lastErr, "all servers unhealthy") {
		t.Fatalf("lastErr = %q, want all servers unhealthy", st.lastErr)
	}
	if st.incumbent != server {
		t.Fatalf("incumbent = %q, want %q", st.incumbent, server)
	}
	if calls := readCalls(t, logPath); len(calls) != 0 {
		t.Fatalf("calls = %v, want no resolvectl write for the only server", calls)
	}
}

func tickState(iface string, domains []string) *state {
	return &state{
		samples:   map[string][]float64{},
		roundMeds: map[string]float64{},
		roundOK:   map[string]bool{},
		cfg: Config{
			Iface: iface, Domains: domains, Samples: 1,
			Timeout: 200 * time.Millisecond, Margin: 0.10,
		},
	}
}

func TestTickMeasuresRound(t *testing.T) {
	const iface = "nspulse-test0"
	server := startTestDNS(t)
	stubResolvectl(t, server, server)
	stubNmcli(t, "127.0.0.1")

	st := tickState(iface, []string{"example.com"})
	st.tick(99)

	if st.round != 1 {
		t.Fatalf("round = %d, want 1", st.round)
	}
	if st.iface != iface {
		t.Fatalf("iface = %q, want %q", st.iface, iface)
	}
	if len(st.measured) != 1 || st.measured[0] != server {
		t.Fatalf("measured = %v, want [%s]", st.measured, server)
	}
	if got := st.samples[server]; len(got) != 1 {
		t.Fatalf("samples[%s] = %v, want 1 sample", server, got)
	}
	if st.measuredAt.IsZero() {
		t.Fatal("measuredAt is zero after a round")
	}
}

// Static/resolv-only links must still be measured when DHCP discovery fails.
func TestTickFallsBackToResolvedWhenDHCPFails(t *testing.T) {
	server := startTestDNS(t)
	stubResolvectl(t, server, server)
	stubNmcliFailing(t)

	st := tickState("nspulse-test1", []string{"example.com"})
	st.tick(99)

	if len(st.measured) != 1 || st.measured[0] != server {
		t.Fatalf("measured = %v, want resolved fallback [%s]", st.measured, server)
	}
	if got := st.samples[server]; len(got) != 1 {
		t.Fatalf("samples[%s] = %v, want 1 sample", server, got)
	}
	if st.lastErr != "" {
		t.Fatalf("lastErr = %q, want empty", st.lastErr)
	}
}

func TestTickResetsHourOnIfaceChange(t *testing.T) {
	const iface = "nspulse-test2"
	server := startTestDNS(t)
	stubResolvectl(t, server, server)
	stubNmcli(t, "127.0.0.1")

	st := tickState(iface, []string{"example.com"})
	st.iface = "old0"
	st.round = 3
	st.samples = map[string][]float64{"old0": {1, 2}}
	st.measured = []string{"old0"}

	st.tick(99)

	if st.round != 1 {
		t.Fatalf("round = %d, want 1 after reset", st.round)
	}
	if _, ok := st.samples["old0"]; ok {
		t.Fatal("old interface samples survived the reset")
	}
	if got := st.samples[server]; len(got) != 1 {
		t.Fatalf("samples[%s] = %v, want 1 sample", server, got)
	}
}

func TestTickResetsHourOnServerSetChange(t *testing.T) {
	const iface = "nspulse-test3"
	server := startTestDNS(t)
	stubResolvectl(t, server, server)
	stubNmcli(t, "127.0.0.1")

	st := tickState(iface, []string{"example.com"})
	st.iface = iface
	st.round = 3
	st.samples = map[string][]float64{"old": {1, 2}}
	st.measured = []string{"old"}

	st.tick(99)

	if st.round != 1 {
		t.Fatalf("round = %d, want 1 after reset", st.round)
	}
	if _, ok := st.samples["old"]; ok {
		t.Fatal("old server samples survived the reset")
	}
	if len(st.measured) != 1 || st.measured[0] != server {
		t.Fatalf("measured = %v, want [%s]", st.measured, server)
	}
}

// The last round of the window must hand off to decide and start a fresh hour.
func TestTickDecidesAtHourEnd(t *testing.T) {
	server := startTestDNS(t)
	stubResolvectl(t, server, server)
	stubNmcli(t, "127.0.0.1")

	// An invalid name makes every query fail, so decide sees no healthy
	// candidate and records the all-unhealthy error.
	st := tickState("nspulse-test4", []string{""})
	st.round = 9
	st.measured = []string{server}

	st.tick(10)

	if st.round != 0 {
		t.Fatalf("round = %d, want 0 after the hourly decision", st.round)
	}
	if !strings.Contains(st.lastErr, "all servers unhealthy") {
		t.Fatalf("lastErr = %q, want hourly decision to run", st.lastErr)
	}
}
