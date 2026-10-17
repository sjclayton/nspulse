// Package cli implements the read-only status and watch commands. These
// never switch DNS order; only the daemon does that.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sjclayton/nspulse/internal/ipc"
)

// Status prints one snapshot: current hour details + probable candidate.
func Status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	socket := fs.String("socket", "/run/nspulse.sock", "daemon socket path")
	asJSON := fs.Bool("json", false, "print raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := ipc.Fetch(*socket)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	printStatus(st)
	return nil
}

// Watch redraws the snapshot every few seconds until interrupted.
func Watch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	socket := fs.String("socket", "/run/nspulse.sock", "daemon socket path")
	every := fs.Duration("every", 5*time.Second, "refresh interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for {
		st, err := ipc.Fetch(*socket)
		if err != nil {
			return err
		}
		fmt.Print("\033[H\033[2J") // clear screen
		printStatus(st)
		fmt.Printf("\nrefresh every %v, Ctrl-C to quit\n", *every)
		time.Sleep(*every)
	}
}

// headerLine prints round progress plus when the round completed (not when
// status was fetched). Before round 1 there is no measurement yet.
func headerLine(st ipc.Status) string {
	at := "(no rounds yet)"
	if !st.Time.IsZero() {
		at = "at " + st.Time.Format("15:04:05")
	}
	return fmt.Sprintf("nspulse  iface=%s  round %d/%d (every %s)  %s",
		nonempty(st.Iface, "?"), st.Round, st.RoundsPerHour, st.Interval, at)
}

func printStatus(st ipc.Status) {
	fmt.Printf("%s\n", headerLine(st))
	fmt.Printf("order:   %s\n", strings.Join(markPrimary(st), "  "))
	for _, line := range tableLines(st) {
		fmt.Println(line)
	}
	fmt.Printf("probable candidate: %s", nonempty(st.ProbableCandidate, "(none: all unhealthy)"))
	if st.WouldSwitch {
		fmt.Printf("  -> WOULD SWITCH from %s (margin beaten)\n", st.Primary)
	} else {
		fmt.Printf("  (no switch needed)\n")
	}
	if st.LastSwitch != "" {
		fmt.Printf("last switch: %s\n", st.LastSwitch)
	}
	if st.LastError != "" {
		fmt.Printf("last error:  %s\n", st.LastError)
	}
	if st.DryRun {
		fmt.Printf("(daemon in dry-run mode: no actual switching)\n")
	}
}

// tableLines renders the per-server table with headings and data sharing
// one set of widths, computed from the longest value in each column, so
// numeric columns stay aligned regardless of IPv6 length or sample counts.
func tableLines(st ipc.Status) []string {
	type row struct {
		server, n, fail, median, p95, last, dhcp string
	}
	keys := make([]string, 0, len(st.PerServer))
	for k := range st.PerServer {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]row, 0, len(keys))
	for _, srv := range keys {
		s := st.PerServer[srv]
		last := fmt.Sprintf("%.2fms", s.LastRoundMedianMs)
		if !s.LastRoundOK {
			last = "down"
		}
		confirmed := "?"
		if st.DHCPConfirmed != nil {
			confirmed = "no"
			if st.DHCPConfirmed[srv] {
				confirmed = "yes"
			}
		}
		rows = append(rows, row{
			server: srv,
			n:      fmt.Sprintf("%d", s.Samples),
			fail:   fmt.Sprintf("%d", s.Failures),
			median: fmt.Sprintf("%.2fms", s.MedianMs),
			p95:    fmt.Sprintf("%.2fms", s.P95Ms),
			last:   last,
			dhcp:   confirmed,
		})
	}
	w := func(head string, col func(row) string) int {
		n := len(head)
		for _, r := range rows {
			if len(col(r)) > n {
				n = len(col(r))
			}
		}
		return n
	}
	ws, wn, wf, wm, wp, wl, wd := w("server", func(r row) string { return r.server }),
		w("n", func(r row) string { return r.n }),
		w("fail", func(r row) string { return r.fail }),
		w("median", func(r row) string { return r.median }),
		w("p95", func(r row) string { return r.p95 }),
		w("last-round", func(r row) string { return r.last }),
		w("dhcp?", func(r row) string { return r.dhcp })
	fmtRow := func(cols ...any) string {
		return fmt.Sprintf("%-*s  %*s  %*s  %*s  %*s  %*s  %*s",
			ws, cols[0], wn, cols[1], wf, cols[2], wm, cols[3],
			wp, cols[4], wl, cols[5], wd, cols[6])
	}
	out := []string{fmtRow("server", "n", "fail", "median", "p95", "last-round", "dhcp?")}
	for _, r := range rows {
		out = append(out, fmtRow(r.server, r.n, r.fail, r.median, r.p95, r.last, r.dhcp))
	}
	return out
}

// markPrimary prefixes the primary server with "*".
func markPrimary(st ipc.Status) []string {
	out := make([]string, len(st.Servers))
	for i, s := range st.Servers {
		if s == st.Primary {
			out[i] = "*" + s
		} else {
			out[i] = " " + s
		}
	}
	return out
}

func nonempty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
