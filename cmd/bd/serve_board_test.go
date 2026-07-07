package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rollup"
	"github.com/steveyegge/beads/internal/types"
)

// wb parses a board JSON blob into a workspaceRollup (test helper).
func wb(dir, data string) workspaceRollup {
	var r rollup.Rollup
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		panic("wb: bad test JSON: " + err.Error())
	}
	return workspaceRollup{dir: dir, r: r}
}

func TestMergeRollups_CombinesProjects(t *testing.T) {
	a := `{"generated_at":"2026-01-01T10:00:00Z","projects":[{"slug":"alpha","epics":[],"loose":[]}],"diagnostics":[]}`
	b := `{"generated_at":"2026-01-01T11:00:00Z","projects":[{"slug":"beta","epics":[],"loose":[]}],"diagnostics":[]}`
	r := mergeRollups([]workspaceRollup{wb("", a), wb("", b)})
	if len(r.Projects) != 2 {
		t.Fatalf("want 2 projects, got %d: %+v", len(r.Projects), r.Projects)
	}
	slugs := map[string]bool{}
	for _, p := range r.Projects {
		slugs[p.Slug] = true
	}
	if !slugs["alpha"] || !slugs["beta"] {
		t.Fatalf("missing slug: %v", slugs)
	}
}

func TestMergeRollups_DeduplicatesSameSlug(t *testing.T) {
	a := `{"generated_at":"2026-01-01T00:00:00Z","projects":[{"slug":"Unassigned","epics":[],"loose":[{"id":"x","title":"x","status":"open","column":"todo","priority":0,"updated_at":"2026-01-01T00:00:00Z"}]}],"diagnostics":[]}`
	b := `{"generated_at":"2026-01-01T00:00:00Z","projects":[{"slug":"Unassigned","epics":[],"loose":[{"id":"y","title":"y","status":"open","column":"todo","priority":0,"updated_at":"2026-01-01T00:00:00Z"}]}],"diagnostics":[]}`
	r := mergeRollups([]workspaceRollup{wb("", a), wb("", b)})
	if len(r.Projects) != 1 {
		t.Fatalf("same-slug projects must merge into one, got %d", len(r.Projects))
	}
	if len(r.Projects[0].Loose) != 2 {
		t.Fatalf("merged project must have both cards, got %d", len(r.Projects[0].Loose))
	}
}

func TestMergeRollups_RenamesUnassignedToWorkspaceName(t *testing.T) {
	data := `{"generated_at":"2026-01-01T00:00:00Z","projects":[{"slug":"Unassigned","epics":[],"loose":[]}],"diagnostics":[]}`
	r := mergeRollups([]workspaceRollup{wb("/tmp/beads-myproject-workspace", data)})
	if len(r.Projects) != 1 || r.Projects[0].Slug != "myproject" {
		t.Fatalf("want slug=myproject, got %+v", r.Projects)
	}
}

