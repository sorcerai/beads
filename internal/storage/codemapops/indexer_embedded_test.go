//go:build cgo

package codemapops_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/steveyegge/beads/codemapops"
	cmops "github.com/steveyegge/beads/internal/storage/codemapops"
)

func TestApplyIsIdempotentAndIncrementalDeletes(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	req := codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc", Graph: twoPkgGraph()}
	first, err := cmops.ApplyInTx(ctx, tx, req, now)
	if err != nil || first.NodesUpserted != 4 || first.EdgesWritten != 4 {
		t.Fatalf("first apply: %+v %v", first, err)
	}
	second, err := cmops.ApplyInTx(ctx, tx, req, now)
	if err != nil || second.NodesDeleted != 0 || second.EdgesWritten != 4 {
		t.Fatalf("second apply must be a no-op upsert: %+v %v", second, err)
	}
	// incremental: rescan only m/b with its file removed -> b/b.go deleted, m/a untouched
	inc := req
	inc.Only = []string{"m/b"}
	inc.Graph = codemapops.Graph{Lang: "go", Nodes: []codemapops.Node{{Kind: codemapops.NodePackage, Path: "m/b", Name: "m/b", Lang: "go"}}}
	res, err := cmops.ApplyInTx(ctx, tx, inc, now)
	if err != nil || res.NodesDeleted != 1 || res.EdgesPruned < 2 {
		t.Fatalf("incremental: %+v %v", res, err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM code_nodes WHERE repo_id='r1'").Scan(&n); err != nil || n != 3 {
		t.Fatalf("want 3 nodes after incremental, got %d (%v)", n, err)
	}
	if sha, _ := cmops.LastSHAInTx(ctx, tx, "r1"); sha != "abc" {
		t.Fatalf("last_sha not recorded: %q", sha)
	}
}

// A rescan of a package that has been deleted outright arrives as Only with
// an empty node set: everything in that package goes, and nothing else does.
func TestApplyIncrementalWithEmptyGraphDeletesThePackage(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc", Graph: twoPkgGraph()}, now); err != nil {
		t.Fatal(err)
	}
	res, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc",
		Only: []string{"m/b"}, Graph: codemapops.Graph{Lang: "go"}}, now)
	if err != nil || res.NodesDeleted != 2 || res.EdgesWritten != 0 || res.EdgesPruned != 3 {
		t.Fatalf("deleting package m/b wholesale: %+v %v", res, err)
	}
	left, err := cmops.PackageContextInTx(ctx, tx, "r1", "m/a")
	if err != nil || len(left.Files) != 1 || len(left.Importers) != 0 {
		t.Fatalf("m/a survives with its file and no importers: %+v %v", left, err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM code_nodes WHERE repo_id='r1'").Scan(&n); err != nil || n != 2 {
		t.Fatalf("want 2 nodes left, got %d (%v)", n, err)
	}
}

func TestSetSummariesRefusesMovedOnBlob(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc", Graph: twoPkgGraph()}, now); err != nil {
		t.Fatal(err)
	}
	res, err := cmops.SetSummariesInTx(ctx, tx, "r1", []codemapops.Summary{
		{Path: "a/a.go", Summary: "Package a entry", Layer: "cli", Tags: []string{"x"}, BlobHash: "1111111111111111111111111111111111111111", Model: "m"},
		{Path: "b/b.go", Summary: "Stale attempt", Layer: "cli", BlobHash: "0000000000000000000000000000000000000000", Model: "m"},
	}, now)
	if err != nil || res.Written != 1 || res.Refused != 1 || res.RefusedPaths[0] != "b/b.go" {
		t.Fatalf("got %+v %v", res, err)
	}
}

// An incremental apply of package m/b re-declares its import into m/a, which
// is OUTSIDE the rescan and so arrives as an ExternalPackages entry with no
// node of its own. The edge must land on m/a's PACKAGE node: resolved to a
// file id it dangles, and step 4's orphan prune deletes it, so every refresh
// would strip the importers of every package it did not happen to rescan.
func TestApplyIncrementalKeepsCrossScopeImportEdges(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc", Graph: twoPkgGraph()}, now); err != nil {
		t.Fatal(err)
	}
	before := countImportsInto(t, ctx, tx, "r1", "m/a")
	if before != 2 {
		t.Fatalf("seed: %d import edges into m/a, want the 2 twoPkgGraph declares", before)
	}

	// What scout.Scan(root, []string{"m/b"}) actually produces: m/b's own
	// nodes, its edges into m/a, and m/a declared external.
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "def", Only: []string{"m/b"},
		Graph: codemapops.Graph{Lang: "go",
			Nodes: []codemapops.Node{
				{Kind: codemapops.NodePackage, Path: "m/b", Name: "m/b", Lang: "go"},
				{Kind: codemapops.NodeFile, Path: "b/b.go", Name: "b.go", PackagePath: "m/b", Lang: "go", BlobHash: "2222222222222222222222222222222222222222", LOC: 20},
			},
			Edges: []codemapops.Edge{
				{Src: "m/b", Dst: "b/b.go", Kind: codemapops.EdgeContains, Weight: 1},
				{Src: "b/b.go", Dst: "m/a", Kind: codemapops.EdgeImports, Weight: 1},
				{Src: "m/b", Dst: "m/a", Kind: codemapops.EdgeImports, Weight: 1},
			},
			ExternalPackages: []string{"m/a"}}}, now); err != nil {
		t.Fatal(err)
	}

	if after := countImportsInto(t, ctx, tx, "r1", "m/a"); after != before {
		t.Errorf("import edges into m/a = %d after a rescan of m/b alone, want the %d it started with", after, before)
	}
	pc, err := cmops.PackageContextInTx(ctx, tx, "r1", "m/a")
	if err != nil {
		t.Fatalf("PackageContext(m/a): %v", err)
	}
	// Importers is every node with an imports edge into m/a, which is the
	// package AND the file inside it; the package is the one the rescan would
	// otherwise have dropped.
	var named bool
	for _, imp := range pc.Importers {
		named = named || imp.Path == "m/b"
	}
	if !named {
		t.Errorf("m/a's importers = %+v, want m/b among them", pc.Importers)
	}
}

// countImportsInto reads the RAW edge count for imports whose destination is a
// package node, which is the row the orphan prune would take away.
func countImportsInto(t *testing.T, ctx context.Context, tx *sql.Tx, repoID, pkg string) int {
	t.Helper()
	var n int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM code_edges WHERE repo_id = ? AND kind = 'imports' AND dst_id = ?",
		repoID, cmops.NodeID(repoID, codemapops.NodePackage, pkg)).Scan(&n); err != nil {
		t.Fatalf("counting imports into %s: %v", pkg, err)
	}
	return n
}
