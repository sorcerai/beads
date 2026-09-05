package codemapops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

const nodeCols = "id, repo_id, kind, path, path_hash, name, package_id, lang, blob_hash, loc, is_test, exported, layer, summary, tags, summary_blob_hash, summary_model, summarized_at, indexed_at"

func scanNode(sc interface{ Scan(...any) error }) (nodeRow, error) {
	var r nodeRow
	err := sc.Scan(&r.ID, &r.RepoID, &r.Kind, &r.Path, &r.PathHash, &r.Name, &r.PackageID, &r.Lang, &r.BlobHash, &r.LOC, &r.IsTest, &r.Exported,
		&r.Layer, &r.Summary, &r.Tags, &r.SummaryBlobHash, &r.SummaryModel, &r.SummarizedAt, &r.IndexedAt)
	return r, err
}

func requireIndexed(ctx context.Context, tx issueops.DBTX, repoID string) (string, error) {
	sha, err := LastSHAInTx(ctx, tx, repoID)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "", &codemapops.ErrNotIndexed{RepoID: repoID}
	}
	return sha, nil
}

func nodeByPath(ctx context.Context, tx issueops.DBTX, repoID string, kind codemapops.NodeKind, path string) (nodeRow, error) {
	r, err := scanNode(tx.QueryRowContext(ctx, "SELECT "+nodeCols+" FROM code_nodes WHERE id = ?", NodeID(repoID, kind, path)))
	if err == sql.ErrNoRows {
		return r, fmt.Errorf("%w: %s %q", storage.ErrNotFound, kind, path)
	}
	return r, err
}

func nodesWhere(ctx context.Context, tx issueops.DBTX, where string, args ...any) ([]nodeRow, error) {
	return nodesQuery(ctx, tx, where, "", args...)
}

// nodesQuery is nodesWhere with a tail appended after the ORDER BY, which is
// where a LIMIT has to go.
func nodesQuery(ctx context.Context, tx issueops.DBTX, where, tail string, args ...any) ([]nodeRow, error) {
	rows, err := tx.QueryContext(ctx, "SELECT "+nodeCols+" FROM code_nodes WHERE "+where+" ORDER BY path"+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nodeRow
	for rows.Next() {
		r, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func refs(rows []nodeRow) []codemapops.NodeRef {
	out := make([]codemapops.NodeRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, refOf(r))
	}
	return out
}

// FileContextInTx answers "what surrounds this file": its package, what it
// imports, what imports its package, and its tests.
func FileContextInTx(ctx context.Context, tx issueops.DBTX, repoID, path string) (codemapops.FileContext, error) {
	var fc codemapops.FileContext
	if _, err := requireIndexed(ctx, tx, repoID); err != nil {
		return fc, err
	}
	n, err := nodeByPath(ctx, tx, repoID, codemapops.NodeFile, path)
	if err != nil {
		return fc, err
	}
	fc.Node = refOf(n)
	if n.PackageID.Valid {
		pk, err := nodesWhere(ctx, tx, "id = ?", n.PackageID.String)
		if err != nil {
			return fc, err
		}
		if len(pk) == 1 {
			fc.Package = refOf(pk[0])
		}
	}
	imports, err := nodesWhere(ctx, tx, "id IN (SELECT dst_id FROM code_edges WHERE repo_id = ? AND src_id = ? AND kind = 'imports')", repoID, n.ID)
	if err != nil {
		return fc, err
	}
	fc.Imports = refs(imports)
	if n.PackageID.Valid {
		importers, err := nodesWhere(ctx, tx, "id IN (SELECT src_id FROM code_edges WHERE repo_id = ? AND dst_id = ? AND kind = 'imports')", repoID, n.PackageID.String)
		if err != nil {
			return fc, err
		}
		fc.Importers = refs(importers)
		tests, err := nodesWhere(ctx, tx, "id IN (SELECT src_id FROM code_edges WHERE repo_id = ? AND dst_id = ? AND kind = 'tests')", repoID, n.PackageID.String)
		if err != nil {
			return fc, err
		}
		fc.Tests = refs(tests)
	}
	return fc, nil
}

// PackageContextInTx is FileContextInTx scoped to a package: its files, what
// it imports and what imports it.
func PackageContextInTx(ctx context.Context, tx issueops.DBTX, repoID, pkg string) (codemapops.PackageContext, error) {
	var pc codemapops.PackageContext
	if _, err := requireIndexed(ctx, tx, repoID); err != nil {
		return pc, err
	}
	n, err := nodeByPath(ctx, tx, repoID, codemapops.NodePackage, pkg)
	if err != nil {
		return pc, err
	}
	pc.Node = refOf(n)
	files, err := nodesWhere(ctx, tx, "package_id = ?", n.ID)
	if err != nil {
		return pc, err
	}
	pc.Files = refs(files)
	imports, err := nodesWhere(ctx, tx, "id IN (SELECT dst_id FROM code_edges WHERE repo_id = ? AND src_id = ? AND kind = 'imports')", repoID, n.ID)
	if err != nil {
		return pc, err
	}
	pc.Imports = refs(imports)
	importers, err := nodesWhere(ctx, tx, "id IN (SELECT src_id FROM code_edges WHERE repo_id = ? AND dst_id = ? AND kind = 'imports')", repoID, n.ID)
	if err != nil {
		return pc, err
	}
	pc.Importers = refs(importers)
	return pc, nil
}

// StaleInTx lists summarized files whose blob has moved since the summary was
// written.
func StaleInTx(ctx context.Context, tx issueops.DBTX, repoID string, limit int) ([]codemapops.NodeRef, error) {
	if _, err := requireIndexed(ctx, tx, repoID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := nodesQuery(ctx, tx, "repo_id = ? AND kind = 'file' AND summary IS NOT NULL AND summary <> '' AND (summary_blob_hash IS NULL OR summary_blob_hash <> blob_hash)",
		" LIMIT "+fmt.Sprint(limit), repoID)
	if err != nil {
		return nil, err
	}
	return refs(rows), nil
}

// ShapeInTx summarizes the whole repository: freshness, size, layers and the
// files with the most importers.
func ShapeInTx(ctx context.Context, tx issueops.DBTX, repoID string, opts codemapops.ShapeOptions) (codemapops.Shape, error) {
	sha, err := requireIndexed(ctx, tx, repoID)
	if err != nil {
		return codemapops.Shape{}, err
	}
	nodes, err := nodesWhere(ctx, tx, "repo_id = ?", repoID)
	if err != nil {
		return codemapops.Shape{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT repo_id, src_id, dst_id, kind, weight FROM code_edges WHERE repo_id = ?", repoID)
	if err != nil {
		return codemapops.Shape{}, err
	}
	defer rows.Close()
	var edges []edgeRow
	for rows.Next() {
		var e edgeRow
		if err := rows.Scan(&e.RepoID, &e.SrcID, &e.DstID, &e.Kind, &e.Weight); err != nil {
			return codemapops.Shape{}, err
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return codemapops.Shape{}, err
	}
	shape := BuildShape(nodes, edges, opts)
	shape.RepoID, shape.HeadSHA = repoID, sha
	return shape, nil
}
