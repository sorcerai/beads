package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
)

// This file holds the contract every implementation of codemapops.Reader must
// satisfy. Each case asserts what codemapops/reader.go PROMISES rather than
// what any one backend happens to do today; a backend that disagrees is parked
// at its own wiring site with a KNOWN DIVERGENCE skip so the case still runs on
// the ones that agree.
//
// THE VOTE COUNT: all three legs share the …InTx bodies in
// internal/storage/codemapops, reached by the stores directly and by the unit
// of work through domain.CodeMapUseCase — ONE reading plus an engine check;
// assert sentinels and typed-error fields, not message text.
//
// The two refusals this role can give are DIFFERENT KINDS and the cases keep
// them apart on purpose: a repository with no map at all is the typed
// *codemapops.ErrNotIndexed, asserted with errors.As down to its RepoID, while
// a path missing from a map that exists is storage.ErrNotFound. A body that
// collapsed the two would still answer plausibly, and only a sentinel assertion
// notices.
//
// EVERY CASE OWNS ITS OWN REPO ID, and the reads are set up by this role's
// PARTNER: the fixture carries an Indexer because there is no other way to put
// a code map in front of a Reader. That seeding is not what these cases pin —
// the Indexer contract pins it — so the seed steps fail the test outright
// rather than asserting.

// CodeMapReaderFixture supplies adapter-specific storage access for the Reader
// assertions. Every field is named and typed exactly like the per-backend
// roleFixtureKit hook it is filled from.
type CodeMapReaderFixture struct {
	// IssuePrefix namespaces the repo ids and paths each assertion writes, so
	// several of them can share one database.
	IssuePrefix string
	// Indexer builds the map each read case reads. It is a SEED hook here, not
	// the subject: its own promises are pinned by the Indexer contract.
	Indexer codemapops.Indexer
	Reader  codemapops.Reader
}

// codeMapGraphWithTest is the two-package graph plus a test file in package a,
// which is the only shape that gives FileContext a non-empty Tests list.
func codeMapGraphWithTest(p codeMapPaths) codemapops.Graph {
	g := codeMapTwoPkgGraph(p)
	g.Nodes = append(g.Nodes, codemapops.Node{Kind: codemapops.NodeFile, Path: p.testA, Name: "a_test.go",
		PackagePath: p.pkgA, Lang: "go", BlobHash: codeMapBlobMoved, LOC: 5, IsTest: true})
	g.Edges = append(g.Edges,
		codemapops.Edge{Src: p.pkgA, Dst: p.testA, Kind: codemapops.EdgeContains, Weight: 1},
		codemapops.Edge{Src: p.testA, Dst: p.pkgA, Kind: codemapops.EdgeTests, Weight: 1})
	return g
}

// codeMapReaderSeed applies a graph for a read case and fails the test if the
// seed itself does not land.
func codeMapReaderSeed(t *testing.T, ctx context.Context, fixture CodeMapReaderFixture, p codeMapPaths, headSHA string, graph codemapops.Graph) {
	t.Helper()
	if _, err := fixture.Indexer.Apply(ctx, codemapops.ApplyRequest{RepoID: p.repoID, HeadSHA: headSHA, Graph: graph}); err != nil {
		t.Fatalf("seeding the code map for %s: %v", p.repoID, err)
	}
}

// RunCodeMapReaderFileContextBeforeBuildIsErrNotIndexed pins ErrNotIndexed in
// codemapops/errors.go — "reports that a repository has no code map built yet"
// — against the read side. An unbuilt repository is not an empty answer.
func RunCodeMapReaderFileContextBeforeBuildIsErrNotIndexed(t *testing.T, ctx context.Context, fixture CodeMapReaderFixture) {
	// Its OWN repo id, never built by any other case.
	p := codeMapPathsFor(fixture.IssuePrefix, "reader-unbuilt")

	fc, err := fixture.Reader.FileContext(ctx, p.repoID, p.fileA)
	var notIndexed *codemapops.ErrNotIndexed
	if !errors.As(err, &notIndexed) {
		t.Fatalf("FileContext on an unbuilt repository = %+v, %v; want *codemapops.ErrNotIndexed", fc, err)
	}
	if notIndexed.RepoID != p.repoID {
		t.Errorf("ErrNotIndexed.RepoID = %q, want %q", notIndexed.RepoID, p.repoID)
	}
}

