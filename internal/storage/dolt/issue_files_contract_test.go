package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestIssueFilesContract runs the IssueFiles contract against the server-backed
// store, which shares the …InTx bodies in internal/storage/codemapops with the
// embedded store. It is not an independent vote on the body.
func TestIssueFilesContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueFilesFixture(t, "cmf")
	defer cleanup()

	t.Run("RecordRefusesUnknownIssue", func(t *testing.T) {
		conformance.RunIssueFilesRecordRefusesUnknownIssue(t, ctx, fixture)
	})
	t.Run("RecordRefusesEscapingPath", func(t *testing.T) {
		conformance.RunIssueFilesRecordRefusesEscapingPath(t, ctx, fixture)
	})
	t.Run("RecordRefusesBadSHA", func(t *testing.T) {
		conformance.RunIssueFilesRecordRefusesBadSHA(t, ctx, fixture)
	})
	t.Run("RecordUpgradesEvidence", func(t *testing.T) {
		conformance.RunIssueFilesRecordUpgradesEvidence(t, ctx, fixture)
	})
	t.Run("ByPathOpenOnlyFiltersClosed", func(t *testing.T) {
		conformance.RunIssueFilesByPathOpenOnlyFiltersClosed(t, ctx, fixture)
	})
	t.Run("IssueCodeContextWithoutMapHasNilContext", func(t *testing.T) {
		conformance.RunIssueFilesIssueCodeContextWithoutMapHasNilContext(t, ctx, fixture)
	})
	t.Run("IssueCodeContextWithMapJoins", func(t *testing.T) {
		conformance.RunIssueFilesIssueCodeContextWithMapJoins(t, ctx, fixture)
	})
	t.Run("DeletingIssueCascades", func(t *testing.T) {
		conformance.RunIssueFilesDeletingIssueCascades(t, ctx, fixture)
	})
}

func newDoltIssueFilesFixture(t *testing.T, prefix string) (conformance.IssueFilesFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	// Through the accessors, never the constructors.
	issueFiles, err := store.IssueFiles()
	if err != nil {
		stop()
		t.Fatalf("IssueFiles(): %v", err)
	}
	indexer, err := store.CodeMapIndexer()
	if err != nil {
		stop()
		t.Fatalf("CodeMapIndexer(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.IssueFilesFixture{
		IssuePrefix: kit.IssuePrefix,
		IssueFiles:  issueFiles,
		Indexer:     indexer,
		CreateIssue: kit.CreateIssue,
		// OUT OF BAND, over the store's own delete: the frozen role fixture kit
		// has no delete seam, and the cascade clause is a promise about what a
		// deletion outside this plane does to it.
		DeleteIssue: func(ctx context.Context, id string) error { return store.DeleteIssue(ctx, id) },
		QueryScalar: kit.QueryScalar,
	}, ctx, stop
}
