package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCodeMapReaderContract runs the Reader contract against the unit-of-work
// provider, which reaches the same …InTx bodies the two stores do through
// domain.CodeMapUseCase. What it votes on is the unit-of-work engine.
func TestCodeMapReaderContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWCodeMapReaderFixture(t, ctx, "cmr")

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

func newUOWCodeMapReaderFixture(t *testing.T, ctx context.Context, prefix string) conformance.CodeMapReaderFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	indexer, reader := uowCodeMapRoles(t, provider)
	return conformance.CodeMapReaderFixture{IssuePrefix: prefix, Indexer: indexer, Reader: reader}
}
