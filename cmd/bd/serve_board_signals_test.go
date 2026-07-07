package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rollup"
)

var sigNow = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

func TestStaleDetection_Thresholding(t *testing.T) {
	after := 2 * time.Hour
	cases := []struct {
		name string
		c    rollup.Card
		want bool
	}{
		{"over threshold", rollup.Card{Column: rollup.ColumnInProgress, Updated: sigNow.Add(-3 * time.Hour)}, true},
		{"just under", rollup.Card{Column: rollup.ColumnInProgress, Updated: sigNow.Add(-119 * time.Minute)}, false},
		{"exactly at threshold", rollup.Card{Column: rollup.ColumnInProgress, Updated: sigNow.Add(-2 * time.Hour)}, false},
		{"wrong column", rollup.Card{Column: rollup.ColumnTodo, Updated: sigNow.Add(-30 * 24 * time.Hour)}, false},
		{"no timestamp", rollup.Card{Column: rollup.ColumnInProgress}, false},
	}
	for _, c := range cases {
		if got := isStale(c.c, sigNow, after); got != c.want {
			t.Errorf("%s: isStale=%v want %v", c.name, got, c.want)
		}
	}
}

// triageRollup covers all four "needs you now" buckets.
func triageRollup() *rollup.Rollup {
	return &rollup.Rollup{Projects: []rollup.Project{{
		Slug: "alpha",
		Loose: []rollup.Card{
			{ID: "a-stale", Title: "stuck", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-5 * time.Hour)},
			{ID: "a-blocked", Title: "waiting", Column: rollup.ColumnTodo, Blocked: true, Updated: sigNow.Add(-time.Hour)},
			{ID: "a-review", Title: "just closed", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-time.Hour), Updated: sigNow.Add(-time.Hour)},
			{ID: "a-old-done", Title: "old close", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-48 * time.Hour)},
			{ID: "a-ready", Title: "unblocked now", Column: rollup.ColumnTodo, LastDepClosed: sigNow.Add(-2 * time.Hour)},
			{ID: "a-plain", Title: "nothing special", Column: rollup.ColumnTodo, Updated: sigNow.Add(-time.Hour)},
			{ID: "a-fresh-wip", Title: "active work", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-10 * time.Minute)},
		},
	}}}
}

func TestBuildTriage(t *testing.T) {
	tr := buildTriage(triageRollup(), "", sigNow, 2*time.Hour, triageLookback)
	if tr.Count != 4 {
		t.Fatalf("want 4 triage items, got %d: %+v", tr.Count, tr.Groups)
	}
	byKey := map[string]vmTriageGroup{}
	for _, g := range tr.Groups {
		byKey[g.Key] = g
	}
	for key, wantID := range map[string]string{
		"stale": "a-stale", "blocked": "a-blocked", "review": "a-review", "ready": "a-ready",
	} {
		g, ok := byKey[key]
		if !ok || g.Count != 1 || g.Items[0].ID != wantID {
			t.Fatalf("group %s: want single item %s, got %+v", key, wantID, g)
		}
		if g.Items[0].Slug != "alpha" {
			t.Fatalf("group %s must carry the workspace slug, got %q", key, g.Items[0].Slug)
		}
	}
}

func TestBuildTriage_CapsPerGroup(t *testing.T) {
	var loose []rollup.Card
	for i := 0; i < triagePerGroupCap+3; i++ {
		loose = append(loose, rollup.Card{
			ID: "s", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-time.Duration(3+i) * time.Hour),
		})
	}
	r := &rollup.Rollup{Projects: []rollup.Project{{Slug: "p", Loose: loose}}}
	tr := buildTriage(r, "", sigNow, 2*time.Hour, triageLookback)
	g := tr.Groups[0]
	if g.Count != triagePerGroupCap+3 || len(g.Items) != triagePerGroupCap || g.More != 3 {
		t.Fatalf("cap/More wrong: count=%d shown=%d more=%d", g.Count, len(g.Items), g.More)
	}
	// stale sorts most-stale (oldest) first.
	if !g.Items[0].at.Before(g.Items[1].at) {
		t.Fatal("stale group must sort oldest-first")
	}
}

