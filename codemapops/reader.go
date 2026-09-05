package codemapops

import (
	"context"
	"time"
)

// NodeRef is a lightweight reference to a code map node, shaped for display
// rather than storage.
type NodeRef struct {
	Path, Name, Lang, Layer, Summary string
	Kind                             NodeKind
	Stale                            bool
	IsTest                           bool
}

// FileContext is everything a caller needs to reason about one file: its own
// node, the package that contains it, what it imports, what imports it, and
// its tests.
type FileContext struct {
	Node      NodeRef
	Package   NodeRef
	Imports   []NodeRef // packages this file imports
	Importers []NodeRef // files/packages importing this file's package (file granularity when known)
	Tests     []NodeRef
}

// PackageContext is the same shape as FileContext but scoped to a package.
type PackageContext struct {
	Node      NodeRef
	Files     []NodeRef
	Imports   []NodeRef
	Importers []NodeRef
}

// ShapeOptions configures a Shape query.
type ShapeOptions struct {
	TopFiles int // default 10
}

// LayerSummary counts nodes in one architectural layer.
type LayerSummary struct {
	Layer    string
	Packages int
	Files    int
}

// Shape is the repository-wide summary `bd codemap` shows first: freshness,
// size, and the files with the most importers.
type Shape struct {
	RepoID, HeadSHA string
	IndexedAt       time.Time
	Nodes, Edges    int
	StaleSummaries  int
	Layers          []LayerSummary
	TopFanIn        []NodeRef // files ranked by importer count
}

// Reader answers read-only queries over a repository's code map.
type Reader interface {
	FileContext(ctx context.Context, repoID, path string) (FileContext, error)
	PackageContext(ctx context.Context, repoID, pkg string) (PackageContext, error)
	Stale(ctx context.Context, repoID string, limit int) ([]NodeRef, error)
	Shape(ctx context.Context, repoID string, opts ShapeOptions) (Shape, error)
}