func TestWorkspaceName(t *testing.T) {
	cases := []struct{ dir, want string }{
		{"WORKSPACE_A", "creator-kb-factory"},
		{"WORKSPACE_B", "KreatorFlow"},
		{"/tmp/beads-workspace", ""}, // generic, no project name
		{"", ""},                            // CWD default
		{"/home/admin/other-dir", ""},       // doesn't match convention
	}
	for _, c := range cases {
		if got := workspaceName(c.dir); got != c.want {
			t.Errorf("workspaceName(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}

func TestResolveWorkspaces_GlobAndExplicitUnion(t *testing.T) {
	dir := t.TempDir()
	// Two matching workspace dirs + a non-matching dir + a file that matches the glob.
	for _, d := range []string{"beads-alpha-workspace", "beads-beta-workspace", "unrelated"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "beads-file-workspace"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	glob := filepath.Join(dir, "beads-*-workspace")

	got := resolveWorkspaces([]string{"/explicit/base"}, []string{glob})

	// explicit first, then the two matching DIRS (file excluded), sorted by glob.
	if len(got) != 3 || got[0] != "/explicit/base" {
		t.Fatalf("want explicit base + 2 dirs, got %v", got)
	}
	found := map[string]bool{}
	for _, g := range got {
		found[filepath.Base(g)] = true
	}
	if !found["beads-alpha-workspace"] || !found["beads-beta-workspace"] {
		t.Fatalf("glob did not pick up both workspace dirs: %v", got)
	}
	if found["beads-file-workspace"] || found["unrelated"] {
		t.Fatalf("glob included a file or non-matching dir: %v", got)
	}
}

func TestResolveWorkspaces_DedupAndEmptyFallback(t *testing.T) {
	// Empty inputs => CWD fallback (back-compat).
	if got := resolveWorkspaces(nil, nil); len(got) != 1 || got[0] != "" {
		t.Fatalf("empty inputs must fall back to {\"\"}, got %v", got)
	}
	// A dir that is both explicit and glob-matched appears once.
	dir := t.TempDir()
	ws := filepath.Join(dir, "beads-x-workspace")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	got := resolveWorkspaces([]string{ws}, []string{filepath.Join(dir, "beads-*-workspace")})
	if len(got) != 1 || got[0] != ws {
		t.Fatalf("explicit+glob dup must collapse to one, got %v", got)
	}
}

func TestMergeRollups_UsesMaxGeneratedAt(t *testing.T) {
	a := `{"generated_at":"2026-01-01T10:00:00Z","projects":[],"diagnostics":[]}`
	b := `{"generated_at":"2026-01-01T11:00:00Z","projects":[],"diagnostics":[]}`
	r := mergeRollups([]workspaceRollup{wb("", a), wb("", b)})
	if got := r.GeneratedAt.UTC().Format(time.RFC3339); got != "2026-01-01T11:00:00Z" {
		t.Fatalf("want max GeneratedAt, got %s", got)
	}
}

func TestMergeRollups_MergesDiagnostics(t *testing.T) {
	a := `{"generated_at":"2026-01-01T00:00:00Z","projects":[],"diagnostics":[{"kind":"multi_project","issue_id":"a-1"}]}`
	b := `{"generated_at":"2026-01-01T00:00:00Z","projects":[],"diagnostics":[{"kind":"invalid_graph","issue_id":"b-2"}]}`
	r := mergeRollups([]workspaceRollup{wb("", a), wb("", b)})
	if len(r.Diagnostics) != 2 {
		t.Fatalf("want 2 diagnostics, got %d", len(r.Diagnostics))
	}
}

// pageOf wraps buildPage for tests that only care about the rollup.
func pageOf(r *rollup.Rollup, stale bool, goodAt string, refresh int, selected string) vmPage {
	return buildPage(&boardPayload{Rollup: *r}, stale, goodAt, refresh, selected, 2*time.Hour, nil)
}

func TestBuildPage_BurndownAndSummary(t *testing.T) {
	r := &rollup.Rollup{Projects: []rollup.Project{
		{
			Slug: "p",
			Epics: []rollup.Epic{
				{Issue: rollup.Card{ID: "p-1"}, Column: rollup.ColumnDone},
				{Issue: rollup.Card{ID: "p-2"}, Column: rollup.ColumnInProgress, Conflict: true},
			},
			Loose: []rollup.Card{
				{ID: "p-3", Column: rollup.ColumnDone},
				{ID: "p-4", Column: rollup.ColumnTodo},
			},
		},
		{Slug: "q", Loose: []rollup.Card{{ID: "q-1", Column: rollup.ColumnDone}}},
	}}
	p := pageOf(r, false, "", 30, "")

	// project p: 4 placed cards, 2 done => 50%, 1 conflict, bar has todo+wip+done.
	pp := p.Projects[0]
	if pp.Total != 4 || pp.Done != 2 || pp.DonePct != 50 || pp.Conflicts != 1 {
		t.Fatalf("project p burn-down wrong: %+v", pp)
	}
	if len(pp.Bar) != 3 {
		t.Fatalf("project p bar should have 3 non-empty segments, got %d", len(pp.Bar))
	}
	// page summary across both shown projects: 5 total, 3 done.
	if p.ProjectCount != 2 || p.SumTotal != 5 || p.SumDone != 3 || p.SumInProgress != 1 ||
		p.SumConflicts != 1 {
		t.Fatalf("page summary wrong: count=%d total=%d done=%d wip=%d conf=%d",
			p.ProjectCount, p.SumTotal, p.SumDone, p.SumInProgress, p.SumConflicts)
	}
	// velocity signals replace the burn-down tiles: sparklines always render.
	if p.ClosedSpark == "" || p.ReadySpark == "" {
		t.Fatal("velocity sparklines must be rendered")
	}
	if p.ReadyNow != 1 { // p-4 is the only unblocked todo card
		t.Fatalf("ReadyNow: want 1, got %d", p.ReadyNow)
	}
}

func TestBuildDigest(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Hour)     // in window
	yesterday := now.Add(-26 * time.Hour) // in window
	old := now.Add(-10 * 24 * time.Hour)  // outside 7d window
	r := &rollup.Rollup{Projects: []rollup.Project{
		{
			Slug: "alpha",
			Epics: []rollup.Epic{{
				Issue:    rollup.Card{ID: "a-1", Title: "epic", Column: rollup.ColumnInProgress, Updated: recent},
				Children: []rollup.Card{{ID: "a-2", Title: "child done", Column: rollup.ColumnDone, Updated: yesterday}},
			}},
			Loose: []rollup.Card{
				{ID: "a-3", Title: "fresh todo", Column: rollup.ColumnTodo, Updated: recent},
				{ID: "a-4", Title: "stale", Column: rollup.ColumnTodo, Updated: old}, // excluded
			},
		},
		{Slug: "beta", Loose: []rollup.Card{{ID: "b-1", Title: "beta done", Column: rollup.ColumnDone, Updated: now.Add(-time.Minute)}}},
	}}

	groups, total := buildDigest(r, "", now, digestWindow)
	if total != 4 { // a-1, a-2, a-3, b-1 — a-4 is too old
		t.Fatalf("want 4 recent items, got %d", total)
	}
	byKey := map[string]vmDigestGroup{}
	for _, g := range groups {
		byKey[g.Key] = g
	}
	if byKey["done"].Count != 2 || byKey["todo"].Count != 1 || byKey["in_progress"].Count != 1 {
		t.Fatalf("group counts wrong: %+v", byKey)
	}
	// done group sorted newest-first: b-1 (1m ago) before a-2 (26h ago).
	if got := byKey["done"].Items; len(got) != 2 || got[0].ID != "b-1" || got[1].ID != "a-2" {
		t.Fatalf("done group not newest-first: %+v", got)
	}
	// filter to beta only.
	bg, bt := buildDigest(r, "beta", now, digestWindow)
	if bt != 1 || len(bg) != 1 || bg[0].Key != "done" || bg[0].Items[0].ID != "b-1" {
		t.Fatalf("filtered digest wrong: total=%d groups=%+v", bt, bg)
	}
}

func TestBuildDigest_CapsPerGroup(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	var loose []rollup.Card
	for i := 0; i < digestPerGroupCap+5; i++ {
		loose = append(loose, rollup.Card{ID: fmt.Sprintf("x-%d", i), Column: rollup.ColumnTodo, Updated: now.Add(-time.Duration(i) * time.Hour)})
	}
	r := &rollup.Rollup{Projects: []rollup.Project{{Slug: "x", Loose: loose}}}
	g, total := buildDigest(r, "", now, digestWindow)
	if total != digestPerGroupCap+5 {
		t.Fatalf("total should count all in-window, got %d", total)
	}
	if len(g) != 1 || g[0].Count != digestPerGroupCap+5 || len(g[0].Items) != digestPerGroupCap || g[0].More != 5 {
		t.Fatalf("cap/More wrong: count=%d shown=%d more=%d", g[0].Count, len(g[0].Items), g[0].More)
	}
}

func TestPct(t *testing.T) {
	cases := []struct{ d, tot, want int }{
		{0, 0, 0}, {1, 0, 0}, {2, 4, 50}, {3, 5, 60}, {1, 3, 33}, {2, 3, 67}, {7, 7, 100},
	}
	for _, c := range cases {
		if got := pct(c.d, c.tot); got != c.want {
			t.Errorf("pct(%d,%d)=%d want %d", c.d, c.tot, got, c.want)
		}
	}
}

func TestBoardCache_SingleflightCollapsesConcurrent(t *testing.T) {
	var calls int32
	bc := newBoardCache(50*time.Millisecond, func(_ context.Context) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond)
		return []byte(`{"projects":[]}`), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _ = bc.get(context.Background()) }()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("singleflight should collapse 25 concurrent callers to 1, got %d", got)
	}
}

func TestBoardCache_LastGoodOnError(t *testing.T) {
	fail := false
	bc := newBoardCache(time.Millisecond, func(_ context.Context) ([]byte, error) {
		if fail {
			return nil, context.DeadlineExceeded
		}
		return []byte(`{"ok":true}`), nil
	})
	if _, stale, err := bc.get(context.Background()); err != nil || stale {
		t.Fatalf("first call should be fresh: stale=%v err=%v", stale, err)
	}
	time.Sleep(2 * time.Millisecond)
	fail = true
	body, stale, err := bc.get(context.Background())
	if err != nil || !stale || string(body) != `{"ok":true}` {
		t.Fatalf("on backend error: want last-good + stale, got body=%q stale=%v err=%v", body, stale, err)
	}
}

func TestBoardCache_GoodTimestampIsFetchTimeNotNow(t *testing.T) {
	fail := false
	bc := newBoardCache(time.Millisecond, func(_ context.Context) ([]byte, error) {
		if fail {
			return nil, context.DeadlineExceeded
		}
		return []byte(`{"ok":true}`), nil
	})
	if _, _, err := bc.get(context.Background()); err != nil {
		t.Fatalf("seed fetch: %v", err)
	}
	fetchedAt := bc.goodTimestamp()
	if fetchedAt.IsZero() {
		t.Fatal("goodTimestamp must be set after a successful fetch")
	}
	time.Sleep(60 * time.Millisecond)
	fail = true
	if _, stale, _ := bc.get(context.Background()); !stale {
		t.Fatal("expected stale after backend error")
	}
	// The stale banner must report the original fetch time, not "now".
	if !bc.goodTimestamp().Equal(fetchedAt) {
		t.Fatalf("goodTimestamp moved: was %v now %v (banner would mislead)", fetchedAt, bc.goodTimestamp())
	}
	if time.Since(bc.goodTimestamp()) < 40*time.Millisecond {
		t.Fatal("goodTimestamp should reflect the older fetch, not the recent failed attempt")
	}
}

func TestBuildPage_PlacementAndSegs(t *testing.T) {
	r := &rollup.Rollup{
		GeneratedAt: time.Now().Add(-90 * time.Minute),
		Projects: []rollup.Project{
			{
				Slug: "alpha",
				Epics: []rollup.Epic{{
					Issue:    rollup.Card{ID: "a-1", Title: "ship board", Status: "closed", Column: rollup.ColumnInProgress, Priority: 1},
					Column:   rollup.ColumnInProgress,
					Conflict: true,
					Children: []rollup.Card{
						{ID: "c1", Column: rollup.ColumnDone}, {ID: "c2", Column: rollup.ColumnDone},
						{ID: "c3", Column: rollup.ColumnTodo},
					},
				}},
				Loose: []rollup.Card{{ID: "l-1", Title: "stray", Status: "open", Column: rollup.ColumnTodo, Priority: 0}},
			},
			{Slug: "Unassigned"},
		},
		Diagnostics: []rollup.Diagnostic{{Kind: "multi_project", IssueID: "x-9"}},
	}
	p := pageOf(r, false, "2026-01-01T00:00:00Z", 30, "")

	if p.Empty || len(p.Projects) != 2 || p.DiagCount != 1 {
		t.Fatalf("page shape wrong: %#v", p)
	}
	alpha := p.Projects[0]
	if alpha.Slug != "alpha" || len(alpha.Lanes) != 5 {
		t.Fatalf("alpha lanes: %#v", alpha)
	}
	laneByKey := map[string]vmLane{}
	for _, l := range alpha.Lanes {
		laneByKey[l.Key] = l
	}
	ip := laneByKey["in_progress"]
	if ip.Count != 1 || !ip.Cards[0].IsEpic || !ip.Cards[0].Conflict {
		t.Fatalf("epic should be in in_progress with conflict: %#v", ip)
	}
	if ip.Cards[0].ChildTotal != 3 || len(ip.Cards[0].Segs) != 2 { // done(2)+todo(1) => 2 segments
		t.Fatalf("epic child segs wrong: %#v", ip.Cards[0])
	}
	if td := laneByKey["todo"]; td.Count != 1 || td.Cards[0].IsEpic {
		t.Fatalf("loose card should be a non-epic in todo: %#v", td)
	}
}

func TestBuildPage_ProjectSwitcher(t *testing.T) {
	r := &rollup.Rollup{Projects: []rollup.Project{
		{Slug: "alpha", Loose: []rollup.Card{{ID: "a", Column: rollup.ColumnTodo}}},
		{Slug: "Unassigned", Loose: []rollup.Card{{ID: "u", Column: rollup.ColumnTodo}}},
	}}
	all := pageOf(r, false, "", 30, "")
	if len(all.AllSlugs) != 2 || all.Selected != "" || len(all.Projects) != 2 {
		t.Fatalf("unfiltered: %#v", all)
	}
	sel := pageOf(r, false, "", 30, "alpha")
	if sel.Selected != "alpha" || len(sel.Projects) != 1 || sel.Projects[0].Slug != "alpha" {
		t.Fatalf("filtered to alpha wrong: %#v", sel)
	}
	if len(sel.AllSlugs) != 2 {
		t.Fatalf("switcher must still list ALL slugs when filtered: %#v", sel.AllSlugs)
	}
	bogus := pageOf(r, false, "", 30, "does-not-exist")
	if bogus.Selected != "" || len(bogus.Projects) != 2 {
		t.Fatalf("unknown project must fall back to all: %#v", bogus)
	}
}

func TestBoardTemplate_RendersAndEscapes(t *testing.T) {
	r := &rollup.Rollup{
		GeneratedAt: time.Now(),
		Projects: []rollup.Project{{
			Slug: "alpha",
			Epics: []rollup.Epic{{
				Issue:    rollup.Card{ID: "a-1", Title: "<script>alert(1)</script>", Status: "open", Column: rollup.ColumnTodo},
				Column:   rollup.ColumnTodo,
				Conflict: true,
				Children: []rollup.Card{{ID: "k", Column: rollup.ColumnDone}},
			}},
		}},
	}
	var buf bytes.Buffer
	if err := boardPageTmpl.Execute(&buf, pageOf(r, true, "2026-01-01T00:00:00Z", 30, "")); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Project Board", "alpha", "closed · open children", "Stale", "auto-refresh 30s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered page missing %q", want)
		}
	}
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatal("XSS: issue title was not HTML-escaped in the rendered board")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatal("expected the malicious title to appear HTML-escaped")
	}
}

