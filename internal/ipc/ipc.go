// Package ipc defines the status payload the daemon publishes on its Unix
// socket and the helpers both sides use to serve/fetch it.
package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// ServerStats is the current-hour measurement summary for one DNS server.
type ServerStats struct {
	Samples           int     `json:"samples"`
	Failures          int     `json:"failures"`
	MedianMs          float64 `json:"median_ms"`
	P95Ms             float64 `json:"p95_ms"`
	LastRoundMedianMs float64 `json:"last_round_median_ms"`
	LastRoundOK       bool    `json:"last_round_ok"`
}

// Status is the full snapshot served by the daemon. Only the daemon writes;
// status/watch only read. Time is when the last measurement round
// completed (zero before round 1), so status shows round time, not
// fetch time.
type Status struct {
	Time              time.Time              `json:"time"`
	Iface             string                 `json:"iface"`
	Servers           []string               `json:"servers"`
	Incumbent         string                 `json:"incumbent"`
	Round             int                    `json:"round"`
	RoundsPerHour     int                    `json:"rounds_per_hour"`
	Interval          string                 `json:"interval"`
	PerServer         map[string]ServerStats `json:"per_server"`
	ProbableCandidate string                 `json:"probable_candidate"`
	WouldSwitch       bool                   `json:"would_switch"`
	LastSwitch        string                 `json:"last_switch,omitempty"`
	LastError         string                 `json:"last_error,omitempty"`
	DryRun            bool                   `json:"dry_run"`
}

// Serve listens on a Unix socket at path and writes the current snapshot to
// each client that connects. It blocks forever. The socket is chmodded 0666
// so unprivileged status/watch clients can connect (connecting requires write
// permission on the socket file).
func Serve(path string, snapshot func() Status) error {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", path, err)
	}
	defer l.Close()
	if err := os.Chmod(path, 0666); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		_ = json.NewEncoder(c).Encode(snapshot())
		c.Close()
	}
}

// Fetch connects to the daemon socket and decodes one Status snapshot.
func Fetch(path string) (Status, error) {
	var st Status
	c, err := net.Dial("unix", path)
	if err != nil {
		return st, fmt.Errorf("dial %s: %w (is the daemon running?)", path, err)
	}
	defer c.Close()
	if err := json.NewDecoder(c).Decode(&st); err != nil {
		return st, fmt.Errorf("decode status: %w", err)
	}
	return st, nil
}
