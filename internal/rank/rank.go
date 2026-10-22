// Package rank picks the hourly DNS candidate from response-time samples.
// The candidate has the lowest hourly median; p95 breaks near-ties. A
// hysteresis margin guards against flapping between servers whose medians
// are close.
package rank

import "sort"

// Stats summarizes one server's successful samples (milliseconds).
type Stats struct {
	Samples int
	Median  float64
	P95     float64
}

// tieEpsilonMs: medians within this are considered tied, p95 decides.
const tieEpsilonMs = 0.05

// Median returns the median of v (0 when empty). It sorts a copy.
func Median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	cp := append([]float64(nil), v...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// percentile returns the p-th percentile (p in 0..100) via nearest-rank.
func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	cp := append([]float64(nil), v...)
	sort.Float64s(cp)
	k := (int(p)*len(cp) + 99) / 100 // ceil(p/100 * n), nearest-rank
	if k < 1 {
		k = 1
	}
	if k > len(cp) {
		k = len(cp)
	}
	return cp[k-1]
}

// Summarize computes per-server stats over successful samples only.
func Summarize(samples map[string][]float64) map[string]Stats {
	out := make(map[string]Stats, len(samples))
	for srv, v := range samples {
		out[srv] = Stats{
			Samples: len(v),
			Median:  Median(v),
			P95:     percentile(v, 95),
		}
	}
	return out
}

// Decide picks the hourly candidate and reports whether the daemon should
// switch away from the incumbent.
//
//   - Servers with zero successful samples are excluded. If none are
//     healthy, it returns ("", false).
//   - The candidate has the lowest median; medians within tieEpsilonMs fall
//     back to lowest p95.
//   - Exact ties (median and p95 equal) prefer the incumbent: there is no
//     evidence to switch on. Ties among non-incumbents resolve to the
//     lexicographically smallest server, so results are deterministic
//     regardless of map iteration order.
//   - A candidate only takes over from the incumbent if its median beats the
//     incumbent's by more than margin (fraction, e.g. 0.10). An incumbent
//     with no healthy samples always loses to a healthy candidate.
//
// Decide works on partial-hour data too, so the CLI can show a live
// "probable candidate".
func Decide(samples map[string][]float64, incumbent string, margin float64) (candidate string, wouldSwitch bool) {
	stats := Summarize(samples)
	best, bestStats := "", Stats{}
	for srv, st := range stats {
		if st.Samples == 0 {
			continue
		}
		switch {
		case best == "":
			best, bestStats = srv, st
		case lower(st, bestStats):
			best, bestStats = srv, st
		case !lower(bestStats, st) && (srv == incumbent || (best != incumbent && srv < best)):
			// Exact tie: prefer the incumbent, else the smallest name.
			best, bestStats = srv, st
		}
	}
	if best == "" {
		return "", false
	}
	if best == incumbent {
		return best, false
	}
	inc, ok := stats[incumbent]
	if !ok || inc.Samples == 0 {
		return best, true
	}
	if bestStats.Median < inc.Median*(1-margin) {
		return best, true
	}
	return best, false
}

// lower reports whether a beats b under median-then-p95 ordering.
func lower(a, b Stats) bool {
	if d := a.Median - b.Median; d < -tieEpsilonMs {
		return true
	} else if d > tieEpsilonMs {
		return false
	}
	return a.P95 < b.P95
}
