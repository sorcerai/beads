package main

// Board signals: triage ("needs you now"), per-workspace health, velocity
// sparklines, and ntfy push notifications for bd serve-board. Everything here
// is pure over the merged rollup payload — computed on refresh from data the
// board already fetches; no daemon, no new storage.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/rollup"
)

const (
	// triageLookback bounds "recently closed awaiting eyes" and "newly ready
	// (dependency recently closed)". A day keeps the panel actionable.
	triageLookback = 24 * time.Hour
	// idleAfter: no card activity for this long => workspace health "idle".
	idleAfter = 3 * 24 * time.Hour
	// triagePerGroupCap mirrors digestPerGroupCap for the triage panel.
	triagePerGroupCap = 8
	// velocityDays is the closed-per-day sparkline window.
	velocityDays = 14
	// depthSamples caps the in-memory ready-queue-depth history.
	depthSamples = 96
	// memoriesPerWorkspaceCap bounds the memories panel per workspace.
	memoriesPerWorkspaceCap = 30
)

// ---- payload: merged rollup + per-workspace status ----

type wsMemory struct {
	Key  string `json:"key"`
	Line string `json:"line"` // first line of the memory value
}

// wsStatus is one workspace's fetch outcome + health signals. A workspace
// that fails to load MUST surface here with Err set — never be dropped.
type wsStatus struct {
	Name         string     `json:"name"`
	Dir          string     `json:"dir"`
	Err          string     `json:"error,omitempty"`
	Blocked      int        `json:"blocked"`
	Stale        int        `json:"stale"`
	RecentCloses int        `json:"recent_closes"`
	LastActivity time.Time  `json:"last_activity,omitzero"`
	BlockedGrew  bool       `json:"blocked_grew,omitempty"` // vs previous fetch (in-memory)
	Memories     []wsMemory `json:"memories,omitempty"`
}

// boardPayload is the cached channel between fetch and render: the merged
// rollup plus explicit per-workspace statuses (including failures).
type boardPayload struct {
	rollup.Rollup
	Workspaces []wsStatus `json:"workspaces,omitempty"`
}

// wsResult is one workspace's raw fetch outcome.
type wsResult struct {
	dir      string
	data     []byte
	err      error
	memories []wsMemory
}

// workspaceLabel names a workspace for display: the derived project slug when
// the dir follows the beads-<project>-workspace convention, else the basename.
func workspaceLabel(dir string) string {
	if n := workspaceName(dir); n != "" {
		return n
	}
	if dir == "" {
		return "(cwd)"
	}
	return filepath.Base(dir)
}

// buildPayload turns raw per-workspace fetch results into the merged board
// payload. Fetch errors and malformed JSON become explicit error entries.
func buildPayload(results []wsResult, now time.Time, staleAfter time.Duration) *boardPayload {
	var parsed []workspaceRollup
	wss := make([]wsStatus, 0, len(results))
	for _, res := range results {
		st := wsStatus{Name: workspaceLabel(res.dir), Dir: res.dir, Memories: res.memories}
		if res.err != nil {
			st.Err = res.err.Error()
			wss = append(wss, st)
			continue
		}
		var r rollup.Rollup
		if err := json.Unmarshal(res.data, &r); err != nil {
			st.Err = "malformed board json: " + err.Error()
			wss = append(wss, st)
			continue
		}
		fillSignals(&st, &r, now, staleAfter)
		wss = append(wss, st)
		parsed = append(parsed, workspaceRollup{dir: res.dir, r: r})
	}
	return &boardPayload{Rollup: mergeRollups(parsed), Workspaces: wss}
}

// forEachCard visits every card (epic issues, their children, loose) in the
// projects matching selected ("" = all), passing the project slug.
func forEachCard(r *rollup.Rollup, selected string, fn func(c rollup.Card, slug string)) {
	for _, proj := range r.Projects {
		if selected != "" && proj.Slug != selected {
			continue
		}
		for _, e := range proj.Epics {
			fn(e.Issue, proj.Slug)
			for _, ch := range e.Children {
				fn(ch, proj.Slug)
			}
		}
		for _, lc := range proj.Loose {
			fn(lc, proj.Slug)
		}
	}
}

// ---- stale / triage ----

// isStale: claimed/in_progress card whose last update is older than the
// threshold. Cards with no timestamp cannot be judged and are not flagged.
func isStale(c rollup.Card, now time.Time, staleAfter time.Duration) bool {
	return c.Column == rollup.ColumnInProgress && !c.Updated.IsZero() &&
		now.Sub(c.Updated) > staleAfter
}

