package codemapops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// NormalizePath canonicalizes a repo-relative path: forward slashes, no
// leading "./", and no escape above the repository root.
func NormalizePath(p string) (string, error) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: path %q must be repo-relative", codemapops.ErrValidation, p)
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%w: path %q escapes the repository", codemapops.ErrValidation, p)
	}
	return c, nil
}

func validSource(s codemapops.Source) bool {
	switch s {
	case codemapops.SourceHook, codemapops.SourceCommit, codemapops.SourceManual, codemapops.SourceBranch:
		return true
	}
	return false
}

// RecordInTx notes that an issue touched some paths, inserting first
// sightings and folding repeat sightings into the existing row.
func RecordInTx(ctx context.Context, tx issueops.DBTX, req codemapops.RecordRequest, now time.Time) (codemapops.RecordResult, error) {
	var res codemapops.RecordResult
	if req.IssueID == "" || req.RepoID == "" || len(req.Paths) == 0 {
		return res, fmt.Errorf("%w: issue id, repo id and at least one path are required", codemapops.ErrValidation)
	}
	if !validSource(req.Source) {
		return res, fmt.Errorf("%w: unknown source %q", codemapops.ErrValidation, req.Source)
	}
	if req.CommitSHA != "" && !shaRE.MatchString(req.CommitSHA) {
		return res, fmt.Errorf("%w: commit sha %q is not 40 hex chars", codemapops.ErrValidation, req.CommitSHA)
	}
	paths := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		n, err := NormalizePath(p)
		if err != nil {
			return res, err
		}
		paths = append(paths, n)
	}
	var probe int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM issues WHERE id = ?", req.IssueID).Scan(&probe); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return res, fmt.Errorf("%w: issue %s", storage.ErrNotFound, req.IssueID)
		}
		return res, err
	}
	for _, p := range paths {
		ph := PathHash(p)
		var ex issueFileRow
		err := tx.QueryRowContext(ctx, "SELECT issue_id, repo_id, path_hash, path, source, commit_sha, first_seen, last_seen, touches FROM issue_files WHERE issue_id = ? AND repo_id = ? AND path_hash = ?",
			req.IssueID, req.RepoID, ph).Scan(&ex.IssueID, &ex.RepoID, &ex.PathHash, &ex.Path, &ex.Source, &ex.CommitSHA, &ex.FirstSeen, &ex.LastSeen, &ex.Touches)
		var row issueFileRow
		switch {
		case errors.Is(err, sql.ErrNoRows):
			row = MergeRecord(nil, req, p, now)
			if _, err := tx.ExecContext(ctx, "INSERT INTO issue_files (issue_id, repo_id, path_hash, path, source, commit_sha, first_seen, last_seen, touches) VALUES (?,?,?,?,?,?,?,?,?)",
				row.IssueID, row.RepoID, row.PathHash, row.Path, string(row.Source), row.CommitSHA, row.FirstSeen, row.LastSeen, row.Touches); err != nil {
				return res, fmt.Errorf("inserting issue_files: %w", err)
			}
			res.Inserted++
		case err != nil:
			return res, err
		default:
			row = MergeRecord(&ex, req, p, now)
			if _, err := tx.ExecContext(ctx, "UPDATE issue_files SET source = ?, commit_sha = ?, last_seen = ?, touches = ? WHERE issue_id = ? AND repo_id = ? AND path_hash = ?",
				string(row.Source), row.CommitSHA, row.LastSeen, row.Touches, row.IssueID, row.RepoID, row.PathHash); err != nil {
				return res, fmt.Errorf("updating issue_files: %w", err)
			}
			res.Updated++
		}
	}
	return res, nil
}

// ByIssueInTx lists the paths one issue has touched.
func ByIssueInTx(ctx context.Context, tx issueops.DBTX, issueID string) ([]codemapops.IssueFile, error) {
	rows, err := tx.QueryContext(ctx, "SELECT path, source, commit_sha, first_seen, last_seen, touches FROM issue_files WHERE issue_id = ? ORDER BY path", issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []codemapops.IssueFile
	for rows.Next() {
		var f codemapops.IssueFile
		var sha sql.NullString
		if err := rows.Scan(&f.Path, &f.Source, &sha, &f.FirstSeen, &f.LastSeen, &f.Touches); err != nil {
			return nil, err
		}
		f.CommitSHA = sha.String
		out = append(out, f)
	}
	return out, rows.Err()
}

// ByPathInTx lists the issues that have touched one path.
func ByPathInTx(ctx context.Context, tx issueops.DBTX, repoID, p string, openOnly bool) ([]codemapops.IssueRef, error) {
	n, err := NormalizePath(p)
	if err != nil {
		return nil, err
	}
	q := "SELECT i.id, i.title, i.status FROM issue_files f JOIN issues i ON i.id = f.issue_id WHERE f.repo_id = ? AND f.path_hash = ?"
	if openOnly {
		q += " AND i.status NOT IN ('closed')"
	}
	rows, err := tx.QueryContext(ctx, q+" ORDER BY i.id", repoID, PathHash(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []codemapops.IssueRef
	for rows.Next() {
		var r codemapops.IssueRef
		if err := rows.Scan(&r.ID, &r.Title, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// IssueCodeContextInTx is the full "what code does this issue touch" answer:
// every touched file, its code-map context when the repository is indexed,
// and the other open issues on the same path.
func IssueCodeContextInTx(ctx context.Context, tx issueops.DBTX, issueID, repoID string) (codemapops.IssueCodeContext, error) {
	cc := codemapops.IssueCodeContext{IssueID: issueID, RepoID: repoID}
	files, err := ByIssueInTx(ctx, tx, issueID)
	if err != nil {
		return cc, err
	}
	sha, err := LastSHAInTx(ctx, tx, repoID)
	if err != nil {
		return cc, err
	}
	cc.Indexed = sha != ""
	if cc.Indexed {
		shape, err := ShapeInTx(ctx, tx, repoID, codemapops.ShapeOptions{TopFiles: 1})
		if err != nil {
			return cc, err
		}
		cc.Shape = &shape
	}
	for _, f := range files {
		cf := codemapops.IssueCodeFile{IssueFile: f}
		if cc.Indexed {
			fc, err := FileContextInTx(ctx, tx, repoID, f.Path)
			if err == nil {
				cf.Context = &fc
			} else if !errors.Is(err, storage.ErrNotFound) {
				return cc, err
			}
		}
		sib, err := ByPathInTx(ctx, tx, repoID, f.Path, true)
		if err != nil {
			return cc, err
		}
		for _, s := range sib {
			if s.ID != issueID {
				cf.Siblings = append(cf.Siblings, s)
			}
		}
		cc.Files = append(cc.Files, cf)
	}
	return cc, nil
}
