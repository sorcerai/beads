package db

import (
	"context"
	"time"

	"github.com/steveyegge/beads/codemapops"
	body "github.com/steveyegge/beads/internal/storage/codemapops"
	"github.com/steveyegge/beads/internal/storage/domain"
)

func NewCodeMapSQLRepository(runner Runner) domain.CodeMapRepository {
	return &codeMapSQLRepositoryImpl{runner: runner}
}

type codeMapSQLRepositoryImpl struct {
	runner Runner
}

var _ domain.CodeMapRepository = (*codeMapSQLRepositoryImpl)(nil)

// Every method below runs the SHARED body UNWRAPPED, for the reason
// WalkDependencyTree gives at dependency.go: the bodies publish
// codemapops.ErrValidation, *codemapops.ErrNotIndexed and storage.ErrNotFound
// as the roles' own vocabulary, and every one of those is classified by
// errors.Is/errors.As at the front doors. A `fmt.Errorf("db: ...: %w")` would
// keep them matchable while putting this repository's name into a message the
// direct store route never decorates.

func (r *codeMapSQLRepositoryImpl) Apply(ctx context.Context, req codemapops.ApplyRequest) (codemapops.ApplyResult, error) {
	return body.ApplyInTx(ctx, r.runner, req, time.Now().UTC())
}

func (r *codeMapSQLRepositoryImpl) SetSummaries(ctx context.Context, repoID string, items []codemapops.Summary) (codemapops.SetSummariesResult, error) {
	return body.SetSummariesInTx(ctx, r.runner, repoID, items, time.Now().UTC())
}

func (r *codeMapSQLRepositoryImpl) FileContext(ctx context.Context, repoID, path string) (codemapops.FileContext, error) {
	return body.FileContextInTx(ctx, r.runner, repoID, path)
}

func (r *codeMapSQLRepositoryImpl) PackageContext(ctx context.Context, repoID, pkg string) (codemapops.PackageContext, error) {
	return body.PackageContextInTx(ctx, r.runner, repoID, pkg)
}

func (r *codeMapSQLRepositoryImpl) Stale(ctx context.Context, repoID string, limit int) ([]codemapops.NodeRef, error) {
	return body.StaleInTx(ctx, r.runner, repoID, limit)
}

func (r *codeMapSQLRepositoryImpl) Shape(ctx context.Context, repoID string, opts codemapops.ShapeOptions) (codemapops.Shape, error) {
	return body.ShapeInTx(ctx, r.runner, repoID, opts)
}

func (r *codeMapSQLRepositoryImpl) Record(ctx context.Context, req codemapops.RecordRequest) (codemapops.RecordResult, error) {
	return body.RecordInTx(ctx, r.runner, req, time.Now().UTC())
}

func (r *codeMapSQLRepositoryImpl) ByIssue(ctx context.Context, issueID string) ([]codemapops.IssueFile, error) {
	return body.ByIssueInTx(ctx, r.runner, issueID)
}

func (r *codeMapSQLRepositoryImpl) ByPath(ctx context.Context, repoID, path string, openOnly bool) ([]codemapops.IssueRef, error) {
	return body.ByPathInTx(ctx, r.runner, repoID, path, openOnly)
}

func (r *codeMapSQLRepositoryImpl) IssueCodeContext(ctx context.Context, issueID, repoID string) (codemapops.IssueCodeContext, error) {
	return body.IssueCodeContextInTx(ctx, r.runner, issueID, repoID)
}
