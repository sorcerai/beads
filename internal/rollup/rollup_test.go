package rollup

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// fakeSource is an in-memory IssueSource for tests (no Dolt). deps is keyed by
// child issue ID, mirroring storage.GetAllDependencyRecords.
type fakeSource struct {
	issues []*types.Issue
	deps   map[string][]*types.Dependency
}

func (f *fakeSource) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	return f.issues, nil
}

func (f *fakeSource) GetAllDependencyRecords(_ context.Context) (map[string][]*types.Dependency, error) {
	if f.deps == nil {
		return map[string][]*types.Dependency{}, nil
	}
	return f.deps, nil
}

func iss(id, title string, st types.Status, labels ...string) *types.Issue {
	return &types.Issue{ID: id, Title: title, Status: st, Labels: labels, UpdatedAt: time.Unix(0, 0)}
}

// pc registers a parent-child edge: child's record points at parent via DependsOnID.
func pc(deps map[string][]*types.Dependency, child, parent string) {
	deps[child] = append(deps[child], &types.Dependency{
		IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
	})
}

func TestCompute_GroupsByProjectLabel(t *testing.T) {
	deps := map[string][]*types.Dependency{}
	pc(deps, "a-2", "a-1")
	src := &fakeSource{issues: []*types.Issue{
		iss("a-1", "epic A", types.StatusOpen, "project:alpha"),
		iss("a-2", "child", types.StatusInProgress, "project:alpha"),
		iss("u-1", "loose", types.StatusOpen),
	}, deps: deps}
	r, err := Compute(context.Background(), src, Options{})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if got := projectBySlug(r, "alpha"); got == nil || len(got.Epics) != 1 {
		t.Fatalf("expected 1 epic in alpha, got %#v", got)
	}
	if len(projectBySlug(r, "alpha").Epics[0].Children) != 1 {
		t.Fatalf("epic a-1 should have 1 child")
	}
	if projectBySlug(r, "Unassigned") == nil {
		t.Fatalf("Unassigned bucket must always be emitted")
	}
}

func TestCompute_EpicComputedColumn_ConflictWhenChildOpen(t *testing.T) {
	deps := map[string][]*types.Dependency{}
	pc(deps, "c-1", "e-1")
	src := &fakeSource{issues: []*types.Issue{
		iss("e-1", "epic", types.StatusClosed, "project:p"),
		iss("c-1", "child still open", types.StatusOpen, "project:p"),
	}, deps: deps}
	r, _ := Compute(context.Background(), src, Options{})
	e := projectBySlug(r, "p").Epics[0]
	if e.Column != ColumnInProgress || !e.Conflict {
		t.Fatalf("closed epic with open child: got column=%q conflict=%v, want in_progress + conflict", e.Column, e.Conflict)
	}
}

func TestCompute_MultiProjectLabel_FirstLexicographicWinsWithDiagnostic(t *testing.T) {
	src := &fakeSource{issues: []*types.Issue{
		iss("x-1", "two projects", types.StatusOpen, "project:zeta", "project:alpha"),
	}}
	r, _ := Compute(context.Background(), src, Options{})
	if projectBySlug(r, "alpha") == nil {
		t.Fatalf("first lexicographic project label (alpha) should win")
	}
	if !hasDiagnostic(r, "multi_project", "x-1") {
		t.Fatalf("expected multi_project diagnostic for x-1")
	}
}