// closeTime is the card's close timestamp. Triage/velocity/ntfy signals use
// this and must NOT fall back to Updated: a late edit of a legacy closed row
// (no closed_at) would otherwise masquerade as a fresh close and flood
// "recently closed", velocity, and ntfy review-ready. Legacy rows without
// closed_at simply don't contribute to those signals.
func closeTime(c rollup.Card) time.Time {
	return c.ClosedAt
}

// recentlyClosed: done within the lookback window => awaiting human eyes.
func recentlyClosed(c rollup.Card, now time.Time, lookback time.Duration) bool {
	t := closeTime(c)
	return c.Column == rollup.ColumnDone && !t.IsZero() && now.Sub(t) <= lookback
}

// newlyReady: open todo, unblocked, and a blocking dependency closed recently.
// Cards with a dependency outside the fetch window (HasUnknownDeps) are excluded:
// their unblocked status is unproven, so announcing them "ready" could be wrong.
func newlyReady(c rollup.Card, now time.Time, lookback time.Duration) bool {
	return c.Column == rollup.ColumnTodo && !c.Blocked && !c.HasUnknownDeps &&
		!c.LastDepClosed.IsZero() && now.Sub(c.LastDepClosed) <= lookback
}

type vmTriageItem struct {
	ID, Title, Slug, Rel string
	at                   time.Time // sort key only
}

type vmTriageGroup struct {
	Key, Title  string
	Count, More int
	Items       []vmTriageItem
}

type vmTriage struct {
	Groups []vmTriageGroup
	Count  int
}

// buildTriage aggregates the "needs you now" panel over the projects matching
// selected ("" = all): blocked, recently closed (review), stale in_progress,
// and newly ready. selected mirrors the summary/velocity tiles so every tile on
// the page reflects the same project scope.
func buildTriage(r *rollup.Rollup, selected string, now time.Time, staleAfter, lookback time.Duration) vmTriage {
	var blocked, review, stale, ready []vmTriageItem
	forEachCard(r, selected, func(c rollup.Card, slug string) {
		item := func(at time.Time) vmTriageItem {
			return vmTriageItem{ID: c.ID, Title: c.Title, Slug: slug, Rel: relTimeAt(at, now), at: at}
		}
		switch {
		case isStale(c, now, staleAfter):
			stale = append(stale, item(c.Updated))
		case c.Blocked && c.Column != rollup.ColumnDone && c.Column != rollup.ColumnDeferred:
			blocked = append(blocked, item(c.Updated))
		case recentlyClosed(c, now, lookback):
			review = append(review, item(closeTime(c)))
		case newlyReady(c, now, lookback):
			ready = append(ready, item(c.LastDepClosed))
		}
	})
	mk := func(key, title string, items []vmTriageItem, oldestFirst bool) *vmTriageGroup {
		if len(items) == 0 {
			return nil
		}
		sort.SliceStable(items, func(i, j int) bool {
			if oldestFirst {
				return items[i].at.Before(items[j].at)
			}
			return items[i].at.After(items[j].at)
		})
		full := len(items)
		more := 0
		if full > triagePerGroupCap {
			items, more = items[:triagePerGroupCap], full-triagePerGroupCap
		}
		return &vmTriageGroup{Key: key, Title: title, Count: full, More: more, Items: items}
	}
	var t vmTriage
	for _, g := range []*vmTriageGroup{
		mk("stale", "Stale in progress", stale, true), // most-stale first
		mk("blocked", "Blocked", blocked, false),
		mk("review", "Recently closed — review", review, false),
		mk("ready", "Newly ready", ready, false),
	} {
		if g != nil {
			t.Groups = append(t.Groups, *g)
			t.Count += g.Count
		}
	}
	return t
}

// ---- per-workspace health ----

// fillSignals computes health inputs for one workspace's rollup.
func fillSignals(st *wsStatus, r *rollup.Rollup, now time.Time, staleAfter time.Duration) {
	forEachCard(r, "", func(c rollup.Card, _ string) {
		if c.Blocked && c.Column != rollup.ColumnDone && c.Column != rollup.ColumnDeferred {
			st.Blocked++
		}
		if isStale(c, now, staleAfter) {
			st.Stale++
		}
		if recentlyClosed(c, now, triageLookback) {
			st.RecentCloses++
		}
		if c.Updated.After(st.LastActivity) {
			st.LastActivity = c.Updated
		}
	})
}

// classifyHealth maps a workspace status to a health-dot class:
// err > risk (stale work or growing blocked count) > idle > ok.
func classifyHealth(st wsStatus, now time.Time) string {
	switch {
	case st.Err != "":
		return "err"
	case st.Stale > 0 || st.BlockedGrew:
		return "risk"
	case st.LastActivity.IsZero() || now.Sub(st.LastActivity) > idleAfter:
		return "idle"
	default:
		return "ok"
	}
}

// ---- velocity: closed/day + ready-queue depth sparklines ----