func TestHealthDotClassification(t *testing.T) {
	cases := []struct {
		name string
		st   wsStatus
		want string
	}{
		{"error", wsStatus{Err: "boom"}, "err"},
		{"error wins over signals", wsStatus{Err: "boom", Stale: 2, LastActivity: sigNow}, "err"},
		{"stale work", wsStatus{Stale: 1, LastActivity: sigNow.Add(-time.Hour)}, "risk"},
		{"blocked grew", wsStatus{BlockedGrew: true, LastActivity: sigNow.Add(-time.Hour)}, "risk"},
		{"idle", wsStatus{LastActivity: sigNow.Add(-4 * 24 * time.Hour)}, "idle"},
		{"never active", wsStatus{}, "idle"},
		{"on track", wsStatus{RecentCloses: 2, LastActivity: sigNow.Add(-time.Hour)}, "ok"},
	}
	for _, c := range cases {
		if got := classifyHealth(c.st, sigNow); got != c.want {
			t.Errorf("%s: classifyHealth=%q want %q", c.name, got, c.want)
		}
	}
}

func TestBuildPayload_ErrorEntriesNeverDropped(t *testing.T) {
	good := `{"generated_at":"2026-07-03T10:00:00Z","projects":[{"slug":"ok","epics":[],"loose":[{"id":"x","title":"x","status":"in_progress","column":"in_progress","priority":1,"updated_at":"2026-07-03T04:00:00Z"}]}],"diagnostics":[]}`
	results := []wsResult{
		{dir: "/w/beads-dead-workspace", err: io.ErrUnexpectedEOF},
		{dir: "/w/beads-garbled-workspace", data: []byte("not json")},
		{dir: "/w/beads-ok-workspace", data: []byte(good)},
	}
	p := buildPayload(results, sigNow, 2*time.Hour)
	if len(p.Workspaces) != 3 {
		t.Fatalf("every workspace must produce a status entry, got %d", len(p.Workspaces))
	}
	byName := map[string]wsStatus{}
	for _, ws := range p.Workspaces {
		byName[ws.Name] = ws
	}
	if byName["dead"].Err == "" || byName["garbled"].Err == "" {
		t.Fatalf("failed/malformed workspaces must carry an error: %+v", p.Workspaces)
	}
	ok := byName["ok"]
	if ok.Err != "" || ok.Stale != 1 {
		t.Fatalf("good workspace signals wrong: %+v", ok)
	}
	if len(p.Projects) != 1 || p.Projects[0].Slug != "ok" {
		t.Fatalf("merged rollup should hold the good project only: %+v", p.Projects)
	}
}

func TestNtfy_DiffAndDebounce(t *testing.T) {
	var mu sync.Mutex
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		posts = append(posts, string(body))
		mu.Unlock()
	}))
	defer srv.Close()

	n := newNtfyNotifier(srv.URL)
	staleAfter := 2 * time.Hour

	// Prime with a payload that already has a stale issue: a restart must not
	// re-announce the existing backlog.
	base := &boardPayload{
		Rollup: rollup.Rollup{Projects: []rollup.Project{{Slug: "alpha", Loose: []rollup.Card{
			{ID: "a-old", Title: "pre-existing stale", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-6 * time.Hour)},
		}}}},
		Workspaces: []wsStatus{{Name: "alpha", Dir: "/w/alpha", Blocked: 1}},
	}
	if msgs := n.observe(base, sigNow, staleAfter); len(msgs) != 0 {
		t.Fatalf("first observe must only seed state, got %v", msgs)
	}

	// Next refresh: a new stale issue, a new review-ready close, blocked 1->3.
	next := &boardPayload{
		Rollup: rollup.Rollup{Projects: []rollup.Project{{Slug: "alpha", Loose: []rollup.Card{
			{ID: "a-old", Title: "pre-existing stale", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-7 * time.Hour)},
			{ID: "a-new", Title: "newly stale", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-3 * time.Hour)},
			{ID: "a-done", Title: "shipped", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-time.Hour)},
		}}}},
		Workspaces: []wsStatus{{Name: "alpha", Dir: "/w/alpha", Blocked: 3}},
	}
	msgs := n.observe(next, sigNow, staleAfter)
	if len(msgs) != 3 {
		t.Fatalf("want 3 notifications (stale, review, blocked rise), got %v", msgs)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"a-new", "newly stale", "a-done", "shipped", "[alpha]", "1 → 3"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notifications missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "a-old") {
		t.Fatal("primed issue must not be re-announced")
	}
	if !next.Workspaces[0].BlockedGrew {
		t.Fatal("observe must mark BlockedGrew for the health dot")
	}

	n.notify(msgs)
	mu.Lock()
	got := len(posts)
	mu.Unlock()
	if got != 3 {
		t.Fatalf("want 3 POSTs to the ntfy server, got %d", got)
	}

	// Same payload again: everything debounced, blocked count unchanged.
	if again := n.observe(next, sigNow, staleAfter); len(again) != 0 {
		t.Fatalf("re-observe must be fully debounced, got %v", again)
	}
}

