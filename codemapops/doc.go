// Package codemapops describes the workspace's CODE MAP PLANE: the graph of
// packages and files a repository scan produces, the summaries an indexer
// attaches to it, the per-issue file-touch history that ties issues to code,
// and the read shapes `bd codemap` surfaces from all of it.
//
// The plane is per-REPOSITORY, not per-issue: a scout run replaces a
// repository's nodes and edges wholesale, keyed by repo_id. issue_files is
// the one per-issue table in the plane, and it cascades with the issue like
// provenance_events.
//
// Like memoryops, this package holds only the wire contracts — the storage
// encoding lives in internal/storage/codemapops. It imports only
// github.com/steveyegge/beads/beadserrors and stdlib, so anything that only
// needs to classify or shape a code-map result can depend on this package
// without dragging in storage.
package codemapops
