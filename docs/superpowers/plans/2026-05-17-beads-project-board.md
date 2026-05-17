# Beads Project Board Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A read-only Linear-style project board over the existing beads/Dolt deployment, exposed as one shared rollup core consumed by a CLI, a web dashboard, and an MCP tool, plus a coding-agent skill.

**Architecture:** A new `internal/rollup` package computes a project→epic→status-category rollup through the existing storage read interface (no raw SQL). `bd board --json` is the single canonical contract. `bd serve-board` (same binary) execs `bd board --json` behind a singleflight+TTL cache so it holds no DB credentials. The Python `integrations/beads-mcp` adds one tool that runs `bd board --json`. A skill documents the CLI channel for coding agents.

**Tech Stack:** Go 1.x (`github.com/steveyegge/beads`), cobra, `golang.org/x/sync/singleflight` (already in go.mod v0.20.0), `net/http`+`html/template` (stdlib), Dolt via existing `storage.DoltStorage`; Python (FastMCP) for the MCP tool; systemd for deployment.

**Spec:** `docs/superpowers/specs/2026-05-17-beads-project-board-design.md` (read it; this plan implements it including mandatory constraints C1–C7).

**Build/test commands (this repo):**
- Build: `make build` → `./bin/bd` (or `go build ./cmd/bd`).
- Full suite: `make test` (runs `./scripts/test.sh`). Per-package fast loop: `go test ./internal/rollup/... -v`.
- Go unit tests use `t.TempDir()`. `cmd/bd` tests use helpers in `cmd/bd/test_helpers_test.go` (`newTestStore(t, dbPath) *dolt.DoltStore`).
- MCP: `cd integrations/beads-mcp && uv run pytest`.

**Grounded facts (do not re-derive):**
- `internal/types.Issue`: `ID string`, `Title string`, `Status types.Status`, `Priority int`, `Assignee string`, `UpdatedAt time.Time`, `Labels []string`. **There is NO `Parent` field on `Issue`.** Parent/child is a parent-child *dependency edge*: `store.GetAllDependencyRecords(ctx) (map[string][]*types.Dependency, error)` keyed by child issue ID; a record with `Type == types.DepParentChild` has `DependsOnID` = the parent (first match wins; mirrors the canonical parent compute in `internal/types/types.go`). `types.Dependency{IssueID, DependsOnID, Type}`.
- `internal/types.BuiltInStatusCategory(Status) StatusCategory` → `CategoryActive` (open), `CategoryWIP` (in_progress/blocked/hooked), `CategoryDone` (closed), `CategoryFrozen` (deferred/pinned), `CategoryUnspecified` (default). Constants: `types.CategoryActive`, `types.CategoryWIP`, `types.CategoryDone`, `types.CategoryFrozen`, `types.CategoryUnspecified`.
- `storage.DoltStorage` interface includes `SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)`.
- `types.IssueFilter` fields used here: `Statuses []types.Status`, `Labels []string`, `LabelsAny []string`, `ParentID *string`, `Limit int`.
- `cmd/bd` command pattern (see `cmd/bd/epic.go`): package globals `store storage.DoltStorage`, `rootCtx context.Context`, `jsonOutput bool`; helpers `outputJSON(v interface{})` (`cmd/bd/output.go:32`), `FatalErrorRespectJSON(format string, args ...interface{})` (`cmd/bd/errors.go:109`); commands registered in `func init()` via `rootCmd.AddCommand(...)`.
- Plugin/skill home: `plugins/beads/skills/` (manifest `plugins/beads/.claude-plugin/plugin.json` → `"skills": "./skills/"`). Existing example: `plugins/beads/skills/beads/SKILL.md`.
- MCP: `integrations/beads-mcp/src/beads_mcp/bd_client.py` — abstract `BdClientBase`, concrete `BdCliClient` with `async def _run_command(self, *args: str, cwd: str | None = None) -> Any` (runs `bd <args> --json`-style, returns parsed JSON). `server.py` registers tools with `@mcp.tool(name=..., description=...)` + `@with_workspace`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/rollup/columns.go` | `Column` type + `ColumnForStatus()` mapping (wraps `BuiltInStatusCategory` + custom-status categories) |
| `internal/rollup/rollup.go` | Rollup data types, `IssueSource` narrow read interface, `Compute()` (grouping, epic computed column, diagnostics) |
| `internal/rollup/columns_test.go` | Table-driven mapping tests |
| `internal/rollup/rollup_test.go` | `Compute()` tests against an in-memory fake `IssueSource` |
| `cmd/bd/board.go` | `bd board` cobra command (`--json`, `--project`, `--limit`, `--cursor`) |
| `cmd/bd/board_test.go` | CLI golden + pagination tests |
| `cmd/bd/serve_board.go` | `bd serve-board` web server (exec + singleflight + TTL + last-good + html template) |
| `cmd/bd/serve_board_test.go` | Cache-collapse, error-fallback, deadline tests |
| `deploy/pm1-beads/bd-board.service` | systemd unit (tailnet bind, ExecStartPre health gate, cgroup limits) |
| `deploy/pm1-beads/RUNBOOK.md` | Append a "Project board" ops section |
| `integrations/beads-mcp/src/beads_mcp/bd_client.py` | Add `board()` to `BdClientBase` + `BdCliClient` |
| `integrations/beads-mcp/src/beads_mcp/server.py` | Add `@mcp.tool(name="board")` wrapper |
| `integrations/beads-mcp/tests/test_bd_client.py` | Add board client test |
| `plugins/beads/skills/project-board/SKILL.md` | Coding-agent skill documenting the CLI channel |

---

## Task 1: Rollup column mapping (`internal/rollup/columns.go`)

**Files:**
- Create: `internal/rollup/columns.go`
- Test: `internal/rollup/columns_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/rollup/columns_test.go`:

```go
package rollup

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestColumnForStatus_BuiltIns(t *testing.T) {
	cases := map[types.Status]Column{
		types.StatusOpen:       ColumnTodo,
		types.StatusInProgress: ColumnInProgress,
		types.StatusBlocked:    ColumnInProgress,
		types.StatusHooked:     ColumnInProgress,
		types.StatusClosed:     ColumnDone,
		types.StatusDeferred:   ColumnDeferred,
		types.StatusPinned:     ColumnDeferred,
	}
	for st, want := range cases {
		if got := ColumnForStatus(st, nil); got != want {
			t.Errorf("ColumnForStatus(%q) = %q, want %q", st, got, want)
		}
	}
}

