// Package daemon runs the monitoring/switching loop. It is the only code
// path that calls resolvectl to change server order; the CLI only reads.
package daemon

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shaun/nspulse/internal/discover"
	"github.com/shaun/nspulse/internal/ipc"
	"github.com/shaun/nspulse/internal/measure"
	"github.com/shaun/nspulse/internal/rank"
	"github.com/shaun/nspulse/internal/resolved"
)

// Config holds daemon flags; there is deliberately no config file.
type Config struct {
	Interval time.Duration
	Window   time.Duration
	Samples  int
	Timeout  time.Duration
	Domains  []string
	Socket   string
	Iface    string // "auto" = default route
	Margin   float64
	DryRun   bool
}

// state is the in-memory measurement hour. Nothing is persisted; a restart
// starts a fresh hour.
type state struct {
	mu        sync.RWMutex
	iface     string
	servers   []string // current resolved order, primary first
	measured  []string // last round's measured set (DHCP ∩ resolved order)
	dhcp      []string
	confirmed map[string]bool
	samples   map[string][]float64
	roundMeds map[string]float64
	roundOK   map[string]bool
	round     int
	lastSw    string
	lastErr   string
	cfg       Config
}

// Run parses flags and blocks until SIGINT/SIGTERM.
func Run(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	interval := fs.Duration("interval", 5*time.Minute, "time between measurement rounds")
	window := fs.Duration("window", time.Hour, "measurement window before switching decision")
	samples := fs.Int("samples", 4, "queries per server per round")
	timeout := fs.Duration("timeout", 2*time.Second, "per-query timeout")
	domains := fs.String("domains", "google.com,cloudflare.com", "comma-separated A-query names, cycled")
	socket := fs.String("socket", "/run/nspulse.sock", "Unix socket for status/watch clients")
	iface := fs.String("iface", "auto", "interface to manage (auto = default route)")
	margin := fs.Float64("margin", 0.10, "challenger must beat incumbent median by this fraction to switch")
	dryRun := fs.Bool("dry-run", false, "log switching decisions without calling resolvectl")
	if err := fs.Parse(args); err != nil {
		return err
	}

	roundsPerHour := int((*window) / (*interval))
	if roundsPerHour < 2 {
		return fmt.Errorf("window/interval gives %d rounds/hour, need >= 2", roundsPerHour)
	}
	if *samples < 1 {
		return fmt.Errorf("samples must be >= 1")
	}
	doms := splitList(*domains)
	if len(doms) == 0 {
		return fmt.Errorf("domains must not be empty")
	}

	cfg := Config{
		Interval: *interval, Window: *window, Samples: *samples,
		Timeout: *timeout, Domains: doms, Socket: *socket,
		Iface: *iface, Margin: *margin, DryRun: *dryRun,
	}
	st := &state{cfg: cfg, samples: map[string][]float64{}, roundMeds: map[string]float64{}, roundOK: map[string]bool{}}

	go func() {
		if err := ipc.Serve(cfg.Socket, st.snapshot); err != nil {
			log.Printf("ipc serve: %v", err)
		}
	}()

	// Immediate first round, then ticker.
	st.tick(roundsPerHour)
	tick := time.NewTicker(cfg.Interval)
	defer tick.Stop()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-tick.C:
			st.tick(roundsPerHour)
		case s := <-sig:
			log.Printf("received %v, exiting (in-memory hour discarded)", s)
			return nil
		}
	}
}

// tick performs one measurement round and, at hour end, a switch decision.
func (st *state) tick(roundsPerHour int) {
	cfg := st.cfg

	iface := cfg.Iface
	if iface == "auto" {
		var err error
		iface, err = discover.DefaultIface()
		if err != nil {
			st.setErr(fmt.Sprintf("default route: %v", err))
			return
		}
	}

	// Detect interface change: fresh hour.
	st.mu.RLock()
	prevIface := st.iface
	st.mu.RUnlock()
	if prevIface != "" && prevIface != iface {
		log.Printf("active interface changed %s -> %s, resetting hour", prevIface, iface)
		st.reset()
	}

	ordered, err := discover.OrderedServers(iface)
	if err != nil {
		st.setErr(fmt.Sprintf("discover order: %v", err))
		return
	}
	dhcp, err := discover.DHCPServers(iface)
	if err != nil {
		// DHCP list unavailable (e.g. static config): fall back to
		// resolved's set so a single static server still gets measured.
		log.Printf("dhcp discovery failed (%v), using resolved set", err)
		dhcp = ordered
	}

	// The measured set is the DHCP set intersected with resolved's set,
	// so we never invent servers. (In practice they are identical.)
	set := intersectKeepOrder(ordered, dhcp)
	if len(set) == 0 {
		st.setErr("no servers in common between resolved and DHCP")
		return
	}

	// Server set change (DHCP renewal): fresh hour. Compare against the
	// last measured set (DHCP ∩ resolved), not the full resolved order,
	// so plain reordering never triggers a reset.
	st.mu.RLock()
	prevSet := append([]string(nil), st.measured...)
	havePrev := st.round > 0 || len(st.samples) > 0
	st.mu.RUnlock()
	if havePrev && !discover.SameSet(prevSet, set) {
		log.Printf("server set changed %v -> %v, resetting hour", prevSet, set)
		st.reset()
	}

	results := measure.Round(set, cfg.Domains, cfg.Samples, cfg.Timeout)

	st.mu.Lock()
	st.iface = iface
	st.servers = ordered
	st.measured = set
	st.dhcp = dhcp
	st.confirmed = map[string]bool{}
	for _, s := range set {
		st.confirmed[s] = true
		st.samples[s] = append(st.samples[s], samplesOf(results, s)...)
	}
	st.roundMeds = map[string]float64{}
	st.roundOK = map[string]bool{}
	for _, r := range results {
		if len(r.Samples) > 0 {
			st.roundMeds[r.Server] = rank.Median(r.Samples)
			st.roundOK[r.Server] = true
		}
	}
	st.round++
	st.lastErr = ""
	if st.round >= roundsPerHour {
		st.mu.Unlock()
		st.decide(roundsPerHour)
		return
	}
	st.mu.Unlock()

	for _, r := range results {
		if len(r.Samples) == 0 {
			log.Printf("[%s] %s: all %d queries failed", iface, r.Server, cfg.Samples)
		} else {
			log.Printf("[%s] %s: %d/%d ok, round median %.2fms", iface, r.Server, len(r.Samples), cfg.Samples, rank.Median(r.Samples))
		}
	}
}

