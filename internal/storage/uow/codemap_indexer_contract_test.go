package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/codemapops"
)

// TestCodeMapIndexerContract runs the Indexer contract against the
// unit-of-work provider. Unlike the memory plane, this leg is NOT an
// independent vote on the body: it reaches the same …InTx functions in
// internal/storage/codemapops that the two stores do, through
// domain.CodeMapUseCase and db.NewCodeMapSQLRepository. What it votes on is the
// unit-of-work engine around them.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so every plane is database-global. Every case owns its
// repo id, which is what keeps the suite order-independent anyway.
func TestCodeMapIndexerContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWCodeMapIndexerFixture(t, ctx, "cmi")

	t.Run("ApplyRefusesInvalidGraph", func(t *testing.T) {
		conformance.RunCodeMapIndexerApplyRefusesInvalidGraph(t, ctx, fixture)
	})
	t.Run("ApplyIsIdempotent", func(t *testing.T) {
		conformance.RunCodeMapIndexerApplyIsIdempotent(t, ctx, fixture)
	})
	t.Run("ApplyWholeRepoDeletesAbsent", func(t *testing.T) {
		conformance.RunCodeMapIndexerApplyWholeRepoDeletesAbsent(t, ctx, fixture)
	})
	t.Run("ApplyIncrementalRefusesUnbuiltRepo", func(t *testing.T) {
		conformance.RunCodeMapIndexerApplyIncrementalRefusesUnbuiltRepo(t, ctx, fixture)
	})
	t.Run("ApplyIncrementalScopesDeletion", func(t *testing.T) {
		conformance.RunCodeMapIndexerApplyIncrementalScopesDeletion(t, ctx, fixture)
	})
	t.Run("ApplyIncrementalKeepsCrossScopeEdges", func(t *testing.T) {
		conformance.RunCodeMapIndexerApplyIncrementalKeepsCrossScopeEdges(t, ctx, fixture)
	})
	t.Run("ApplyPrunesOrphanEdges", func(t *testing.T) {
		conformance.RunCodeMapIndexerApplyPrunesOrphanEdges(t, ctx, fixture)
	})
	t.Run("SetSummariesRefusesMovedBlob", func(t *testing.T) {
		conformance.RunCodeMapIndexerSetSummariesRefusesMovedBlob(t, ctx, fixture)
	})
	t.Run("SetSummariesRefusesOverlongSummary", func(t *testing.T) {
		conformance.RunCodeMapIndexerSetSummariesRefusesOverlongSummary(t, ctx, fixture)
	})
}

func newUOWCodeMapIndexerFixture(t *testing.T, ctx context.Context, prefix string) conformance.CodeMapIndexerFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	indexer, reader := uowCodeMapRoles(t, provider)
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.CodeMapIndexerFixture{
		IssuePrefix: kit.IssuePrefix,
		Indexer:     indexer,
		Reader:      reader,
		QueryScalar: kit.QueryScalar,
	}
}

// uowCodeMapRoles resolves the two code-map read/write roles through the
// capability accessors, not NewCodeMapIndexer/NewCodeMapReader: a provider that
// stopped offering a role is the regression a constructor call would hide.
func uowCodeMapRoles(t *testing.T, provider UnitOfWorkProvider) (codemapops.Indexer, codemapops.Reader) {
	t.Helper()
	indexerSource, ok := provider.(CodeMapIndexerSource)
	if !ok {
		t.Fatalf("provider %T does not offer the CodeMapIndexer accessor", provider)
	}
	readerSource, ok := provider.(CodeMapReaderSource)
	if !ok {
		t.Fatalf("provider %T does not offer the CodeMapReader accessor", provider)
	}
	indexer, err := indexerSource.CodeMapIndexer()
	if err != nil {
		t.Fatalf("CodeMapIndexer(): %v", err)
	}
	reader, err := readerSource.CodeMapReader()
	if err != nil {
		t.Fatalf("CodeMapReader(): %v", err)
	}
	return indexer, reader
}