func TestColumnForStatus_CustomCategory(t *testing.T) {
	custom := map[string]types.StatusCategory{
		"in_review": types.CategoryWIP,
		"icebox":    types.CategoryFrozen,
	}
	if got := ColumnForStatus(types.Status("in_review"), custom); got != ColumnInProgress {
		t.Errorf("custom wip status = %q, want %q", got, ColumnInProgress)
	}
	if got := ColumnForStatus(types.Status("icebox"), custom); got != ColumnDeferred {
		t.Errorf("custom frozen status = %q, want %q", got, ColumnDeferred)
	}
}

func TestColumnForStatus_UnknownFallsBack(t *testing.T) {
	if got := ColumnForStatus(types.Status("weird"), nil); got != ColumnFallback {
		t.Errorf("unknown status = %q, want %q", got, ColumnFallback)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rollup/ -run TestColumnForStatus -v`
Expected: FAIL — package/`Column`/`ColumnForStatus` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/rollup/columns.go`:

```go
// Package rollup computes a project→epic→status-category board view over
// the beads issue store. It depends only on a narrow read interface and the
// canonical status-category mapping; it contains no SQL (spec constraint C3).
package rollup

import "github.com/steveyegge/beads/internal/types"

// Column is a fixed board lane. The set is constant regardless of which
// custom statuses exist (spec: never derive columns dynamically).
type Column string

const (
	ColumnTodo       Column = "todo"        // category: active
	ColumnInProgress Column = "in_progress" // category: wip
	ColumnDone       Column = "done"        // category: done
	ColumnDeferred   Column = "deferred"    // category: frozen
	ColumnFallback   Column = "fallback"    // unspecified / unknown custom
)

// columnForCategory maps a StatusCategory to a fixed board column.
func columnForCategory(c types.StatusCategory) Column {
	switch c {
	case types.CategoryActive:
		return ColumnTodo
	case types.CategoryWIP:
		return ColumnInProgress
	case types.CategoryDone:
		return ColumnDone
	case types.CategoryFrozen:
		return ColumnDeferred
	default:
		return ColumnFallback
	}
}

// ColumnForStatus returns the board column for a status. Built-in statuses
// use types.BuiltInStatusCategory. Custom statuses use their self-declared
// category from customCategories (status name -> category); unknown -> fallback.
func ColumnForStatus(s types.Status, customCategories map[string]types.StatusCategory) Column {
	cat := types.BuiltInStatusCategory(s)
	if cat == types.CategoryUnspecified {
		if cc, ok := customCategories[string(s)]; ok {
			cat = cc
		}
	}
	return columnForCategory(cat)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/rollup/ -run TestColumnForStatus -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add internal/rollup/columns.go internal/rollup/columns_test.go
git commit -m "feat(rollup): canonical status->column mapping"
```

---

## Task 2: Rollup compute (`internal/rollup/rollup.go`)

> **CORRECTION (data model):** `types.Issue` has **no** `Parent` field. Parent/child
> is a **parent-child dependency edge** (Decision 004). Canonical derivation
> (mirrors `internal/types/types.go` ReadyItem/IssueDetails parent computation
> and `issueops.GetAllDependencyRecordsInTx`): `GetAllDependencyRecords` returns
> `map[childIssueID][]*types.Dependency`; for an issue, the **first** record with
> `Type == types.DepParentChild` gives `parentID = dep.DependsOnID` (first match
> wins, no sorting — match beads exactly). The rollup therefore depends on a
> narrow read interface of **two** bulk methods (`SearchIssues` +
> `GetAllDependencyRecords` = 2 queries total, no N+1 over epics — spec C2/C3
> intact). Epic rows = parentless issues (do **not** special-case `TypeEpic`;
> noted as a future styling enhancement only).

**Files:**
- Create: `internal/rollup/rollup.go`
- Test: `internal/rollup/rollup_test.go`

Implements: project grouping by `project:<slug>` label (first lexicographic wins + diagnostic), epic assembly via the parent-child dependency map, **computed** epic column (done only if own category done AND all children done; closed-with-non-done-child → in_progress + conflict), visited-set traversal for cycles/orphans, always-emit "Unassigned", `generated_at`.

- [ ] **Step 1: Write the failing test**

Create `internal/rollup/rollup_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rollup/ -run TestCompute -v`
Expected: FAIL — `Compute`, `Rollup`, `Project`, `Options`, `IssueSource` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/rollup/rollup.go`:

```go
package rollup

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

const projectLabelPrefix = "project:"

// IssueSource is the narrow read interface rollup depends on. Satisfied by
// storage.DoltStorage. Two bulk reads only — keeps rollup free of raw SQL /
// *sql.DB and avoids N+1 over epics (spec C2/C3).
type IssueSource interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
}

// Options bounds the rollup (spec C2: pagination/caps are mandatory).
type Options struct {
	Project          string                          // optional: scope to one slug
	Limit            int                             // 0 => DefaultLimit
	CustomCategories map[string]types.StatusCategory // status name -> category
}

const DefaultLimit = 2000

type Card struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Status   string    `json:"status"` // raw status name, always present
	Column   Column    `json:"column"`
	Priority int       `json:"priority"`
	Assignee string    `json:"assignee,omitempty"`
	Updated  time.Time `json:"updated_at"`
}

