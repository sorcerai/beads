package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCodeMapIndexerContract runs the Indexer contract against the
// server-backed store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. Every case owns its repo id, which is what makes
// that sharing safe: the code-map plane is keyed by repo id all the way down.
// setupTestStore already marks the PARENT parallel; no subtest here calls
// t.Parallel.
func TestCodeMapIndexerContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltCodeMapIndexerFixture(t, "cmi")
	defer cleanup()

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

func newDoltCodeMapIndexerFixture(t *testing.T, prefix string) (conformance.CodeMapIndexerFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	// Through the accessors, never the constructors: the accessor is where each
	// storage decorator adds its layer.
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
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.CodeMapIndexerFixture{
		IssuePrefix: kit.IssuePrefix,
		Indexer:     indexer,
		Reader:      reader,
		QueryScalar: kit.QueryScalar,
	}, ctx, stop
}