func TestExplainIssue_NoFilesNoGraph(t *testing.T) {
	dir := t.TempDir()
	resp, err := explainIssueInWorkspace(context.Background(), dir, "bd-123")
	if err != nil {
		t.Fatal(err)
	}
	if resp.IssueID != "bd-123" {
		t.Errorf("expected issue ID bd-123, got %s", resp.IssueID)
	}
	if resp.HasGraph {
		t.Error("expected HasGraph to be false")
	}
	if len(resp.Files) != 0 {
		t.Errorf("expected 0 files, got %d", len(resp.Files))
	}
}

func TestExplainIssue_WithGraph(t *testing.T) {
	dir := t.TempDir()

	// Write a mock knowledge-graph.json
	graph := uaGraph{
		Project: uaProject{
			Name: "test-proj",
		},
		Nodes: []uaNode{
			{
				ID:       "file:src/auth.go",
				Type:     "file",
				Name:     "auth.go",
				FilePath: "src/auth.go",
				Summary:  "Handles user authentication",
			},
		},
		Edges: []uaEdge{},
		Layers: []uaLayer{
			{
				ID:          "layer:api",
				Name:        "API Layer",
				Description: "HTTP endpoints",
				NodeIds:     []string{"file:src/auth.go"},
			},
		},
	}
	uaDir := filepath.Join(dir, ".understand-anything")
	if err := os.MkdirAll(uaDir, 0755); err != nil {
		t.Fatal(err)
	}
	graphData, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uaDir, "knowledge-graph.json"), graphData, 0644); err != nil {
		t.Fatal(err)
	}

	resp, err := explainIssueInWorkspace(context.Background(), dir, "bd-123")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.HasGraph {
		t.Error("expected HasGraph to be true")
	}
}

