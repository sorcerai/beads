//go:build cgo

package codemapops_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
	cmops "github.com/steveyegge/beads/internal/storage/codemapops"
	"github.com/steveyegge/beads/internal/types"
)

func TestRecordByIssueByPathAndContext(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	seedIssue(t, tx, &types.Issue{ID: "t-1", Title: "one", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeTask})
	seedIssue(t, tx, &types.Issue{ID: "t-2", Title: "two", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask})
	now := time.Now().UTC().Truncate(time.Second)
	res, err := cmops.RecordInTx(ctx, tx, codemapops.RecordRequest{IssueID: "t-1", RepoID: "r1", Paths: []string{"./a/a.go", "b/b.go"}, Source: codemapops.SourceHook}, now)
	if err != nil || res.Inserted != 2 {
		t.Fatalf("record: %+v %v", res, err)
	}
	res, err = cmops.RecordInTx(ctx, tx, codemapops.RecordRequest{IssueID: "t-1", RepoID: "r1", Paths: []string{"a/a.go"}, Source: codemapops.SourceCommit, CommitSHA: "0123456789012345678901234567890123456789"}, now.Add(time.Minute))
	if err != nil || res.Updated != 1 {
		t.Fatalf("re-record: %+v %v", res, err)
	}
	if _, err := cmops.RecordInTx(ctx, tx, codemapops.RecordRequest{IssueID: "t-2", RepoID: "r1", Paths: []string{"a/a.go"}, Source: codemapops.SourceManual}, now); err != nil {
		t.Fatal(err)
	}
	files, err := cmops.ByIssueInTx(ctx, tx, "t-1")
	if err != nil || len(files) != 2 || files[0].Path != "a/a.go" || files[0].Touches != 2 || files[0].Source != codemapops.SourceCommit {
		t.Fatalf("by issue: %+v %v", files, err)
	}
	open, _ := cmops.ByPathInTx(ctx, tx, "r1", "a/a.go", true)
	all, _ := cmops.ByPathInTx(ctx, tx, "r1", "a/a.go", false)
	if len(open) != 1 || len(all) != 2 {
		t.Fatalf("by path: open=%d all=%d", len(open), len(all))
	}
	if _, err := cmops.RecordInTx(ctx, tx, codemapops.RecordRequest{IssueID: "t-1", RepoID: "r1", Paths: []string{"../x"}, Source: codemapops.SourceHook}, now); err == nil {
		t.Fatal("path escaping the repo must be refused")
	}
	cc, err := cmops.IssueCodeContextInTx(ctx, tx, "t-1", "r1")
	if err != nil || cc.Indexed || len(cc.Files) != 2 || cc.Files[0].Context != nil {
		t.Fatalf("context without a map: %+v %v", cc, err)
	}
	if _, err := cmops.ApplyInTx(ctx, tx, codemapops.ApplyRequest{RepoID: "r1", HeadSHA: "abc", Graph: twoPkgGraph()}, now); err != nil {
		t.Fatal(err)
	}
	cc, err = cmops.IssueCodeContextInTx(ctx, tx, "t-1", "r1")
	if err != nil || !cc.Indexed || cc.Files[0].Context == nil || cc.Files[0].Context.Package.Path != "m/a" || len(cc.Files[0].Siblings) != 0 {
		t.Fatalf("context with a map: %+v %v", cc, err)
	}
}

func TestRecordValidation(t *testing.T) {
	tx, cleanup := newTestTx(t)
	defer cleanup()
	ctx := context.Background()
	seedIssue(t, tx, &types.Issue{ID: "t-1", Title: "one", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
	now := time.Now().UTC()
	base := codemapops.RecordRequest{IssueID: "t-1", RepoID: "r1", Paths: []string{"a/a.go"}, Source: codemapops.SourceHook}

	missing := base
	missing.IssueID = "nope"
	if _, err := cmops.RecordInTx(ctx, tx, missing, now); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unknown issue must be ErrNotFound, got %v", err)
	}
	badSource := base
	badSource.Source = "guess"
	if _, err := cmops.RecordInTx(ctx, tx, badSource, now); !errors.Is(err, codemapops.ErrValidation) {
		t.Fatalf("unknown source must be a validation error, got %v", err)
	}
	badSHA := base
	badSHA.CommitSHA = "abc"
	if _, err := cmops.RecordInTx(ctx, tx, badSHA, now); !errors.Is(err, codemapops.ErrValidation) {
		t.Fatalf("short commit sha must be a validation error, got %v", err)
	}
	noPaths := base
	noPaths.Paths = nil
	if _, err := cmops.RecordInTx(ctx, tx, noPaths, now); !errors.Is(err, codemapops.ErrValidation) {
		t.Fatalf("empty paths must be a validation error, got %v", err)
	}
}

func TestNormalizePath(t *testing.T) {
	ok := map[string]string{
		"a/a.go":      "a/a.go",
		"./a/a.go":    "a/a.go",
		`a\b.go`:      "a/b.go",
		"  a/a.go  ":  "a/a.go",
		"a/./b/c.go":  "a/b/c.go",
		"a/b/../c.go": "a/c.go",
	}
	for in, want := range ok {
		got, err := cmops.NormalizePath(in)
		if err != nil || got != want {
			t.Errorf("NormalizePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "/abs/path.go", "../x", "..", ".", "a/../../x"} {
		if got, err := cmops.NormalizePath(bad); err == nil {
			t.Errorf("NormalizePath(%q) = %q; want an error", bad, got)
		}
	}
}
