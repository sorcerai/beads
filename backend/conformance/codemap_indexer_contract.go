package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/codemapops"
)

// This file holds the contract every implementation of codemapops.Indexer must
// satisfy. Each case asserts what codemapops/indexer.go PROMISES rather than
// what any one backend happens to do today; a backend that disagrees is parked
// at its own wiring site with a KNOWN DIVERGENCE skip so the case still runs on
// the ones that agree.
//
// THE VOTE COUNT: all three legs share the …InTx bodies in
// internal/storage/codemapops, reached by the stores directly and by the unit
// of work through domain.CodeMapUseCase — ONE reading plus an engine check;
// assert sentinels and typed-error fields, not message text.
//
// So nothing below may read an error's message. codemapops.ErrValidation is an
// ALIAS of beadserrors.ErrValidation and is asserted with errors.Is;
// ErrNotIndexed is a typed pointer error and is asserted with errors.As and its
// RepoID field. A case matching on wording would pass on a body that classified
// the failure wrongly, which is the one thing three legs sharing a body cannot
// otherwise tell you.
//
// EVERY CASE OWNS ITS OWN REPO ID. The code-map plane is keyed by repo id all
// the way down — node ids are NodeID(repoID, kind, path) and the head marker is
// the config row codemap.<repoID>.last_sha — so a per-case repo id makes this
// suite order-independent. That is a correctness requirement rather than
// tidiness: the unit-of-work leg has no per-test copy-on-write branch, and the
// two store legs share one store across the whole suite.
//
// PATHS ARE NAMESPACED WITH THE FIXTURE PREFIX for the same reason, and the
// namespacing has to be CONSISTENT: a file node's PackagePath must keep naming
// its package node's Path, or Package and Importers come back empty and every
// read case is quietly vacuous.

// CodeMapIndexerFixture supplies adapter-specific storage access for the
// Indexer assertions. Every field is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from.
type CodeMapIndexerFixture struct {
	// IssuePrefix namespaces the repo ids and paths each assertion writes, so
	// several of them can share one database.
	IssuePrefix string
	Indexer     codemapops.Indexer
	// Reader is how a case reads an apply back through the ROLE, next to the
	// raw-row reading QueryScalar gives it.
	Reader codemapops.Reader
	// QueryScalar runs a single-row query and scans it, and RETURNS the error
	// rather than failing the test. It is how the cases read RAW ROWS — the
	// only way to tell "the answer looks right" from "the table is right".
	QueryScalar func(context.Context, string, []any, ...any) error
}

// Blob hashes the code-map cases attach to their file nodes. They are 40 hex
// characters because SetSummariesInTx refuses anything else, and they are
// constants because staleness is defined as a summary's blob differing from the
// node's.
const (
	codeMapBlobA      = "1111111111111111111111111111111111111111"
	codeMapBlobB      = "2222222222222222222222222222222222222222"
	codeMapBlobMoved  = "3333333333333333333333333333333333333333"
	codeMapBlobAbsent = "0000000000000000000000000000000000000000"
)

// codeMapPaths are one case's namespaced node paths: two packages, a file in
// each, and the test file the Reader's Tests case adds.
type codeMapPaths struct {
	repoID, pkgA, pkgB, fileA, fileB, testA string
}

// codeMapPathsFor namespaces a case's whole plane under the fixture prefix and
// the case's own name, so two cases sharing a database never collide.
func codeMapPathsFor(prefix, name string) codeMapPaths {
	return codeMapPaths{
		repoID: prefix + "-" + name,
		pkgA:   prefix + "/m/a",
		pkgB:   prefix + "/m/b",
		fileA:  prefix + "/a/a.go",
		fileB:  prefix + "/b/b.go",
		testA:  prefix + "/a/a_test.go",
	}
}

// codeMapTwoPkgGraph is the shared fixture graph, the namespaced twin of the
// one internal/storage/codemapops tests the bodies with: packages m/a and m/b,
// one file each, with m/b and its file importing m/a.
func codeMapTwoPkgGraph(p codeMapPaths) codemapops.Graph {
	return codemapops.Graph{Lang: "go",
		Nodes: []codemapops.Node{
			{Kind: codemapops.NodePackage, Path: p.pkgA, Name: p.pkgA, Lang: "go"},
			{Kind: codemapops.NodePackage, Path: p.pkgB, Name: p.pkgB, Lang: "go"},
			{Kind: codemapops.NodeFile, Path: p.fileA, Name: "a.go", PackagePath: p.pkgA, Lang: "go", BlobHash: codeMapBlobA, LOC: 10},
			{Kind: codemapops.NodeFile, Path: p.fileB, Name: "b.go", PackagePath: p.pkgB, Lang: "go", BlobHash: codeMapBlobB, LOC: 20},
		},
		Edges: []codemapops.Edge{
			{Src: p.pkgA, Dst: p.fileA, Kind: codemapops.EdgeContains, Weight: 1},
			{Src: p.pkgB, Dst: p.fileB, Kind: codemapops.EdgeContains, Weight: 1},
			{Src: p.fileB, Dst: p.pkgA, Kind: codemapops.EdgeImports, Weight: 1},
			{Src: p.pkgB, Dst: p.pkgA, Kind: codemapops.EdgeImports, Weight: 1},
		}}
}

