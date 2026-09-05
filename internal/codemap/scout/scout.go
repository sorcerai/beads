// Package scout extracts a repository's code map from source: which packages
// and files exist, and how they reference each other. Each language has one
// Scout; the registry runs every scout that detects its language at the root.
//
// A scout never guesses. A reference it cannot resolve is counted in
// Graph.Dropped rather than pointed at a plausible node.
package scout

import (
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/codemapops"
)

// Scout extracts the code map for one language from a repository root.
type Scout interface {
	Name() string
	Detect(root string) bool
	// Scan reads the repository at root. only lists package Paths to rescan;
	// nil means the whole repository.
	Scan(root string, only []string) (codemapops.Graph, error)
}

// ErrToolMissing reports that the external tool a scout drives is absent, so
// its language cannot be scanned on this machine.
type ErrToolMissing struct{ Tool string }

func (e *ErrToolMissing) Error() string { return e.Tool + " is not on PATH" }

// relTo returns p relative to root with forward slashes. It retries through
// EvalSymlinks when the direct answer escapes root, because tools like cargo
// report a realpath (/private/var/...) for a root the caller knows by its
// symlink (/var/...).
func relTo(root, p string) (string, error) {
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel), nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
