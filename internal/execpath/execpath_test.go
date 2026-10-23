package execpath

import (
	"os"
	"path/filepath"
	"testing"
)

// Lookup must use only the fixed search list, never PATH.
func TestLookupUsesFixedDirsNotPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helper"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := SearchDirs
	SearchDirs = []string{dir}
	t.Cleanup(func() { SearchDirs = old })

	// A PATH entry must not be able to shadow the fixed list.
	decoy := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoy, "helper"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", decoy)

	got, err := Lookup("helper")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "helper"); got != want {
		t.Fatalf("Lookup = %q, want %q", got, want)
	}

	if _, err := Lookup("missing"); err == nil {
		t.Fatal("want error for missing helper")
	}
	if _, err := Lookup(filepath.Join(dir, "helper")); err == nil {
		t.Fatal("want error for name containing a separator")
	}
}

func TestLookupSkipsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helper"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := SearchDirs
	SearchDirs = []string{dir}
	t.Cleanup(func() { SearchDirs = old })
	if _, err := Lookup("helper"); err == nil {
		t.Fatal("want error for non-executable file")
	}
}
