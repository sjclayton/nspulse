package discover

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Regression: the incumbent is Current DNS Server, not list position 0.
func TestParseLinkStatusIncumbentIsCurrentServer(t *testing.T) {
	out := `Link 2 (eno1)
    Current Scopes: DNS LLMNR/IPv4 LLMNR/IPv6 mDNS/IPv4 mDNS/IPv6
         Protocols: +DefaultRoute +LLMNR +mDNS -DNSOverTLS DNSSEC=no/unsupported
Current DNS Server: 192.168.1.20
       DNS Servers: 192.168.1.10 192.168.1.20
        DNS Domain: clayton-tech.ca
     Default Route: yes
`
	link, err := parseLinkStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	if link.Current != "192.168.1.20" {
		t.Fatalf("got current %q, want 192.168.1.20", link.Current)
	}
	want := []string{"192.168.1.10", "192.168.1.20"}
	if !reflect.DeepEqual(link.Servers, want) {
		t.Fatalf("got servers %v, want %v", link.Servers, want)
	}
}

func TestParseLinkStatusOrderPreserved(t *testing.T) {
	out := `Link 2 (eno1)
Current DNS Server: 192.168.1.20
       DNS Servers: 192.168.1.20 192.168.1.10
        DNS Domain: clayton-tech.ca
     Default Route: yes
`
	link, err := parseLinkStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.20", "192.168.1.10"}
	if !reflect.DeepEqual(link.Servers, want) {
		t.Fatalf("got %v, want %v", link.Servers, want)
	}
	if link.Current != "192.168.1.20" {
		t.Fatalf("got current %q, want 192.168.1.20", link.Current)
	}
}

func TestParseStatusOrderWrappedLines(t *testing.T) {
	out := `Link 2 (eno1)
       DNS Servers: 192.168.1.20 192.168.1.10
                    192.168.1.30
        DNS Domain: example.com
`
	link, err := parseLinkStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.20", "192.168.1.10", "192.168.1.30"}
	if !reflect.DeepEqual(link.Servers, want) {
		t.Fatalf("got %v, want %v", link.Servers, want)
	}
	if link.Current != "" {
		t.Fatalf("got current %q, want empty", link.Current)
	}
}

