package ipc

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func fetchEventually(t *testing.T, path string) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, err := Fetch(path)
		if err == nil {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("fetch %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A stale socket path is replaced, the snapshot round-trips, and unprivileged
// clients can connect.
func TestServeFetchRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nspulse.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := Status{
		Iface:     "eno1",
		Servers:   []string{"192.168.1.10", "192.168.1.20"},
		Incumbent: "192.168.1.20",
		Round:     2,
		PerServer: map[string]ServerStats{
			"192.168.1.20": {Samples: 8, Failures: 0, MedianMs: 0.31, P95Ms: 0.36, LastRoundOK: true},
		},
	}
	go func() {
		_ = Serve(path, func() Status { return want })
	}()

	got := fetchEventually(t, path)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o666 {
		t.Fatalf("socket mode = %o, want 666", perm)
	}
}

func TestFetchMissingSocket(t *testing.T) {
	if _, err := Fetch(filepath.Join(t.TempDir(), "missing.sock")); err == nil {
		t.Fatal("want error for missing socket")
	}
}

// A hostile or replaced socket must not be able to exhaust the client.
func TestFetchRejectsOversizedStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write(bytes.Repeat([]byte(" "), maxStatusSize+1))
	}()
	if _, err := Fetch(path); err == nil {
		t.Fatal("want error for oversized status payload")
	}
}
