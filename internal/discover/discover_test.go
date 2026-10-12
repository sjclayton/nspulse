package discover

import (
	"reflect"
	"strings"
	"testing"
)

// Regression: resolved lists 192.168.1.20 first; order must survive intact.
func TestParseStatusOrderPreservesPrimary(t *testing.T) {
	out := `Link 2 (eno1)
    Current Scopes: DNS LLMNR/IPv4 LLMNR/IPv6 mDNS/IPv4 mDNS/IPv6
         Protocols: +DefaultRoute +LLMNR +mDNS -DNSOverTLS DNSSEC=no/unsupported
Current DNS Server: 192.168.1.20
       DNS Servers: 192.168.1.20 192.168.1.10
        DNS Domain: clayton-tech.ca
     Default Route: yes
`
	got, err := parseStatusOrder(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.20", "192.168.1.10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseStatusOrderWrappedLines(t *testing.T) {
	out := `Link 2 (eno1)
       DNS Servers: 192.168.1.20 192.168.1.10
                    192.168.1.30
        DNS Domain: example.com
`
	got, err := parseStatusOrder(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.20", "192.168.1.10", "192.168.1.30"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseStatusOrderMissing(t *testing.T) {
	if _, err := parseStatusOrder("Link 3 (wlan0)\n     Default Route: no\n"); err == nil {
		t.Fatal("want error for link with no DNS Servers line")
	}
}

func TestSameSet(t *testing.T) {
	if !SameSet([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("order-insensitive equality broken")
	}
	if SameSet([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("different lengths must not be equal")
	}
}

func TestParseV4RoutesPicksLowestMetric(t *testing.T) {
	in := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"wlan0\t00000000\t0201A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t600\t00FFFFFF\t0\t0\t0\n"
	iface, err := pickDefault(parseV4Routes(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	if iface != "wlan0" {
		t.Fatalf("got %q, want wlan0 (metric 100 beats 600)", iface)
	}
}

func TestParseV6RoutesDefaultOnly(t *testing.T) {
	// fe80::/64 (plen 40) and ff00::/8 (plen 08) must not count as
	// defaults; the all-zero/00 line is the default, metric 0x400 hex.
	in := "fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000004 00000000 00000001     eno1\n" +
		"ff000000000000000000000000000000 08 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000007 00000000 00000001  docker0\n" +
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00200200     wan0\n" +
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo\n"
	routes := parseV6Routes(in)
	if len(routes) != 1 {
		t.Fatalf("want exactly 1 default candidate, got %v", routes)
	}
	if routes[0].iface != "wan0" || routes[0].metric != 0x400 {
		t.Fatalf("got %+v, want {wan0 1024}", routes[0])
	}
}

func TestPickDefaultAcrossFamilies(t *testing.T) {
	// v6 metric 50 must beat v4 metric 100.
	iface, err := pickDefault([]defRoute{{"eth0", 100}, {"wan0", 50}})
	if err != nil {
		t.Fatal(err)
	}
	if iface != "wan0" {
		t.Fatalf("got %q, want wan0", iface)
	}
	iface, err = pickDefault([]defRoute{{"eth0", 100}, {"wan0", 200}})
	if err != nil {
		t.Fatal(err)
	}
	if iface != "eth0" {
		t.Fatalf("got %q, want eth0", iface)
	}
}

func TestPickDefaultSkipsLoopback(t *testing.T) {
	iface, err := pickDefault([]defRoute{{"lo", 1}, {"eth0", 500}})
	if err != nil {
		t.Fatal(err)
	}
	if iface != "eth0" {
		t.Fatalf("got %q, want eth0", iface)
	}
	if _, err := pickDefault(nil); err == nil {
		t.Fatal("want error for no routes")
	}
}

func TestParseStatusOrderIPv6(t *testing.T) {
	out := `Link 2 (eno1)
Current DNS Server: 2001:db8::1
       DNS Servers: 2001:db8::1 192.168.1.20
                    2001:db8::2#dns.example
     Default Route: yes
`
	got, err := parseStatusOrder(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2001:db8::1", "192.168.1.20", "2001:db8::2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