// RunCodeMapReaderFileContextUnknownPathIsErrNotFound pins the distinction the
// previous case leaves open: FileContext in codemapops/reader.go is "everything
// a caller needs to reason about one file", so a path the built map does not
// hold is storage.ErrNotFound, NOT the not-indexed error and not an empty
// context.
func RunCodeMapReaderFileContextUnknownPathIsErrNotFound(t *testing.T, ctx context.Context, fixture CodeMapReaderFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "reader-unknown")
	codeMapReaderSeed(t, ctx, fixture, p, "abc", codeMapTwoPkgGraph(p))

	_, err := fixture.Reader.FileContext(ctx, p.repoID, p.pkgA+"/nowhere.go")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("FileContext of an unknown path = %v, want storage.ErrNotFound", err)
	}
	var notIndexed *codemapops.ErrNotIndexed
	if errors.As(err, &notIndexed) {
		t.Errorf("an unknown path inside a BUILT map answered ErrNotIndexed; the two refusals are different questions")
	}
}

// RunCodeMapReaderFileContextListsImportersAndTests pins FileContext's field
// docs in codemapops/reader.go: "Importers []NodeRef // files/packages
// importing this file's package" and "Tests []NodeRef". Importers is answered
// at the PACKAGE's granularity, which is why both the importing file and the
// importing package appear.
func RunCodeMapReaderFileContextListsImportersAndTests(t *testing.T, ctx context.Context, fixture CodeMapReaderFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "reader-file")
	codeMapReaderSeed(t, ctx, fixture, p, "abc", codeMapGraphWithTest(p))

	fc, err := fixture.Reader.FileContext(ctx, p.repoID, p.fileA)
	if err != nil {
		t.Fatalf("FileContext(%s): %v", p.fileA, err)
	}
	if fc.Node.Path != p.fileA {
		t.Errorf("FileContext.Node.Path = %q, want %q", fc.Node.Path, p.fileA)
	}
	if fc.Package.Path != p.pkgA {
		t.Errorf("FileContext.Package.Path = %q, want %q", fc.Package.Path, p.pkgA)
	}
	if !codeMapHasPath(fc.Importers, p.fileB) || !codeMapHasPath(fc.Importers, p.pkgB) {
		t.Errorf("Importers = %v, want both the importing file %s and the importing package %s",
			codeMapPathsOf(fc.Importers), p.fileB, p.pkgB)
	}
	if !codeMapHasPath(fc.Tests, p.testA) {
		t.Errorf("Tests = %v, want the test file %s", codeMapPathsOf(fc.Tests), p.testA)
	}
	if len(fc.Imports) != 0 {
		t.Errorf("Imports = %v, want none: this file imports nothing", codeMapPathsOf(fc.Imports))
	}
}

// RunCodeMapReaderPackageContextListsFiles pins PackageContext in
// codemapops/reader.go: "the same shape as FileContext but scoped to a
// package". Its Files are the package's own, and its Importers are what imports
// the package.
func RunCodeMapReaderPackageContextListsFiles(t *testing.T, ctx context.Context, fixture CodeMapReaderFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "reader-package")
	codeMapReaderSeed(t, ctx, fixture, p, "abc", codeMapTwoPkgGraph(p))

	pc, err := fixture.Reader.PackageContext(ctx, p.repoID, p.pkgA)
	if err != nil {
		t.Fatalf("PackageContext(%s): %v", p.pkgA, err)
	}
	if pc.Node.Path != p.pkgA {
		t.Errorf("PackageContext.Node.Path = %q, want %q", pc.Node.Path, p.pkgA)
	}
	if len(pc.Files) != 1 || pc.Files[0].Path != p.fileA {
		t.Errorf("Files = %v, want just %s", codeMapPathsOf(pc.Files), p.fileA)
	}
	if len(pc.Imports) != 0 {
		t.Errorf("Imports = %v, want none: package a imports nothing", codeMapPathsOf(pc.Imports))
	}
	if !codeMapHasPath(pc.Importers, p.fileB) || !codeMapHasPath(pc.Importers, p.pkgB) {
		t.Errorf("Importers = %v, want the importing file %s and the importing package %s",
			codeMapPathsOf(pc.Importers), p.fileB, p.pkgB)
	}
	if _, err := fixture.Reader.PackageContext(ctx, p.repoID, p.pkgA+"-nowhere"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("PackageContext of an unknown package = %v, want storage.ErrNotFound", err)
	}
}

