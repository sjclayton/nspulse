package rank

import "testing"

func TestDecideLowestMedianWins(t *testing.T) {
	samples := map[string][]float64{
		"a": {10, 11, 10, 12},
		"b": {2, 3, 2, 3},
	}
	c, sw := Decide(samples, "a", 0.10)
	if c != "b" || !sw {
		t.Fatalf("got (%q,%v), want (b,true)", c, sw)
	}
}

func TestDecideIncumbentKeepsOnTie(t *testing.T) {
	samples := map[string][]float64{
		"a": {5, 5, 5, 5},
		"b": {5, 5, 5, 5},
	}
	c, sw := Decide(samples, "a", 0.10)
	if c != "a" || sw {
		t.Fatalf("got (%q,%v), want (a,false)", c, sw)
	}
}

func TestDecideP95BreaksNearTie(t *testing.T) {
	// Same median (5), but a's tail is worse: two outliers each so the
	// nearest-rank p95 (index 18 of 20) lands on 50 vs 6.
	a := []float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 50, 50}
	b := []float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 6, 6}
	samples := map[string][]float64{"a": a, "b": b}
	c, sw := Decide(samples, "a", 0.0)
	if c != "b" {
		t.Fatalf("got candidate %q, want b (p95 tie-break)", c)
	}
	_ = sw
}

// Nearest-rank p95 of four values is the maximum, not the third smallest.
func TestPercentileSmallSample(t *testing.T) {
	if got := percentile([]float64{1, 2, 3, 100}, 95); got != 100 {
		t.Fatalf("p95 = %v, want 100", got)
	}
}

func TestDecideExactTiePrefersIncumbent(t *testing.T) {
	samples := map[string][]float64{
		"a": {5, 5, 5, 5},
		"b": {5, 5, 5, 5},
	}
	// Regardless of map iteration order, the incumbent must stay on an exact tie.
	for i := 0; i < 50; i++ {
		c, sw := Decide(samples, "b", 0.0)
		if c != "b" || sw {
			t.Fatalf("iter %d: got (%q,%v), want (b,false)", i, c, sw)
		}
	}
}

func TestDecideMarginPreventsFlap(t *testing.T) {
	samples := map[string][]float64{
		"a": {100, 100, 100, 100}, // incumbent
		"b": {95, 95, 95, 95},     // only 5% better, margin is 10%
	}
	c, sw := Decide(samples, "a", 0.10)
	if c != "b" || sw {
		t.Fatalf("got (%q,%v), want (b,false): 5%% gain must not switch", c, sw)
	}
}

func TestDecideMarginAllowsClearWin(t *testing.T) {
	samples := map[string][]float64{
		"a": {100, 100, 100, 100},
		"b": {50, 50, 50, 50},
	}
	c, sw := Decide(samples, "a", 0.10)
	if c != "b" || !sw {
		t.Fatalf("got (%q,%v), want (b,true)", c, sw)
	}
}

func TestDecideAllFailing(t *testing.T) {
	samples := map[string][]float64{"a": {}, "b": {}}
	c, sw := Decide(samples, "a", 0.10)
	if c != "" || sw {
		t.Fatalf("got (%q,%v), want (\"\",false)", c, sw)
	}
}

func TestDecideDownIncumbentLoses(t *testing.T) {
	samples := map[string][]float64{"a": {}, "b": {30, 31, 30}}
	c, sw := Decide(samples, "a", 0.10)
	if c != "b" || !sw {
		t.Fatalf("got (%q,%v), want (b,true)", c, sw)
	}
}

func TestDecideAlreadyIncumbentIsNoop(t *testing.T) {
	samples := map[string][]float64{
		"a": {1, 1, 1},
		"b": {99, 99, 99},
	}
	c, sw := Decide(samples, "a", 0.10)
	if c != "a" || sw {
		t.Fatalf("got (%q,%v), want (a,false)", c, sw)
	}
}

func TestMedianEvenCount(t *testing.T) {
	if got := Median([]float64{1, 3}); got != 2 {
		t.Fatalf("got %v, want 2", got)
	}
	if got := Median(nil); got != 0 {
		t.Fatalf("got %v, want 0", got)
	}
}
