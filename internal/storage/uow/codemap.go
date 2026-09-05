package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/codemapops"
)

// CodeMapIndexerSource, CodeMapReaderSource and IssueFilesSource are the
// capability accessors a unit-of-work provider offers for the three code-map
// roles, the siblings of MemoriesSource and TreeWalkerSource.
type CodeMapIndexerSource interface {
	CodeMapIndexer() (codemapops.Indexer, error)
}

type CodeMapReaderSource interface {
	CodeMapReader() (codemapops.Reader, error)
}

type IssueFilesSource interface {
	IssueFiles() (codemapops.IssueFiles, error)
}

// CodeMapIndexer, CodeMapReader and IssueFiles return the guarded code-map
// surfaces for this provider.
func (p *doltSQLProvider) CodeMapIndexer() (codemapops.Indexer, error) { return NewCodeMapIndexer(p) }
func (p *doltSQLProvider) CodeMapReader() (codemapops.Reader, error)   { return NewCodeMapReader(p) }
func (p *doltSQLProvider) IssueFiles() (codemapops.IssueFiles, error)  { return NewIssueFiles(p) }

// NewCodeMapIndexer constructs a public code-map indexer backed by provider.
func NewCodeMapIndexer(provider UnitOfWorkProvider) (codemapops.Indexer, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new code map indexer: unit-of-work provider must not be nil")
	}
	return &uowIndexer{provider: provider}, nil
}

// NewCodeMapReader constructs a public code-map reader backed by provider.
func NewCodeMapReader(provider UnitOfWorkProvider) (codemapops.Reader, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new code map reader: unit-of-work provider must not be nil")
	}
	return &uowReader{provider: provider}, nil
}

// NewIssueFiles constructs a public issue-to-path binding backed by provider.
func NewIssueFiles(provider UnitOfWorkProvider) (codemapops.IssueFiles, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new issue files: unit-of-work provider must not be nil")
	}
	return &uowIssueFiles{provider: provider}, nil
}

type uowIndexer struct{ provider UnitOfWorkProvider }
type uowReader struct{ provider UnitOfWorkProvider }
type uowIssueFiles struct{ provider UnitOfWorkProvider }

var (
	_ codemapops.Indexer    = (*uowIndexer)(nil)
	_ codemapops.Reader     = (*uowReader)(nil)
	_ codemapops.IssueFiles = (*uowIssueFiles)(nil)
)

// Apply runs the whole replace — nodes, edges, orphan prune and head marker —
// in ONE write unit of work, which is what makes a partial rescan either
// wholly visible or not visible at all.
func (i *uowIndexer) Apply(ctx context.Context, req codemapops.ApplyRequest) (codemapops.ApplyResult, error) {
	return RunTxResult(ctx, i.provider, func(ctx context.Context, uw UnitOfWork) (codemapops.ApplyResult, string, error) {
		res, err := uw.CodeMapUseCase().Apply(ctx, req)
		return res, "bd: codemap apply " + req.HeadSHA, err
	})
}

// SetSummaries returns an EMPTY commit message when nothing was written, which
// is how this layer says "commit nothing" (uow/memories.go Forget). A pass
// whose every summary was refused changed no row, and the direct store route
// skips its Dolt commit for the same reason.
func (i *uowIndexer) SetSummaries(ctx context.Context, repoID string, items []codemapops.Summary) (codemapops.SetSummariesResult, error) {
	return RunTxResult(ctx, i.provider, func(ctx context.Context, uw UnitOfWork) (codemapops.SetSummariesResult, string, error) {
		res, err := uw.CodeMapUseCase().SetSummaries(ctx, repoID, items)
		if err != nil || res.Written == 0 {
			return res, "", err
		}
		return res, "bd: codemap summaries", nil
	})
}

func (r *uowReader) FileContext(ctx context.Context, repoID, path string) (codemapops.FileContext, error) {
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (codemapops.FileContext, error) {
		return uw.CodeMapUseCase().FileContext(ctx, repoID, path)
	})
}

func (r *uowReader) PackageContext(ctx context.Context, repoID, pkg string) (codemapops.PackageContext, error) {
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (codemapops.PackageContext, error) {
		return uw.CodeMapUseCase().PackageContext(ctx, repoID, pkg)
	})
}

func (r *uowReader) Stale(ctx context.Context, repoID string, limit int) ([]codemapops.NodeRef, error) {
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) ([]codemapops.NodeRef, error) {
		return uw.CodeMapUseCase().Stale(ctx, repoID, limit)
	})
}

func (r *uowReader) Shape(ctx context.Context, repoID string, opts codemapops.ShapeOptions) (codemapops.Shape, error) {
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (codemapops.Shape, error) {
		return uw.CodeMapUseCase().Shape(ctx, repoID, opts)
	})
}

func (f *uowIssueFiles) Record(ctx context.Context, req codemapops.RecordRequest) (codemapops.RecordResult, error) {
	return RunTxResult(ctx, f.provider, func(ctx context.Context, uw UnitOfWork) (codemapops.RecordResult, string, error) {
		res, err := uw.CodeMapUseCase().Record(ctx, req)
		return res, "bd: codemap link " + req.IssueID, err
	})
}

func (f *uowIssueFiles) ByIssue(ctx context.Context, issueID string) ([]codemapops.IssueFile, error) {
	return RunTxRead(ctx, f.provider, func(ctx context.Context, uw UnitOfWork) ([]codemapops.IssueFile, error) {
		return uw.CodeMapUseCase().ByIssue(ctx, issueID)
	})
}

func (f *uowIssueFiles) ByPath(ctx context.Context, repoID, path string, openOnly bool) ([]codemapops.IssueRef, error) {
	return RunTxRead(ctx, f.provider, func(ctx context.Context, uw UnitOfWork) ([]codemapops.IssueRef, error) {
		return uw.CodeMapUseCase().ByPath(ctx, repoID, path, openOnly)
	})
}

// IssueCodeContext answers the whole "what code does this issue touch" question
// in ONE read unit of work: the shape header, every touched file's context and
// the sibling issues on each path describe one moment rather than several.
func (f *uowIssueFiles) IssueCodeContext(ctx context.Context, issueID, repoID string) (codemapops.IssueCodeContext, error) {
	return RunTxRead(ctx, f.provider, func(ctx context.Context, uw UnitOfWork) (codemapops.IssueCodeContext, error) {
		return uw.CodeMapUseCase().IssueCodeContext(ctx, issueID, repoID)
	})
}