func TestCompute_CycleEmitsDiagnosticNoHang(t *testing.T) {
	deps := map[string][]*types.Dependency{}
	pc(deps, "n-1", "n-2")
	pc(deps, "n-2", "n-1")
	src := &fakeSource{issues: []*types.Issue{
		iss("n-1", "n1", types.StatusOpen, "project:p"),
		iss("n-2", "n2", types.StatusOpen, "project:p"),
	}, deps: deps}
	done := make(chan struct{})
	go func() { _, _ = Compute(context.Background(), src, Options{}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Compute hung on a parent cycle")
	}
	r, _ := Compute(context.Background(), src, Options{})
	if !hasDiagnostic(r, "invalid_graph", "") {
		t.Fatalf("expected invalid_graph diagnostic for cycle")
	}
}

func TestCompute_ZeroChildEpicUsesOwnColumn(t *testing.T) {
	src := &fakeSource{issues: []*types.Issue{
		iss("e-1", "lone open epic", types.StatusInProgress, "project:p"),
		iss("e-2", "lone closed epic", types.StatusClosed, "project:p"),
	}}
	r, _ := Compute(context.Background(), src, Options{})
	p := projectBySlug(r, "p")
	byID := map[string]Epic{}
	for _, e := range p.Epics {
		byID[e.Issue.ID] = e
	}
	if e := byID["e-1"]; e.Column != ColumnInProgress || e.Conflict {
		t.Fatalf("childless open epic: got column=%q conflict=%v, want in_progress, no conflict", e.Column, e.Conflict)
	}
	if e := byID["e-2"]; e.Column != ColumnDone || e.Conflict {
		t.Fatalf("childless closed epic: got column=%q conflict=%v, want done, no conflict", e.Column, e.Conflict)
	}
}

func TestCompute_ChildWithMissingParentEpicGoesLoose(t *testing.T) {
	deps := map[string][]*types.Dependency{}
	pc(deps, "c-1", "absent-epic") // parent not in the issue set
	src := &fakeSource{issues: []*types.Issue{
		iss("c-1", "orphaned child", types.StatusOpen, "project:p"),
	}, deps: deps}
	r, _ := Compute(context.Background(), src, Options{})
	p := projectBySlug(r, "p")
	if len(p.Epics) != 0 {
		t.Fatalf("expected no epics, got %d", len(p.Epics))
	}
	if len(p.Loose) != 1 || p.Loose[0].ID != "c-1" {
		t.Fatalf("expected c-1 in Loose, got %#v", p.Loose)
	}
}

func TestCompute_BlockSignals(t *testing.T) {
	closedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	blockerDone := iss("d-1", "done blocker", types.StatusClosed, "project:p")
	blockerDone.ClosedAt = &closedAt
	deps := map[string][]*types.Dependency{
		"b-1": {{IssueID: "b-1", DependsOnID: "o-1", Type: types.DepBlocks}},
		"r-1": {{IssueID: "r-1", DependsOnID: "d-1", Type: types.DepBlocks}},
		"u-1": {{IssueID: "u-1", DependsOnID: "gone", Type: types.DepBlocks}}, // blocker outside fetch
	}
	src := &fakeSource{issues: []*types.Issue{
		iss("o-1", "open blocker", types.StatusOpen, "project:p"),
		blockerDone,
		iss("b-1", "blocked", types.StatusOpen, "project:p"),
		iss("r-1", "ready again", types.StatusOpen, "project:p"),
		iss("u-1", "unknown blocker", types.StatusOpen, "project:p"),
	}, deps: deps}
	r, err := Compute(context.Background(), src, Options{})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	byID := map[string]Card{}
	for _, e := range projectBySlug(r, "p").Epics {
		byID[e.Issue.ID] = e.Issue
	}
	if !byID["b-1"].Blocked {
		t.Fatal("b-1 depends on an open issue: must be Blocked")
	}
	if c := byID["r-1"]; c.Blocked || !c.LastDepClosed.Equal(closedAt) {
		t.Fatalf("r-1: want unblocked with LastDepClosed=%v, got %+v", closedAt, c)
	}
	if c := byID["u-1"]; c.Blocked || !c.HasUnknownDeps {
		t.Fatalf("blocker outside the fetch window: want Blocked=false + HasUnknownDeps=true, got %+v", c)
	}
	if c := byID["d-1"]; !c.ClosedAt.Equal(closedAt) {
		t.Fatalf("closed card must carry ClosedAt, got %+v", c)
	}
}

// helpers
func projectBySlug(r *Rollup, slug string) *Project {
	for i := range r.Projects {
		if r.Projects[i].Slug == slug {
			return &r.Projects[i]
		}
	}
	return nil
}
func hasDiagnostic(r *Rollup, kind, issueID string) bool {
	for _, d := range r.Diagnostics {
		if d.Kind == kind && (issueID == "" || d.IssueID == issueID) {
			return true
		}
	}
	return false
}
