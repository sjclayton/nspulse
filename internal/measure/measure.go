// Package measure probes DNS servers directly with real A queries and
// records per-query response times (not ICMP ping).
//
// The DNS client is stdlib-only: it builds one A-query packet, sends it over
// UDP with a deadline, and times any well-formed reply carrying a matching
// transaction ID. No external DNS library, so crypto/tls and friends never
// enter the binary.
package measure

import (
	"encoding/binary"
	"errors"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"
)

// Result holds one round's samples for one server (milliseconds; empty
// round = server down or fully timed out).
type Result struct {
	Server  string
	Samples []float64
	Err     error // non-nil only when every query failed
}

// Round queries each server samples times for the configured names (cycled
// across queries) and returns one Result per server. Queries run
// concurrently across servers; failures record nothing.
func Round(servers []string, names []string, samples int, timeout time.Duration) []Result {
	var wg sync.WaitGroup
	out := make([]Result, len(servers))
	for i, srv := range servers {
		wg.Add(1)
		go func(i int, srv string) {
			defer wg.Done()
			out[i] = probe(srv, names, samples, timeout)
		}(i, srv)
	}
	wg.Wait()
	return out
}

func probe(server string, names []string, samples int, timeout time.Duration) Result {
	addr := serverAddr(server)
	res := Result{Server: server}
	for i := 0; i < samples; i++ {
		if rtt, err := query(addr, names[i%len(names)], timeout); err == nil {
			res.Samples = append(res.Samples, rtt)
		}
	}
	if len(res.Samples) == 0 {
		res.Err = errAllFailed
	}
	return res
}

// Probe sends one A query for name to server and reports whether a matching
// reply arrived within timeout. Used to check a candidate is still
// contactable right before switching to it.
func Probe(server, name string, timeout time.Duration) error {
	_, err := query(serverAddr(server), name, timeout)
	return err
}

// serverAddr normalizes a server entry to host:port for dialing. Bare IPs
// (v4 or v6) get port 53 via JoinHostPort, which handles IPv6 bracketing
// ("2001:db8::1" -> "[2001:db8::1]:53"); host:port values pass through.
func serverAddr(server string) string {
	if _, _, err := net.SplitHostPort(server); err != nil {
		return net.JoinHostPort(server, "53")
	}
	return server
}

// query sends one A query for name to addr and returns the RTT in
// milliseconds. Any UDP reply with a matching ID and the QR bit set counts
// as success, including NXDOMAIN/SERVFAIL: the server answered, so it is up
// and its response time is what we are measuring.
func query(addr, name string, timeout time.Duration) (float64, error) {
	id := uint16(rand.Intn(65536))
	req, err := buildQuery(id, name)
	if err != nil {
		return 0, err
	}
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	start := time.Now()
	_ = conn.SetDeadline(start.Add(timeout))
	if _, err := conn.Write(req); err != nil {
		return 0, err
	}
	buf := make([]byte, 512)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return 0, err
		}
		if n >= 4 && binary.BigEndian.Uint16(buf[0:2]) == id && buf[2]&0x80 != 0 {
			return float64(time.Since(start).Microseconds()) / 1000.0, nil
		}
		// Stray packet (not our ID): keep reading until the deadline.
	}
}

// buildQuery encodes a minimal recursive A-query packet (RFC 1035 §4.1).
func buildQuery(id uint16, name string) ([]byte, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" || len(name) > 253 {
		return nil, errors.New("bad query name")
	}
	pkt := make([]byte, 0, 32+len(name))
	pkt = append(pkt, byte(id>>8), byte(id), 0x01, 0x00)              // RD=1
	pkt = append(pkt, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00) // QD=1
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, errors.New("bad query label")
		}
		pkt = append(pkt, byte(len(label)))
		pkt = append(pkt, label...)
	}
	pkt = append(pkt, 0x00, 0x00, 0x01, 0x00, 0x01) // root, QTYPE=A, QCLASS=IN
	return pkt, nil
}

type allFailed struct{}

func (allFailed) Error() string { return "all queries failed" }

var errAllFailed error = allFailed{}
