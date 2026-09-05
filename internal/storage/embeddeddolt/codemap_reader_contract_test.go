//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCodeMapReaderContract runs the Reader contract against the embedded
// store, which hands back the SAME body the server-backed store does (the …InTx
// functions in internal/storage/codemapops), differing only in the engine
// underneath. It is not an independent vote on the body.
func TestCodeMapReaderContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cmr")
	ctx := t.Context()
	fixture := newEmbeddedCodeMapReaderFixture(t, te)

	t.Run("FileContextBeforeBuildIsErrNotIndexed", func(t *testing.T) {
		conformance.RunCodeMapReaderFileContextBeforeBuildIsErrNotIndexed(t, ctx, fixture)
	})
	t.Run("FileContextUnknownPathIsErrNotFound", func(t *testing.T) {
		conformance.RunCodeMapReaderFileContextUnknownPathIsErrNotFound(t, ctx, fixture)
	})
	t.Run("FileContextListsImportersAndTests", func(t *testing.T) {
		conformance.RunCodeMapReaderFileContextListsImportersAndTests(t, ctx, fixture)
	})
	t.Run("PackageContextListsFiles", func(t *testing.T) {
		conformance.RunCodeMapReaderPackageContextListsFiles(t, ctx, fixture)
	})
	t.Run("StaleReflectsBlobDrift", func(t *testing.T) {
		conformance.RunCodeMapReaderStaleReflectsBlobDrift(t, ctx, fixture)
	})
	t.Run("ShapeRanksFanIn", func(t *testing.T) {
		conformance.RunCodeMapReaderShapeRanksFanIn(t, ctx, fixture)
	})
}

func newEmbeddedCodeMapReaderFixture(t *testing.T, te *testEnv) conformance.CodeMapReaderFixture {
	t.Helper()
	// Through the accessors, never the constructors.
	indexer, err := te.store.CodeMapIndexer()
	if err != nil {
		t.Fatalf("CodeMapIndexer(): %v", err)
	}
	reader, err := te.store.CodeMapReader()
	if err != nil {
		t.Fatalf("CodeMapReader(): %v", err)
	}
	return conformance.CodeMapReaderFixture{IssuePrefix: "cmr", Indexer: indexer, Reader: reader}
}