// codeMapOnlyPkgAGraph is the same repository with package m/b gone entirely:
// what a whole-repo rescan produces after b is deleted from the tree.
func codeMapOnlyPkgAGraph(p codeMapPaths) codemapops.Graph {
	return codemapops.Graph{Lang: "go",
		Nodes: []codemapops.Node{
			{Kind: codemapops.NodePackage, Path: p.pkgA, Name: p.pkgA, Lang: "go"},
			{Kind: codemapops.NodeFile, Path: p.fileA, Name: "a.go", PackagePath: p.pkgA, Lang: "go", BlobHash: codeMapBlobA, LOC: 10},
		},
		Edges: []codemapops.Edge{{Src: p.pkgA, Dst: p.fileA, Kind: codemapops.EdgeContains, Weight: 1}}}
}

// codeMapApply is the "this apply must succeed" seed step: it fails the test on
// any error, so a case's own assertions are about the apply it is pinning.
func codeMapApply(t *testing.T, ctx context.Context, indexer codemapops.Indexer, req codemapops.ApplyRequest) codemapops.ApplyResult {
	t.Helper()
	res, err := indexer.Apply(ctx, req)
	if err != nil {
		t.Fatalf("Apply(%s): %v", req.RepoID, err)
	}
	return res
}

// codeMapCountNodes reads the repository's node count as a RAW ROW.
func codeMapCountNodes(t *testing.T, ctx context.Context, query func(context.Context, string, []any, ...any) error, repoID string) int {
	t.Helper()
	var n int
	if err := query(ctx, "SELECT COUNT(*) FROM code_nodes WHERE repo_id = ?", []any{repoID}, &n); err != nil {
		t.Fatalf("counting code_nodes for %s: %v", repoID, err)
	}
	return n
}

// codeMapCountEdges reads the repository's edge count as a RAW ROW.
func codeMapCountEdges(t *testing.T, ctx context.Context, query func(context.Context, string, []any, ...any) error, repoID string) int {
	t.Helper()
	var n int
	if err := query(ctx, "SELECT COUNT(*) FROM code_edges WHERE repo_id = ?", []any{repoID}, &n); err != nil {
		t.Fatalf("counting code_edges for %s: %v", repoID, err)
	}
	return n
}

// codeMapCountImportsInto reads, as a RAW ROW, how many imports edges LAND on a
// package node. The join is the assertion: node ids are a body's business, so
// the case names the package by PATH and lets the join fail to match an edge
// whose destination is not a node at all — which is exactly the shape a
// mis-resolved external target has.
func codeMapCountImportsInto(t *testing.T, ctx context.Context, query func(context.Context, string, []any, ...any) error, repoID, pkg string) int {
	t.Helper()
	var n int
	if err := query(ctx, `SELECT COUNT(*) FROM code_edges e JOIN code_nodes n ON n.id = e.dst_id
        WHERE e.repo_id = ? AND e.kind = ? AND n.path = ? AND n.kind = ?`,
		[]any{repoID, string(codemapops.EdgeImports), pkg, string(codemapops.NodePackage)}, &n); err != nil {
		t.Fatalf("counting imports into %s: %v", pkg, err)
	}
	return n
}

// codeMapCountHeadMarkers reads how many head markers a repository has, which
// is how a refused apply is shown to have recorded nothing. The marker is a
// config row, so this also proves the refusal did not touch the config plane.
func codeMapCountHeadMarkers(t *testing.T, ctx context.Context, query func(context.Context, string, []any, ...any) error, repoID string) int {
	t.Helper()
	var n int
	if err := query(ctx, "SELECT COUNT(*) FROM config WHERE `key` = ?", []any{"codemap." + repoID + ".last_sha"}, &n); err != nil {
		t.Fatalf("counting head markers for %s: %v", repoID, err)
	}
	return n
}