func TestNtfy_DisabledURLSendsNothing(t *testing.T) {
	n := newNtfyNotifier("")
	n.notify([]string{"msg"}) // must not panic or post anywhere
}

func TestVelocityClosedPerDay(t *testing.T) {
	r := &rollup.Rollup{Projects: []rollup.Project{{Slug: "p", Loose: []rollup.Card{
		{ID: "today-1", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-2 * time.Hour)},
		{ID: "today-2", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-3 * time.Hour)},
		{ID: "yesterday", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-30 * time.Hour)},
		{ID: "ancient", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-20 * 24 * time.Hour)},
		{ID: "no-closed-at", Column: rollup.ColumnDone, Updated: sigNow.Add(-time.Hour)}, // legacy row, no closed_at
		{ID: "open", Column: rollup.ColumnTodo, Updated: sigNow},
	}}}}
	got := closedPerDay(r, "", sigNow, velocityDays)
	if len(got) != velocityDays {
		t.Fatalf("want %d buckets, got %d", velocityDays, len(got))
	}
	if got[velocityDays-1] != 2 { // today-1, today-2; no-closed-at must NOT count
		t.Fatalf("today bucket: want 2, got %d (%v)", got[velocityDays-1], got)
	}
	if got[velocityDays-2] != 1 {
		t.Fatalf("yesterday bucket: want 1, got %d (%v)", got[velocityDays-2], got)
	}
	if sum(got) != 3 { // ancient + no-closed-at excluded
		t.Fatalf("window total: want 3, got %d", sum(got))
	}
}

func TestVelocityReadyDepthAndSampler(t *testing.T) {
	// todo cards: a-blocked (blocked), a-ready, a-plain => 2 unblocked.
	if d := readyDepth(triageRollup(), ""); d != 2 {
		t.Fatalf("readyDepth: want 2 unblocked todo cards, got %d", d)
	}
	var s depthSampler
	for i := 0; i < depthSamples+10; i++ {
		s.add(i)
	}
	snap := s.snapshot()
	if len(snap) != depthSamples || snap[0] != 10 || snap[len(snap)-1] != depthSamples+9 {
		t.Fatalf("sampler ring wrong: len=%d first=%d last=%d", len(snap), snap[0], snap[len(snap)-1])
	}
}

func TestVelocitySparklineSVG(t *testing.T) {
	svg := string(sparklineSVG([]int{0, 2, 1, 4}, 220, 36))
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "<polyline") {
		t.Fatalf("not an svg polyline: %s", svg)
	}
	// max value (4) must touch the top padding; zero the bottom.
	if !strings.Contains(svg, ",2.0") || !strings.Contains(svg, ",34.0") {
		t.Fatalf("scaling wrong: %s", svg)
	}
	if s := string(sparklineSVG(nil, 220, 36)); !strings.Contains(s, "<polyline") {
		t.Fatalf("empty input must still render a flat line: %s", s)
	}
}

func TestParseMemoriesJSON(t *testing.T) {
	legacy := `{"schema_version":2,"alpha-key":{"key":"alpha-key","value":"first line\nsecond line"},"beta":{"key":"beta","value":"one-liner"}}`
	got := parseMemoriesJSON([]byte(legacy))
	if len(got) != 2 || got[0].Key != "alpha-key" || got[0].Line != "first line" || got[1].Line != "one-liner" {
		t.Fatalf("legacy shape parsed wrong: %+v", got)
	}
	envelope := `{"schema_version":2,"data":{"k":{"key":"k","value":"v"}}}`
	if got := parseMemoriesJSON([]byte(envelope)); len(got) != 1 || got[0].Key != "k" || got[0].Line != "v" {
		t.Fatalf("envelope shape parsed wrong: %+v", got)
	}
	// Legacy flat map that includes a memory literally keyed "data" must NOT be
	// mistaken for the envelope — all sibling memories must survive.
	dataKeyed := `{"schema_version":2,"data":{"key":"data","value":"a config value"},"other":{"key":"other","value":"kept"}}`
	got = parseMemoriesJSON([]byte(dataKeyed))
	byKey := map[string]string{}
	for _, m := range got {
		byKey[m.Key] = m.Line
	}
	if len(got) != 2 || byKey["data"] != "a config value" || byKey["other"] != "kept" {
		t.Fatalf(`legacy map with a "data" memory dropped siblings: %+v`, got)
	}
	// Degenerate: the ONLY memory is keyed "data" (top-level {schema_version,data}).
	// The record has a string "value", so it stays flat and is surfaced, not
	// descended into as an envelope.
	onlyData := `{"schema_version":2,"data":{"key":"data","value":"sole memory"}}`
	if got := parseMemoriesJSON([]byte(onlyData)); len(got) != 1 || got[0].Key != "data" || got[0].Line != "sole memory" {
		t.Fatalf(`sole "data" memory must be surfaced, got %+v`, got)
	}
	if got := parseMemoriesJSON([]byte("not json")); got != nil {
		t.Fatalf("garbage must parse to nil, got %+v", got)
	}
}