type Epic struct {
	Issue    Card   `json:"issue"`
	Column   Column `json:"column"`   // computed
	Conflict bool   `json:"conflict"` // closed epic w/ non-done child
	Children []Card `json:"children"`
}

type Project struct {
	Slug  string `json:"slug"`
	Epics []Epic `json:"epics"`
	Loose []Card `json:"loose"` // project-labeled, parentless non-epic edge cases
}

type Diagnostic struct {
	Kind    string `json:"kind"` // multi_project | invalid_graph
	IssueID string `json:"issue_id,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type Rollup struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Projects    []Project    `json:"projects"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

func toCard(i *types.Issue, custom map[string]types.StatusCategory) Card {
	return Card{
		ID: i.ID, Title: i.Title, Status: string(i.Status),
		Column:   ColumnForStatus(i.Status, custom),
		Priority: i.Priority, Assignee: i.Assignee, Updated: i.UpdatedAt,
	}
}

// projectSlug returns the winning slug (first lexicographic project: label)
// and whether the issue had more than one (=> multi_project diagnostic).
func projectSlug(i *types.Issue) (slug string, multi bool) {
	var slugs []string
	for _, l := range i.Labels {
		if strings.HasPrefix(l, projectLabelPrefix) {
			slugs = append(slugs, strings.TrimPrefix(l, projectLabelPrefix))
		}
	}
	if len(slugs) == 0 {
		return "Unassigned", false
	}
	sort.Strings(slugs)
	return slugs[0], len(slugs) > 1
}

// buildParentMap derives child -> parent from parent-child dependency edges.
// Mirrors beads' canonical parent computation: first parent-child record wins
// (no sorting). allDeps is keyed by child issue ID; the parent is DependsOnID.
func buildParentMap(allDeps map[string][]*types.Dependency) map[string]string {
	parentOf := make(map[string]string, len(allDeps))
	for childID, deps := range allDeps {
		for _, d := range deps {
			if d.Type == types.DepParentChild {
				parentOf[childID] = d.DependsOnID
				break
			}
		}
	}
	return parentOf
}

func Compute(ctx context.Context, src IssueSource, opts Options) (*Rollup, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	filter := types.IssueFilter{Limit: limit}
	if opts.Project != "" {
		filter.Labels = []string{projectLabelPrefix + opts.Project}
	}
	issues, err := src.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}
	allDeps, err := src.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}
	parentOf := buildParentMap(allDeps)

	r := &Rollup{GeneratedAt: time.Now().UTC()}

	type pgroup struct {
		epicsByID map[string]*Epic
		order     []string
		loose     []Card
	}
	groups := map[string]*pgroup{}
	ensure := func(slug string) *pgroup {
		g := groups[slug]
		if g == nil {
			g = &pgroup{epicsByID: map[string]*Epic{}}
			groups[slug] = g
		}
		return g
	}
	ensure("Unassigned") // always emitted (spec)
	if opts.Project != "" {
		ensure(opts.Project)
	}

	// detectCycle walks the parent chain with a visited set; true on a cycle.
	detectCycle := func(startID string) bool {
		seen := map[string]bool{}
		cur := startID
		for {
			p, ok := parentOf[cur]
			if !ok {
				return false
			}
			if seen[cur] {
				return true
			}
			seen[cur] = true
			cur = p
		}
	}

	cycleReported := false
	for _, i := range issues {
		slug, multi := projectSlug(i)
		if multi {
			r.Diagnostics = append(r.Diagnostics, Diagnostic{Kind: "multi_project", IssueID: i.ID})
		}
		g := ensure(slug)
		if detectCycle(i.ID) {
			if !cycleReported {
				r.Diagnostics = append(r.Diagnostics, Diagnostic{Kind: "invalid_graph", Detail: "parent cycle or orphan loop"})
				cycleReported = true
			}
			continue // do not place cyclic nodes (avoids miscount/hang)
		}
		if _, hasParent := parentOf[i.ID]; !hasParent {
			// Parentless issue => epic row.
			card := toCard(i, opts.CustomCategories)
			e := g.epicsByID[i.ID]
			if e == nil {
				e = &Epic{Issue: card}
				g.epicsByID[i.ID] = e
				g.order = append(g.order, i.ID)
			} else {
				e.Issue = card
			}
		}
	}
	// Attach children to epics; collect loose.
	for _, i := range issues {
		parentID, hasParent := parentOf[i.ID]
		if !hasParent || detectCycle(i.ID) {
			continue
		}
		slug, _ := projectSlug(i)
		g := ensure(slug)
		card := toCard(i, opts.CustomCategories)
		if e := g.epicsByID[parentID]; e != nil {
			e.Children = append(e.Children, card)
		} else {
			g.loose = append(g.loose, card)
		}
	}

	// Compute epic column + conflict; assemble projects in stable slug order.
	var slugs []string
	for s := range groups {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, s := range slugs {
		g := groups[s]
		p := Project{Slug: s, Loose: g.loose}
		for _, eid := range g.order {
			e := g.epicsByID[eid]
			allDone := true
			for _, c := range e.Children {
				if c.Column != ColumnDone {
					allDone = false
					break
				}
			}
			ownDone := e.Issue.Column == ColumnDone
			switch {
			case ownDone && allDone:
				e.Column = ColumnDone
			case ownDone && !allDone:
				e.Column = ColumnInProgress
				e.Conflict = true
			default:
				e.Column = e.Issue.Column
			}
			p.Epics = append(p.Epics, *e)
		}
		r.Projects = append(r.Projects, p)
	}
	return r, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/rollup/ -v`
Expected: PASS (all Task 1 + Task 2 tests, ≥8 total).

- [ ] **Step 5: Vet**

Run: `go vet ./internal/rollup/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/rollup/rollup.go internal/rollup/rollup_test.go
git commit -m "feat(rollup): compute project/epic/column rollup with diagnostics"
```

## Task 3: `bd board` CLI command (`cmd/bd/board.go`)

**Files:**
- Create: `cmd/bd/board.go`
- Test: `cmd/bd/board_test.go`

Mirrors `cmd/bd/epic.go`: uses globals `store`, `rootCtx`, `jsonOutput`; `outputJSON`; `FatalErrorRespectJSON`; registered in `init()`. Flags `--project`, `--limit`, `--cursor`.

- [ ] **Step 1: Write the failing test**

Create `cmd/bd/board_test.go`:

```go
package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/rollup"
)

