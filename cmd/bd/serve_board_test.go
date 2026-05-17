package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rollup"
)

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
	p := buildPage(r, false, "2026-01-01T00:00:00Z", 30, "")

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
	all := buildPage(r, false, "", 30, "")
	if len(all.AllSlugs) != 2 || all.Selected != "" || len(all.Projects) != 2 {
		t.Fatalf("unfiltered: %#v", all)
	}
	sel := buildPage(r, false, "", 30, "alpha")
	if sel.Selected != "alpha" || len(sel.Projects) != 1 || sel.Projects[0].Slug != "alpha" {
		t.Fatalf("filtered to alpha wrong: %#v", sel)
	}
	if len(sel.AllSlugs) != 2 {
		t.Fatalf("switcher must still list ALL slugs when filtered: %#v", sel.AllSlugs)
	}
	bogus := buildPage(r, false, "", 30, "does-not-exist")
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
	if err := boardPageTmpl.Execute(&buf, buildPage(r, true, "2026-01-01T00:00:00Z", 30, "")); err != nil {
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