// decide picks the hourly winner and switches if needed, then resets.
func (st *state) decide(roundsPerHour int) {
	st.mu.RLock()
	samples := copySamples(st.samples)
	servers := append([]string(nil), st.servers...)
	iface := st.iface
	cfg := st.cfg
	st.mu.RUnlock()

	incumbent := ""
	if len(servers) > 0 {
		incumbent = servers[0]
	}
	candidate, wouldSwitch := rank.Decide(samples, incumbent, cfg.Margin)

	stats := rank.Summarize(samples)
	log.Printf("hour complete on %s (%d rounds): incumbent=%s candidate=%s", iface, roundsPerHour, incumbent, candidate)
	for _, srv := range servers {
		s := stats[srv]
		log.Printf("  %s: n=%d median=%.2fms p95=%.2fms", srv, s.Samples, s.Median, s.P95)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if candidate == "" {
		st.lastErr = "all servers unhealthy this hour, no switch"
		log.Print("nspulse: " + st.lastErr)
	} else if !wouldSwitch {
		log.Printf("nspulse: keeping %s (candidate %s within margin or already primary)", incumbent, candidate)
	} else if cfg.DryRun {
		st.lastSw = fmt.Sprintf("dry-run: would switch %s -> %s", incumbent, candidate)
		log.Printf("nspulse: %s", st.lastSw)
	} else if err := resolved.SetOrder(iface, servers, candidate); err != nil {
		st.lastErr = err.Error()
		log.Printf("nspulse: switch failed: %v", err)
	} else {
		st.lastSw = fmt.Sprintf("%s: primary %s -> %s at %s", iface, incumbent, candidate, time.Now().Format(time.RFC3339))
		log.Printf("nspulse: %s", st.lastSw)
		// Update local view of order so WouldSwitch reads correctly
		// until the next tick refreshes from resolvectl.
		st.servers = reorderFirst(servers, candidate)
	}
	st.round = 0
	st.samples = map[string][]float64{}
}

// snapshot builds the read-only status payload for CLI clients.
func (st *state) snapshot() ipc.Status {
	st.mu.RLock()
	defer st.mu.RUnlock()
	stats := rank.Summarize(st.samples)
	per := map[string]ipc.ServerStats{}
	for srv, s := range stats {
		per[srv] = ipc.ServerStats{
			Samples:           s.Samples,
			Failures:          st.cfg.Samples*st.round - s.Samples,
			MedianMs:          s.Median,
			P95Ms:             s.P95,
			LastRoundMedianMs: st.roundMeds[srv],
			LastRoundOK:       st.roundOK[srv],
		}
	}
	incumbent := ""
	if len(st.servers) > 0 {
		incumbent = st.servers[0]
	}
	candidate, wouldSwitch := rank.Decide(st.samples, incumbent, st.cfg.Margin)
	return ipc.Status{
		Time: time.Now(), Iface: st.iface, Servers: st.servers,
		Primary: incumbent, DHCPConfirmed: st.confirmed,
		Round: st.round, RoundsPerHour: int(st.cfg.Window / st.cfg.Interval),
		Interval: st.cfg.Interval.String(), PerServer: per,
		ProbableCandidate: candidate, WouldSwitch: wouldSwitch,
		LastSwitch: st.lastSw, LastError: st.lastErr, DryRun: st.cfg.DryRun,
	}
}

func (st *state) setErr(msg string) {
	st.mu.Lock()
	st.lastErr = msg
	st.mu.Unlock()
	log.Printf("nspulse: %s", msg)
}

func (st *state) reset() {
	st.mu.Lock()
	st.round = 0
	st.samples = map[string][]float64{}
	st.mu.Unlock()
}

func samplesOf(results []measure.Result, server string) []float64 {
	for _, r := range results {
		if r.Server == server {
			return r.Samples
		}
	}
	return nil
}

func copySamples(in map[string][]float64) map[string][]float64 {
	out := make(map[string][]float64, len(in))
	for k, v := range in {
		out[k] = append([]float64(nil), v...)
	}
	return out
}

// intersectKeepOrder returns ordered filtered to members of set.
func intersectKeepOrder(ordered, set []string) []string {
	in := map[string]bool{}
	for _, s := range set {
		in[s] = true
	}
	var out []string
	for _, s := range ordered {
		if in[s] {
			out = append(out, s)
		}
	}
	return out
}

func reorderFirst(servers []string, first string) []string {
	out := []string{first}
	for _, s := range servers {
		if s != first {
			out = append(out, s)
		}
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
