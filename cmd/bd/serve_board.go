package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rollup"
	"golang.org/x/sync/singleflight"
)

const maxBoardJSONBytes = 8 << 20 // 8 MiB stdout cap

type fetchFn func(ctx context.Context) ([]byte, error)

// boardCache: singleflight + TTL + last-good (spec C4/C7).
type boardCache struct {
	ttl   time.Duration
	fetch fetchFn
	sf    singleflight.Group

	mu     sync.Mutex
	good   []byte
	goodAt time.Time
}

func newBoardCache(ttl time.Duration, fetch fetchFn) *boardCache {
	return &boardCache{ttl: ttl, fetch: fetch}
}

// get returns (body, stale, err). stale=true means body is last-good after a
// backend error. err!=nil only when there is no last-good to fall back to.
func (b *boardCache) get(ctx context.Context) ([]byte, bool, error) {
	b.mu.Lock()
	fresh := b.good != nil && time.Since(b.goodAt) < b.ttl
	cached := b.good
	b.mu.Unlock()
	if fresh {
		return cached, false, nil
	}
	// singleflight passes the first caller's ctx to fetch; if that caller
	// cancels, peers share the error. Acceptable for an O(1)-viewer tailnet
	// board (last-good still serves), not worth a detached-fetch rework.
	v, err, _ := b.sf.Do("board", func() (interface{}, error) {
		body, ferr := b.fetch(ctx)
		if ferr != nil {
			return nil, ferr
		}
		b.mu.Lock()
		b.good, b.goodAt = body, time.Now()
		b.mu.Unlock()
		return body, nil
	})
	if err != nil {
		b.mu.Lock()
		good := b.good
		b.mu.Unlock()
		if good != nil {
			return good, true, nil
		}
		return nil, false, err
	}
	return v.([]byte), false, nil
}

// goodTimestamp returns when the last successful fetch completed (zero if
// none yet). The stale banner must show this, not the request time, or it
// defeats spec C7 (operators must not mistake old data for live).
func (b *boardCache) goodTimestamp() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.goodAt
}