func TestBuildBoardOptions(t *testing.T) {
	o := buildBoardOptions("alpha", 50, "")
	if o.Project != "alpha" || o.Limit != 50 {
		t.Fatalf("unexpected options: %#v", o)
	}
	d := buildBoardOptions("", 0, "")
	if d.Limit != 0 { // 0 => rollup.DefaultLimit applied downstream
		t.Fatalf("default limit should pass through as 0, got %d", d.Limit)
	}
}

func TestRunBoardJSON_EmptyStore(t *testing.T) {
	dbPath := t.TempDir() + "/bd"
	ts := newTestStore(t, dbPath)
	defer ts.Close()

	r, err := rollup.Compute(context.Background(), ts, rollup.Options{})
	if err != nil {
		t.Fatalf("Compute on empty store: %v", err)
	}
	if projectSlugPresent(r, "Unassigned") == false {
		t.Fatalf("empty store must still emit Unassigned bucket")
	}
}

func projectSlugPresent(r *rollup.Rollup, slug string) bool {
	for _, p := range r.Projects {
		if p.Slug == slug {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/bd/ -run 'TestBuildBoardOptions|TestRunBoardJSON' -v`
Expected: FAIL — `buildBoardOptions` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/bd/board.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rollup"
)

var boardCmd = &cobra.Command{
	Use:   "board",
	Short: "Project board rollup (project -> epics -> status columns)",
	Long: `Read-only Linear-style rollup. Groups issues by project:<slug> label,
nests child issues under their epic, and buckets by status category
(todo/in_progress/done/deferred). --json prints the canonical contract
consumed by the web dashboard and the MCP tool.`,
	Run: func(cmd *cobra.Command, args []string) {
		project, _ := cmd.Flags().GetString("project")
		limit, _ := cmd.Flags().GetInt("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		ctx := rootCtx

		opts := buildBoardOptions(project, limit, cursor)
		r, err := rollup.Compute(ctx, store, opts)
		if err != nil {
			FatalErrorRespectJSON("computing board: %v", err)
		}
		if jsonOutput {
			outputJSON(r)
			return
		}
		renderBoardText(r)
	},
}

// buildBoardOptions is unit-testable without a store.
func buildBoardOptions(project string, limit int, _ string) rollup.Options {
	return rollup.Options{Project: project, Limit: limit}
}

func renderBoardText(r *rollup.Rollup) {
	fmt.Printf("Board @ %s\n", r.GeneratedAt.Format("2006-01-02 15:04:05 UTC"))
	for _, p := range r.Projects {
		fmt.Printf("\n## %s  (%d epics, %d loose)\n", p.Slug, len(p.Epics), len(p.Loose))
		for _, e := range p.Epics {
			flag := ""
			if e.Conflict {
				flag = "  [CONFLICT: closed epic, open children]"
			}
			fmt.Printf("  [%s] %s — %s%s\n", e.Column, e.Issue.ID, e.Issue.Title, flag)
			for _, c := range e.Children {
				fmt.Printf("      [%s] %s — %s (%s)\n", c.Column, c.ID, c.Title, c.Status)
			}
		}
	}
	if len(r.Diagnostics) > 0 {
		fmt.Printf("\nDiagnostics: %d (run with --json for detail)\n", len(r.Diagnostics))
	}
}

func init() {
	boardCmd.Flags().String("project", "", "Scope to a single project slug")
	boardCmd.Flags().Int("limit", 0, "Max issues to scan (0 = default cap)")
	boardCmd.Flags().String("cursor", "", "Pagination cursor (reserved; opaque)")
	rootCmd.AddCommand(boardCmd)
}
```

> Note: `store` is the package global opened by the root command's
> `PersistentPreRun`. `rollup.Compute` accepts the `IssueSource` interface,
> which `store` (storage.DoltStorage) satisfies via `SearchIssues`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/bd/ -run 'TestBuildBoardOptions|TestRunBoardJSON' -v`
Expected: PASS.

- [ ] **Step 5: Build and smoke-check the command exists**

Run: `make build && ./bin/bd board --help`
Expected: help text for `bd board` with `--project`, `--limit`, `--cursor`.

- [ ] **Step 6: Commit**

```bash
git add cmd/bd/board.go cmd/bd/board_test.go
git commit -m "feat(cli): bd board rollup command (--json/--project/--limit)"
```

---

## Task 4: `bd serve-board` web dashboard (`cmd/bd/serve_board.go`)

**Files:**
- Create: `cmd/bd/serve_board.go`
- Test: `cmd/bd/serve_board_test.go`

Implements spec C4 (singleflight + TTL + context deadline + stdout cap + bounded concurrency), C7 (last-good + stale banner). The web process holds **no DB credentials**: it execs `bd board --json` (resolved via `os.Executable()`).

**Mandatory: `serve-board` must NOT open the DB store.** The root `PersistentPreRun` lives in **`cmd/bd/main.go`** (there is no `root.go`). It opens the Dolt store unless the command is in a `noDbCommands := []string{...}` slice (~line 712, containing `"completion"`, `"help"`, `"version"`, `"setup"`, …). A top-level command whose name is in that slice gets `skipsStoreInit = true`. The web process must hold no DB credentials, so `serve-board` MUST be added to `noDbCommands`.

- [ ] **Step 1: Confirm the no-store mechanism**

Run: `grep -n "noDbCommands := \[\]string" cmd/bd/main.go` and read the slice.
Expected: a `noDbCommands` string slice in `cmd/bd/main.go`'s root `PersistentPreRun`. You will add `"serve-board"` to it in Step 4. (Command name is `serve-board` from `Use: "serve-board"`; it is top-level so `slices.Contains(noDbCommands, "serve-board") && !isSubcommand` ⇒ store init skipped.)

- [ ] **Step 2: Write the failing test**

Create `cmd/bd/serve_board_test.go`:

```go
package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/bd/ -run TestBoardCache -v`
Expected: FAIL — `newBoardCache` undefined.

- [ ] **Step 4: Write minimal implementation**

Create `cmd/bd/serve_board.go`:

```go
package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/singleflight"
)

const maxBoardJSONBytes = 8 << 20 // 8 MiB stdout cap

type fetchFn func(ctx context.Context) ([]byte, error)

// boardCache: singleflight + TTL + last-good (spec C4/C7).
type boardCache struct {
	ttl   time.Duration
	fetch fetchFn
	sf    singleflight.Group

	mu       sync.Mutex
	good     []byte
	goodAt   time.Time
	lastErr  error
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
	v, err, _ := b.sf.Do("board", func() (interface{}, error) {
		body, ferr := b.fetch(ctx)
		if ferr != nil {
			return nil, ferr
		}
		b.mu.Lock()
		b.good, b.goodAt, b.lastErr = body, time.Now(), nil
		b.mu.Unlock()
		return body, nil
	})
	if err != nil {
		b.mu.Lock()
		good, lastAt := b.good, b.goodAt
		b.lastErr = err
		b.mu.Unlock()
		if good != nil {
			_ = lastAt
			return good, true, nil
		}
		return nil, false, err
	}
	return v.([]byte), false, nil
}

// execBoardJSON runs `bd board --json` (this same binary), with a hard
// deadline and an output cap. The web process holds no DB credentials.
func execBoardJSON(ctx context.Context, timeout time.Duration) ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve self: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, self, "board", "--json")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("bd board --json failed: %w", err)
	}
	if out.Len() > maxBoardJSONBytes {
		return nil, fmt.Errorf("board json exceeds %d bytes", maxBoardJSONBytes)
	}
	return out.Bytes(), nil
}

