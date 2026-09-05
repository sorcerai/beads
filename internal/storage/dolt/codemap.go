package dolt

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
	body "github.com/steveyegge/beads/internal/storage/codemapops"
)

// CodeMapIndexer, CodeMapReader and IssueFiles are the three code-map roles.
// The shared bodies they call are the InTx functions in
// internal/storage/codemapops; this file is only the transaction each one runs
// in, plus the Dolt commit a write leaves behind.
func (s *DoltStore) CodeMapIndexer() (codemapops.Indexer, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "CodeMapIndexer", Backend: "nil"}
	}
	return &codeMapIndexer{store: s}, nil
}

func (s *DoltStore) CodeMapReader() (codemapops.Reader, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "CodeMapReader", Backend: "nil"}
	}
	return &codeMapReader{store: s}, nil
}

func (s *DoltStore) IssueFiles() (codemapops.IssueFiles, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "IssueFiles", Backend: "nil"}
	}
	return &issueFiles{store: s}, nil
}

type codeMapIndexer struct{ store *DoltStore }
type codeMapReader struct{ store *DoltStore }
type issueFiles struct{ store *DoltStore }

var (
	_ codemapops.Indexer    = (*codeMapIndexer)(nil)
	_ codemapops.Reader     = (*codeMapReader)(nil)
	_ codemapops.IssueFiles = (*issueFiles)(nil)
)

// Apply writes nodes, edges and the head marker in ONE retryable transaction,
// then commits the three tables it touched. The config table is in the list
// because the head SHA the apply records lives there.
func (i *codeMapIndexer) Apply(ctx context.Context, req codemapops.ApplyRequest) (res codemapops.ApplyResult, err error) {
	err = i.store.withRetryTx(ctx, func(tx *sql.Tx) error {
		var e error
		res, e = body.ApplyInTx(ctx, tx, req, time.Now().UTC())
		return e
	})
	if err != nil {
		return res, err
	}
	return res, i.store.doltAddAndCommit(ctx, []string{"code_nodes", "code_edges", "config"}, "bd: codemap apply "+req.HeadSHA)
}

// SetSummaries skips the commit when nothing was written: a pass whose every
// summary was refused changed no row, and must leave no Dolt commit either.
func (i *codeMapIndexer) SetSummaries(ctx context.Context, repoID string, items []codemapops.Summary) (res codemapops.SetSummariesResult, err error) {
	err = i.store.withRetryTx(ctx, func(tx *sql.Tx) error {
		var e error
		res, e = body.SetSummariesInTx(ctx, tx, repoID, items, time.Now().UTC())
		return e
	})
	if err != nil || res.Written == 0 {
		return res, err
	}
	return res, i.store.doltAddAndCommit(ctx, []string{"code_nodes"}, "bd: codemap summaries")
}

func (r *codeMapReader) FileContext(ctx context.Context, repoID, path string) (out codemapops.FileContext, err error) {
	err = r.store.withReadTx(ctx, func(tx *sql.Tx) error { var e error; out, e = body.FileContextInTx(ctx, tx, repoID, path); return e })
	return
}

func (r *codeMapReader) PackageContext(ctx context.Context, repoID, pkg string) (out codemapops.PackageContext, err error) {
	err = r.store.withReadTx(ctx, func(tx *sql.Tx) error { var e error; out, e = body.PackageContextInTx(ctx, tx, repoID, pkg); return e })
	return
}

func (r *codeMapReader) Stale(ctx context.Context, repoID string, limit int) (out []codemapops.NodeRef, err error) {
	err = r.store.withReadTx(ctx, func(tx *sql.Tx) error { var e error; out, e = body.StaleInTx(ctx, tx, repoID, limit); return e })
	return
}

func (r *codeMapReader) Shape(ctx context.Context, repoID string, opts codemapops.ShapeOptions) (out codemapops.Shape, err error) {
	err = r.store.withReadTx(ctx, func(tx *sql.Tx) error { var e error; out, e = body.ShapeInTx(ctx, tx, repoID, opts); return e })
	return
}

func (f *issueFiles) Record(ctx context.Context, req codemapops.RecordRequest) (res codemapops.RecordResult, err error) {
	err = f.store.withRetryTx(ctx, func(tx *sql.Tx) error {
		var e error
		res, e = body.RecordInTx(ctx, tx, req, time.Now().UTC())
		return e
	})
	if err != nil {
		return res, err
	}
	return res, f.store.doltAddAndCommit(ctx, []string{"issue_files"}, "bd: codemap link "+req.IssueID)
}

func (f *issueFiles) ByIssue(ctx context.Context, issueID string) (out []codemapops.IssueFile, err error) {
	err = f.store.withReadTx(ctx, func(tx *sql.Tx) error { var e error; out, e = body.ByIssueInTx(ctx, tx, issueID); return e })
	return
}

func (f *issueFiles) ByPath(ctx context.Context, repoID, path string, openOnly bool) (out []codemapops.IssueRef, err error) {
	err = f.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = body.ByPathInTx(ctx, tx, repoID, path, openOnly)
		return e
	})
	return
}

func (f *issueFiles) IssueCodeContext(ctx context.Context, issueID, repoID string) (out codemapops.IssueCodeContext, err error) {
	err = f.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = body.IssueCodeContextInTx(ctx, tx, issueID, repoID)
		return e
	})
	return
}