// RunCodeMapIndexerApplyRefusesInvalidGraph pins Graph.Validate's clause in
// codemapops/graph.go: "Validate checks that every edge endpoint names a node
// (or a declared external package)". A dangling edge is ErrValidation, and the
// refusal writes NOTHING — not a node, not the head marker.
func RunCodeMapIndexerApplyRefusesInvalidGraph(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "invalid")
	graph := codeMapTwoPkgGraph(p)
	graph.Edges = append(graph.Edges, codemapops.Edge{Src: p.pkgA, Dst: p.pkgA + "/nowhere", Kind: codemapops.EdgeImports, Weight: 1})

	_, err := fixture.Indexer.Apply(ctx, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "abc", Graph: graph})
	if !errors.Is(err, codemapops.ErrValidation) {
		t.Fatalf("Apply with a dangling edge = %v, want codemapops.ErrValidation", err)
	}
	if n := codeMapCountNodes(t, ctx, fixture.QueryScalar, p.repoID); n != 0 {
		t.Errorf("a refused apply wrote %d code_nodes rows, want 0", n)
	}
	if n := codeMapCountHeadMarkers(t, ctx, fixture.QueryScalar, p.repoID); n != 0 {
		t.Errorf("a refused apply recorded %d head markers, want 0", n)
	}
}

// RunCodeMapIndexerApplyIsIdempotent pins ApplyRequest's clause in
// codemapops/indexer.go: an apply "replaces a repository's nodes and edges …
// with a freshly scanned Graph". Replacing with the SAME graph deletes nothing
// and leaves the same rows behind, read raw rather than through the role.
func RunCodeMapIndexerApplyIsIdempotent(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "idempotent")
	req := codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "abc", Graph: codeMapTwoPkgGraph(p)}

	first := codeMapApply(t, ctx, fixture.Indexer, req)
	if first.NodesUpserted != 4 || first.EdgesWritten != 4 || first.NodesDeleted != 0 {
		t.Fatalf("first apply = %+v, want 4 nodes upserted, 4 edges written and nothing deleted", first)
	}
	nodes, edges := codeMapCountNodes(t, ctx, fixture.QueryScalar, p.repoID), codeMapCountEdges(t, ctx, fixture.QueryScalar, p.repoID)

	second := codeMapApply(t, ctx, fixture.Indexer, req)
	if second.NodesUpserted != first.NodesUpserted || second.EdgesWritten != first.EdgesWritten {
		t.Errorf("second apply = %+v, want the same counts as the first (%+v)", second, first)
	}
	if second.NodesDeleted != 0 {
		t.Errorf("second apply deleted %d nodes; re-applying the same graph deletes nothing", second.NodesDeleted)
	}
	if got := codeMapCountNodes(t, ctx, fixture.QueryScalar, p.repoID); got != nodes {
		t.Errorf("code_nodes = %d after the second apply, want the %d the first left", got, nodes)
	}
	if got := codeMapCountEdges(t, ctx, fixture.QueryScalar, p.repoID); got != edges {
		t.Errorf("code_edges = %d after the second apply, want the %d the first left", got, edges)
	}
}

// RunCodeMapIndexerApplyWholeRepoDeletesAbsent pins the "whole repo" half of
// ApplyRequest.Only in codemapops/indexer.go: "nil = whole repo". A rescan with
// no Only deletes every node the new graph does not name.
func RunCodeMapIndexerApplyWholeRepoDeletesAbsent(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "whole")
	codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "abc", Graph: codeMapTwoPkgGraph(p)})

	res := codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "def", Graph: codeMapOnlyPkgAGraph(p)})
	if res.NodesDeleted != 2 {
		t.Errorf("NodesDeleted = %d, want the 2 nodes of package b the new graph no longer names", res.NodesDeleted)
	}
	if n := codeMapCountNodes(t, ctx, fixture.QueryScalar, p.repoID); n != 2 {
		t.Errorf("code_nodes = %d after a whole-repo rescan of package a alone, want 2", n)
	}
}

// RunCodeMapIndexerApplyIncrementalRefusesUnbuiltRepo pins ErrNotIndexed in
// codemapops/errors.go: "reports that a repository has no code map built yet".
// An incremental apply has nothing to be incremental to, and the typed error
// names the repository it refused.
func RunCodeMapIndexerApplyIncrementalRefusesUnbuiltRepo(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	// Its OWN repo id, never built by any other case: the refusal is about the
	// repository having no map at all.
	p := codeMapPathsFor(fixture.IssuePrefix, "unbuilt")

	_, err := fixture.Indexer.Apply(ctx, codemapops.ApplyRequest{
		RepoID: p.repoID, HeadSHA: "abc", Only: []string{p.pkgB}, Graph: codeMapTwoPkgGraph(p)})
	var notIndexed *codemapops.ErrNotIndexed
	if !errors.As(err, &notIndexed) {
		t.Fatalf("incremental apply to an unbuilt repository = %v, want *codemapops.ErrNotIndexed", err)
	}
	if notIndexed.RepoID != p.repoID {
		t.Errorf("ErrNotIndexed.RepoID = %q, want %q", notIndexed.RepoID, p.repoID)
	}
	if n := codeMapCountNodes(t, ctx, fixture.QueryScalar, p.repoID); n != 0 {
		t.Errorf("a refused incremental apply wrote %d code_nodes rows, want 0", n)
	}
}