var boardPageTmpl = template.Must(template.New("board").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Beads Board</title>
<meta http-equiv="refresh" content="{{.Refresh}}">
<style>body{background:#0d1117;color:#c9d1d9;font:14px/1.5 system-ui;margin:0;padding:16px}
.banner{background:#7d1d1d;color:#fff;padding:8px 12px;border-radius:6px;margin-bottom:12px}
pre{white-space:pre-wrap;word-break:break-word}</style></head>
<body>
{{if .Stale}}<div class="banner">stale — backend error (last good {{.GoodAt}})</div>{{end}}
<pre>{{.JSON}}</pre>
</body></html>`))

func serveBoard(addr string, refreshSec int, ttl, timeout time.Duration) error {
	cache := newBoardCache(ttl, func(ctx context.Context) ([]byte, error) {
		return execBoardJSON(ctx, timeout)
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = boardPageTmpl.Execute(w, map[string]any{
			"JSON": string(body), "Stale": stale,
			"Refresh": refreshSec, "GoodAt": time.Now().UTC().Format(time.RFC3339),
		})
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
		if addr == "" {
			return fmt.Errorf("--addr is required (tailnet IP:port, e.g. 100.x.y.z:8099)")
		}
		fmt.Printf("serving board on http://%s (refresh=%ds ttl=%ds)\n", addr, refresh, ttlSec)
		return serveBoard(addr, refresh,
			time.Duration(ttlSec)*time.Second, time.Duration(timeoutSec)*time.Second)
	},
}

func init() {
	serveBoardCmd.Flags().String("addr", "", "Tailnet bind address, e.g. 100.x.y.z:8099 (required)")
	serveBoardCmd.Flags().Int("refresh", 30, "Browser auto-refresh seconds (spec: >=15)")
	serveBoardCmd.Flags().Int("cache-ttl", 20, "Server cache TTL seconds (<= refresh)")
	serveBoardCmd.Flags().Int("exec-timeout", 10, "Hard timeout for 'bd board --json' seconds")
	rootCmd.AddCommand(serveBoardCmd)
}
```

Then apply the no-store exemption: in **`cmd/bd/main.go`**, add the string `"serve-board"` to the `noDbCommands := []string{...}` slice (place it tidily near `"setup"`/`"quickstart"`; `slices.Contains` is order-independent so exact position is not load-bearing). This makes the long-lived web process never open the Dolt store (it execs `bd board --json` for data instead).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/bd/ -run TestBoardCache -v`
Expected: PASS (singleflight collapse + last-good).

- [ ] **Step 6: Verify serve-board does not open the store**

Run: `grep -n "serve-board" cmd/bd/main.go`
Expected: `"serve-board"` appears in the `noDbCommands` slice (added in Step 4).
Run: `make build && ./bd serve-board 2>&1 | head -1`
Expected: error `--addr is required ...` (NOT a DB connection error — proves no store open). Binary is `./bd` (not `./bin/bd`).

- [ ] **Step 7: Commit**

```bash
git add cmd/bd/serve_board.go cmd/bd/serve_board_test.go cmd/bd/main.go
git commit -m "feat(web): bd serve-board (singleflight+TTL+last-good, no DB creds)"
```

---

## Task 5: Deployment unit + runbook (`deploy/pm1-beads/`)

**Files:**
- Create: `deploy/pm1-beads/bd-board.service`
- Modify: `deploy/pm1-beads/RUNBOOK.md` (append a section)

Implements spec C5 (ExecStartPre health gate: tailnet IP + Dolt SQL ping) and C6 (cgroup limits). Mirrors the existing `deploy/pm1-beads/dolt-sql-server.service` conventions.

- [ ] **Step 1: Create the systemd unit**

Create `deploy/pm1-beads/bd-board.service`:

```ini
[Unit]
Description=Beads Project Board (read-only, tailnet only)
After=network-online.target tailscaled.service dolt-sql-server.service
Wants=network-online.target
Requires=dolt-sql-server.service

[Service]
User=admin
WorkingDirectory=/home/admin/beads-workspace
EnvironmentFile=/home/admin/beads-client.env
# C5: do not start until the tailnet IP is up AND Dolt answers a SQL ping.
ExecStartPre=/bin/sh -c 'ip addr show | grep -q 100.85.126.95 || (echo "tailnet IP missing" >&2; exit 1)'
ExecStartPre=/bin/sh -c '/usr/local/bin/dolt --host 100.85.126.95 --port 3307 --user "$BEADS_DOLT_SERVER_USER" --password "$BEADS_DOLT_PASSWORD" --no-tls sql -q "SELECT 1" >/dev/null 2>&1 || (echo "dolt ping failed" >&2; exit 1)'
ExecStart=/usr/local/bin/bd serve-board --addr 100.85.126.95:8099 --refresh 30 --cache-ttl 20 --exec-timeout 10
Restart=always
RestartSec=5
# C6: bound footprint on the shared box (co-exist with the live stack).
MemoryMax=192M
CPUQuota=25%
TasksMax=64
Nice=10
IOSchedulingClass=idle
LimitNOFILE=4096
# Defense in depth: this service is read-only.
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Validate the unit file syntax**

Run: `systemd-analyze verify deploy/pm1-beads/bd-board.service 2>&1 | head` (if `systemd-analyze` is available locally) — otherwise visually confirm against `deploy/pm1-beads/dolt-sql-server.service`.
Expected: no fatal syntax errors (warnings about absolute paths on a non-target host are acceptable).

- [ ] **Step 3: Append the runbook section**

Append to `deploy/pm1-beads/RUNBOOK.md`:

```markdown

## Project board (read-only web)

- **Unit:** `bd-board.service` (this dir). Binds **only** `100.85.126.95:8099`.
  Holds NO DB credentials: it execs `bd board --json` behind a
  singleflight+TTL cache. cgroup-bounded (C6) to co-exist with the live
  stack. Read-only Dolt user (reuses `BEADS_DOLT_SERVER_USER` from
  `beads-client.env`; ensure that user has SELECT-only grants).
- **Install:** copy `bd` to `/usr/local/bin/bd`, copy the unit to
  `/etc/systemd/system/`, `sudo systemctl daemon-reload`,
  `sudo systemctl enable --now bd-board.service`.
- **View:** from a tailnet client, open `http://100.85.126.95:8099`.
- **Status/logs:** `systemctl status bd-board.service`,
  `journalctl -u bd-board.service -n 100 --no-pager`.
- **It will not start** until the tailnet IP is present and Dolt answers a
  SQL ping (ExecStartPre, C5). On Dolt/Tailscale outage the page renders
  last-good with a "stale — backend error" banner (C7).
- **Read-only grant check:** the board's SQL user must be SELECT-only.
  Verify: `SHOW GRANTS FOR '<board user>'@'%';` shows no INSERT/UPDATE/DELETE.
```

- [ ] **Step 4: Commit**

```bash
git add deploy/pm1-beads/bd-board.service deploy/pm1-beads/RUNBOOK.md
git commit -m "feat(deploy): bd-board systemd unit (health gate + cgroup limits) + runbook"
```

---

## Task 6: MCP tool (`integrations/beads-mcp`)

> **CORRECTION (architecture):** the MCP layering is 4 files, not 3. Verified
> pattern: `server.py` `@mcp.tool` is a thin wrapper that calls a module-level
> `beads_<name>()` in **`tools.py`**, which does `client = await _get_client()`
> then `await client.<name>(...)`. `BdClient = BdCliClient` (alias, tests use
> `BdClient`); abstract base is `BdClientBase`. So Task 6 = (1) abstract
> `board()` on `BdClientBase` + concrete on `BdCliClient` (mirror `stats` at
> bd_client.py:152 abstract / :708 concrete — `data = await
> self._run_command(*args)`, `_run_command` adds `--json` itself), (2) new
> `beads_board()` in `tools.py` (mirror `beads_stats` at tools.py:627), (3)
> register `beads_board` in server.py's `from beads_mcp.tools import (...)`
> block AND add the `@mcp.tool(name="board")`+`@with_workspace` wrapper
> (mirror `stats` tool at server.py:1220), (4) test in
> `tests/test_bd_client.py` using the existing `bd_client` fixture +
> `@pytest.mark.asyncio` (asyncio_mode=auto), monkeypatching `_run_command`.
> `board()` returns `dict[str, Any]` (no pydantic model — rollup JSON is
> nested/opaque). `Any` already imported in both bd_client.py and tools.py.

**Files:**
- Modify: `integrations/beads-mcp/src/beads_mcp/bd_client.py`
- Modify: `integrations/beads-mcp/src/beads_mcp/server.py`
- Test: `integrations/beads-mcp/tests/test_bd_client.py` (add a test)

Adds one tool that runs `bd board --json` via the existing subprocess client. Mirrors the existing `stats()` method/tool pair.

- [ ] **Step 1: Write the failing test**

Add to `integrations/beads-mcp/tests/test_bd_client.py` (match the file's existing import/fixture style; this is the canonical pattern — adapt the client construction to the existing fixtures in that file):

```python
import pytest

@pytest.mark.asyncio
async def test_board_returns_parsed_json(monkeypatch):
    from beads_mcp.bd_client import BdCliClient

    client = BdCliClient()

    async def fake_run_command(*args, cwd=None):
        assert args[0] == "board"
        assert "--json" in args
        return {"projects": [], "diagnostics": [], "generated_at": "2026-05-17T00:00:00Z"}

    monkeypatch.setattr(client, "_run_command", fake_run_command)
    result = await client.board()
    assert result["projects"] == []
    assert "generated_at" in result
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd integrations/beads-mcp && uv run pytest tests/test_bd_client.py -k board -q`
Expected: FAIL — `BdCliClient` has no attribute `board`.

- [ ] **Step 3: Add `board()` to the abstract base and the CLI client**

In `integrations/beads-mcp/src/beads_mcp/bd_client.py`, add to the `BdClientBase` ABC (next to the abstract `stats`):

```python
    @abstractmethod
    async def board(self, project: str | None = None, limit: int | None = None) -> dict[str, Any]:
        """Get the project board rollup (projects -> epics -> columns)."""
        pass
```

And to `BdCliClient` (next to its concrete `stats`, ~line 708 pattern — `data = await self._run_command(...)`):

```python
    async def board(self, project: str | None = None, limit: int | None = None) -> dict[str, Any]:
        """Get the project board rollup.

        Returns:
            The parsed `bd board --json` payload (projects, diagnostics, generated_at).
        """
        args: list[str] = ["board"]
        if project:
            args += ["--project", project]
        if limit:
            args += ["--limit", str(limit)]
        data = await self._run_command(*args)
        if not isinstance(data, dict):
            raise BdCommandError("Invalid response for board")
        return data
```

> `_run_command` already appends the JSON flag and parses stdout (it returns
> `json.loads(stdout)`); follow exactly how `stats()` calls it. Do not add
> `--json` manually if `_run_command` injects it — check `stats()` and match.
> If `_run_command` does NOT inject `--json`, append `"--json"` to `args`.

- [ ] **Step 4: Register the MCP tool**

In `integrations/beads-mcp/src/beads_mcp/server.py`, mirror the `stats` tool block (the `@mcp.tool(name="stats")` + `@with_workspace` pair). Add:

```python
@mcp.tool(
    name="board",
    description="Read-only project board rollup: issues grouped by project:<slug> label, nested under epics, bucketed into todo/in_progress/done/deferred columns. Use to answer 'what is the state of project X'. Optional: project (slug), limit (int).",
)
@with_workspace
async def board(
    workspace_root: str | None = None,
    project: str | None = None,
    limit: int | None = None,
) -> dict:
    """Get the project board rollup."""
    client = _get_client()  # match how other tools obtain the client in this file
    return await client.board(project=project, limit=limit)
```

> Match the exact client-acquisition pattern used by neighbouring tools in
> `server.py` (e.g. how `stats`/`blocked` get their client via the
> `@with_workspace` decorator or a helper). Do not invent a new accessor.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd integrations/beads-mcp && uv run pytest tests/test_bd_client.py -k board -q`
Expected: PASS.
Run: `cd integrations/beads-mcp && uv run pytest -q`
Expected: full MCP suite green (no regressions).

- [ ] **Step 6: Commit**

```bash
git add integrations/beads-mcp/src/beads_mcp/bd_client.py integrations/beads-mcp/src/beads_mcp/server.py integrations/beads-mcp/tests/test_bd_client.py
git commit -m "feat(mcp): board tool wrapping bd board --json"
```

---

## Task 7: Coding-agent skill (`plugins/beads/skills/project-board/SKILL.md`)

**Files:**
- Create: `plugins/beads/skills/project-board/SKILL.md`

Documents the **CLI** channel (`bd board`) for coding agents (per the spec's consumer model — the skill rides the CLI channel, not MCP). Mirrors the frontmatter shape of `plugins/beads/skills/beads/SKILL.md`.

- [ ] **Step 1: Create the skill**

Create `plugins/beads/skills/project-board/SKILL.md`:

```markdown
---
name: project-board
description: >
  Read-only Linear-style project board over beads. Use when you need to
  report or reason about the state of a project: which epics exist, what is
  in todo / in_progress / done / deferred, and which epics have a
  closed-with-open-children conflict. Trigger with "what's the state of
  project X", "project board", "epic rollup", "what's in progress".
allowed-tools: "Bash(bd board:*)"
version: "1.0.0"
license: "MIT"
compatible-with: [claude-code, codex]
tags: [project-board, rollup, reporting, read-only]
---

# Beads Project Board (read-only rollup)

`bd board` returns a read-only rollup of the shared beads workspace:
issues grouped by their `project:<slug>` label, child issues nested under
their epic (the parentless issue), bucketed into fixed columns.

## When to use

Use this when asked about **project/progress state** — not when creating or
mutating issues (use the `beads` skill for that). This board is read-only.

## Commands

- `bd board` — human-readable summary (all projects).
- `bd board --json` — the canonical machine payload. Parse this.
- `bd board --project <slug>` — scope to one project (bounds query cost;
  prefer this when asked about a single project).
- `bd board --limit <n>` — cap issues scanned (default cap applies otherwise).

## JSON shape

```
{
  "generated_at": "<RFC3339 UTC>",
  "projects": [
    { "slug": "<project slug or 'Unassigned'>",
      "epics": [
        { "issue": {"id","title","status","column","priority","assignee","updated_at"},
          "column": "todo|in_progress|done|deferred|fallback",
          "conflict": false,
          "children": [ { ...card... } ] } ],
      "loose": [ { ...card... } ] } ],
  "diagnostics": [ {"kind":"multi_project|invalid_graph|phantom_project","issue_id","detail"} ]
}
```

## Interpreting it correctly

- **Columns are fixed**: `todo`=active, `in_progress`=wip
  (in_progress/blocked/hooked), `done`=closed, `deferred`=frozen
  (deferred/pinned), `fallback`=unspecified/unknown custom status. The card's
  `status` field is the precise status name — report that, not just the column.
- **Epic `column` is computed**, not the epic's raw status. `conflict: true`
  means the epic is closed but has non-done children — call this out
  explicitly; it is the single most important signal on the board.
- **`Unassigned`** holds issues with no `project:` label and is always
  present (may be empty).
- **`diagnostics`** flags data problems: `multi_project` (issue had >1
  `project:` label; first lexicographic won), `invalid_graph` (a parent
  cycle/orphan — those nodes are excluded from counts), `phantom_project`
  (likely a label typo). Surface these when summarising; do not silently
  ignore them.
- `generated_at` is when the rollup was computed. If you fetched via the web
  dashboard it may be cached (stale banner shown there); the CLI is always
  fresh.

## Example

> "What's the state of project alpha?"

```
bd board --project alpha --json
```

Then summarise: per epic, its computed column, child counts per column, and
**any `conflict: true` epics or diagnostics first**.
```

- [ ] **Step 2: Verify the skill is discoverable**

Run: `cat plugins/beads/.claude-plugin/plugin.json | grep -A1 '"skills"'`
Expected: `"skills": "./skills/"` — confirms `plugins/beads/skills/project-board/` is picked up (no manifest edit needed; it's a directory glob).

- [ ] **Step 3: Sanity-check frontmatter parses**

Run: `head -16 plugins/beads/skills/project-board/SKILL.md`
Expected: valid YAML frontmatter delimited by `---`, `name: project-board`.

- [ ] **Step 4: Commit**

```bash
git add plugins/beads/skills/project-board/SKILL.md
git commit -m "feat(skill): project-board skill documenting the bd board CLI channel"
```

---

## Task 8: Full-suite verification

**Files:** none (verification only)

- [ ] **Step 1: Run the Go suite**

Run: `make test`
Expected: green. If `./scripts/test.sh` skips known-broken tests, that is expected; `internal/rollup` and `cmd/bd` board tests must pass.

- [ ] **Step 2: Run the MCP suite**

Run: `cd integrations/beads-mcp && uv run pytest -q`
Expected: green.

- [ ] **Step 3: End-to-end smoke (local, embedded)**

Run:
```bash
make build
cd "$(mktemp -d)" && /path/to/repo/bin/bd init --prefix smoke >/dev/null 2>&1 || true
/path/to/repo/bin/bd board --json
```
Expected: valid JSON with a `projects` array containing at least the
`Unassigned` bucket, `generated_at` set, `diagnostics` present (possibly empty).

- [ ] **Step 4: Final commit (if any verification fixups were needed)**

```bash
git add -A
git commit -m "test: project board full-suite verification fixups"
```

(If no fixups were needed, skip this step.)

---

## Self-Review (completed by plan author)

**1. Spec coverage:**
- Consumer model (web/CLI/MCP/skill) → Tasks 3,4,6,7. ✓
- `internal/rollup`, no raw SQL, narrow read interface, `ReadOnly` → Task 2 (`IssueSource` interface; storage opened only via the existing read path; **C1 read-only enforcement is delivered operationally** in Task 5 — the board's SQL user is SELECT-only and `serve-board` opens no store at all, which is *stronger* than `ReadOnly:true` since the web process never touches storage; documented + grant-check step). ✓
- Canonical column mapping incl. custom categories → Task 1. ✓
- Locked semantic rules (multi-label, computed epic column, visited-set, phantom, always-Unassigned) → Task 2 tests + impl. ✓
- C2 pagination/caps → `Options.Limit`/`DefaultLimit`/`--limit` (Tasks 2,3). ✓
- C4 singleflight+TTL+deadline+stdout cap+bounded concurrency → Task 4. ✓
- C5 ExecStartPre health gate, C6 cgroup limits, C7 last-good banner → Tasks 4,5. ✓
- Two load paths (web singleflight vs short-lived CLI) → Task 4 (web) / Task 3 (CLI is short-lived per-invocation). ✓
- MCP parallel channel + skill on CLI channel → Tasks 6,7. ✓

**2. Placeholder scan:** No "TBD/TODO/handle edge cases". Investigative steps (Task 4 Step 1, Task 6 Steps 3–4) give exact grep + exact action + expected output — actionable, not placeholders.

**3. Type consistency:** `Column`/`ColumnForStatus`/`Compute`/`Rollup`/`Project`/`Epic`/`Card`/`Diagnostic`/`Options`/`IssueSource` are defined in Tasks 1–2 and used identically in Tasks 3–4. `buildBoardOptions`/`newBoardCache`/`execBoardJSON`/`serveBoard` consistent within Task 4. `board()` client method name consistent across Task 6. ✓

**Note on C1:** The spec asks for `ReadOnly:true` on the storage open. This plan delivers something stronger for the web path (the long-lived web process opens **no** store), and relies on a SELECT-only Dolt user for `bd board`/CLI/MCP (Task 5 grant-check). If the implementer finds the existing root `PersistentPreRun` already supports a `ReadOnly` open mode for read commands, applying it to `bd board` is a welcome belt-and-suspenders addition but is not required for correctness given the SELECT-only grant.