func TestExplainEndpoint(t *testing.T) {
	// Simple test for the route handler
	req := httptest.NewRequest("GET", "/explain?issue=bd-123", nil)
	w := httptest.NewRecorder()

	sema := make(chan struct{}, 1)
	explicit := []string{t.TempDir()}
	var globs []string

	handler := func(w http.ResponseWriter, r *http.Request) {
		select {
		case sema <- struct{}{}:
			defer func() { <-sema }()
		default:
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}

		issue := r.URL.Query().Get("issue")
		project := r.URL.Query().Get("project")
		if issue == "" {
			http.Error(w, "missing issue parameter", http.StatusBadRequest)
			return
		}

		wDirs := resolveWorkspaces(explicit, globs)
		var targetDir string

		if project != "" {
			for _, d := range wDirs {
				if workspaceName(d) == project {
					targetDir = d
					break
				}
			}
		}

		if targetDir == "" {
			if len(wDirs) > 0 {
				targetDir = wDirs[0]
			} else {
				targetDir = ""
			}
		}

		resp, err := explainIssueInWorkspace(r.Context(), targetDir, issue)
		if err != nil {
			http.Error(w, "failed to explain issue: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}

	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var explainResp ExplainResponse
	if err := json.NewDecoder(resp.Body).Decode(&explainResp); err != nil {
		t.Fatal(err)
	}

	if explainResp.IssueID != "bd-123" {
		t.Errorf("expected issue ID bd-123, got %s", explainResp.IssueID)
	}
}

func TestParseShowIssueJSON(t *testing.T) {
	// 1. Direct array format
	arrJSON := `[{"id":"bd-123","title":"Test Issue","status":"in_progress","priority":1}]`
	var issue *types.IssueDetails
	var err error
	issue, err = parseShowIssueJSON([]byte(arrJSON))
	if err != nil {
		t.Fatalf("failed to parse array JSON: %v", err)
	}
	if issue.ID != "bd-123" || issue.Title != "Test Issue" || issue.Status != "in_progress" {
		t.Errorf("incorrect fields parsed from array JSON: %+v", issue)
	}

	// 2. Wrapped envelope format: {"schema_version": 2, "data": [...]}
	envJSON := `{"schema_version":2,"data":[{"id":"bd-123","title":"Test Issue Env","status":"done","priority":2}]}`
	issue, err = parseShowIssueJSON([]byte(envJSON))
	if err != nil {
		t.Fatalf("failed to parse envelope JSON: %v", err)
	}
	if issue.ID != "bd-123" || issue.Title != "Test Issue Env" || issue.Status != "done" {
		t.Errorf("incorrect fields parsed from envelope JSON: %+v", issue)
	}

	// 3. Single object format
	objJSON := `{"id":"bd-123","title":"Test Issue Obj","status":"todo","priority":0}`
	issue, err = parseShowIssueJSON([]byte(objJSON))
	if err != nil {
		t.Fatalf("failed to parse object JSON: %v", err)
	}
	if issue.ID != "bd-123" || issue.Title != "Test Issue Obj" || issue.Status != "todo" {
		t.Errorf("incorrect fields parsed from object JSON: %+v", issue)
	}
}
