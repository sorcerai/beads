package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/codemapops"
)

func TestWriteReadRoundTripAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	f := File{GeneratedAt: time.Now().UTC(), HeadSHA: "abc", RepoID: "r1",
		Files: map[string]Entry{"a.go": {Path: "a.go", Summary: "s", Importers: []string{"b.go"}}}}
	if err := Write(dir, f); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
	got, err := Read(dir)
	if err != nil || got.HeadSHA != "abc" || got.Files["a.go"].Importers[0] != "b.go" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := os.WriteFile(Path(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("corrupt cache must error")
	}
	if _, err := Read(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cache: %v", err)
	}
}

// goMiniGraph is the shape the go scout produces for scout/testdata/go-mini.
func goMiniGraph() codemapops.Graph {
	return codemapops.Graph{Lang: "go",
		Nodes: []codemapops.Node{
			{Kind: codemapops.NodePackage, Path: "example.com/mini/a", Name: "a", Lang: "go"},
			{Kind: codemapops.NodePackage, Path: "example.com/mini/b", Name: "b", Lang: "go"},
			{Kind: codemapops.NodeFile, Path: "a/a.go", Name: "a.go", PackagePath: "example.com/mini/a", Lang: "go"},
			{Kind: codemapops.NodeFile, Path: "b/b.go", Name: "b.go", PackagePath: "example.com/mini/b", Lang: "go"},
			{Kind: codemapops.NodeFile, Path: "b/b_test.go", Name: "b_test.go", PackagePath: "example.com/mini/b", Lang: "go", IsTest: true},
		},
		Edges: []codemapops.Edge{
			{Src: "example.com/mini/a", Dst: "a/a.go", Kind: codemapops.EdgeContains, Weight: 1},
			{Src: "example.com/mini/b", Dst: "b/b.go", Kind: codemapops.EdgeContains, Weight: 1},
			{Src: "example.com/mini/b", Dst: "example.com/mini/a", Kind: codemapops.EdgeImports, Weight: 1},
			{Src: "b/b.go", Dst: "example.com/mini/a", Kind: codemapops.EdgeImports, Weight: 1},
			{Src: "b/b_test.go", Dst: "example.com/mini/b", Kind: codemapops.EdgeTests, Weight: 1},
		}}
}

func TestBuildFromGraph(t *testing.T) {
	g := goMiniGraph()
	prev := File{Files: map[string]Entry{
		"a/a.go": {Path: "a/a.go", Summary: "does a", Layer: "core"},
		"old.go": {Path: "old.go", Summary: "outside this scan"},
	}}
	stale := []codemapops.NodeRef{{Path: "a/a.go"}}
	links := map[string][]codemapops.IssueRef{"b/b.go": {{ID: "bd-1", Title: "fix b", Status: "open"}}}

	f := BuildFromGraph(context.Background(), "r1", "sha1", g, prev, stale, links, nil)
	if f.RepoID != "r1" || f.HeadSHA != "sha1" || f.GeneratedAt.IsZero() {
		t.Fatalf("header wrong: %+v", f)
	}
	a := f.Files["a/a.go"]
	if a.Summary != "does a" || a.Layer != "core" {
		t.Errorf("summary/layer must carry forward from prev: %+v", a)
	}
	if !a.Stale {
		t.Errorf("a/a.go is in the stale slice: %+v", a)
	}
	// a/a.go imports nothing; its PACKAGE is imported by package b and file b/b.go.
	if len(a.Imports) != 0 {
		t.Errorf("imports wrong: %v", a.Imports)
	}
	if len(a.Importers) != 2 || a.Importers[0] != "b/b.go" || a.Importers[1] != "example.com/mini/b" {
		t.Errorf("importers must come from the graph edges, sorted: %v", a.Importers)
	}
	b := f.Files["b/b.go"]
	if len(b.Imports) != 1 || b.Imports[0] != "example.com/mini/a" {
		t.Errorf("b imports wrong: %v", b.Imports)
	}
	if b.Stale {
		t.Errorf("b/b.go is not in the stale slice: %+v", b)
	}
	if len(b.OpenIssues) != 1 || b.OpenIssues[0].ID != "bd-1" {
		t.Errorf("open issues not joined: %v", b.OpenIssues)
	}
	if _, ok := f.Files["example.com/mini/a"]; ok {
		t.Error("package nodes must not become cache entries")
	}
	// An entry the graph does not mention survives an incremental rebuild.
	if _, ok := f.Files["old.go"]; !ok {
		t.Error("prev entry outside the scan must carry forward")
	}
}