func TestBoardTemplate_RendersSignals(t *testing.T) {
	pl := &boardPayload{
		Rollup: *triageRollup(),
		Workspaces: []wsStatus{
			{Name: "alpha", Stale: 1, LastActivity: sigNow.Add(-time.Hour)},
			{Name: "broken", Err: "exec failed"},
			{Name: "quiet", Memories: []wsMemory{{Key: "gotcha", Line: "ports drift"}}},
		},
	}
	page := buildPage(pl, false, "", 30, "", 2*time.Hour, []int{1, 2, 3})
	if len(page.Workspaces) != 3 {
		t.Fatalf("all workspaces must render, got %+v", page.Workspaces)
	}
	dots := map[string]string{}
	for _, w := range page.Workspaces {
		dots[w.Name] = w.Dot
	}
	if dots["broken"] != "err" || dots["alpha"] != "risk" {
		t.Fatalf("health dots wrong: %v", dots)
	}
	var buf bytes.Buffer
	if err := boardPageTmpl.Execute(&buf, page); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Needs you now",        // triage panel
		"ws-pill err",          // explicit error entry in the strip
		"broken · failed",      // error workspace named, not dropped
		"stale-b",              // red stale badge on the kanban card
		"Closed / day",         // velocity tile
		"Ready queue depth",    // velocity tile
		"<svg class=\"spark\"", // inline sparkline
		"Memories",             // memories panel
		"ports drift",          // memory first line
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered page missing %q", want)
		}
	}
	if strings.Contains(out, "Overall ·") {
		t.Fatal("burn-down overall tile must be removed, not kept alongside velocity")
	}
}

// A card with a blocking dependency outside the fetch window (HasUnknownDeps)
// must not be announced "newly ready" even if it otherwise qualifies.
func TestNewlyReady_ExcludesUnknownDeps(t *testing.T) {
	base := rollup.Card{Column: rollup.ColumnTodo, LastDepClosed: sigNow.Add(-time.Hour)}
	if !newlyReady(base, sigNow, triageLookback) {
		t.Fatal("baseline card should be newly ready")
	}
	unknown := base
	unknown.HasUnknownDeps = true
	if newlyReady(unknown, sigNow, triageLookback) {
		t.Fatal("card with an unknown (out-of-window) dep must not be newly ready")
	}
	// It must also drop out of the triage "ready" bucket.
	r := &rollup.Rollup{Projects: []rollup.Project{{Slug: "p", Loose: []rollup.Card{
		{ID: "u", Column: rollup.ColumnTodo, LastDepClosed: sigNow.Add(-time.Hour), HasUnknownDeps: true},
	}}}}
	if tr := buildTriage(r, "", sigNow, 2*time.Hour, triageLookback); tr.Count != 0 {
		t.Fatalf("unknown-dep card must not appear in triage, got %+v", tr.Groups)
	}
}

// Filter threading: with a project selected, triage/velocity/ready scope to that
// project, matching the summary tiles instead of computing fleet-wide.
func TestSignals_HonorSelectedProject(t *testing.T) {
	r := &rollup.Rollup{Projects: []rollup.Project{
		{Slug: "alpha", Loose: []rollup.Card{
			{ID: "a-stale", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-5 * time.Hour)},
			{ID: "a-ready", Column: rollup.ColumnTodo},
			{ID: "a-done", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-time.Hour)},
		}},
		{Slug: "beta", Loose: []rollup.Card{
			{ID: "b-stale", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-5 * time.Hour)},
			{ID: "b-ready", Column: rollup.ColumnTodo},
			{ID: "b-done", Column: rollup.ColumnDone, ClosedAt: sigNow.Add(-time.Hour)},
		}},
	}}
	if tr := buildTriage(r, "alpha", sigNow, 2*time.Hour, triageLookback); tr.Count != 2 {
		t.Fatalf("filtered triage should see only alpha's stale+review, got %d: %+v", tr.Count, tr.Groups)
	}
	if tr := buildTriage(r, "", sigNow, 2*time.Hour, triageLookback); tr.Count != 4 {
		t.Fatalf("unfiltered triage should see both projects, got %d", tr.Count)
	}
	if d := readyDepth(r, "alpha"); d != 1 {
		t.Fatalf("filtered readyDepth should be 1, got %d", d)
	}
	if d := readyDepth(r, ""); d != 2 {
		t.Fatalf("unfiltered readyDepth should be 2, got %d", d)
	}
	if got := sum(closedPerDay(r, "alpha", sigNow, velocityDays)); got != 1 {
		t.Fatalf("filtered closedPerDay should count 1, got %d", got)
	}
	if got := sum(closedPerDay(r, "", sigNow, velocityDays)); got != 2 {
		t.Fatalf("unfiltered closedPerDay should count 2, got %d", got)
	}
}