// RunCodeMapIndexerApplyIncrementalScopesDeletion pins the other half of
// ApplyRequest.Only in codemapops/indexer.go: "package Paths rescanned". The
// deletion is confined to those packages, so a package outside the rescan keeps
// its nodes — asserted through the Reader as well as through raw rows.
func RunCodeMapIndexerApplyIncrementalScopesDeletion(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "scoped")
	codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "abc", Graph: codeMapTwoPkgGraph(p)})

	// Package b rescanned and found to have lost its file; package a is not in
	// the rescan and must be untouched by it.
	res := codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{
		RepoID: p.repoID, HeadSHA: "def", Only: []string{p.pkgB},
		Graph: codemapops.Graph{Lang: "go", Nodes: []codemapops.Node{
			{Kind: codemapops.NodePackage, Path: p.pkgB, Name: p.pkgB, Lang: "go"}}}})
	if res.NodesDeleted != 1 {
		t.Errorf("NodesDeleted = %d, want just the file the rescan of package b no longer names", res.NodesDeleted)
	}
	if n := codeMapCountNodes(t, ctx, fixture.QueryScalar, p.repoID); n != 3 {
		t.Errorf("code_nodes = %d, want 3: package a, its file, and package b", n)
	}
	pc, err := fixture.Reader.PackageContext(ctx, p.repoID, p.pkgA)
	if err != nil {
		t.Fatalf("PackageContext(%s): %v", p.pkgA, err)
	}
	if len(pc.Files) != 1 || pc.Files[0].Path != p.fileA {
		t.Errorf("package a's files = %+v, want just %s: a scoped rescan does not reach outside Only", pc.Files, p.fileA)
	}
}

// RunCodeMapIndexerApplyIncrementalKeepsCrossScopeEdges pins Graph's clause in
// codemapops/graph.go: an ExternalPackages entry names "a package an
// incremental scan imports but did not rescan … the package lives outside the
// scan's scope, not missing from it". Outside the scope is where the package
// NODE already is, so the edge has to land on it. Resolved to anything else the
// edge dangles and the orphan prune takes it, which would strip the importers
// of every package a refresh did not happen to cover — commit by commit, with
// no error anywhere.
func RunCodeMapIndexerApplyIncrementalKeepsCrossScopeEdges(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "crossscope")
	codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "abc", Graph: codeMapTwoPkgGraph(p)})
	before := codeMapCountImportsInto(t, ctx, fixture.QueryScalar, p.repoID, p.pkgA)

	// What an incremental scan of package b alone produces: b's own nodes, the
	// edges it declares into package a, and package a declared external.
	codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{
		RepoID: p.repoID, HeadSHA: "def", Only: []string{p.pkgB},
		Graph: codemapops.Graph{Lang: "go",
			Nodes: []codemapops.Node{
				{Kind: codemapops.NodePackage, Path: p.pkgB, Name: p.pkgB, Lang: "go"},
				{Kind: codemapops.NodeFile, Path: p.fileB, Name: "b.go", PackagePath: p.pkgB, Lang: "go", BlobHash: codeMapBlobB, LOC: 20},
			},
			Edges: []codemapops.Edge{
				{Src: p.pkgB, Dst: p.fileB, Kind: codemapops.EdgeContains, Weight: 1},
				{Src: p.fileB, Dst: p.pkgA, Kind: codemapops.EdgeImports, Weight: 1},
				{Src: p.pkgB, Dst: p.pkgA, Kind: codemapops.EdgeImports, Weight: 1},
			},
			ExternalPackages: []string{p.pkgA}}})

	if after := codeMapCountImportsInto(t, ctx, fixture.QueryScalar, p.repoID, p.pkgA); after != before {
		t.Errorf("code_edges importing package a = %d after a rescan of package b alone, want the %d it started with", after, before)
	}
	pc, err := fixture.Reader.PackageContext(ctx, p.repoID, p.pkgA)
	if err != nil {
		t.Fatalf("PackageContext(%s): %v", p.pkgA, err)
	}
	// Importers is every node with an imports edge into package a — the
	// package and the file inside it — and the package is the one a dangling
	// resolution would have dropped.
	var named bool
	for _, imp := range pc.Importers {
		named = named || imp.Path == p.pkgB
	}
	if !named {
		t.Errorf("package a's importers = %+v, want package b among them", pc.Importers)
	}
}

