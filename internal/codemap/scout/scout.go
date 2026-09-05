// Package scout extracts a repository's code map from source: which packages
// and files exist, and how they reference each other. Each language has one
// Scout; the registry runs every scout that detects its language at the root.
//
// A scout never guesses. A reference it cannot resolve is counted in
// Graph.Dropped rather than pointed at a plausible node.
package scout

import "github.com/steveyegge/beads/codemapops"

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