// Bug 5 regression: two workspaces with the SAME display Name but distinct Dir
// must track their blocked counts independently — no cross-contamination flap.
func TestNtfy_BlockedKeyedByDirNotName(t *testing.T) {
	n := newNtfyNotifier("") // notifications disabled; observe still diffs state
	staleAfter := 2 * time.Hour
	mk := func(aBlocked, bBlocked int) *boardPayload {
		return &boardPayload{Workspaces: []wsStatus{
			{Name: "beads", Dir: "/w/a", Blocked: aBlocked},
			{Name: "beads", Dir: "/w/b", Blocked: bBlocked},
		}}
	}
	n.observe(mk(1, 1), sigNow, staleAfter) // seed
	// Only workspace /w/b grows. If keyed by Name, /w/a's entry would clobber
	// /w/b's baseline and either miss or misattribute the rise.
	p := mk(1, 3)
	n.observe(p, sigNow, staleAfter)
	if p.Workspaces[0].BlockedGrew {
		t.Fatal("/w/a did not grow; must not be flagged")
	}
	if !p.Workspaces[1].BlockedGrew {
		t.Fatal("/w/b grew 1->3; must be flagged (dir-keyed state)")
	}
}

// Bug 6: the debounce set is capped; crossing it cold-starts (re-seeds this
// cycle without re-announcing the whole backlog).
func TestNtfy_SeenCapColdStarts(t *testing.T) {
	n := newNtfyNotifier("")
	for i := 0; i <= seenCap; i++ {
		n.seen[fmt.Sprintf("k%d", i)] = true
	}
	n.primed = true
	p := &boardPayload{Rollup: rollup.Rollup{Projects: []rollup.Project{{Slug: "p", Loose: []rollup.Card{
		{ID: "s", Column: rollup.ColumnInProgress, Updated: sigNow.Add(-5 * time.Hour)},
	}}}}}
	msgs := n.observe(p, sigNow, 2*time.Hour)
	if len(msgs) != 0 {
		t.Fatalf("over-cap observe must cold-start (seed only), got %v", msgs)
	}
	if len(n.seen) > seenCap {
		t.Fatalf("seen map must be cleared at the cap, len=%d", len(n.seen))
	}
	// Next cycle announces normally again.
	if msgs := n.observe(p, sigNow, 2*time.Hour); len(msgs) != 0 {
		t.Fatalf("same card is debounced after reseed, got %v", msgs)
	}
}

// Bug 4: notifyAsync must not block the fetch path on a slow ntfy host, and
// overlapping batches drop rather than pile up.
func TestNtfy_NotifyAsyncDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-release // hold the connection open
	}))
	defer srv.Close()

	n := newNtfyNotifier(srv.URL)
	start := time.Now()
	n.notifyAsync([]string{"first"})  // occupies the single slot, blocks in-goroutine
	n.notifyAsync([]string{"second"}) // slot busy -> dropped, must return at once
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("notifyAsync blocked the caller for %s", elapsed)
	}
	close(release)
	// The dropped batch must never reach the server.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&hits) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Fatalf("overlapping batch should have been dropped: want 1 POST, got %d", h)
	}
}

// Bug 8: --show-memories defaults off so the board doesn't spawn a second bd
// subprocess per workspace unless explicitly asked.
func TestServeBoard_ShowMemoriesDefaultsOff(t *testing.T) {
	v, err := serveBoardCmd.Flags().GetBool("show-memories")
	if err != nil {
		t.Fatalf("show-memories flag missing: %v", err)
	}
	if v {
		t.Fatal("show-memories must default to off")
	}
}