func TestParseStatusOrderMissing(t *testing.T) {
	if _, err := parseLinkStatus("Link 3 (wlan0)\n     Default Route: no\n"); err == nil {
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
		t.Fatalf("want exactly 1 default route, got %v", routes)
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
	link, err := parseLinkStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2001:db8::1", "192.168.1.20", "2001:db8::2#dns.example"}
	if !reflect.DeepEqual(link.Servers, want) {
		t.Fatalf("got %v, want %v", link.Servers, want)
	}
	if link.Current != "2001:db8::1" {
		t.Fatalf("got current %q, want 2001:db8::1", link.Current)
	}
}

// resolvectl accepts "%zone" and "#server-name" suffixes. Both must survive
// parsing so switching writes the link config back unchanged.
func TestParseStatusKeepsZonesAndAnnotations(t *testing.T) {
	out := `Link 2 (eno1)
Current DNS Server: fe80::1%eno1
       DNS Servers: fe80::1%eno1 2001:db8::2#dns.example
                    192.168.1.20#dot.example
     Default Route: yes
`
	link, err := parseLinkStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fe80::1%eno1", "2001:db8::2#dns.example", "192.168.1.20#dot.example"}
	if !reflect.DeepEqual(link.Servers, want) {
		t.Fatalf("got %v, want %v", link.Servers, want)
	}
	if link.Current != "fe80::1%eno1" {
		t.Fatalf("got current %q, want fe80::1%%eno1", link.Current)
	}
}

// nmcli terse mode escapes ":" by default, which breaks IPv6 DNS values.
// The -e no flag must be passed and both address families must parse.
func TestFromNMCLIDisablesTerseEscaping(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
*"-e no"*) printf '%s\n' 'IP4.DNS[1]:192.168.1.10' 'IP6.DNS[1]:2001:db8::1' ;;
*) printf '%s\n' 'IP4.DNS[1]:192.168.1.10' 'IP6.DNS[1]:2001\:db8\:\:1' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "nmcli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := fromNMCLI("eno1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.10", "2001:db8::1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A lease for another interface must never contribute DNS servers, even when
// the active interface has no lease of its own.
func TestNetworkdLeasesOnlyReadsActiveIface(t *testing.T) {
	dir := t.TempDir()
	old := networkdLeaseDir
	networkdLeaseDir = dir
	t.Cleanup(func() { networkdLeaseDir = old })

	ni, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skipf("loopback interface unavailable: %v", err)
	}
	write := func(index int, dns string) {
		path := filepath.Join(dir, strconv.Itoa(index))
		if err := os.WriteFile(path, []byte("DNS="+dns+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(ni.Index+1000, "10.0.0.1")
	if _, err := fromNetworkdLeases("lo"); err == nil {
		t.Fatal("used another interface's lease")
	}

	write(ni.Index, "192.168.1.10")
	got, err := fromNetworkdLeases("lo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestServerKey(t *testing.T) {
	cases := map[string]string{
		"192.168.1.1":                     "192.168.1.1",
		"192.168.1.1#dns.example":         "192.168.1.1",
		"1.1.1.1:9953#dns.example":        "1.1.1.1",
		"fe80::1%eno1":                    "fe80::1",
		"[fe80::1%eno1]:9953#dns.example": "fe80::1",
		"not-an-ip":                       "",
	}
	for in, want := range cases {
		if got := ServerKey(in); got != want {
			t.Errorf("ServerKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- DHCP discovery chain -------------------------------------------------

func stubNmcli(t *testing.T, lines ...string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n"
	for _, line := range lines {
		script += "printf '%s\\n' '" + line + "'\n"
	}
	script += "exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "nmcli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func stubNmcliFailing(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nmcli"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func setNetworkdLeaseDir(t *testing.T, dir string) {
	t.Helper()
	old := networkdLeaseDir
	networkdLeaseDir = dir
	t.Cleanup(func() { networkdLeaseDir = old })
}

func writeNetworkdLease(t *testing.T, dir, iface, dns string) {
	t.Helper()
	ni, err := net.InterfaceByName(iface)
	if err != nil {
		t.Skipf("interface %s unavailable: %v", iface, err)
	}
	path := filepath.Join(dir, strconv.Itoa(ni.Index))
	if err := os.WriteFile(path, []byte("DNS="+dns+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setDHClientGlobs(t *testing.T, patterns ...string) {
	t.Helper()
	old := dhclientLeaseGlobs
	dhclientLeaseGlobs = patterns
	t.Cleanup(func() { dhclientLeaseGlobs = old })
}

func writeDHClientLease(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dhclientLease(iface, dns string) string {
	return "lease {\n  interface \"" + iface + "\";\n  option domain-name-servers " + dns + ";\n}\n"
}

// nmcli wins when it has a non-empty answer, even if networkd also has an
// IPv6 server. The source priority is deliberate: one discovery path owns
// the link's DHCP answer.
func TestDHCPServersPrefersNMCLI(t *testing.T) {
	ndDir := t.TempDir()
	setNetworkdLeaseDir(t, ndDir)
	writeNetworkdLease(t, ndDir, "lo", "10.0.0.2 2001:db8::2")

	dhDir := t.TempDir()
	writeDHClientLease(t, dhDir, "dhclient-lo.lease", dhclientLease("lo", "10.0.0.3"))
	setDHClientGlobs(t, filepath.Join(dhDir, "*.lease"))

	stubNmcli(t, "IP4.DNS[1]:10.0.0.1")

	got, err := DHCPServers("lo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// An empty/failed nmcli falls through to the active interface's networkd lease.
func TestDHCPServersFallsBackToNetworkd(t *testing.T) {
	ndDir := t.TempDir()
	setNetworkdLeaseDir(t, ndDir)
	writeNetworkdLease(t, ndDir, "lo", "10.0.0.2")

	dhDir := t.TempDir()
	writeDHClientLease(t, dhDir, "dhclient-lo.lease", dhclientLease("lo", "10.0.0.3"))
	setDHClientGlobs(t, filepath.Join(dhDir, "*.lease"))

	stubNmcliFailing(t)

	got, err := DHCPServers("lo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// With no nmcli or networkd answer, dhclient is the last source.
func TestDHCPServersFallsBackToDHClient(t *testing.T) {
	setNetworkdLeaseDir(t, t.TempDir())

	dhDir := t.TempDir()
	writeDHClientLease(t, dhDir, "dhclient-lo.lease", dhclientLease("lo", "10.0.0.3"))
	setDHClientGlobs(t, filepath.Join(dhDir, "*.lease"))

	stubNmcliFailing(t)

	got, err := DHCPServers("lo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A shared lease file may hold blocks for several interfaces; only the
// matching block's servers are used.
func TestFromDHClientLeasesUsesMatchingBlock(t *testing.T) {
	dir := t.TempDir()
	body := dhclientLease("eth9", "10.0.0.9") + dhclientLease("lo", "192.0.2.9, 192.0.2.10")
	writeDHClientLease(t, dir, "dhclient.leases", body)
	setDHClientGlobs(t, filepath.Join(dir, "*.leases"))

	got, err := fromDHClientLeases("lo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.10", "192.0.2.9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if _, err := fromDHClientLeases("enoX"); err == nil {
		t.Fatal("want error when no block matches the interface")
	}
}
