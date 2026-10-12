package cli

import (
	"strings"
	"testing"

	"github.com/shaun/nspulse/internal/ipc"
)

// fieldSpans returns (start, end) offsets of each whitespace-separated field.
func fieldSpans(s string) [][2]int {
	var spans [][2]int
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		j := strings.IndexByte(s[i:], ' ')
		if j < 0 {
			spans = append(spans, [2]int{i, len(s)})
			break
		}
		spans = append(spans, [2]int{i, i + j})
		i += j
	}
	return spans
}

// Headings and data must share column boundaries: the server column is
// left-aligned (values start where the heading starts), the rest are
// right-aligned (values end where the heading ends). Exercises IPv6-wide
// servers, multi-digit counts, and a "down" last-round value.
func TestTableAlignment(t *testing.T) {
	st := ipc.Status{
		Iface:   "eno1",
		Servers: []string{"2001:4860:4860::8888", "192.168.1.20"},
		Primary: "2001:4860:4860::8888",
		DHCPConfirmed: map[string]bool{
			"2001:4860:4860::8888": true,
			"192.168.1.20":         true,
		},
		PerServer: map[string]ipc.ServerStats{
			"2001:4860:4860::8888": {Samples: 128, Failures: 3, MedianMs: 12.34, P95Ms: 56.78, LastRoundMedianMs: 9.01, LastRoundOK: true},
			"192.168.1.20":         {Samples: 4, Failures: 0, MedianMs: 0.31, P95Ms: 0.36, LastRoundOK: false},
		},
	}
	lines := tableLines(st)
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d", len(lines))
	}
	headSpans := fieldSpans(lines[0])
	if len(headSpans) != 7 {
		t.Fatalf("want 7 heading fields, got %d: %q", len(headSpans), lines[0])
	}
	for i, line := range lines[1:] {
		rowSpans := fieldSpans(line)
		if len(rowSpans) != 7 {
			t.Fatalf("row %d: want 7 fields, got %d: %q", i, len(rowSpans), line)
		}
		if rowSpans[0][0] != headSpans[0][0] {
			t.Errorf("row %d server starts at %d, heading starts at %d\nhead: %q\nrow:  %q",
				i, rowSpans[0][0], headSpans[0][0], lines[0], line)
		}
		for c := 1; c < 7; c++ {
			if rowSpans[c][1] != headSpans[c][1] {
				t.Errorf("row %d col %d ends at %d, heading ends at %d\nhead: %q\nrow:  %q",
					i, c, rowSpans[c][1], headSpans[c][1], lines[0], line)
			}
		}
	}
}
