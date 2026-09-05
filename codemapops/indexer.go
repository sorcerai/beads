package codemapops

import "context"

// ApplyRequest asks an Indexer to replace a repository's nodes and edges (or
// a subset of packages, via Only) with a freshly scanned Graph.
type ApplyRequest struct {
	RepoID  string
	HeadSHA string
	Graph   Graph
	Only    []string // package Paths rescanned; nil = whole repo
}

// ApplyResult reports what an Apply call actually changed.
type ApplyResult struct {
	NodesUpserted, NodesDeleted, EdgesWritten, EdgesPruned int
}

// Summary is one node's attached summary, as produced by a summarizer pass.
type Summary struct {
	Path, Summary, Layer string
	Tags                 []string
	BlobHash, Model      string
}

// SetSummariesResult reports how many summaries were written versus refused
// (e.g. because the node's blob hash moved since the summary was computed).
type SetSummariesResult struct {
	Written, Refused int
	RefusedPaths     []string
}

// Indexer applies scanned graphs and summaries to a repository's code map.
type Indexer interface {
	Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error)
	SetSummaries(ctx context.Context, repoID string, items []Summary) (SetSummariesResult, error)
}
