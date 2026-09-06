package codemapops

import "context"

// ApplyRequest asks an Indexer to replace a repository's nodes and edges (or
// a subset of packages, via Only) with a freshly scanned Graph.
type ApplyRequest struct {
	RepoID  string   `json:"repo_id"`
	HeadSHA string   `json:"head_sha"`
	Graph   Graph    `json:"graph"`
	Only    []string `json:"only,omitempty"` // package Paths rescanned; nil = whole repo
}

// ApplyResult reports what an Apply call actually changed.
//
// EdgesPruned counts EVERY edge the apply removed: the edges replaced because
// their source was rescanned, plus the orphans left by a node deletion.
type ApplyResult struct {
	NodesUpserted int `json:"nodes_upserted"`
	NodesDeleted  int `json:"nodes_deleted"`
	EdgesWritten  int `json:"edges_written"`
	EdgesPruned   int `json:"edges_pruned"`
}

// Summary is one node's attached summary, as produced by a summarizer pass.
type Summary struct {
	Path     string   `json:"path"`
	Summary  string   `json:"summary"`
	Layer    string   `json:"layer,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	BlobHash string   `json:"blob_hash,omitempty"`
	Model    string   `json:"model,omitempty"`
}

// SetSummariesResult reports how many summaries were written versus refused
// (e.g. because the node's blob hash moved since the summary was computed).
type SetSummariesResult struct {
	Written      int      `json:"written"`
	Refused      int      `json:"refused"`
	RefusedPaths []string `json:"refused_paths,omitempty"`
}

// Indexer applies scanned graphs and summaries to a repository's code map.
type Indexer interface {
	Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error)
	SetSummaries(ctx context.Context, repoID string, items []Summary) (SetSummariesResult, error)
}