func TestBuildFromGraphCapsNames(t *testing.T) {
	g := codemapops.Graph{Lang: "go",
		Nodes: []codemapops.Node{{Kind: codemapops.NodeFile, Path: "a.go", Name: "a.go", PackagePath: "pkg/a", Lang: "go"}}}
	for i := 0; i < 20; i++ {
		g.Edges = append(g.Edges, codemapops.Edge{Src: "a.go", Dst: fmt.Sprintf("pkg/x%02d", i), Kind: codemapops.EdgeImports, Weight: 1})
	}
	f := BuildFromGraph(context.Background(), "r1", "sha1", g, File{}, nil, nil, nil)
	if got := f.Files["a.go"].Imports; len(got) != maxNames || got[0] != "pkg/x00" {
		t.Errorf("imports not capped at %d: %v", maxNames, got)
	}
}

// bOnlyGraph is what an incremental refresh of package b alone produces: no
// node for package a or its files, because the scan never looked there.
func bOnlyGraph() codemapops.Graph {
	return codemapops.Graph{Lang: "go",
		Nodes: []codemapops.Node{
			{Kind: codemapops.NodePackage, Path: "example.com/mini/b", Name: "b", Lang: "go"},
			{Kind: codemapops.NodeFile, Path: "b/b.go", Name: "b.go", PackagePath: "example.com/mini/b", Lang: "go"},
		},
		Edges:            []codemapops.Edge{{Src: "example.com/mini/b", Dst: "b/b.go", Kind: codemapops.EdgeContains, Weight: 1}},
		ExternalPackages: []string{"example.com/mini/a"},
		ScopedPackages:   []string{"example.com/mini/b"}}
}

// TestBuildFromGraphKeepsUnscannedImporters is the regression guard: a package
// this scan never looked at still imports the rescanned one, and its edge is
// absent from the incremental graph. Deriving importers from g alone silently
// dropped it and overwrote a correct entry.
func TestBuildFromGraphKeepsUnscannedImporters(t *testing.T) {
	prev := File{Files: map[string]Entry{
		"b/b.go": {Path: "b/b.go", Importers: []string{"a/x.go", "example.com/mini/a"}},
	}}

	f := BuildFromGraph(context.Background(), "r1", "sha2", bOnlyGraph(), prev, nil, nil, nil)
	got := f.Files["b/b.go"].Importers
	if len(got) != 2 || got[0] != "a/x.go" || got[1] != "example.com/mini/a" {
		t.Errorf("an importer outside the scan must survive it: %v", got)
	}

	// Deleted names are the exception: there the commit, not the scan, is
	// authoritative, and carrying one would resurrect a file that is gone.
	f = BuildFromGraph(context.Background(), "r1", "sha2", bOnlyGraph(), prev, nil, nil, []string{"a/x.go"})
	got = f.Files["b/b.go"].Importers
	if len(got) != 1 || got[0] != "example.com/mini/a" {
		t.Errorf("a deleted importer must not be carried forward: %v", got)
	}
}

// TestBuildFromGraphDropsRescannedImporter is the other half of the rule: when
// the scan DID look at the importer, the scan wins. A file that stopped
// importing the package must not come back from the old cache.
func TestBuildFromGraphDropsRescannedImporter(t *testing.T) {
	g := bOnlyGraph()
	// This scan covered a/a.go too, and its edges no longer import b.
	g.Nodes = append(g.Nodes,
		codemapops.Node{Kind: codemapops.NodePackage, Path: "example.com/mini/a", Name: "a", Lang: "go"},
		codemapops.Node{Kind: codemapops.NodeFile, Path: "a/a.go", Name: "a.go", PackagePath: "example.com/mini/a", Lang: "go"})
	g.ExternalPackages = nil

	prev := File{Files: map[string]Entry{
		"b/b.go": {Path: "b/b.go", Importers: []string{"a/a.go"}},
	}}
	f := BuildFromGraph(context.Background(), "r1", "sha2", g, prev, nil, nil, nil)
	if got := f.Files["b/b.go"].Importers; len(got) != 0 {
		t.Errorf("a rescanned importer that dropped the import must not persist: %v", got)
	}
}
