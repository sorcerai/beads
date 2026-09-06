//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readUAGraph decodes the exported knowledge graph at path.
func readUAGraph(t *testing.T, path string) uaGraph {
	t.Helper()
	// #nosec G304 -- test-controlled path under t.TempDir()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var g uaGraph
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return g
}

func uaNodeByPath(g uaGraph, filePath string) *uaNode {
	for i := range g.Nodes {
		if g.Nodes[i].FilePath == filePath {
			return &g.Nodes[i]
		}
	}
	return nil
}

func hasUAEdge(g uaGraph, src, dst, kind string) bool {
	for _, e := range g.Edges {
		if e.Source == src && e.Target == dst && e.Type == kind {
			return true
		}
	}
	return false
}

// TestCodemapExport covers `bd codemap export`: the Understand-Anything
// knowledge graph the `bd explain` reader already knows how to read, written
// from the indexed map rather than by a separate scan.
func TestCodemapExport(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cx")
	writeGoMini(t, dir)
	runCodemap(t, bd, dir, "build")

	out := runCodemap(t, bd, dir, "export", "--json")
	var res struct {
		Path   string `json:"path"`
		Nodes  int    `json:"nodes"`
		Edges  int    `json:"edges"`
		Layers int    `json:"layers"`
	}
	mustCodemapJSON(t, out, &res)
	if res.Nodes == 0 || res.Edges == 0 {
		t.Fatalf("export reported an empty graph: %+v", res)
	}

	def := filepath.Join(dir, ".understand-anything", "knowledge-graph.json")
	// Suffix, not equality: git reports the repository root with symlinks
	// resolved, and a temp directory on macOS is reached through one.
	if !strings.HasSuffix(res.Path, filepath.Join(".understand-anything", "knowledge-graph.json")) {
		t.Errorf("path = %q, want the default knowledge-graph.json location", res.Path)
	}
	g := readUAGraph(t, def)

	if g.Project.Name != filepath.Base(dir) {
		t.Errorf("project.name = %q, want %q", g.Project.Name, filepath.Base(dir))
	}
	if !containsString(g.Project.Languages, "go") {
		t.Errorf("project.languages = %v, want go", g.Project.Languages)
	}

	for _, path := range []string{"a/a.go", "b/b.go", "b/b_test.go"} {
		n := uaNodeByPath(g, path)
		if n == nil {
			t.Fatalf("no node for %s: %+v", path, g.Nodes)
		}
		if n.Type != "file" {
			t.Errorf("%s: type = %q, want file", path, n.Type)
		}
	}
	var pkgs int
	for _, n := range g.Nodes {
		if n.Type == "package" {
			pkgs++
		}
	}
	if pkgs == 0 {
		t.Errorf("no package nodes: %+v", g.Nodes)
	}

	fileB, pkgA, pkgB := "file:b/b.go", "package:example.com/mini/a", "package:example.com/mini/b"
	if !hasUAEdge(g, fileB, pkgA, "imports") {
		t.Errorf("no imports edge %s -> %s: %+v", fileB, pkgA, g.Edges)
	}
	if !hasUAEdge(g, pkgB, fileB, "contains") {
		t.Errorf("no contains edge %s -> %s: %+v", pkgB, fileB, g.Edges)
	}
	if !hasUAEdge(g, "file:b/b_test.go", pkgB, "tests") {
		t.Errorf("no tests edge from b/b_test.go: %+v", g.Edges)
	}
	for _, e := range g.Edges {
		if e.Direction != "directed" || e.Weight != 1 {
			t.Errorf("edge %+v: want direction=directed weight=1", e)
		}
	}

	// Every edge endpoint names a node this document carries: the graph is
	// closed, so a reader never dereferences an id that is not here.
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, e := range g.Edges {
		if !ids[e.Source] || !ids[e.Target] {
			t.Errorf("dangling edge %+v", e)
		}
	}

	// Idempotent: a second export of an unchanged map writes the same bytes.
	before, err := os.ReadFile(def) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	runCodemap(t, bd, dir, "export")
	after, err := os.ReadFile(def) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("export is not idempotent:\nfirst:\n%s\nsecond:\n%s", before, after)
	}

	// --out puts it where the caller asked.
	alt := filepath.Join(dir, "kg.json")
	runCodemap(t, bd, dir, "export", "--out", alt)
	if alt := readUAGraph(t, alt); len(alt.Nodes) != len(g.Nodes) {
		t.Errorf("--out graph has %d nodes, want %d", len(alt.Nodes), len(g.Nodes))
	}
}

// TestExplainReadsTheCodeMap is the other half of Task 22: an issue linked by
// hand appears in `bd explain` with its code-map context, even though no
// commit mentions the id and no knowledge-graph.json was exported.
func TestExplainReadsTheCodeMap(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ce")
	writeGoMini(t, dir)
	runCodemap(t, bd, dir, "build")

	id := createIssue(t, bd, dir, "Touch a")
	runCodemap(t, bd, dir, "link", id, "a/a.go")

	stdout, stderr, err := runBDRaw(t, bd, dir, "explain", "--json", id)
	if err != nil {
		t.Fatalf("bd explain: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var resp ExplainResponse
	mustCodemapJSON(t, stdout, &resp)
	if !resp.HasGraph {
		t.Errorf("has_graph = false on an indexed repository: %+v", resp)
	}
	var found *ExplainFile
	for i := range resp.Files {
		if resp.Files[i].Path == "a/a.go" {
			found = &resp.Files[i]
		}
	}
	if found == nil {
		t.Fatalf("linked file missing from explain: %+v", resp.Files)
	}
	if found.Status != "linked:manual" {
		t.Errorf("status = %q, want linked:manual", found.Status)
	}
	var importedBy bool
	for _, c := range found.Connections {
		if strings.Contains(c, "b/b.go") && strings.Contains(c, "imported by") {
			importedBy = true
		}
	}
	if !importedBy {
		t.Errorf("connections = %v, want b/b.go as an importer", found.Connections)
	}
}
