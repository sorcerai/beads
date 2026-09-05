//go:build cgo

// The embedded tests live in the EXTERNAL test package: a later task makes
// internal/storage/embeddeddolt import this package, so an internal test
// package here would close an import cycle. Everything under test is
// therefore reached through the cmops alias.
package codemapops_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// newTestTx opens a fresh embedded Dolt store (which bootstraps the schema,
// migration 0067 included), begins a transaction on it, and returns a cleanup
// that rolls the transaction back and closes everything.
func newTestTx(t *testing.T) (*sql.Tx, func()) {
	t.Helper()
	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "test", "main")
	if err != nil {
		t.Fatalf("open embedded dolt store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		t.Fatalf("set issue_prefix: %v", err)
	}
	db, closeSQL, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "test", "main")
	if err != nil {
		store.Close()
		t.Fatalf("open sql: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		closeSQL()
		store.Close()
		t.Fatalf("begin tx: %v", err)
	}
	return tx, func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("rollback: %v", err)
		}
		if err := closeSQL(); err != nil {
			t.Errorf("close sql: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}
}

// seedIssue inserts the minimal issues row the issue_files foreign key needs.
func seedIssue(t *testing.T, tx *sql.Tx, issue *types.Issue) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
		 VALUES (?, ?, '', '', '', '', ?, ?, ?, ?, ?)`,
		issue.ID, issue.Title, string(issue.Status), int(issue.Priority), string(issue.IssueType), now, now); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

// twoPkgGraph is the shared fixture graph: packages m/a and m/b, one file
// each, with m/b (and its file) importing m/a.
func twoPkgGraph() codemapops.Graph {
	return codemapops.Graph{Lang: "go",
		Nodes: []codemapops.Node{
			{Kind: codemapops.NodePackage, Path: "m/a", Name: "m/a", Lang: "go"},
			{Kind: codemapops.NodePackage, Path: "m/b", Name: "m/b", Lang: "go"},
			{Kind: codemapops.NodeFile, Path: "a/a.go", Name: "a.go", PackagePath: "m/a", Lang: "go", BlobHash: "1111111111111111111111111111111111111111", LOC: 10},
			{Kind: codemapops.NodeFile, Path: "b/b.go", Name: "b.go", PackagePath: "m/b", Lang: "go", BlobHash: "2222222222222222222222222222222222222222", LOC: 20},
		},
		Edges: []codemapops.Edge{
			{Src: "m/a", Dst: "a/a.go", Kind: codemapops.EdgeContains, Weight: 1},
			{Src: "m/b", Dst: "b/b.go", Kind: codemapops.EdgeContains, Weight: 1},
			{Src: "b/b.go", Dst: "m/a", Kind: codemapops.EdgeImports, Weight: 1},
			{Src: "m/b", Dst: "m/a", Kind: codemapops.EdgeImports, Weight: 1},
		}}
}