// RunCodeMapIndexerApplyPrunesOrphanEdges pins ApplyResult's clause in
// codemapops/indexer.go: "EdgesPruned counts EVERY edge the apply removed: the
// edges replaced because their source was rescanned, plus the orphans left by a
// node deletion". The assertion is >= because the two halves overlap by
// construction and only the floor is promised.
func RunCodeMapIndexerApplyPrunesOrphanEdges(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "orphans")
	codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "abc", Graph: codeMapTwoPkgGraph(p)})

	// Dropping package b orphans both edges INTO package a, whose sources are
	// gone; neither is replaced by the new graph, so only the prune can remove
	// them.
	res := codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "def", Graph: codeMapOnlyPkgAGraph(p)})
	if res.EdgesPruned < 2 {
		t.Errorf("EdgesPruned = %d, want at least the 2 edges orphaned by deleting package b", res.EdgesPruned)
	}
	if n := codeMapCountEdges(t, ctx, fixture.QueryScalar, p.repoID); n != 1 {
		t.Errorf("code_edges = %d, want just the contains edge the surviving package declares; an orphan edge outlived its endpoint", n)
	}
}

// RunCodeMapIndexerSetSummariesRefusesMovedBlob pins SetSummariesResult in
// codemapops/indexer.go: it "reports how many summaries were written versus
// refused (e.g. because the node's blob hash moved since the summary was
// computed)". A refusal is a COUNT, not an error: the fresh summary in the same
// batch still lands.
func RunCodeMapIndexerSetSummariesRefusesMovedBlob(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "moved")
	codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "abc", Graph: codeMapTwoPkgGraph(p)})

	res, err := fixture.Indexer.SetSummaries(ctx, p.repoID, []codemapops.Summary{
		{Path: p.fileA, Summary: "package a entry point", Layer: "cli", Tags: []string{"x"}, BlobHash: codeMapBlobA, Model: "m"},
		{Path: p.fileB, Summary: "computed against a blob that has moved", Layer: "cli", BlobHash: codeMapBlobAbsent, Model: "m"},
	})
	if err != nil {
		t.Fatalf("SetSummaries: %v", err)
	}
	if res.Written != 1 || res.Refused != 1 {
		t.Fatalf("SetSummaries = %+v, want 1 written and 1 refused", res)
	}
	if len(res.RefusedPaths) != 1 || res.RefusedPaths[0] != p.fileB {
		t.Errorf("RefusedPaths = %v, want just %s", res.RefusedPaths, p.fileB)
	}
	var summary string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(summary, '') FROM code_nodes WHERE repo_id = ? AND path = ?",
		[]any{p.repoID, p.fileB}, &summary); err != nil {
		t.Fatalf("reading the refused node's summary: %v", err)
	}
	if summary != "" {
		t.Errorf("the refused node's summary = %q, want it unwritten", summary)
	}
}

// RunCodeMapIndexerSetSummariesRefusesOverlongSummary pins the OTHER kind of
// refusal: a summary too long for the plane is a request-validation failure —
// codemapops/errors.go's ErrValidation, "this plane's deterministic
// request-validation failures" — not a Refused count. The batch holds one item
// so no partial write can muddy the reading.
func RunCodeMapIndexerSetSummariesRefusesOverlongSummary(t *testing.T, ctx context.Context, fixture CodeMapIndexerFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "overlong")
	codeMapApply(t, ctx, fixture.Indexer, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: "abc", Graph: codeMapTwoPkgGraph(p)})

	_, err := fixture.Indexer.SetSummaries(ctx, p.repoID, []codemapops.Summary{
		{Path: p.fileA, Summary: strings.Repeat("x", 161), Layer: "cli", BlobHash: codeMapBlobA, Model: "m"},
	})
	if !errors.Is(err, codemapops.ErrValidation) {
		t.Fatalf("SetSummaries with an overlong summary = %v, want codemapops.ErrValidation", err)
	}
	var summary string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(summary, '') FROM code_nodes WHERE repo_id = ? AND path = ?",
		[]any{p.repoID, p.fileA}, &summary); err != nil {
		t.Fatalf("reading the node's summary: %v", err)
	}
	if summary != "" {
		t.Errorf("a refused summary wrote %q; a validation failure writes nothing", summary)
	}
}
