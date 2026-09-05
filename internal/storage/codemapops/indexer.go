package codemapops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

const lastSHAKey = "codemap.%s.last_sha"
const upsertChunk = 500

// ApplyInTx replaces a repository's code map (or, with req.Only, the packages
// named there) with a freshly scanned graph and records the head it was built
// from.
func ApplyInTx(ctx context.Context, tx issueops.DBTX, req codemapops.ApplyRequest, now time.Time) (codemapops.ApplyResult, error) {
	var res codemapops.ApplyResult
	if req.RepoID == "" || req.HeadSHA == "" {
		return res, fmt.Errorf("%w: repo id and head sha are required", codemapops.ErrValidation)
	}
	if err := req.Graph.Validate(); err != nil {
		return res, err
	}
	if req.Only != nil {
		if sha, err := LastSHAInTx(ctx, tx, req.RepoID); err != nil {
			return res, err
		} else if sha == "" {
			return res, &codemapops.ErrNotIndexed{RepoID: req.RepoID}
		}
	}
	// 1. upsert nodes
	idOf := func(kind codemapops.NodeKind, path string) string { return NodeID(req.RepoID, kind, path) }
	keep := make([]string, 0, len(req.Graph.Nodes))
	rows := make([]nodeRow, 0, len(req.Graph.Nodes))
	for _, n := range req.Graph.Nodes {
		r := nodeRow{ID: idOf(n.Kind, n.Path), RepoID: req.RepoID, Kind: n.Kind, Path: n.Path, PathHash: PathHash(n.Path),
			Name: n.Name, Lang: n.Lang, IsTest: n.IsTest, IndexedAt: now,
			BlobHash: sql.NullString{String: n.BlobHash, Valid: n.BlobHash != ""},
			LOC:      sql.NullInt64{Int64: int64(n.LOC), Valid: n.Kind == codemapops.NodeFile},
			Exported: sql.NullInt64{Int64: int64(n.Exported), Valid: n.Kind == codemapops.NodeFile}}
		if n.PackagePath != "" {
			r.PackageID = sql.NullString{String: idOf(codemapops.NodePackage, n.PackagePath), Valid: true}
		}
		rows = append(rows, r)
		keep = append(keep, r.ID)
	}
	for i := 0; i < len(rows); i += upsertChunk {
		end := min(i+upsertChunk, len(rows))
		if err := upsertNodes(ctx, tx, rows[i:end]); err != nil {
			return res, err
		}
	}
	res.NodesUpserted = len(rows)
	// 2. delete nodes no longer present: whole repo, or only within rescanned packages
	var deleted int64
	if req.Only == nil {
		r, err := execNotIn(ctx, tx, "DELETE FROM code_nodes WHERE repo_id = ? AND id NOT IN (%s)", req.RepoID, keep)
		if err != nil {
			return res, err
		}
		deleted = r
	} else {
		pkgIDs := make([]string, 0, len(req.Only))
		for _, p := range req.Only {
			pkgIDs = append(pkgIDs, idOf(codemapops.NodePackage, p))
		}
		r, err := execScoped(ctx, tx, req.RepoID, pkgIDs, keep)
		if err != nil {
			return res, err
		}
		deleted = r
	}
	res.NodesDeleted = int(deleted)
	// 3. replace edges whose src is in the applied node set
	// EdgesPruned counts every edge this apply removed, whether it was replaced
	// because its source was rescanned (here) or orphaned by a node deletion (4).
	replaced, err := execNotIn(ctx, tx, "DELETE FROM code_edges WHERE repo_id = ? AND src_id IN (%s)", req.RepoID, keep)
	if err != nil {
		return res, err
	}
	res.EdgesPruned = int(replaced)
	if len(req.Graph.Edges) > 0 {
		edges := make([]edgeRow, 0, len(req.Graph.Edges))
		for _, e := range req.Graph.Edges {
			edges = append(edges, edgeRow{RepoID: req.RepoID, SrcID: idOfPath(req, e.Src), DstID: idOfPath(req, e.Dst), Kind: e.Kind, Weight: max(e.Weight, 1)})
		}
		for i := 0; i < len(edges); i += upsertChunk {
			end := min(i+upsertChunk, len(edges))
			if err := upsertEdges(ctx, tx, edges[i:end]); err != nil {
				return res, err
			}
		}
		res.EdgesWritten = len(edges)
	}
	// 4. prune orphan edges
	pr, err := tx.ExecContext(ctx, `DELETE FROM code_edges WHERE repo_id = ? AND (
        src_id NOT IN (SELECT id FROM code_nodes WHERE repo_id = ?) OR
        dst_id NOT IN (SELECT id FROM code_nodes WHERE repo_id = ?))`, req.RepoID, req.RepoID, req.RepoID)
	if err != nil {
		return res, fmt.Errorf("pruning edges: %w", err)
	}
	if n, _ := pr.RowsAffected(); n > 0 {
		res.EdgesPruned += int(n)
	}
	// 5. record head
	key := fmt.Sprintf(lastSHAKey, req.RepoID)
	if _, err := tx.ExecContext(ctx, "INSERT INTO config (`key`, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)", key, req.HeadSHA); err != nil {
		return res, fmt.Errorf("recording last sha: %w", err)
	}
	return res, nil
}

