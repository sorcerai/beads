package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestIssueFilesContract runs the IssueFiles contract against the unit-of-work
// provider, which reaches the same …InTx bodies the two stores do through
// domain.CodeMapUseCase. What it votes on is the unit-of-work engine.
func TestIssueFilesContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWIssueFilesFixture(t, ctx, "cmf")

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

func newUOWIssueFilesFixture(t *testing.T, ctx context.Context, prefix string) conformance.IssueFilesFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewIssueFiles: a provider that
	// stopped offering the role is the regression a constructor call would hide.
	source, ok := provider.(IssueFilesSource)
	if !ok {
		t.Fatalf("provider %T does not offer the IssueFiles accessor", provider)
	}
	issueFiles, err := source.IssueFiles()
	if err != nil {
		t.Fatalf("IssueFiles(): %v", err)
	}
	indexer, _ := uowCodeMapRoles(t, provider)
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.IssueFilesFixture{
		IssuePrefix: kit.IssuePrefix,
		IssueFiles:  issueFiles,
		Indexer:     indexer,
		CreateIssue: kit.CreateIssue,
		// OUT OF BAND, past this role: the frozen role fixture kit has no delete
		// seam, and the cascade clause is a promise about what a deletion
		// outside this plane does to it. The delete result is discarded — the
		// case reads the consequence out of issue_files, not out of the verb.
		DeleteIssue: func(ctx context.Context, id string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				_, err := uw.IssueUseCase().DeleteIssue(ctx, id, "seed")
				return "bd: delete " + id, err
			})
		},
		QueryScalar: kit.QueryScalar,
	}
}
