//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCodeMapIndexerContract runs the Indexer contract against the embedded
// store, which hands back the SAME body the server-backed store does (the …InTx
// functions in internal/storage/codemapops), differing only in the engine
// underneath. It is not an independent vote on the body.
//
// One environment for the whole suite: every case owns its repo id, so the
// cases are order-independent and share one embedded database rather than
// paying a bootstrap each.
func TestCodeMapIndexerContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cmi")
	ctx := t.Context()
	fixture := newEmbeddedCodeMapIndexerFixture(t, te, "cmi")

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

func newEmbeddedCodeMapIndexerFixture(t *testing.T, te *testEnv, prefix string) conformance.CodeMapIndexerFixture {
	t.Helper()
	// Through the accessors, never the constructors: the accessor is where each
	// storage decorator adds its layer.
	indexer, err := te.store.CodeMapIndexer()
	if err != nil {
		t.Fatalf("CodeMapIndexer(): %v", err)
	}
	reader, err := te.store.CodeMapReader()
	if err != nil {
		t.Fatalf("CodeMapReader(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.CodeMapIndexerFixture{
		IssuePrefix: kit.IssuePrefix,
		Indexer:     indexer,
		Reader:      reader,
		QueryScalar: kit.QueryScalar,
	}
}
