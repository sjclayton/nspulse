package resolved

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stub puts a fake resolvectl on PATH that records each invocation. When
// current is non-empty, status calls report it as Current DNS Server.
func stub(t *testing.T, current string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n"
	if current != "" {
		script += "if [ \"$2\" = status ]; then\n" +
			"  printf 'Current DNS Server: " + current + "\\n       DNS Servers: 10.0.0.1 10.0.0.2\\n'\n" +
			"fi\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "resolvectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldLimit, oldPoll := adoptLimit, adoptPoll
	adoptLimit, adoptPoll = 0, 0
	t.Cleanup(func() { adoptLimit, adoptPoll = oldLimit, oldPoll })
	return log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// The switch must narrow to the candidate alone first (which is what moves
// resolved's Current DNS Server), then restore the full list candidate-first.
func TestSetOrderNarrowsThenRestores(t *testing.T) {
	log := stub(t, "")
	if err := SetOrder("eno1", []string{"10.0.0.1", "10.0.0.2"}, "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	want := "dns eno1 10.0.0.2\ndns eno1 10.0.0.2 10.0.0.1\n"
	if got := strings.Join(calls(t, log), "\n") + "\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Adoption is confirmed by status before the full list is restored, so the
// link is not left narrowed any longer than necessary.
func TestSetOrderRestoresAfterAdoption(t *testing.T) {
	log := stub(t, "10.0.0.2")
	adoptLimit, adoptPoll = time.Second, time.Millisecond
	if err := SetOrder("eno1", []string{"10.0.0.1", "10.0.0.2"}, "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	want := []string{"dns eno1 10.0.0.2", "--no-pager status eno1", "dns eno1 10.0.0.2 10.0.0.1"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// If resolved never adopts the candidate, the full list still gets restored
// rather than leaving the link with a single server.
func TestSetOrderRestoresWhenAdoptionNeverHappens(t *testing.T) {
	log := stub(t, "10.0.0.1")
	adoptLimit, adoptPoll = 5*time.Millisecond, time.Millisecond
	if err := SetOrder("eno1", []string{"10.0.0.1", "10.0.0.2"}, "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	if got[0] != "dns eno1 10.0.0.2" {
		t.Fatalf("first call = %q, want narrow", got[0])
	}
	if last := got[len(got)-1]; last != "dns eno1 10.0.0.2 10.0.0.1" {
		t.Fatalf("last call = %q, want full restore", last)
	}
}

func TestSetOrderRejectsUnknownCandidate(t *testing.T) {
	log := stub(t, "")
	if err := SetOrder("eno1", []string{"10.0.0.1"}, "10.0.0.2"); err == nil {
		t.Fatal("want error for candidate absent from server list")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("no resolvectl call expected")
	}
}