// closedPerDay buckets done cards by rolling 24h windows ending at now;
// index days-1 is today. Uses closed_at only (see closeTime); rows without a
// close timestamp don't contribute. selected ("" = all) scopes the window to
// one project so the tile matches the rest of the filtered page.
func closedPerDay(r *rollup.Rollup, selected string, now time.Time, days int) []int {
	out := make([]int, days)
	forEachCard(r, selected, func(c rollup.Card, _ string) {
		if c.Column != rollup.ColumnDone {
			return
		}
		t := closeTime(c)
		if t.IsZero() || t.After(now) {
			return
		}
		idx := days - 1 - int(now.Sub(t).Hours()/24)
		if idx >= 0 && idx < days {
			out[idx]++
		}
	})
	return out
}

// readyDepth counts open, unblocked todo cards in the projects matching
// selected ("" = all).
func readyDepth(r *rollup.Rollup, selected string) int {
	n := 0
	forEachCard(r, selected, func(c rollup.Card, _ string) {
		if c.Column == rollup.ColumnTodo && !c.Blocked {
			n++
		}
	})
	return n
}

// depthSampler keeps an in-memory ring of ready-queue-depth samples, one per
// board fetch. ponytail: resets on restart; persist to disk if history across
// restarts ever matters.
type depthSampler struct {
	mu   sync.Mutex
	vals []int
}

func (s *depthSampler) add(v int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vals = append(s.vals, v)
	if len(s.vals) > depthSamples {
		s.vals = s.vals[len(s.vals)-depthSamples:]
	}
}

func (s *depthSampler) snapshot() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, len(s.vals))
	copy(out, s.vals)
	return out
}

// sparklineSVG renders vals as an inline SVG polyline. Safe as template.HTML:
// built purely from integers and fixed markup.
func sparklineSVG(vals []int, w, h int) template.HTML {
	if len(vals) == 0 {
		vals = []int{0}
	}
	maxV := 1
	for _, v := range vals {
		if v > maxV {
			maxV = v
		}
	}
	pad := 2.0
	var pts []string
	step := 0.0
	if len(vals) > 1 {
		step = (float64(w) - 2*pad) / float64(len(vals)-1)
	}
	for i, v := range vals {
		x := pad + step*float64(i)
		if len(vals) == 1 {
			x = float64(w) / 2
		}
		y := float64(h) - pad - (float64(h)-2*pad)*float64(v)/float64(maxV)
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
	}
	// #nosec G203 -- all interpolated values are numeric (ints w/h, formatted floats); no user-controlled strings.
	return template.HTML(fmt.Sprintf(
		`<svg class="spark" viewBox="0 0 %d %d" width="%d" height="%d" preserveAspectRatio="none" aria-hidden="true"><polyline points="%s" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/></svg>`,
		w, h, w, h, strings.Join(pts, " ")))
}

func sum(vals []int) int {
	n := 0
	for _, v := range vals {
		n += v
	}
	return n
}

// ---- ntfy notifications ----

// ntfyNotifier observes each fresh payload, marks BlockedGrew on workspace
// statuses, and (when a topic URL is configured) POSTs one-line messages.
// Debounce: an issue+event pair fires at most once per process lifetime.
// The first observation only seeds state — a systemd restart must not
// re-announce the whole backlog.
type ntfyNotifier struct {
	url    string // "" = notifications disabled (observe still runs for health)
	client *http.Client

	mu          sync.Mutex
	primed      bool
	seen        map[string]bool
	prevBlocked map[string]int // workspace dir -> blocked count at last fetch

	// notifySlot bounds outstanding notify goroutines to one: notifyAsync does a
	// send-and-drop so a slow ntfy host can't pile up sends behind the fetch path.
	notifySlot chan struct{}
}

// seenCap bounds the debounce set. ponytail: at the cap we clear and cold-start
// (this cycle re-seeds without announcing), trading a rare missed notification
// for bounded memory. Per-key TTL eviction if that trade ever bites.
const seenCap = 50000

func newNtfyNotifier(url string) *ntfyNotifier {
	return &ntfyNotifier{
		url:         url,
		client:      &http.Client{Timeout: 10 * time.Second},
		seen:        map[string]bool{},
		prevBlocked: map[string]int{},
		notifySlot:  make(chan struct{}, 1),
	}
}

