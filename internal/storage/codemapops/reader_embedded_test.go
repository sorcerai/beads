//go:build cgo

package codemapops_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
	cmops "github.com/steveyegge/beads/internal/storage/codemapops"
)

func TestFileContextAndShape(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := cmops.FileContextInTx(ctx, tx, "r1", "a/a.go"); !errors.As(err, new(*codemapops.ErrNotIndexed)) {
		t.Fatalf("want ErrNotIndexed before build, got %v", err)
	}
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc", Graph: twoPkgGraph()}, time.Now()); err != nil {
		t.Fatal(err)
	}
	fc, err := cmops.FileContextInTx(ctx, tx, "r1", "a/a.go")
	if err != nil {
		t.Fatal(err)
	}
	if fc.Package.Path != "m/a" || len(fc.Importers) != 2 { // b/b.go and package m/b import m/a
		t.Fatalf("unexpected context: %+v", fc)
	}
	if _, err := cmops.FileContextInTx(ctx, tx, "r1", "nope.go"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	sh, err := cmops.ShapeInTx(ctx, tx, "r1", codemapops.ShapeOptions{})
	if err != nil || sh.HeadSHA != "abc" || sh.Nodes != 4 || len(sh.TopFanIn) == 0 || sh.TopFanIn[0].Path != "a/a.go" {
		t.Fatalf("shape: %+v %v", sh, err)
	}
	stale, err := cmops.StaleInTx(ctx, tx, "r1", 10)
	if err != nil || len(stale) != 0 {
		t.Fatalf("nothing summarized yet, nothing stale: %v %v", stale, err)
	}
}

func TestPackageContext(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc", Graph: twoPkgGraph()}, time.Now()); err != nil {
		t.Fatal(err)
	}
	pc, err := cmops.PackageContextInTx(ctx, tx, "r1", "m/a")
	if err != nil {
		t.Fatal(err)
	}
	if pc.Node.Path != "m/a" || len(pc.Files) != 1 || pc.Files[0].Path != "a/a.go" {
		t.Fatalf("files of m/a: %+v", pc)
	}
	if len(pc.Imports) != 0 || len(pc.Importers) != 2 {
		t.Fatalf("m/a imports nothing and is imported by b/b.go and m/b: %+v", pc)
	}
	if _, err := cmops.PackageContextInTx(ctx, tx, "r1", "m/nope"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStaleAfterBlobMoves(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc", Graph: twoPkgGraph()}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := cmops.SetSummariesInTx(ctx, tx, "r1", []codemapops.Summary{
		{Path: "a/a.go", Summary: "entry point", Layer: "cli", BlobHash: "1111111111111111111111111111111111111111", Model: "m"},
	}, now); err != nil {
		t.Fatal(err)
	}
	if stale, err := cmops.StaleInTx(ctx, tx, "r1", 10); err != nil || len(stale) != 0 {
		t.Fatalf("fresh summary is not stale: %v %v", stale, err)
	}
	// rescan the same file with a different blob: the summary stays but goes stale.
	moved := twoPkgGraph()
	for i := range moved.Nodes {
		if moved.Nodes[i].Path == "a/a.go" {
			moved.Nodes[i].BlobHash = "3333333333333333333333333333333333333333"
		}
	}
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "def", Graph: moved}, now); err != nil {
		t.Fatal(err)
	}
	stale, err := cmops.StaleInTx(ctx, tx, "r1", 10)
	if err != nil || len(stale) != 1 || stale[0].Path != "a/a.go" || !stale[0].Stale {
		t.Fatalf("want a/a.go stale: %+v %v", stale, err)
	}
	sh, err := cmops.ShapeInTx(ctx, tx, "r1", codemapops.ShapeOptions{})
	if err != nil || sh.StaleSummaries != 1 || sh.HeadSHA != "def" {
		t.Fatalf("shape after rescan: %+v %v", sh, err)
	}
}
