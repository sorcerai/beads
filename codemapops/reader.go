package codemapops

import (
	"context"
	"time"
)

// NodeRef is a lightweight reference to a code map node, shaped for display
// rather than storage.
type NodeRef struct {
	Path    string   `json:"path"`
	Name    string   `json:"name,omitempty"`
	Lang    string   `json:"lang,omitempty"`
	Layer   string   `json:"layer,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Kind    NodeKind `json:"kind"`
	Stale   bool     `json:"stale"`
	IsTest  bool     `json:"is_test"`
}

// FileContext is everything a caller needs to reason about one file: its own
// node, the package that contains it, what it imports, what imports it, and
// its tests.
type FileContext struct {
	Node      NodeRef   `json:"node"`
	Package   NodeRef   `json:"package"`
	Imports   []NodeRef `json:"imports,omitempty"`   // packages this file imports
	Importers []NodeRef `json:"importers,omitempty"` // files/packages importing this file's package (file granularity when known)
	Tests     []NodeRef `json:"tests,omitempty"`
}

// PackageContext is the same shape as FileContext but scoped to a package.
type PackageContext struct {
	Node      NodeRef   `json:"node"`
	Files     []NodeRef `json:"files,omitempty"`
	Imports   []NodeRef `json:"imports,omitempty"`
	Importers []NodeRef `json:"importers,omitempty"`
}

// ShapeOptions configures a Shape query.
type ShapeOptions struct {
	TopFiles int `json:"top_files"` // default 10
}

// LayerSummary counts nodes in one architectural layer.
type LayerSummary struct {
	Layer    string `json:"layer"`
	Packages int    `json:"packages"`
	Files    int    `json:"files"`
}

// Shape is the repository-wide summary `bd codemap` shows first: freshness,
// size, and the files with the most importers.
type Shape struct {
	RepoID         string         `json:"repo_id"`
	HeadSHA        string         `json:"head_sha"`
	IndexedAt      time.Time      `json:"indexed_at"`
	Nodes          int            `json:"nodes"`
	Edges          int            `json:"edges"`
	StaleSummaries int            `json:"stale_summaries"`
	Layers         []LayerSummary `json:"layers,omitempty"`
	TopFanIn       []NodeRef      `json:"top_fan_in,omitempty"` // files ranked by importer count
}

// Reader answers read-only queries over a repository's code map.
type Reader interface {
	FileContext(ctx context.Context, repoID, path string) (FileContext, error)
	PackageContext(ctx context.Context, repoID, pkg string) (PackageContext, error)
	Stale(ctx context.Context, repoID string, limit int) ([]NodeRef, error)
	Shape(ctx context.Context, repoID string, opts ShapeOptions) (Shape, error)
}
