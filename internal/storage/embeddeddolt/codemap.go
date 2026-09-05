//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
	body "github.com/steveyegge/beads/internal/storage/codemapops"
)

// CodeMapIndexer, CodeMapReader and IssueFiles are the three code-map roles on
// the embedded engine. The bodies are the same InTx functions the server-backed
// store calls; only the transaction differs — withConn(ctx, true, …) for a
// write and withConn(ctx, false, …) for a read.
//
// NOTHING HERE CALLS doltAddAndCommit, and that is the same decision
// memories.go records beside it: on this route durability comes from the CLI's
// auto-commit epilogue, not from a per-operation commit inside the store.
func (s *EmbeddedDoltStore) CodeMapIndexer() (codemapops.Indexer, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "CodeMapIndexer", Backend: "nil"}
	}
	return &codeMapIndexer{store: s}, nil
}

func (s *EmbeddedDoltStore) CodeMapReader() (codemapops.Reader, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "CodeMapReader", Backend: "nil"}
	}
	return &codeMapReader{store: s}, nil
}

func (s *EmbeddedDoltStore) IssueFiles() (codemapops.IssueFiles, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "IssueFiles", Backend: "nil"}
	}
	return &issueFiles{store: s}, nil
}

type codeMapIndexer struct{ store *EmbeddedDoltStore }
type codeMapReader struct{ store *EmbeddedDoltStore }
type issueFiles struct{ store *EmbeddedDoltStore }

var (
	_ codemapops.Indexer    = (*codeMapIndexer)(nil)
	_ codemapops.Reader     = (*codeMapReader)(nil)
	_ codemapops.IssueFiles = (*issueFiles)(nil)
)

func (i *codeMapIndexer) Apply(ctx context.Context, req codemapops.ApplyRequest) (res codemapops.ApplyResult, err error) {
	err = i.store.withConn(ctx, true, func(tx *sql.Tx) error {
		var e error
		res, e = body.ApplyInTx(ctx, tx, req, time.Now().UTC())
		return e
	})
	return
}

func (i *codeMapIndexer) SetSummaries(ctx context.Context, repoID string, items []codemapops.Summary) (res codemapops.SetSummariesResult, err error) {
	err = i.store.withConn(ctx, true, func(tx *sql.Tx) error {
		var e error
		res, e = body.SetSummariesInTx(ctx, tx, repoID, items, time.Now().UTC())
		return e
	})
	return
}

func (r *codeMapReader) FileContext(ctx context.Context, repoID, path string) (out codemapops.FileContext, err error) {
	err = r.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var e error
		out, e = body.FileContextInTx(ctx, tx, repoID, path)
		return e
	})
	return
}

func (r *codeMapReader) PackageContext(ctx context.Context, repoID, pkg string) (out codemapops.PackageContext, err error) {
	err = r.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var e error
		out, e = body.PackageContextInTx(ctx, tx, repoID, pkg)
		return e
	})
	return
}

func (r *codeMapReader) Stale(ctx context.Context, repoID string, limit int) (out []codemapops.NodeRef, err error) {
	err = r.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var e error
		out, e = body.StaleInTx(ctx, tx, repoID, limit)
		return e
	})
	return
}

func (r *codeMapReader) Shape(ctx context.Context, repoID string, opts codemapops.ShapeOptions) (out codemapops.Shape, err error) {
	err = r.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var e error
		out, e = body.ShapeInTx(ctx, tx, repoID, opts)
		return e
	})
	return
}

func (f *issueFiles) Record(ctx context.Context, req codemapops.RecordRequest) (res codemapops.RecordResult, err error) {
	err = f.store.withConn(ctx, true, func(tx *sql.Tx) error {
		var e error
		res, e = body.RecordInTx(ctx, tx, req, time.Now().UTC())
		return e
	})
	return
}

func (f *issueFiles) ByIssue(ctx context.Context, issueID string) (out []codemapops.IssueFile, err error) {
	err = f.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var e error
		out, e = body.ByIssueInTx(ctx, tx, issueID)
		return e
	})
	return
}

func (f *issueFiles) ByPath(ctx context.Context, repoID, path string, openOnly bool) (out []codemapops.IssueRef, err error) {
	err = f.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var e error
		out, e = body.ByPathInTx(ctx, tx, repoID, path, openOnly)
		return e
	})
	return
}

func (f *issueFiles) IssueCodeContext(ctx context.Context, issueID, repoID string) (out codemapops.IssueCodeContext, err error) {
	err = f.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var e error
		out, e = body.IssueCodeContextInTx(ctx, tx, issueID, repoID)
		return e
	})
	return
}
