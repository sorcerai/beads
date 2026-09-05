package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCodeMapReaderContract runs the Reader contract against the server-backed
// store, which shares the …InTx bodies in internal/storage/codemapops with the
// embedded store. It is not an independent vote on the body.
func TestCodeMapReaderContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltCodeMapReaderFixture(t, "cmr")
	defer cleanup()

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

func newDoltCodeMapReaderFixture(t *testing.T, prefix string) (conformance.CodeMapReaderFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	// Through the accessors, never the constructors.
	indexer, err := store.CodeMapIndexer()
	if err != nil {
		stop()
		t.Fatalf("CodeMapIndexer(): %v", err)
	}
	reader, err := store.CodeMapReader()
	if err != nil {
		stop()
		t.Fatalf("CodeMapReader(): %v", err)
	}
	return conformance.CodeMapReaderFixture{IssuePrefix: prefix, Indexer: indexer, Reader: reader}, ctx, stop
}