// embeddedMode reports whether the beads workspace at dir uses embedded Dolt
// (as opposed to a remote server). Used to avoid leaking BEADS_DOLT_* server
// credentials into subprocess environments where they would override local DB.
func embeddedMode(dir string) bool {
	metaPath := filepath.Join(dir, ".beads", "metadata.json")
	if dir == "" {
		metaPath = filepath.Join(".beads", "metadata.json")
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var m struct {
		DoltMode string `json:"dolt_mode"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	return m.DoltMode == "embedded"
}

// execBoardJSONIn runs `bd board --json` (this same binary) in dir, with a
// hard deadline and an output cap. dir="" uses the process CWD. The web
// process holds no DB credentials.
func execBoardJSONIn(ctx context.Context, dir string, timeout time.Duration) ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve self: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Use -C to explicitly set the working directory so bd's .beads discovery
	// finds the workspace's own database rather than the parent process's CWD.
	args := []string{"board", "--json"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(cctx, self, args...)
	// For embedded workspaces, strip inherited BEADS_DOLT_* server credentials
	// so the subprocess reads its own local metadata.json instead of connecting
	// to the parent process's remote Dolt server.
	if embeddedMode(dir) {
		env := os.Environ()
		filtered := env[:0]
		for _, e := range env {
			if !strings.HasPrefix(e, "BEADS_DOLT_") {
				filtered = append(filtered, e)
			}
		}
		cmd.Env = filtered
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start bd board: %w", err)
	}
	// Bound memory on the shared host (spec C6): never buffer more than the
	// cap, even if the child misbehaves. Read at most cap+1; if we hit that,
	// the output is over-large — kill the child now rather than wait out the
	// timeout. Read fully before Wait (StdoutPipe contract).
	var out bytes.Buffer
	n, copyErr := io.Copy(&out, io.LimitReader(stdout, maxBoardJSONBytes+1))
	if n > maxBoardJSONBytes {
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("board json exceeds %d bytes", maxBoardJSONBytes)
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		msg := errBuf.String()
		if len(msg) > 2000 {
			msg = msg[:2000] + "…"
		}
		return nil, fmt.Errorf("bd board --json failed: %w (stderr: %s)", waitErr, msg)
	}
	if copyErr != nil {
		return nil, fmt.Errorf("reading bd board output: %w", copyErr)
	}
	return out.Bytes(), nil
}

// workspaceBlob pairs a workspace directory with its bd-board JSON payload.
type workspaceBlob struct {
	dir  string
	data []byte
}

// workspaceName derives an implied project slug from a workspace directory
// path that follows the beads-<project>-workspace convention.
// Returns "" when the dir doesn't match or no meaningful name can be derived.
func workspaceName(dir string) string {
	if dir == "" {
		return ""
	}
	base := filepath.Base(dir)
	after, ok := strings.CutPrefix(base, "beads-")
	if !ok {
		return ""
	}
	name, ok := strings.CutSuffix(after, "-workspace")
	if !ok || name == "" {
		return "" // "beads-workspace" has no project segment
	}
	return name
}

// mergeRollups combines rollup JSON blobs from multiple workspaces into one.
// Projects with the same slug are merged (their cards combined). Unassigned
// issues in a named workspace (beads-<project>-workspace) are re-slugged to
// that project name so they surface under the right column. GeneratedAt is
// the max across all inputs. Malformed blobs are skipped silently.
func mergeRollups(wbs []workspaceBlob) ([]byte, error) {
	var merged rollup.Rollup
	bySlug := map[string]*rollup.Project{}
	var slugOrder []string

	add := func(p rollup.Project) {
		if ex, ok := bySlug[p.Slug]; ok {
			ex.Epics = append(ex.Epics, p.Epics...)
			ex.Loose = append(ex.Loose, p.Loose...)
			return
		}
		cp := p
		bySlug[cp.Slug] = &cp
		slugOrder = append(slugOrder, cp.Slug)
	}

	for _, wb := range wbs {
		var r rollup.Rollup
		if err := json.Unmarshal(wb.data, &r); err != nil {
			continue
		}
		if r.GeneratedAt.After(merged.GeneratedAt) {
			merged.GeneratedAt = r.GeneratedAt
		}
		implied := workspaceName(wb.dir)
		for _, p := range r.Projects {
			if p.Slug == "Unassigned" && implied != "" {
				p.Slug = implied
			}
			add(p)
		}
		merged.Diagnostics = append(merged.Diagnostics, r.Diagnostics...)
	}

	for _, slug := range slugOrder {
		merged.Projects = append(merged.Projects, *bySlug[slug])
	}
	return json.Marshal(merged)
}

// resolveWorkspaces expands glob patterns into concrete workspace dirs and
// unions them with the explicit list, de-duplicated, order-stable (explicit
// first). Called on every fetch so a workspace created after startup (e.g. by
// register.sh) is picked up live — no restart. Non-matching/!dir globs are
// skipped. Empty result falls back to {""} (process CWD) for back-compat.
func resolveWorkspaces(explicit, globs []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, w := range explicit {
		add(w)
	}
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve-board: bad --workspace-glob %q: %v\n", g, err)
			continue
		}
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.IsDir() {
				add(m)
			}
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// fetchWorkspaces runs bd board --json in each workspace concurrently and
// merges the results. Workspaces that fail are skipped; error is returned
// only when all workspaces fail.
func fetchWorkspaces(ctx context.Context, workspaces []string, timeout time.Duration) ([]byte, error) {
	if len(workspaces) == 1 {
		return execBoardJSONIn(ctx, workspaces[0], timeout)
	}
	type result struct {
		data []byte
		err  error
	}
	results := make([]result, len(workspaces))
	var wg sync.WaitGroup
	for i, ws := range workspaces {
		wg.Add(1)
		go func(i int, ws string) {
			defer wg.Done()
			data, err := execBoardJSONIn(ctx, ws, timeout)
			results[i] = result{data, err}
		}(i, ws)
	}
	wg.Wait()

	var wbs []workspaceBlob
	for i, r := range results {
		if r.err == nil {
			wbs = append(wbs, workspaceBlob{dir: workspaces[i], data: r.data})
		} else {
			fmt.Fprintf(os.Stderr, "serve-board: workspace fetch error: %v\n", r.err)
		}
	}
	if len(wbs) == 0 {
		return nil, fmt.Errorf("all %d workspace(s) failed to produce board data", len(workspaces))
	}
	if len(wbs) == 1 {
		return wbs[0].data, nil
	}
	return mergeRollups(wbs)
}

// ---- view model: structured rollup -> premium board render ----

type vmSeg struct {
	Class, Label string
	Count        int
	Width        string // CSS % (pre-formatted)
}
type vmCard struct {
	ID, Title, Status, Assignee string
	Priority                    int
	PrioClass                   string
	Conflict, IsEpic            bool
	ChildTotal                  int
	Segs                        []vmSeg
}
type vmLane struct {
	Key, Title string
	Count      int
	Cards      []vmCard
}
type vmProject struct {
	Slug         string
	Epics, Loose int
	Lanes        []vmLane
	// Progress (burn-down) over top-level cards (epics + loose):
	Total, Done, DonePct, Conflicts int
	Bar                             []vmSeg // proportional segments by status column
}
type vmPage struct {
	Projects                       []vmProject
	AllSlugs                       []string // every project slug (for the switcher)
	Selected                       string   // "" = all projects
	Diagnostics                    []rollup.Diagnostic
	GeneratedAtAbs, GeneratedAtRel string
	Stale                          bool
	GoodAt                         string
	Refresh                        int
	Empty                          bool
	DiagCount                      int
	// Summary across the projects currently shown (adapts to the switcher filter):
	ProjectCount                                   int
	SumTotal, SumDone, SumInProgress, SumConflicts int
	SumDonePct                                     int
	SumBar                                         []vmSeg
	// "Recently active" digest (cards updated within digestWindow):
	Digest      []vmDigestGroup
	DigestCount int
	DigestSince string
}

var laneOrder = []struct {
	Key, Title string
	Col        rollup.Column
}{
	{"todo", "Todo", rollup.ColumnTodo},
	{"in_progress", "In Progress", rollup.ColumnInProgress},
	{"done", "Done", rollup.ColumnDone},
	{"deferred", "Deferred", rollup.ColumnDeferred},
	{"fallback", "Other", rollup.ColumnFallback},
}

func prioClass(p int) string {
	switch p {
	case 0:
		return "p0"
	case 1:
		return "p1"
	case 2:
		return "p2"
	case 3:
		return "p3"
	default:
		return "p4"
	}
}

func relTime(t time.Time) string { return relTimeAt(t, time.Now()) }

// relTimeAt renders t relative to now (injectable for testing).
func relTimeAt(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	switch d := now.Sub(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}

// columnKey maps a rollup column to its lane key (todo/in_progress/...).
func columnKey(c rollup.Column) string {
	for _, lo := range laneOrder {
		if lo.Col == c {
			return lo.Key
		}
	}
	return "fallback"
}

// pct returns done/total as an integer percent, rounded, 0 when total==0.
func pct(done, total int) int {
	if total <= 0 {
		return 0
	}
	return (done*100 + total/2) / total
}

// barSegs builds proportional status segments from per-column counts, in lane
// order, skipping empty columns. total must be the sum of the counts.
func barSegs(colCount map[string]int, total int) []vmSeg {
	if total <= 0 {
		return nil
	}
	var segs []vmSeg
	for _, lo := range laneOrder {
		n := colCount[lo.Key]
		if n == 0 {
			continue
		}
		segs = append(segs, vmSeg{
			Class: lo.Key, Label: lo.Title, Count: n,
			Width: fmt.Sprintf("%.3f", float64(n)*100/float64(total)),
		})
	}
	return segs
}

const (
	digestWindow      = 7 * 24 * time.Hour // "recently active" lookback
	digestPerGroupCap = 8                  // max items shown per status group
)

type vmDigestItem struct {
	Rel, Title, Slug, ID, Status, ColKey string
	updated                              time.Time // unexported: sort key only
}
type vmDigestGroup struct {
	Key, Title  string
	Count, More int // Count = full count; More = hidden beyond the cap
	Items       []vmDigestItem
}

// buildDigest collects cards (epics, their children, and loose) updated within
// `window` of `now` across the shown projects, grouped by status column in lane
// order, newest first, capped per group. Returns the groups + total updated.
func buildDigest(r *rollup.Rollup, selected string, now time.Time, window time.Duration) ([]vmDigestGroup, int) {
	cutoff := now.Add(-window)
	byCol := map[string][]vmDigestItem{}
	total := 0
	add := func(c rollup.Card, slug string) {
		if c.Updated.IsZero() || c.Updated.Before(cutoff) {
			return
		}
		k := columnKey(c.Column)
		byCol[k] = append(byCol[k], vmDigestItem{
			Rel: relTimeAt(c.Updated, now), Title: c.Title, Slug: slug,
			ID: c.ID, Status: c.Status, ColKey: k, updated: c.Updated,
		})
		total++
	}
	for _, proj := range r.Projects {
		if selected != "" && proj.Slug != selected {
			continue
		}
		for _, e := range proj.Epics {
			add(e.Issue, proj.Slug)
			for _, ch := range e.Children {
				add(ch, proj.Slug)
			}
		}
		for _, lc := range proj.Loose {
			add(lc, proj.Slug)
		}
	}
	var groups []vmDigestGroup
	for _, lo := range laneOrder {
		items := byCol[lo.Key]
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].updated.After(items[j].updated) })
		full := len(items)
		more := 0
		if full > digestPerGroupCap {
			items, more = items[:digestPerGroupCap], full-digestPerGroupCap
		}
		groups = append(groups, vmDigestGroup{Key: lo.Key, Title: lo.Title, Count: full, More: more, Items: items})
	}
	return groups, total
}

func childSegs(children []rollup.Card) (int, []vmSeg) {
	if len(children) == 0 {
		return 0, nil
	}
	counts := map[rollup.Column]int{}
	for _, c := range children {
		counts[c.Column]++
	}
	total := len(children)
	var segs []vmSeg
	for _, lo := range laneOrder {
		n := counts[lo.Col]
		if n == 0 {
			continue
		}
		segs = append(segs, vmSeg{
			Class: lo.Key, Label: lo.Title, Count: n,
			Width: fmt.Sprintf("%.3f", float64(n)*100/float64(total)),
		})
	}
	return total, segs
}

func buildPage(r *rollup.Rollup, stale bool, goodAt string, refresh int, selected string) vmPage {
	p := vmPage{
		Stale: stale, GoodAt: goodAt, Refresh: refresh, Selected: selected,
		Diagnostics: r.Diagnostics, DiagCount: len(r.Diagnostics),
	}
	if r.GeneratedAt.IsZero() {
		p.GeneratedAtAbs, p.GeneratedAtRel = "—", "—"
	} else {
		p.GeneratedAtAbs = r.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC")
		p.GeneratedAtRel = relTime(r.GeneratedAt)
	}
	mkCard := func(c rollup.Card, isEpic bool, children []rollup.Card) vmCard {
		vc := vmCard{
			ID: c.ID, Title: c.Title, Status: c.Status, Assignee: c.Assignee,
			Priority: c.Priority, PrioClass: prioClass(c.Priority), IsEpic: isEpic,
		}
		if isEpic {
			vc.ChildTotal, vc.Segs = childSegs(children)
		}
		return vc
	}
	for _, proj := range r.Projects {
		p.AllSlugs = append(p.AllSlugs, proj.Slug)
	}
	known := false
	for _, s := range p.AllSlugs {
		if s == selected {
			known = true
			break
		}
	}
	if !known {
		selected = "" // unknown/blank => show all
		p.Selected = ""
	}
	total := 0
	globalCol := map[string]int{} // per-column totals across shown projects
	for _, proj := range r.Projects {
		if selected != "" && proj.Slug != selected {
			continue // single-cache fetch; filter the parsed rollup (no extra Dolt)
		}
		vp := vmProject{Slug: proj.Slug, Epics: len(proj.Epics), Loose: len(proj.Loose)}
		byCol := map[rollup.Column][]vmCard{}
		for _, e := range proj.Epics {
			vc := mkCard(e.Issue, true, e.Children)
			vc.Conflict = e.Conflict
			byCol[e.Column] = append(byCol[e.Column], vc)
			total++
		}
		for _, lc := range proj.Loose {
			byCol[lc.Column] = append(byCol[lc.Column], mkCard(lc, false, nil))
			total++
		}
		colCount := map[string]int{}
		for _, lo := range laneOrder {
			cards := byCol[lo.Col]
			vp.Lanes = append(vp.Lanes, vmLane{Key: lo.Key, Title: lo.Title, Count: len(cards), Cards: cards})
			colCount[lo.Key] = len(cards)
			vp.Total += len(cards)
			for _, c := range cards {
				if c.Conflict {
					vp.Conflicts++
				}
			}
		}
		vp.Done = colCount["done"]
		vp.DonePct = pct(vp.Done, vp.Total)
		vp.Bar = barSegs(colCount, vp.Total)
		// Aggregate into the page summary (over shown projects).
		globalCol["todo"] += colCount["todo"]
		globalCol["in_progress"] += colCount["in_progress"]
		globalCol["done"] += colCount["done"]
		globalCol["deferred"] += colCount["deferred"]
		globalCol["fallback"] += colCount["fallback"]
		p.SumConflicts += vp.Conflicts
		p.Projects = append(p.Projects, vp)
	}
	p.ProjectCount = len(p.Projects)
	p.SumTotal = total
	p.SumDone = globalCol["done"]
	p.SumInProgress = globalCol["in_progress"]
	p.SumDonePct = pct(p.SumDone, p.SumTotal)
	p.SumBar = barSegs(globalCol, p.SumTotal)
	p.Digest, p.DigestCount = buildDigest(r, selected, time.Now(), digestWindow)
	p.DigestSince = "7 days"
	p.Empty = total == 0
	return p
}

// Ethereal-Glass dark board. Server-rendered, zero JS, GPU-safe motion
// (transform/opacity only), backdrop-blur confined to the sticky rail.
var boardPageTmpl = template.Must(template.New("board").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="{{.Refresh}}">
<title>Beads · Project Board</title>
<link rel="preconnect" href="https://cdn.jsdelivr.net" crossorigin>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@fontsource/geist-sans@5/index.css">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@fontsource/geist-mono@5/index.css">
<style>
:root{
  --bg:#050505; --ink:#f2f3f5; --ink-2:#9aa1ad; --ink-3:#5f6672;
  --hair:rgba(255,255,255,.07); --hair-2:rgba(255,255,255,.045);
  --ease:cubic-bezier(.32,.72,0,1);
  --todo:#8b93a7; --in_progress:#e3b341; --done:#3fb950; --deferred:#8b95e8; --fallback:#bd7ac0;
  --p0:#f85149; --p1:#fb8a3c; --p2:#e3b341; --p3:#58a6ff; --p4:#6e7681;
  --space:clamp(20px,4vw,56px);
}
*{box-sizing:border-box;margin:0;padding:0}
html{-webkit-text-size-adjust:100%}
body{
  background:var(--bg); color:var(--ink); min-height:100dvh;
  font-family:"Geist Sans","Geist",ui-sans-serif,system-ui,-apple-system,"SF Pro Display",sans-serif;
  font-feature-settings:"cv11","ss01"; -webkit-font-smoothing:antialiased;
  letter-spacing:-.011em; line-height:1.5; padding-bottom:80px;
}
/* ambient mesh orbs — fixed, non-interactive, GPU-cheap */
body::before,body::after{content:"";position:fixed;inset:0;pointer-events:none;z-index:0}
body::before{background:
  radial-gradient(560px 460px at 14% -6%,rgba(99,84,247,.16),transparent 60%),
  radial-gradient(620px 520px at 92% 4%,rgba(16,185,129,.10),transparent 62%)}
body::after{background:radial-gradient(900px 700px at 50% 120%,rgba(139,149,232,.07),transparent 60%)}
.wrap{position:relative;z-index:1;max-width:1480px;margin:0 auto;padding:0 var(--space)}

/* floating glass rail (detached — not glued edge-to-edge) */
.rail{position:sticky;top:0;z-index:30;padding-top:18px;margin-bottom:8px}
.rail-in{
  display:flex;align-items:center;justify-content:space-between;gap:20px;
  padding:13px 13px 13px 22px;border-radius:9999px;
  background:rgba(15,16,20,.62);border:1px solid var(--hair);
  backdrop-filter:blur(20px) saturate(140%);-webkit-backdrop-filter:blur(20px) saturate(140%);
  box-shadow:0 1px 0 rgba(255,255,255,.04) inset,0 18px 50px -22px rgba(0,0,0,.8);
}
.brand{display:flex;align-items:baseline;gap:10px;font-size:13px;letter-spacing:-.01em}
.brand b{font-weight:600}
.brand .sep{color:var(--ink-3)}
.brand .ey{font-size:10px;font-weight:600;letter-spacing:.22em;color:var(--ink-2);text-transform:uppercase}
.meta{display:flex;align-items:center;gap:14px;font-size:12px;color:var(--ink-2)}
.meta .abs{color:var(--ink-3)}
.switch{position:relative;display:flex;align-items:center}
.switch::after{content:"";position:absolute;right:14px;top:50%;width:6px;height:6px;
  border-right:1.5px solid var(--ink-2);border-bottom:1.5px solid var(--ink-2);
  transform:translateY(-65%) rotate(45deg);pointer-events:none}
.switch select{
  appearance:none;-webkit-appearance:none;font:inherit;font-size:12px;font-weight:560;
  color:var(--ink);background:rgba(255,255,255,.045);border:1px solid var(--hair);
  border-radius:9999px;padding:8px 34px 8px 15px;cursor:pointer;letter-spacing:-.005em;
  transition:border-color .45s var(--ease),background .45s var(--ease);max-width:240px;
  text-overflow:ellipsis}
.switch select:hover{border-color:rgba(255,255,255,.16);background:rgba(255,255,255,.07)}
.switch select:focus-visible{outline:2px solid rgba(139,149,232,.5);outline-offset:1px}
.switch select option{background:#0c0d11;color:var(--ink)}
.dot-live{width:6px;height:6px;border-radius:50%;background:var(--done);box-shadow:0 0 0 4px rgba(63,185,80,.15);display:inline-block}
.pill-stale{
  display:inline-flex;align-items:center;gap:7px;font-size:11px;font-weight:560;
  color:#ffb4ad;background:rgba(248,81,73,.13);border:1px solid rgba(248,81,73,.28);
  padding:6px 13px;border-radius:9999px;letter-spacing:.02em;
}

/* page head */
.head{padding:48px 0 14px}
.eyebrow{
  display:inline-block;font-size:10px;font-weight:600;letter-spacing:.24em;text-transform:uppercase;
  color:var(--ink-2);background:rgba(255,255,255,.045);border:1px solid var(--hair);
  padding:6px 13px;border-radius:9999px;
}
.head h1{margin-top:18px;font-size:clamp(28px,4.4vw,46px);font-weight:620;letter-spacing:-.03em;line-height:1.04}
.head p{margin-top:12px;color:var(--ink-2);font-size:14px;max-width:60ch}
.diag{
  margin-top:18px;display:inline-flex;flex-wrap:wrap;gap:8px 14px;align-items:center;
  font-size:12px;color:#e9c08a;background:rgba(227,179,65,.08);
  border:1px solid rgba(227,179,65,.22);padding:10px 16px;border-radius:14px;
}
.diag b{font-weight:600;letter-spacing:.16em;text-transform:uppercase;font-size:10px;color:#e3b341}

/* project section */
.proj{padding:40px 0 8px}
.proj-h{display:flex;align-items:flex-end;justify-content:space-between;gap:20px;flex-wrap:wrap;margin-bottom:22px}
.proj-h .t{display:flex;flex-direction:column;gap:11px}
.proj-h .slug{font-size:clamp(19px,2.3vw,25px);font-weight:600;letter-spacing:-.02em}
.proj-h .cnt{font-size:12px;color:var(--ink-3);font-variant-numeric:tabular-nums}
.proj-h .cnt b{color:var(--ink-2);font-weight:560}
.proj-h .cnt .cf{color:var(--p0);font-weight:560}

/* summary — at-a-glance burn-down across the shown projects */
.summary{margin:8px 0 34px}
.tiles{display:grid;grid-template-columns:repeat(5,minmax(0,1fr)) 2fr;gap:12px}
@media (max-width:760px){.tiles{grid-template-columns:repeat(2,1fr)}.tile.big{grid-column:1/-1}}
.tile{background:rgba(255,255,255,.022);border:1px solid var(--hair-2);border-radius:16px;padding:15px 17px;display:flex;flex-direction:column;gap:7px;box-shadow:inset 0 1px 1px rgba(255,255,255,.03)}
.tile .k{font-size:10px;text-transform:uppercase;letter-spacing:.16em;color:var(--ink-3)}
.tile b{font-size:24px;font-weight:600;letter-spacing:-.02em;font-variant-numeric:tabular-nums}
.tile b.wip{color:var(--in_progress)} .tile b.ok{color:var(--done)}
.tile.alert{border-color:rgba(248,81,73,.32)} .tile.alert b{color:var(--p0)}
.tile.big{justify-content:center;gap:11px}
.tile.big .bar{height:7px}

/* per-project burn-down row */
.proj-prog{display:flex;align-items:center;gap:13px;margin:-6px 0 20px;font-size:11px;color:var(--ink-3);font-variant-numeric:tabular-nums}
.proj-prog .pc{font-size:13px;font-weight:600;color:var(--ink);min-width:38px}
.proj-prog .bar{flex:1;max-width:520px}
.proj-prog .frac{white-space:nowrap}

/* digest — cross-project "what moved recently", grouped by status */
.digest{margin:0 0 40px}
.dg-h{display:flex;align-items:baseline;gap:14px;margin-bottom:16px}
.dg-h .dg-sub{font-size:10px;text-transform:uppercase;letter-spacing:.16em;color:var(--ink-3)}
.dg-cols{display:grid;grid-template-columns:repeat(auto-fill,minmax(238px,1fr));gap:14px;align-items:start}
.dg-col{background:rgba(255,255,255,.018);border:1px solid var(--hair-2);border-radius:16px;padding:13px 15px;box-shadow:inset 0 1px 1px rgba(255,255,255,.025)}
.dg-cap{display:flex;align-items:center;gap:8px;font-size:10px;text-transform:uppercase;letter-spacing:.13em;color:var(--ink-2);margin-bottom:9px}
.dg-cap .nm{flex:1}
.dg-cap .ct{color:var(--ink-3);font-variant-numeric:tabular-nums}
.dg-cap .acc{width:7px;height:7px;border-radius:50%;flex:0 0 auto}
.dg-col[data-k=todo] .acc{background:var(--todo)} .dg-col[data-k=in_progress] .acc{background:var(--in_progress)}
.dg-col[data-k=done] .acc{background:var(--done)} .dg-col[data-k=deferred] .acc{background:var(--deferred)} .dg-col[data-k=fallback] .acc{background:var(--fallback)}
.dg-item{display:flex;flex-direction:column;gap:3px;padding:8px 0;border-top:1px solid var(--hair-2)}
.dg-item:first-of-type{border-top:0;padding-top:0}
.dg-item .when{font-size:10px;color:var(--ink-3);font-variant-numeric:tabular-nums}
.dg-item .ti{font-size:12.5px;color:var(--ink);line-height:1.32;letter-spacing:-.01em}
.dg-item .mt{font-size:10px;color:var(--ink-3);font-family:"Geist Mono","Geist Mono Fallback",ui-monospace,monospace}
.dg-more{font-size:10px;color:var(--ink-3);padding-top:9px}

/* lanes — domain dictates columns; depth comes from texture/cards/motion */
.lanes{display:flex;gap:16px;overflow-x:auto;padding:6px 2px 22px;scroll-snap-type:x proximity}
.lanes::-webkit-scrollbar{height:8px}
.lanes::-webkit-scrollbar-thumb{background:rgba(255,255,255,.07);border-radius:9999px}
.lane{flex:0 0 312px;min-width:312px;scroll-snap-align:start;display:flex;flex-direction:column;gap:11px}
.lane-h{display:flex;align-items:center;gap:9px;padding:2px 6px 6px}
.lane-h .acc{width:7px;height:7px;border-radius:50%}
.lane-h .nm{font-size:11px;font-weight:600;letter-spacing:.15em;text-transform:uppercase;color:var(--ink-2)}
.lane-h .ct{margin-left:auto;font-size:11px;color:var(--ink-3);font-variant-numeric:tabular-nums;
  background:rgba(255,255,255,.04);border:1px solid var(--hair-2);padding:2px 9px;border-radius:9999px}
.lane[data-k=todo] .acc{background:var(--todo)} .lane[data-k=in_progress] .acc{background:var(--in_progress)}
.lane[data-k=done] .acc{background:var(--done)} .lane[data-k=deferred] .acc{background:var(--deferred)}
.lane[data-k=fallback] .acc{background:var(--fallback)}
.empty{padding:26px 0;text-align:center;color:var(--ink-3);font-size:12px;
  border:1px dashed var(--hair-2);border-radius:16px}

/* double-bezel card: outer shell (tray) + inner core (plate) */
.card{
  background:rgba(255,255,255,.022);border:1px solid var(--hair-2);
  border-radius:20px;padding:5px;
  transition:transform .55s var(--ease),border-color .55s var(--ease),background .55s var(--ease);
  animation:rise .62s var(--ease) both;
}
.card:hover{transform:translateY(-2px);border-color:var(--hair);background:rgba(255,255,255,.04)}
.core{
  background:linear-gradient(180deg,rgba(255,255,255,.035),rgba(255,255,255,0) 42%),#0c0d11;
  border-radius:15px;padding:15px 15px 14px;
  box-shadow:inset 0 1px 0 rgba(255,255,255,.055),0 14px 30px -22px rgba(0,0,0,.9);
}
.c-top{display:flex;align-items:flex-start;gap:10px}
.prio{flex:none;width:8px;height:8px;border-radius:50%;margin-top:5px}
.prio.p0{background:var(--p0);box-shadow:0 0 0 3px rgba(248,81,73,.16)}
.prio.p1{background:var(--p1)} .prio.p2{background:var(--p2)}
.prio.p3{background:var(--p3)} .prio.p4{background:var(--p4)}
.c-title{font-size:13.5px;font-weight:560;line-height:1.4;letter-spacing:-.012em;color:var(--ink);
  display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden}
.c-meta{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-top:11px}
.id{font-family:"Geist Mono",ui-monospace,SFMono-Regular,monospace;font-size:10.5px;
  color:var(--ink-3);letter-spacing:.01em}
.st{font-size:9.5px;font-weight:600;letter-spacing:.13em;text-transform:uppercase;color:var(--ink-2);
  background:rgba(255,255,255,.05);border:1px solid var(--hair-2);padding:3px 9px;border-radius:9999px}
.conf{font-size:9.5px;font-weight:600;letter-spacing:.06em;color:#ff9c94;
  background:rgba(248,81,73,.12);border:1px solid rgba(248,81,73,.26);padding:3px 9px;border-radius:9999px}
.epi{font-size:9px;font-weight:600;letter-spacing:.14em;text-transform:uppercase;color:var(--ink-3);
  border:1px solid var(--hair-2);padding:3px 8px;border-radius:9999px}
/* epic child-progress micro-bar */
.prog{margin-top:13px}
.bar{height:5px;border-radius:9999px;background:rgba(255,255,255,.05);overflow:hidden;display:flex}
.bar i{display:block;height:100%}
.bar i.todo{background:var(--todo)} .bar i.in_progress{background:var(--in_progress)}
.bar i.done{background:var(--done)} .bar i.deferred{background:var(--deferred)} .bar i.fallback{background:var(--fallback)}
.legend{margin-top:9px;display:flex;flex-wrap:wrap;gap:5px 13px;font-size:10.5px;color:var(--ink-3);
  font-variant-numeric:tabular-nums}
.legend span{display:inline-flex;align-items:center;gap:6px}
.legend i{width:6px;height:6px;border-radius:50%}
.legend i.todo{background:var(--todo)} .legend i.in_progress{background:var(--in_progress)}
.legend i.done{background:var(--done)} .legend i.deferred{background:var(--deferred)} .legend i.fallback{background:var(--fallback)}

.foot{position:relative;z-index:1;max-width:1480px;margin:38px auto 0;padding:22px var(--space) 0;
  border-top:1px solid var(--hair-2);color:var(--ink-3);font-size:11px;letter-spacing:.02em;
  display:flex;gap:8px 18px;flex-wrap:wrap}

@keyframes rise{from{opacity:0;transform:translateY(14px)}to{opacity:1;transform:none}}
.card:nth-child(2){animation-delay:.04s}.card:nth-child(3){animation-delay:.08s}
.card:nth-child(4){animation-delay:.12s}.card:nth-child(5){animation-delay:.16s}
.card:nth-child(n+6){animation-delay:.2s}
@media (max-width:760px){
  .lane{flex-basis:84vw;min-width:84vw}
  .rail-in{flex-direction:column;align-items:flex-start;gap:12px;border-radius:22px;padding:16px 18px}
  .head{padding:34px 0 8px}
}
@media (prefers-reduced-motion:reduce){
  .card{animation:none}.card:hover{transform:none}*{transition:none!important}
}
</style></head>
<body>
<div class="rail"><div class="wrap"><div class="rail-in">
  <div class="brand"><span class="ey">Beads</span><span class="sep">/</span><b>Project Board</b></div>
  {{if .AllSlugs}}<form class="switch" method="get" action="/">
    <select name="project" aria-label="Filter by project" onchange="this.form.submit()">
      <option value=""{{if eq .Selected ""}} selected{{end}}>All projects</option>
      {{range .AllSlugs}}<option value="{{.}}"{{if eq . $.Selected}} selected{{end}}>{{.}}</option>{{end}}
    </select>
  </form>{{end}}
  <div class="meta">
    {{if .Stale}}<span class="pill-stale">● Stale — last good {{.GoodAt}}</span>
    {{else}}<span><span class="dot-live"></span> &nbsp;live</span>{{end}}
    <span>{{.GeneratedAtRel}}</span><span class="abs">{{.GeneratedAtAbs}}</span>
  </div>
</div></div></div>

<div class="wrap">
  <div class="head">
    <span class="eyebrow">Read-only rollup</span>
    <h1>What's moving across every project.</h1>
    <p>Issues grouped by their project label, nested under epics, bucketed by status category. Epic cards show their rolled-up child progress; conflicts are flagged first.</p>
    {{if .DiagCount}}<div class="diag"><b>Diagnostics</b>
      {{range .Diagnostics}}<span>{{.Kind}}{{if .IssueID}} · {{.IssueID}}{{end}}{{if .Detail}} — {{.Detail}}{{end}}</span>{{end}}
    </div>{{end}}
  </div>

  {{if .Empty}}
    <div class="proj"><div class="empty" style="padding:60px 0">No issues yet. The board will populate as work is tracked.</div></div>
  {{else}}
    <section class="summary">
      <div class="tiles">
        <div class="tile"><span class="k">Projects</span><b>{{.ProjectCount}}</b></div>
        <div class="tile"><span class="k">Tracked</span><b>{{.SumTotal}}</b></div>
        <div class="tile"><span class="k">In progress</span><b class="wip">{{.SumInProgress}}</b></div>
        <div class="tile"><span class="k">Done</span><b class="ok">{{.SumDone}}</b></div>
        <div class="tile{{if .SumConflicts}} alert{{end}}"><span class="k">Conflicts</span><b>{{.SumConflicts}}</b></div>
        <div class="tile big"><span class="k">Overall · {{.SumDonePct}}% done</span>
          <div class="bar">{{range .SumBar}}<i class="{{.Class}}" style="width:{{.Width}}%"></i>{{end}}</div>
        </div>
      </div>
    </section>
    {{if .DigestCount}}
    <section class="digest">
      <div class="dg-h"><span class="eyebrow">Recently active</span><span class="dg-sub">last {{.DigestSince}} · {{.DigestCount}} updated</span></div>
      <div class="dg-cols">
        {{range .Digest}}
        <div class="dg-col" data-k="{{.Key}}">
          <div class="dg-cap"><span class="acc"></span><span class="nm">{{.Title}}</span><span class="ct">{{.Count}}</span></div>
          {{range .Items}}
          <div class="dg-item"><span class="when">{{.Rel}}</span><span class="ti">{{.Title}}</span><span class="mt">{{.Slug}} · {{.ID}}</span></div>
          {{end}}
          {{if .More}}<div class="dg-more">+{{.More}} more</div>{{end}}
        </div>
        {{end}}
      </div>
    </section>
    {{end}}
    {{range .Projects}}
    <section class="proj">
      <div class="proj-h">
        <div class="t"><span class="eyebrow">Project</span><span class="slug">{{.Slug}}</span></div>
        <div class="cnt"><b>{{.Epics}}</b> epics · <b>{{.Loose}}</b> without an epic{{if .Conflicts}} · <span class="cf">{{.Conflicts}} conflict{{if gt .Conflicts 1}}s{{end}}</span>{{end}}</div>
      </div>
      {{if .Total}}<div class="proj-prog"><span class="pc">{{.DonePct}}%</span>
        <div class="bar">{{range .Bar}}<i class="{{.Class}}" style="width:{{.Width}}%"></i>{{end}}</div>
        <span class="frac">{{.Done}}/{{.Total}} done</span>
      </div>{{end}}
      <div class="lanes">
        {{range .Lanes}}
        <div class="lane" data-k="{{.Key}}">
          <div class="lane-h"><span class="acc"></span><span class="nm">{{.Title}}</span><span class="ct">{{.Count}}</span></div>
          {{if .Cards}}{{range .Cards}}
          <article class="card"><div class="core">
            <div class="c-top">
              <span class="prio {{.PrioClass}}" title="P{{.Priority}}"></span>
              <div class="c-title">{{.Title}}</div>
            </div>
            <div class="c-meta">
              <span class="id">{{.ID}}</span>
              <span class="st">{{.Status}}</span>
              {{if .IsEpic}}<span class="epi">Epic{{if .ChildTotal}} · {{.ChildTotal}}{{end}}</span>{{end}}
              {{if .Conflict}}<span class="conf">⚠ closed · open children</span>{{end}}
            </div>
            {{if .Segs}}<div class="prog">
              <div class="bar">{{range .Segs}}<i class="{{.Class}}" style="width:{{.Width}}%"></i>{{end}}</div>
              <div class="legend">{{range .Segs}}<span><i class="{{.Class}}"></i>{{.Count}} {{.Label}}</span>{{end}}</div>
            </div>{{end}}
          </div></article>
          {{end}}{{else}}<div class="empty">—</div>{{end}}
        </div>
        {{end}}
      </div>
    </section>
    {{end}}
  {{end}}
</div>
<div class="foot">
  <span>tailnet-only · read-only</span><span>auto-refresh {{.Refresh}}s</span>
  <span>generated {{.GeneratedAtAbs}}</span><span>beads project board</span>
</div>
</body></html>`))

func serveBoard(addr string, refreshSec int, ttl, timeout time.Duration, explicit, globs []string) error {
	cache := newBoardCache(ttl, func(ctx context.Context) ([]byte, error) {
		// Resolve per fetch so workspaces created after startup are picked up
		// live (no restart). Cheap: a few globs + stats.
		return fetchWorkspaces(ctx, resolveWorkspaces(explicit, globs), timeout)
	})
	sema := make(chan struct{}, 8) // bounded concurrency (spec C4)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		select {
		case sema <- struct{}{}:
			defer func() { <-sema }()
		default:
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		body, stale, err := cache.get(r.Context())
		if err != nil {
			http.Error(w, "board unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		var rl rollup.Rollup
		if jerr := json.Unmarshal(body, &rl); jerr != nil {
			http.Error(w, "board payload parse error: "+jerr.Error(), http.StatusBadGateway)
			return
		}
		page := buildPage(&rl, stale, cache.goodTimestamp().UTC().Format(time.RFC3339), refreshSec, r.URL.Query().Get("project"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if terr := boardPageTmpl.Execute(w, page); terr != nil {
			// headers/body may be partially written; nothing safe left to do
			// but record it server-side rather than swallow silently.
			fmt.Fprintf(os.Stderr, "serve-board: template render failed: %v\n", terr)
		}
	})
	srv := &http.Server{
		Addr: addr, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	return srv.ListenAndServe()
}

var serveBoardCmd = &cobra.Command{
	Use:   "serve-board",
	Short: "Serve the read-only project board over HTTP (tailnet-only)",
	Long: `Serves a read-only HTML board. Holds NO database credentials: it
execs 'bd board --json' behind a singleflight+TTL cache. Bind to a tailnet
IP only; never a public interface.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		refresh, _ := cmd.Flags().GetInt("refresh")
		ttlSec, _ := cmd.Flags().GetInt("cache-ttl")
		timeoutSec, _ := cmd.Flags().GetInt("exec-timeout")
		workspaces, _ := cmd.Flags().GetStringArray("workspace")
		globs, _ := cmd.Flags().GetStringArray("workspace-glob")
		if addr == "" {
			return fmt.Errorf("--addr is required (tailnet IP:port, e.g. 100.x.y.z:8099)")
		}
		fmt.Printf("serving board on http://%s (refresh=%ds ttl=%ds workspaces=%d globs=%d)\n",
			addr, refresh, ttlSec, len(workspaces), len(globs))
		return serveBoard(addr, refresh,
			time.Duration(ttlSec)*time.Second, time.Duration(timeoutSec)*time.Second, workspaces, globs)
	},
}

func init() {
	serveBoardCmd.Flags().String("addr", "", "Tailnet bind address, e.g. 100.x.y.z:8099 (required)")
	serveBoardCmd.Flags().Int("refresh", 30, "Browser auto-refresh seconds (spec: >=15)")
	serveBoardCmd.Flags().Int("cache-ttl", 20, "Server cache TTL seconds (<= refresh)")
	serveBoardCmd.Flags().Int("exec-timeout", 10, "Hard timeout for 'bd board --json' seconds")
	serveBoardCmd.Flags().StringArray("workspace", nil, "Workspace directory to include; repeatable (default: process CWD)")
	serveBoardCmd.Flags().StringArray("workspace-glob", nil, "Glob for workspace dirs, expanded live on each fetch so new projects appear without a restart; repeatable (e.g. /home/admin/beads-*-workspace)")
	rootCmd.AddCommand(serveBoardCmd)
}
