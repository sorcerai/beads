package domain

import (
	"context"

	"github.com/steveyegge/beads/codemapops"
)

// CodeMapRepository is the persistence surface for the three code-map roles.
// It is ONE repository rather than three because the ten operations share one
// pair of tables plus the issue_files binding, and a unit of work hands them
// all the same transaction.
type CodeMapRepository interface {
	Apply(ctx context.Context, req codemapops.ApplyRequest) (codemapops.ApplyResult, error)
	SetSummaries(ctx context.Context, repoID string, items []codemapops.Summary) (codemapops.SetSummariesResult, error)
	FileContext(ctx context.Context, repoID, path string) (codemapops.FileContext, error)
	PackageContext(ctx context.Context, repoID, pkg string) (codemapops.PackageContext, error)
	Stale(ctx context.Context, repoID string, limit int) ([]codemapops.NodeRef, error)
	Shape(ctx context.Context, repoID string, opts codemapops.ShapeOptions) (codemapops.Shape, error)
	Record(ctx context.Context, req codemapops.RecordRequest) (codemapops.RecordResult, error)
	ByIssue(ctx context.Context, issueID string) ([]codemapops.IssueFile, error)
	ByPath(ctx context.Context, repoID, path string, openOnly bool) ([]codemapops.IssueRef, error)
	IssueCodeContext(ctx context.Context, issueID, repoID string) (codemapops.IssueCodeContext, error)
}

// CodeMapUseCase is THIN ON PURPOSE: it is the repository surface and nothing
// more. Every rule this plane has — the validation, the ordering of the apply,
// the refusal of a summary whose blob hash moved — already lives in the shared
// InTx bodies the repository calls, which is what keeps the direct store route
// and this one answering identically. A rule added here would be a rule only
// the proxied route obeys.
type CodeMapUseCase interface {
	CodeMapRepository
}

func NewCodeMapUseCase(repo CodeMapRepository) CodeMapUseCase {
	return &codeMapUseCaseImpl{repo: repo}
}

type codeMapUseCaseImpl struct {
	repo CodeMapRepository
}

var _ CodeMapUseCase = (*codeMapUseCaseImpl)(nil)

func (u *codeMapUseCaseImpl) Apply(ctx context.Context, req codemapops.ApplyRequest) (codemapops.ApplyResult, error) {
	return u.repo.Apply(ctx, req)
}

func (u *codeMapUseCaseImpl) SetSummaries(ctx context.Context, repoID string, items []codemapops.Summary) (codemapops.SetSummariesResult, error) {
	return u.repo.SetSummaries(ctx, repoID, items)
}

func (u *codeMapUseCaseImpl) FileContext(ctx context.Context, repoID, path string) (codemapops.FileContext, error) {
	return u.repo.FileContext(ctx, repoID, path)
}

func (u *codeMapUseCaseImpl) PackageContext(ctx context.Context, repoID, pkg string) (codemapops.PackageContext, error) {
	return u.repo.PackageContext(ctx, repoID, pkg)
}

func (u *codeMapUseCaseImpl) Stale(ctx context.Context, repoID string, limit int) ([]codemapops.NodeRef, error) {
	return u.repo.Stale(ctx, repoID, limit)
}

func (u *codeMapUseCaseImpl) Shape(ctx context.Context, repoID string, opts codemapops.ShapeOptions) (codemapops.Shape, error) {
	return u.repo.Shape(ctx, repoID, opts)
}

func (u *codeMapUseCaseImpl) Record(ctx context.Context, req codemapops.RecordRequest) (codemapops.RecordResult, error) {
	return u.repo.Record(ctx, req)
}

func (u *codeMapUseCaseImpl) ByIssue(ctx context.Context, issueID string) ([]codemapops.IssueFile, error) {
	return u.repo.ByIssue(ctx, issueID)
}

func (u *codeMapUseCaseImpl) ByPath(ctx context.Context, repoID, path string, openOnly bool) ([]codemapops.IssueRef, error) {
	return u.repo.ByPath(ctx, repoID, path, openOnly)
}

func (u *codeMapUseCaseImpl) IssueCodeContext(ctx context.Context, issueID, repoID string) (codemapops.IssueCodeContext, error) {
	return u.repo.IssueCodeContext(ctx, issueID, repoID)
}
