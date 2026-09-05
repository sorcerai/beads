package codemapops

import "fmt"

// NodeKind distinguishes a package node from a file node in a Graph.
type NodeKind string

const (
	NodePackage NodeKind = "package"
	NodeFile    NodeKind = "file"
)

// EdgeKind distinguishes the relationships a Graph can encode between nodes.
type EdgeKind string

const (
	EdgeImports  EdgeKind = "imports"
	EdgeContains EdgeKind = "contains"
	EdgeTests    EdgeKind = "tests"
)

// Node is one package or file in a repository's code map, as produced by a
// scout and consumed by the indexer.
type Node struct {
	Kind        NodeKind
	Path        string // repo-relative, forward slashes; package: import path or crate::mod path
	Name        string
	PackagePath string // files only: Path of the containing package node
	Lang        string // "go" | "rust"
	BlobHash    string // files only, 40 hex
	LOC         int
	Exported    int
	IsTest      bool
}

// Edge is one directed relationship between two node Paths in a Graph.
type Edge struct {
	Src, Dst string // node Paths
	Kind     EdgeKind
	Weight   int
}

// Graph is the wire shape a scout fills in and the indexer applies.
type Graph struct {
	Lang    string
	Nodes   []Node
	Edges   []Edge
	Dropped int // references the scout could not resolve and refused to guess

	// ExternalPackages lists package paths an incremental scan imports but
	// did not rescan. An edge may name one of these as its Dst even though
	// no Node in this Graph carries that Path — the package lives outside
	// the scan's scope, not missing from it.
	ExternalPackages []string
}

// Validate checks that every edge endpoint names a node (or a declared
// external package) and that no two nodes share a (Kind, Path).
func (g Graph) Validate() error {
	seen := make(map[string]struct{}, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.Path == "" || n.Kind == "" || n.Lang == "" {
			return fmt.Errorf("%w: node %q missing kind, path or lang", ErrValidation, n.Path)
		}
		k := string(n.Kind) + "\x00" + n.Path
		if _, dup := seen[k]; dup {
			return fmt.Errorf("%w: duplicate node %s %q", ErrValidation, n.Kind, n.Path)
		}
		seen[k] = struct{}{}
	}
	paths := make(map[string]struct{}, len(g.Nodes))
	for _, n := range g.Nodes {
		paths[n.Path] = struct{}{}
	}
	external := make(map[string]struct{}, len(g.ExternalPackages))
	for _, p := range g.ExternalPackages {
		external[p] = struct{}{}
	}
	for _, e := range g.Edges {
		if _, ok := paths[e.Src]; !ok {
			return fmt.Errorf("%w: edge source %q is not a node", ErrValidation, e.Src)
		}
		if _, ok := paths[e.Dst]; !ok {
			if _, ok := external[e.Dst]; !ok {
				return fmt.Errorf("%w: edge target %q is not a node", ErrValidation, e.Dst)
			}
		}
		if e.Kind == "" {
			return fmt.Errorf("%w: edge %q->%q has no kind", ErrValidation, e.Src, e.Dst)
		}
	}
	return nil
}