// idOfPath resolves an edge endpoint: a path that names a package node is a
// package id, else a file id. The fallback also catches an edge into a
// declared ExternalPackages target, which has no node in this graph; the
// orphan prune in step 4 sweeps the resulting dangling edge.
func idOfPath(req codemapops.ApplyRequest, path string) string {
	for _, n := range req.Graph.Nodes {
		if n.Path == path {
			return NodeID(req.RepoID, n.Kind, path)
		}
	}
	return NodeID(req.RepoID, codemapops.NodeFile, path)
}

func upsertNodes(ctx context.Context, tx issueops.DBTX, rows []nodeRow) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO code_nodes (id, repo_id, kind, path, path_hash, name, package_id, lang, blob_hash, loc, is_test, exported, indexed_at) VALUES ")
	args := make([]any, 0, len(rows)*13)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args, r.ID, r.RepoID, string(r.Kind), r.Path, r.PathHash, r.Name, r.PackageID, r.Lang, r.BlobHash, r.LOC, r.IsTest, r.Exported, r.IndexedAt)
	}
	// summary columns are deliberately NOT in the update list: a rescan keeps the summary and its blob so staleness is derivable.
	sb.WriteString(" ON DUPLICATE KEY UPDATE path = VALUES(path), name = VALUES(name), package_id = VALUES(package_id), lang = VALUES(lang), blob_hash = VALUES(blob_hash), loc = VALUES(loc), is_test = VALUES(is_test), exported = VALUES(exported), indexed_at = VALUES(indexed_at)")
	if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("upserting code_nodes: %w", err)
	}
	return nil
}

func upsertEdges(ctx context.Context, tx issueops.DBTX, rows []edgeRow) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO code_edges (repo_id, src_id, dst_id, kind, weight) VALUES ")
	args := make([]any, 0, len(rows)*5)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?,?)")
		args = append(args, r.RepoID, r.SrcID, r.DstID, string(r.Kind), r.Weight)
	}
	sb.WriteString(" ON DUPLICATE KEY UPDATE weight = VALUES(weight)")
	if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("upserting code_edges: %w", err)
	}
	return nil
}

// execNotIn runs a DELETE with an id list; an empty list means "everything for
// the repo" for the node template and "nothing" for the edge template, so the
// two cases get their own statement rather than an expression with no operands.
func execNotIn(ctx context.Context, tx issueops.DBTX, tmpl, repoID string, ids []string) (int64, error) {
	if len(ids) == 0 {
		empty := strings.Replace(tmpl, " AND id NOT IN (%s)", "", 1)
		empty = strings.Replace(empty, " AND src_id IN (%s)", " AND 1=0", 1)
		r, err := tx.ExecContext(ctx, empty, repoID)
		if err != nil {
			return 0, err
		}
		return r.RowsAffected()
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, repoID)
	for _, id := range ids {
		args = append(args, id)
	}
	r, err := tx.ExecContext(ctx, fmt.Sprintf(tmpl, ph), args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// execScoped deletes nodes inside the rescanned packages (the packages themselves and their files) that the new graph no longer names.
func execScoped(ctx context.Context, tx issueops.DBTX, repoID string, pkgIDs, keep []string) (int64, error) {
	if len(pkgIDs) == 0 {
		return 0, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(pkgIDs)), ",")
	kp := strings.TrimSuffix(strings.Repeat("?,", len(keep)), ",")
	q := fmt.Sprintf("DELETE FROM code_nodes WHERE repo_id = ? AND (id IN (%s) OR package_id IN (%s)) AND id NOT IN (%s)", ph, ph, kp)
	args := []any{repoID}
	for i := 0; i < 2; i++ {
		for _, id := range pkgIDs {
			args = append(args, id)
		}
	}
	for _, id := range keep {
		args = append(args, id)
	}
	r, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("deleting rescanned nodes: %w", err)
	}
	return r.RowsAffected()
}

// LastSHAInTx reads the head a repository's code map was last built from, or
// "" when the repository has never been indexed.
func LastSHAInTx(ctx context.Context, tx issueops.DBTX, repoID string) (string, error) {
	var v string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", fmt.Sprintf(lastSHAKey, repoID)).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSummariesInTx attaches summaries to file nodes, refusing any whose blob
// has moved since the summary was computed.
func SetSummariesInTx(ctx context.Context, tx issueops.DBTX, repoID string, items []codemapops.Summary, now time.Time) (codemapops.SetSummariesResult, error) {
	var res codemapops.SetSummariesResult
	for _, it := range items {
		if it.Path == "" || it.Summary == "" || len(it.Summary) > 160 || len(it.BlobHash) != 40 {
			return res, fmt.Errorf("%w: summary for %q must have path, <=160-char summary and a 40-char blob", codemapops.ErrValidation, it.Path)
		}
		tags, _ := json.Marshal(it.Tags)
		r, err := tx.ExecContext(ctx, `UPDATE code_nodes SET summary = ?, layer = ?, tags = ?, summary_blob_hash = ?, summary_model = ?, summarized_at = ?
            WHERE id = ? AND blob_hash = ?`, it.Summary, it.Layer, string(tags), it.BlobHash, it.Model, now, NodeID(repoID, codemapops.NodeFile, it.Path), it.BlobHash)
		if err != nil {
			return res, fmt.Errorf("writing summary for %s: %w", it.Path, err)
		}
		if n, _ := r.RowsAffected(); n == 1 {
			res.Written++
		} else {
			res.Refused++
			res.RefusedPaths = append(res.RefusedPaths, it.Path)
		}
	}
	return res, nil
}