// observe diffs the payload against previous state and returns the messages
// to publish. It also sets BlockedGrew on p.Workspaces for health dots.
func (n *ntfyNotifier) observe(p *boardPayload, now time.Time, staleAfter time.Duration) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.seen) > seenCap {
		n.seen = map[string]bool{}
		n.primed = false // cold-start this cycle: re-seed without re-announcing
	}
	var msgs []string
	fire := func(key, msg string) {
		if n.seen[key] {
			return
		}
		n.seen[key] = true
		if n.primed {
			msgs = append(msgs, msg)
		}
	}
	forEachCard(&p.Rollup, "", func(c rollup.Card, slug string) {
		if recentlyClosed(c, now, triageLookback) {
			fire("review|"+c.ID, fmt.Sprintf("review-ready: %s %s [%s]", c.ID, c.Title, slug))
		}
		if isStale(c, now, staleAfter) {
			fire("stale|"+c.ID, fmt.Sprintf("stale in_progress: %s %s [%s] (no update in %s)", c.ID, c.Title, slug, relTimeAt(c.Updated, now)))
		}
	})
	for i := range p.Workspaces {
		ws := &p.Workspaces[i]
		if ws.Err != "" {
			continue // unknown count; keep previous baseline
		}
		// Key by Dir (unique): workspace display Names can collide, and a shared
		// key would flap BlockedGrew / notifications between two workspaces.
		if prev, ok := n.prevBlocked[ws.Dir]; ok && ws.Blocked > prev {
			ws.BlockedGrew = true
			if n.primed {
				msgs = append(msgs, fmt.Sprintf("blocked count rose in %s: %d → %d", ws.Name, prev, ws.Blocked))
			}
		}
		n.prevBlocked[ws.Dir] = ws.Blocked
	}
	n.primed = true
	return msgs
}

// notify POSTs each message to the ntfy topic URL. Errors are logged and
// swallowed — the board stays read-only and must never fail on notify.
func (n *ntfyNotifier) notify(msgs []string) {
	if n.url == "" {
		return
	}
	for _, m := range msgs {
		resp, err := n.client.Post(n.url, "text/plain", strings.NewReader(m))
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve-board: ntfy post failed: %v\n", err)
			continue
		}
		_ = resp.Body.Close()
	}
}

// notifyAsync fires notify off the fetch path. HTTP POSTs to ntfy can each take
// up to the client's 10s timeout; running K of them inline would stall every
// board refresh. observe() must still run synchronously (it snapshots state and
// sets BlockedGrew under the fetch) — only the sends move here. Overlapping
// batches are dropped via notifySlot: the channel is decorative, a missed push
// is acceptable.
func (n *ntfyNotifier) notifyAsync(msgs []string) {
	if n.url == "" || len(msgs) == 0 {
		return
	}
	select {
	case n.notifySlot <- struct{}{}:
	default:
		return // a prior batch is still sending; drop this one
	}
	go func() {
		defer func() { <-n.notifySlot }()
		n.notify(msgs)
	}()
}

// ---- memories ----

// parseMemoriesJSON decodes `bd memories --json` output: a map of
// key -> {key, value, superseded_by?}, optionally wrapped in the
// {"schema_version":N,"data":{...}} envelope, with a stray top-level
// "schema_version" key in the legacy shape. Returns key + first value line,
// sorted by key, capped.
func parseMemoriesJSON(data []byte) []wsMemory {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil
	}
	// bd --json wraps output with a top-level "schema_version", producing two
	// shapes for the memories map:
	//   legacy flat: {"schema_version":N, "<key>":{record}, ...}
	//   envelope:    {"schema_version":N, "data":{"<key>":{record}, ...}}
	// schema_version is injected in BOTH, so its presence can't distinguish them.
	// The envelope has EXACTLY {schema_version, data} at top level; any other
	// key count means the flat shape, where "data" may itself be a memory key.
	// Descend only when data is the sole non-schema_version key AND holds a map
	// of records — so a memory literally keyed "data" isn't mistaken for the
	// envelope (which previously dropped every other memory).
	if inner, ok := top["data"]; ok && len(top) == 2 {
		if _, hasVer := top["schema_version"]; hasVer {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(inner, &m); err == nil && !looksLikeMemoryRecord(m) {
				top = m
			}
		}
	}
	var out []wsMemory
	for k, raw := range top {
		if k == "schema_version" {
			continue
		}
		var rec struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil || rec.Value == "" {
			continue
		}
		line, _, _ := strings.Cut(rec.Value, "\n")
		out = append(out, wsMemory{Key: k, Line: line})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if len(out) > memoriesPerWorkspaceCap {
		out = out[:memoriesPerWorkspaceCap]
	}
	return out
}

// looksLikeMemoryRecord reports whether m is a single memory record
// ({"key":...,"value":"..."}) rather than the envelope's map of key->record.
// A record has a string "value"; in a memories map keyed "value" that entry
// would be an object, so the string check disambiguates the degenerate case of
// exactly one memory literally keyed "data".
func looksLikeMemoryRecord(m map[string]json.RawMessage) bool {
	v, ok := m["value"]
	return ok && len(v) > 0 && v[0] == '"'
}
