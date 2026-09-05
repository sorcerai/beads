//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestIssueFilesContract runs the IssueFiles contract against the embedded
// store, which hands back the SAME body the server-backed store does (the …InTx
// functions in internal/storage/codemapops), differing only in the engine
// underneath. It is not an independent vote on the body.
func TestIssueFilesContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cmf")
	ctx := t.Context()
	fixture := newEmbeddedIssueFilesFixture(t, te, "cmf")

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

func newEmbeddedIssueFilesFixture(t *testing.T, te *testEnv, prefix string) conformance.IssueFilesFixture {
	t.Helper()
	// Through the accessors, never the constructors.
	issueFiles, err := te.store.IssueFiles()
	if err != nil {
		t.Fatalf("IssueFiles(): %v", err)
	}
	indexer, err := te.store.CodeMapIndexer()
	if err != nil {
		t.Fatalf("CodeMapIndexer(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.IssueFilesFixture{
		IssuePrefix: kit.IssuePrefix,
		IssueFiles:  issueFiles,
		Indexer:     indexer,
		CreateIssue: kit.CreateIssue,
		// OUT OF BAND, over the store's own delete: the frozen role fixture kit
		// has no delete seam, and the cascade clause is a promise about what a
		// deletion outside this plane does to it.
		DeleteIssue: func(ctx context.Context, id string) error { return te.store.DeleteIssue(ctx, id) },
		QueryScalar: kit.QueryScalar,
	}
}
