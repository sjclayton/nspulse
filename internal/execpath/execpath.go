// Package execpath resolves external helper programs against a fixed,
// root-owned search list instead of the inherited PATH. The daemon runs as
// root, so it must never execute a binary planted in a user-writable PATH
// entry.
package execpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SearchDirs is consulted in order. It is a variable only so tests can point
// Lookup at stub binaries; production code never changes it.
var SearchDirs = []string{
	"/usr/sbin",
	"/usr/bin",
	"/sbin",
	"/bin",
	"/usr/local/sbin",
	"/usr/local/bin",
}

// Lookup returns the absolute path of an executable named name from
// SearchDirs. It ignores PATH and rejects names containing a path separator.
func Lookup(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, os.PathSeparator) {
		return "", fmt.Errorf("invalid command name %q", name)
	}
	for _, dir := range SearchDirs {
		path := filepath.Join(dir, name)
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("%s not found in fixed search path %v", name, SearchDirs)
}
