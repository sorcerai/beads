//go:build cgo

package codemapops_test

import (
	"context"
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
