package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/codemapops"
)

func TestWriteReadRoundTripAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	f := File{GeneratedAt: time.Now().UTC(), HeadSHA: "abc", RepoID: "r1",
		Files: map[string]Entry{"a.go": {Path: "a.go", Summary: "s", Importers: []string{"b.go"}}}}
	if err := Write(dir, f); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
	got, err := Read(dir)
	if err != nil || got.HeadSHA != "abc" || got.Files["a.go"].Importers[0] != "b.go" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := os.WriteFile(Path(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("corrupt cache must error")
	}
	if _, err := Read(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cache: %v", err)
	}
}

// fakeReader answers FileContext from a fixed map; an absent path is not found.
type fakeReader struct {
	ctxs map[string]codemapops.FileContext
}

func (f fakeReader) FileContext(_ context.Context, _, path string) (codemapops.FileContext, error) {
	fc, ok := f.ctxs[path]
	if !ok {
		return codemapops.FileContext{}, fmt.Errorf("file %q: %w", path, beadserrors.ErrNotFound)
	}
	return fc, nil
}

func (fakeReader) PackageContext(context.Context, string, string) (codemapops.PackageContext, error) {
	return codemapops.PackageContext{}, nil
}
func (fakeReader) Stale(context.Context, string, int) ([]codemapops.NodeRef, error) { return nil, nil }
func (fakeReader) Shape(context.Context, string, codemapops.ShapeOptions) (codemapops.Shape, error) {
	return codemapops.Shape{}, nil
}

// fakeFiles answers ByPath from a fixed map.
type fakeFiles struct {
	byPath map[string][]codemapops.IssueRef
}

func (f fakeFiles) ByPath(_ context.Context, _, path string, openOnly bool) ([]codemapops.IssueRef, error) {
	if !openOnly {
		return nil, errors.New("cache must ask for open issues only")
	}
	return f.byPath[path], nil
}

func (fakeFiles) Record(context.Context, codemapops.RecordRequest) (codemapops.RecordResult, error) {
	return codemapops.RecordResult{}, nil
}
func (fakeFiles) ByIssue(context.Context, string) ([]codemapops.IssueFile, error) { return nil, nil }
func (fakeFiles) IssueCodeContext(context.Context, string, string) (codemapops.IssueCodeContext, error) {
	return codemapops.IssueCodeContext{}, nil
}

func TestBuild(t *testing.T) {
	var importers []codemapops.NodeRef
	for i := 0; i < 20; i++ {
		importers = append(importers, codemapops.NodeRef{Path: fmt.Sprintf("imp%02d.go", i)})
	}
	reader := fakeReader{ctxs: map[string]codemapops.FileContext{
		"a.go": {
			Node:      codemapops.NodeRef{Path: "a.go", Summary: "does a", Layer: "core", Stale: true},
			Imports:   []codemapops.NodeRef{{Path: "pkg/x"}, {Path: "pkg/y"}},
			Importers: importers,
		},
	}}
	files := fakeFiles{byPath: map[string][]codemapops.IssueRef{
		"a.go": {{ID: "bd-1", Title: "fix a", Status: "open"}},
	}}

	f, err := Build(context.Background(), "r1", "sha1", reader, files, []string{"a.go", "gone.go"})
	if err != nil {
		t.Fatal(err)
	}
	if f.RepoID != "r1" || f.HeadSHA != "sha1" || f.GeneratedAt.IsZero() {
		t.Errorf("header wrong: %+v", f)
	}
	a := f.Files["a.go"]
	if a.Summary != "does a" || a.Layer != "core" || !a.Stale {
		t.Errorf("entry metadata wrong: %+v", a)
	}
	if len(a.Importers) != 12 || a.Importers[0] != "imp00.go" {
		t.Errorf("importers not capped at 12: %v", a.Importers)
	}
	if len(a.Imports) != 2 || a.Imports[0] != "pkg/x" {
		t.Errorf("imports wrong: %v", a.Imports)
	}
	if len(a.OpenIssues) != 1 || a.OpenIssues[0].ID != "bd-1" {
		t.Errorf("open issues not joined: %v", a.OpenIssues)
	}
	gone, ok := f.Files["gone.go"]
	if !ok || gone.Path != "gone.go" || gone.Summary != "" || len(gone.Importers) != 0 {
		t.Errorf("unindexed path must yield a bare entry, got %+v (present=%v)", gone, ok)
	}
}
