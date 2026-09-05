package telemetry

import (
	"context"

	"github.com/steveyegge/beads/codemapops"
)

// CodeMapIndexer, CodeMapReader and IssueFiles return the inner store's
// code-map roles wrapped in this layer's instrumentation. They RECURSE instead
// of delegating for the reason Memories does: a blind delegation would return
// the inner surface unspanned and untimed.
func (s *InstrumentedStorage) CodeMapIndexer() (codemapops.Indexer, error) {
	inner, err := s.Unwrap().CodeMapIndexer()
	if err != nil {
		return nil, err
	}
	return s.WrapCodeMapIndexer(inner), nil
}

func (s *InstrumentedStorage) CodeMapReader() (codemapops.Reader, error) {
	inner, err := s.Unwrap().CodeMapReader()
	if err != nil {
		return nil, err
	}
	return s.WrapCodeMapReader(inner), nil
}

func (s *InstrumentedStorage) IssueFiles() (codemapops.IssueFiles, error) {
	inner, err := s.Unwrap().IssueFiles()
	if err != nil {
		return nil, err
	}
	return s.WrapIssueFiles(inner), nil
}

// WrapCodeMapIndexer instruments code-map writes with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapCodeMapIndexer(inner codemapops.Indexer) codemapops.Indexer {
	return &instrumentedCodeMapIndexer{storage: s, inner: inner}
}

// WrapCodeMapReader instruments code-map reads with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapCodeMapReader(inner codemapops.Reader) codemapops.Reader {
	return &instrumentedCodeMapReader{storage: s, inner: inner}
}

// WrapIssueFiles instruments issue-to-path binding with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapIssueFiles(inner codemapops.IssueFiles) codemapops.IssueFiles {
	return &instrumentedIssueFiles{storage: s, inner: inner}
}

type instrumentedCodeMapIndexer struct {
	storage *InstrumentedStorage
	inner   codemapops.Indexer
}

type instrumentedCodeMapReader struct {
	storage *InstrumentedStorage
	inner   codemapops.Reader
}

type instrumentedIssueFiles struct {
	storage *InstrumentedStorage
	inner   codemapops.IssueFiles
}

func (i *instrumentedCodeMapIndexer) Apply(ctx context.Context, request codemapops.ApplyRequest) (result codemapops.ApplyResult, err error) {
	ctx, span, started := i.storage.op(ctx, "CodeMapIndexer.Apply")
	result, err = i.inner.Apply(ctx, request)
	i.storage.done(ctx, span, started, err)
	return result, err
}

func (i *instrumentedCodeMapIndexer) SetSummaries(ctx context.Context, repoID string, items []codemapops.Summary) (result codemapops.SetSummariesResult, err error) {
	ctx, span, started := i.storage.op(ctx, "CodeMapIndexer.SetSummaries")
	result, err = i.inner.SetSummaries(ctx, repoID, items)
	i.storage.done(ctx, span, started, err)
	return result, err
}

func (r *instrumentedCodeMapReader) FileContext(ctx context.Context, repoID, path string) (result codemapops.FileContext, err error) {
	ctx, span, started := r.storage.op(ctx, "CodeMapReader.FileContext")
	result, err = r.inner.FileContext(ctx, repoID, path)
	r.storage.done(ctx, span, started, err)
	return result, err
}

func (r *instrumentedCodeMapReader) PackageContext(ctx context.Context, repoID, pkg string) (result codemapops.PackageContext, err error) {
	ctx, span, started := r.storage.op(ctx, "CodeMapReader.PackageContext")
	result, err = r.inner.PackageContext(ctx, repoID, pkg)
	r.storage.done(ctx, span, started, err)
	return result, err
}

func (r *instrumentedCodeMapReader) Stale(ctx context.Context, repoID string, limit int) (result []codemapops.NodeRef, err error) {
	ctx, span, started := r.storage.op(ctx, "CodeMapReader.Stale")
	result, err = r.inner.Stale(ctx, repoID, limit)
	r.storage.done(ctx, span, started, err)
	return result, err
}

func (r *instrumentedCodeMapReader) Shape(ctx context.Context, repoID string, opts codemapops.ShapeOptions) (result codemapops.Shape, err error) {
	ctx, span, started := r.storage.op(ctx, "CodeMapReader.Shape")
	result, err = r.inner.Shape(ctx, repoID, opts)
	r.storage.done(ctx, span, started, err)
	return result, err
}

func (f *instrumentedIssueFiles) Record(ctx context.Context, request codemapops.RecordRequest) (result codemapops.RecordResult, err error) {
	ctx, span, started := f.storage.op(ctx, "IssueFiles.Record")
	result, err = f.inner.Record(ctx, request)
	f.storage.done(ctx, span, started, err)
	return result, err
}

func (f *instrumentedIssueFiles) ByIssue(ctx context.Context, issueID string) (result []codemapops.IssueFile, err error) {
	ctx, span, started := f.storage.op(ctx, "IssueFiles.ByIssue")
	result, err = f.inner.ByIssue(ctx, issueID)
	f.storage.done(ctx, span, started, err)
	return result, err
}

func (f *instrumentedIssueFiles) ByPath(ctx context.Context, repoID, path string, openOnly bool) (result []codemapops.IssueRef, err error) {
	ctx, span, started := f.storage.op(ctx, "IssueFiles.ByPath")
	result, err = f.inner.ByPath(ctx, repoID, path, openOnly)
	f.storage.done(ctx, span, started, err)
	return result, err
}

func (f *instrumentedIssueFiles) IssueCodeContext(ctx context.Context, issueID, repoID string) (result codemapops.IssueCodeContext, err error) {
	ctx, span, started := f.storage.op(ctx, "IssueFiles.IssueCodeContext")
	result, err = f.inner.IssueCodeContext(ctx, issueID, repoID)
	f.storage.done(ctx, span, started, err)
	return result, err
}
