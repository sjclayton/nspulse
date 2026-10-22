package measure

import (
	"net"
	"strings"
	"testing"
	"time"
)

// startTestServer serves minimal canned DNS replies on 127.0.0.1:0 and
// returns its host:port.
func startTestServer(t *testing.T) string {
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
			if err != nil || n < 2 {
				return
			}
			// Echo ID, set QR, copy question section back.
			resp := []byte{buf[0], buf[1], 0x81, 0x80, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}
			if n > 12 {
				resp = append(resp, buf[12:n]...)
			}
			// One A answer: pointer to QNAME, type A, class IN, TTL 60, 127.0.0.1.
			resp = append(resp, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04, 127, 0, 0, 1)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestRoundSuccess(t *testing.T) {
	addr := startTestServer(t)
	res := Round([]string{addr}, []string{"google.com", "cloudflare.com"}, 3, 2*time.Second)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if len(res[0].Samples) != 3 {
		t.Fatalf("want 3 samples, got %d (%+v)", len(res[0].Samples), res[0])
	}
	for _, s := range res[0].Samples {
		if s < 0 {
			t.Fatalf("negative sample %v", s)
		}
	}
}

func TestRoundAllFail(t *testing.T) {
	// Nothing listens on 127.0.0.2:53535; short timeout.
	res := Round([]string{"127.0.0.2:53535"}, []string{"google.com"}, 2, 200*time.Millisecond)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if len(res[0].Samples) != 0 {
		t.Fatalf("want all-failed result, got %+v", res[0])
	}
}

func TestServerAddr(t *testing.T) {
	cases := map[string]string{
		"192.168.1.20":      "192.168.1.20:53",
		"2001:db8::1":       "[2001:db8::1]:53",
		"::1":               "[::1]:53",
		"192.168.1.20:5353": "192.168.1.20:5353",
		"[::1]:5353":        "[::1]:5353",
	}
	for in, want := range cases {
		if got := serverAddr(in); got != want {
			t.Errorf("serverAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// startTestServerV6 serves canned DNS replies on [::1]:0. Skipped where
// the kernel has IPv6 disabled.
func startTestServerV6(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil || n < 2 {
				return
			}
			resp := []byte{buf[0], buf[1], 0x81, 0x80, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}
			if n > 12 {
				resp = append(resp, buf[12:n]...)
			}
			resp = append(resp, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04, 127, 0, 0, 1)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestRoundV6Success(t *testing.T) {
	addr := startTestServerV6(t) // "[::1]:port"
	res := Round([]string{addr}, []string{"google.com"}, 2, 2*time.Second)
	if len(res) != 1 || len(res[0].Samples) != 2 {
		t.Fatalf("want 2 v6 samples, got %+v", res)
	}
	// Bare v6 IP must also work (JoinHostPort bracketing, not ":53" suffix).
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	res = Round([]string{host}, []string{"google.com"}, 2, 2*time.Second)
	if len(res) != 1 || len(res[0].Samples) != 2 {
		t.Fatalf("want 2 bare-v6 samples, got %+v", res)
	}
}

func TestServerAddrHandlesAnnotationsAndZones(t *testing.T) {
	cases := map[string]string{
		"192.168.1.1":              "192.168.1.1:53",
		"192.168.1.1#dns.example":  "192.168.1.1:53",
		"2001:db8::1":              "[2001:db8::1]:53",
		"2001:db8::1#dns.example":  "[2001:db8::1]:53",
		"fe80::1%eno1":             "[fe80::1%eno1]:53",
		"1.1.1.1:9953#dns.example": "1.1.1.1:9953",
	}
	for in, want := range cases {
		if got := serverAddr(in); got != want {
			t.Errorf("serverAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildQueryShape(t *testing.T) {
	pkt, err := buildQuery(0x1234, "Google.COM.")
	if err != nil {
		t.Fatal(err)
	}
	if pkt[0] != 0x12 || pkt[1] != 0x34 || pkt[2] != 0x01 {
		t.Fatalf("bad header: % x", pkt[:4])
	}
	// "google" label must be lowercased, length-prefixed with 6.
	if !strings.Contains(string(pkt), "google") {
		t.Fatalf("qname not encoded: % x", pkt)
	}
	if _, err := buildQuery(1, ""); err == nil {
		t.Fatal("want error for empty name")
	}
	// Uppercase + trailing dot must normalize identically to lowercase.
	a, _ := buildQuery(7, "Example.com.")
	b, _ := buildQuery(7, "example.com")
	if string(a) != string(b) {
		t.Fatal("name normalization mismatch")
	}
}
