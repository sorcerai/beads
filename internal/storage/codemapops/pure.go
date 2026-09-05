package codemapops

import (
	"database/sql"
	"sort"
	"time"

	"github.com/steveyegge/beads/codemapops"
)

// nodeRow is the code_nodes table row shape.
type nodeRow struct {
	ID, RepoID                                          string
	Kind                                                codemapops.NodeKind
	Path, PathHash, Name                                string
	PackageID                                           sql.NullString
	Lang                                                string
	BlobHash                                            sql.NullString
	LOC, Exported                                       sql.NullInt64
	IsTest                                              bool
	Layer, Summary, Tags, SummaryBlobHash, SummaryModel sql.NullString
	SummarizedAt                                        sql.NullTime
	IndexedAt                                           time.Time
}

// edgeRow is the code_edges table row shape.
type edgeRow struct {
	RepoID, SrcID, DstID string
	Kind                 codemapops.EdgeKind
	Weight               int
}

// issueFileRow is the issue_files table row shape.
type issueFileRow struct {
	IssueID, RepoID, PathHash, Path string
	Source                          codemapops.Source
	CommitSHA                       sql.NullString
	FirstSeen, LastSeen             time.Time
	Touches                         int
}

// sourceRank orders evidence strength: manual and commit are facts, hook is an
// observation, branch is an inference. MergeRecord never lowers a row's rank.
var sourceRank = map[codemapops.Source]int{
	codemapops.SourceManual: 3, codemapops.SourceCommit: 3, codemapops.SourceHook: 2, codemapops.SourceBranch: 1,
}

// StaleOf reports whether a node's summary was computed against an older blob
// than the one currently indexed. A node with no summary yet is unsummarized,
// not stale.
func StaleOf(blobHash, summaryBlobHash, summary string) bool {
	return summary != "" && blobHash != summaryBlobHash
}

// RankFanIn orders paths by importer count descending, then path ascending,
// and returns at most top of them.
func RankFanIn(importers map[string]int, top int) []string {
	paths := make([]string, 0, len(importers))
	for p := range importers {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		if importers[paths[i]] != importers[paths[j]] {
			return importers[paths[i]] > importers[paths[j]]
		}
		return paths[i] < paths[j]
	})
	if top > 0 && len(paths) > top {
		paths = paths[:top]
	}
	return paths
}

// MergeRecord folds one RecordRequest observation into an existing
// issue_files row (nil for a first sighting), bumping touches and widening
// the seen range while never letting a weaker source downgrade a stronger one.
func MergeRecord(existing *issueFileRow, req codemapops.RecordRequest, path string, now time.Time) issueFileRow {
	sha := sql.NullString{String: req.CommitSHA, Valid: req.CommitSHA != ""}
	if existing == nil {
		return issueFileRow{IssueID: req.IssueID, RepoID: req.RepoID, PathHash: PathHash(path), Path: path,
			Source: req.Source, CommitSHA: sha, FirstSeen: now, LastSeen: now, Touches: 1}
	}
	row := *existing
	row.LastSeen = now
	row.Touches++
	if sourceRank[req.Source] >= sourceRank[row.Source] {
		row.Source = req.Source
		if sha.Valid {
			row.CommitSHA = sha
		}
	}
	return row
}

// BuildShape folds node and edge rows into the repository-wide summary
// `bd codemap` shows first. Layer "" is reported as "unassigned".
func BuildShape(nodes []nodeRow, edges []edgeRow, opts codemapops.ShapeOptions) codemapops.Shape {
	top := opts.TopFiles
	if top <= 0 {
		top = 10
	}
	byID := make(map[string]nodeRow, len(nodes))
	layers := map[string]*codemapops.LayerSummary{}
	var shape codemapops.Shape
	for _, n := range nodes {
		byID[n.ID] = n
		layer := n.Layer.String
		if layer == "" {
			layer = "unassigned"
		}
		ls := layers[layer]
		if ls == nil {
			ls = &codemapops.LayerSummary{Layer: layer}
			layers[layer] = ls
		}
		if n.Kind == codemapops.NodePackage {
			ls.Packages++
		} else {
			ls.Files++
			if StaleOf(n.BlobHash.String, n.SummaryBlobHash.String, n.Summary.String) {
				shape.StaleSummaries++
			}
		}
		if n.IndexedAt.After(shape.IndexedAt) {
			shape.IndexedAt = n.IndexedAt
		}
	}
	// importer count per FILE: an edge file->package counts for every file of the target package.
	filesOfPkg := map[string][]string{}
	for _, n := range nodes {
		if n.Kind == codemapops.NodeFile && n.PackageID.Valid {
			filesOfPkg[n.PackageID.String] = append(filesOfPkg[n.PackageID.String], n.ID)
		}
	}
	fanIn := map[string]int{}
	for _, e := range edges {
		if e.Kind != codemapops.EdgeImports {
			continue
		}
		for _, fid := range filesOfPkg[e.DstID] {
			fanIn[byID[fid].Path] += e.Weight
		}
	}
	for _, p := range RankFanIn(fanIn, top) {
		for _, n := range nodes {
			if n.Kind == codemapops.NodeFile && n.Path == p {
				shape.TopFanIn = append(shape.TopFanIn, refOf(n))
				break
			}
		}
	}
	for _, ls := range layers {
		shape.Layers = append(shape.Layers, *ls)
	}
	sort.Slice(shape.Layers, func(i, j int) bool { return shape.Layers[i].Layer < shape.Layers[j].Layer })
	shape.Nodes, shape.Edges = len(nodes), len(edges)
	return shape
}

func refOf(n nodeRow) codemapops.NodeRef {
	return codemapops.NodeRef{Path: n.Path, Name: n.Name, Lang: n.Lang, Kind: n.Kind, Layer: n.Layer.String,
		Summary: n.Summary.String, IsTest: n.IsTest,
		Stale: StaleOf(n.BlobHash.String, n.SummaryBlobHash.String, n.Summary.String)}
}