// RunCodeMapReaderStaleReflectsBlobDrift pins NodeRef.Stale and
// Shape.StaleSummaries in codemapops/reader.go against the sequence that
// actually produces drift: summarize a file, then rescan it with a different
// blob. The summary SURVIVES the rescan and goes stale, which is what makes
// staleness derivable at all.
func RunCodeMapReaderStaleReflectsBlobDrift(t *testing.T, ctx context.Context, fixture CodeMapReaderFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "reader-stale")
	codeMapReaderSeed(t, ctx, fixture, p, "abc", codeMapTwoPkgGraph(p))

	if _, err := fixture.Indexer.SetSummaries(ctx, p.repoID, []codemapops.Summary{
		{Path: p.fileA, Summary: "entry point", Layer: "cli", BlobHash: codeMapBlobA, Model: "m"},
	}); err != nil {
		t.Fatalf("seeding a summary: %v", err)
	}
	if stale, err := fixture.Reader.Stale(ctx, p.repoID, 10); err != nil || len(stale) != 0 {
		t.Fatalf("Stale right after summarizing = %v, %v; a fresh summary is not stale", codeMapPathsOf(stale), err)
	}

	moved := codeMapTwoPkgGraph(p)
	for i := range moved.Nodes {
		if moved.Nodes[i].Path == p.fileA {
			moved.Nodes[i].BlobHash = codeMapBlobMoved
		}
	}
	codeMapReaderSeed(t, ctx, fixture, p, "def", moved)

	stale, err := fixture.Reader.Stale(ctx, p.repoID, 10)
	if err != nil {
		t.Fatalf("Stale: %v", err)
	}
	if len(stale) != 1 || stale[0].Path != p.fileA {
		t.Fatalf("Stale = %v, want just %s", codeMapPathsOf(stale), p.fileA)
	}
	if !stale[0].Stale {
		t.Errorf("the listed node's Stale flag is false; a stale node has to say so in its own NodeRef")
	}
	shape, err := fixture.Reader.Shape(ctx, p.repoID, codemapops.ShapeOptions{})
	if err != nil {
		t.Fatalf("Shape: %v", err)
	}
	if shape.StaleSummaries != 1 {
		t.Errorf("Shape.StaleSummaries = %d, want 1", shape.StaleSummaries)
	}
	if shape.HeadSHA != "def" {
		t.Errorf("Shape.HeadSHA = %q, want the head the rescan recorded", shape.HeadSHA)
	}
}

// RunCodeMapReaderShapeRanksFanIn pins Shape in codemapops/reader.go: it is
// "the repository-wide summary bd codemap shows first: freshness, size, and the
// files with the most importers", with "TopFanIn []NodeRef // files ranked by
// importer count".
func RunCodeMapReaderShapeRanksFanIn(t *testing.T, ctx context.Context, fixture CodeMapReaderFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "reader-shape")
	codeMapReaderSeed(t, ctx, fixture, p, "abc", codeMapTwoPkgGraph(p))

	shape, err := fixture.Reader.Shape(ctx, p.repoID, codemapops.ShapeOptions{TopFiles: 10})
	if err != nil {
		t.Fatalf("Shape: %v", err)
	}
	if shape.RepoID != p.repoID || shape.HeadSHA != "abc" {
		t.Errorf("Shape identity = %q at %q, want %q at abc", shape.RepoID, shape.HeadSHA, p.repoID)
	}
	if shape.Nodes != 4 || shape.Edges != 4 {
		t.Errorf("Shape size = %d nodes and %d edges, want 4 and 4", shape.Nodes, shape.Edges)
	}
	if len(shape.TopFanIn) == 0 || shape.TopFanIn[0].Path != p.fileA {
		t.Fatalf("TopFanIn = %v, want %s first: it is the file of the only package anything imports",
			codeMapPathsOf(shape.TopFanIn), p.fileA)
	}
	if codeMapHasPath(shape.TopFanIn, p.fileB) {
		t.Errorf("TopFanIn = %v, want %s absent: nothing imports its package",
			codeMapPathsOf(shape.TopFanIn), p.fileB)
	}
	if len(shape.Layers) == 0 {
		t.Errorf("Shape.Layers is empty; every node lands in a layer, unassigned ones included")
	}
	if shape.IndexedAt.IsZero() {
		t.Errorf("Shape.IndexedAt is zero; the freshness header is the first thing bd codemap shows")
	}
}

// codeMapHasPath reports whether a NodeRef list holds a path.
func codeMapHasPath(refs []codemapops.NodeRef, path string) bool {
	for _, r := range refs {
		if r.Path == path {
			return true
		}
	}
	return false
}

// codeMapPathsOf renders a NodeRef list as its paths, so a failure message
// names what came back rather than dumping whole structs.
func codeMapPathsOf(refs []codemapops.NodeRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Path)
	}
	return out
}
