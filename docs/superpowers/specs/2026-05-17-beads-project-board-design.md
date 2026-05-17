# Beads Project Board — Design Spec

**Date:** 2026-05-17
**Status:** Approved (design); pending spec-review gate before writing-plans
**Follows:** [`2026-05-17-beads-pm1-central-ticketing-design.md`](./2026-05-17-beads-pm1-central-ticketing-design.md) — the live pm1 ticketing deployment this builds on (see also `deploy/pm1-beads/RUNBOOK.md`).

## Goal

A read-mostly "nicer Jira / Linear-style" project board over the existing
beads deployment, delivered as **one program: three surfaces over a single
shared rollup, plus a skill that documents the CLI surface**:

1. **Web Kanban dashboard** — always-on, tailnet-only, server-rendered, for a
   human to *see* project/epic/status at a glance.
2. **CLI `bd board`** — the canonical machine + human interface; the channel
   coding agents (Claude Code, Codex, reverie-driven agents) invoke via shell.
3. **MCP tool** — the same rollup exposed over MCP for runtimes that consume
   MCP and invoke tools autonomously (may not shell out).
4. **Custom Agent Skill** — teaches **coding agents** to use the **CLI**
   channel (`bd board`), not the MCP tool.

A dashboard whose data agents cannot also query programmatically is half a
tool; the three surfaces exist so humans and agents share one truth.

## Consumer model (locked)

| Surface | Audience | Channel |
|---|---|---|
| Web dashboard | Human (visibility/tracking) | Browser over Tailscale |
| `bd board --json` | Coding agents that run shell | Direct CLI invocation |
| MCP tool | Autonomous MCP-consuming runtimes | MCP protocol → `bd board --json` |
| Agent Skill | Coding agents | Documents the **CLI** channel |

MCP and CLI are **two parallel delivery channels over the same canonical
`bd board --json` contract**, for two different audiences. The skill rides the
CLI channel.

## Architecture

Factor the rollup **once**; every surface consumes the same canonical JSON.

```
internal/rollup  ──(in-process, ReadOnly)──>  internal/storage/issueops
      │
      ├── cmd/bd/board.go ............ `bd board [--json] [--limit] [--cursor]`
      │       │
      │       ├── bd serve-board ..... web dashboard (execs `bd board --json`)
      │       └── integrations/beads-mcp tool ... `bd board --json`
      │
      └── (skill documents `bd board` for coding agents)
```

### gemini ↔ codex reconciliation (recorded rationale)

Gemini recommended the web layer **exec** `bd board --json` (keeps DB
credentials out of the long-lived tailnet-facing process on a shared
production box — non-negotiable). Codex independently ranked that exact
exec-per-refresh path as its **#1 severity** (process + cold DB-pool storm on
shared pm1).

Both are correct. Resolution: **exec + a mandatory singleflight cache**. With
a short server-side TTL and singleflight, subprocess invocations decouple from
viewer count — one exec per TTL window regardless of open tabs. This yields
the same backend load profile as an in-process design while preserving
credential isolation. The guard is **spec-mandated, not optional** (see
Mandatory Constraints). An in-process rollup in `serve-board` remains the
documented escalation path if the design targets are exceeded; the rollup API
does not change, so the switch is scoped.

## Component 1 — `internal/rollup` (shared core)

**Responsibility:** compute the project → epic → status-bucket rollup. Single
source of truth for all surfaces.

- **No raw SQL.** All access goes through `internal/storage/issueops` patterns.
  The repo already carries documented Dolt JOIN pathologies (`joinIter`
  panics, single-table workarounds); bypassing the established helpers
  regresses known bugs. The package accepts a **narrow read interface**, never
  a raw `*sql.DB`.
- **Opens storage with `ReadOnly: true`.** Without it, schema-init /
  remote-sync write paths fire, and the dedicated read-only `beads` SQL user
  turns the board into a startup failure.
- **One bulk pass.** No N+1 over epics or per-project re-query. Fetch issues,
  labels, and parent/child edges in a bounded query plan, then assemble
  in-memory.

### Data model returned

```
Rollup {
  generated_at: timestamp
  projects: []Project
  diagnostics: []Diagnostic   // phantom projects, invalid graphs, multi-label warnings
}
Project {
  slug: string                // from `project:<slug>` label; "Unassigned" / "Unrecognized"
  epics: []Epic
  loose: []Card               // labeled-to-project but not under any epic
}
Epic {
  issue: Card
  column: Column              // COMPUTED (see rules)
  conflict: bool              // closed epic with non-done children
  children: []Card
}
Card {
  id, title, status (raw name), column: Column, priority, assignee, updated_at
}
Column = Todo | InProgress | Done | Deferred | Fallback
```

### Column mapping (canonical, not hand-rolled)

Derived from `internal/types.BuiltInStatusCategory(Status) StatusCategory`:

| StatusCategory | Built-in statuses | Column |
|---|---|---|
| `active` | `open` | **Todo** |
| `wip` | `in_progress`, `blocked`, `hooked` | **In-Progress** |
| `done` | `closed` | **Done** |
| `frozen` | `deferred`, `pinned` | **Deferred** |
| `unspecified` / unknown custom | custom statuses w/o category | **Fallback** |

Custom statuses use their **self-declared** category. The raw status name is
**always** shown on the card regardless of column. Columns are the **fixed set
above** — never derived dynamically from the status set (that produces
unstable per-project column counts).

### Locked semantic rules

1. **Multiple `project:` labels on one issue** → first label wins
   (lexicographic), and a `multi_project` diagnostic is emitted. Deterministic
   across runs.
2. **Epic column is computed**, not the epic's raw status: `Done` only if the
   epic's own category is `done` **and** all immediate children are category
   `done`. A `closed` epic with any non-done child renders in **In-Progress**
   with `conflict: true`.
3. **Parent/child traversal uses a visited-set.** Cycles, multi-parent, and
   orphaned epics do not loop or miscount; they emit an `invalid_graph`
   diagnostic.
4. **Custom / unspecified category** → **Fallback** column, raw status on card.
5. **Label typo → phantom project** → grouped under an **"Unrecognized
   projects"** bucket with a diagnostic, not silently split into a real
   project. (A future allowlist is out of scope — YAGNI for v1.)
6. **"Unassigned"** (issues with no `project:` label) is **always emitted**,
   even when empty, so the web layout never shifts between refreshes.

## Component 2 — `cmd/bd/board.go` (canonical CLI contract)

- New file in the existing `cmd/bd/` package.
- `bd board` → human-readable table; `bd board --json` → the canonical
  machine contract every other surface consumes.
- **`--limit` and `--cursor`** with default per-column caps. Pagination is
  **mandatory in v1**, not deferred — without it the board serializes the
  whole workspace every call and fails deterministically at ~10k issues.
- `--project <slug>` to scope to one project (used by agents asking about a
  single project; bounds query cost).

## Component 3 — `bd serve-board` (web dashboard)

- Same `bd` binary, new subcommand. Single static binary, one systemd unit.
- Server-rendered HTML, dark Linear-style board. **Binds to the tailnet IP
  only**, never the public interface.
- Data path: `exec.CommandContext("bd board --json", ...)` wrapped by:
  - **singleflight + short TTL cache** (TTL ≤ refresh interval) — collapses N
    concurrent viewers to ≤1 subprocess per TTL window.
  - a hard `context` deadline below the refresh interval.
  - a max-stdout byte cap; parse **only on exit code 0**.
  - bounded concurrent in-flight requests.
- On subprocess failure: render **last-good** data with a visible
  **"stale — backend error (HH:MM:SS)"** banner. Never a blank or silently
  stale board.
- Every render shows `generated_at` so stale is never mistaken for live.

## Component 4 — MCP tool

- One new tool in `integrations/beads-mcp` (Python, already subprocess-wraps
  `bd`): runs `bd board --json`, returns the parsed rollup. Near-trivial
  given the existing client pattern.
- For autonomous MCP-consuming runtimes only. Not what the skill teaches.

## Component 5 — Custom Agent Skill

- Teaches **coding agents** (shell-capable) to call **`bd board --json`** to
  answer "what is the state of project X" — and to scope with `--project` /
  `--limit`. Documents the JSON shape and the column semantics so agents
  interpret `conflict` / diagnostics correctly.
- Does **not** teach the MCP tool (different audience/channel).

## Mandatory constraints (architecture, not a risk appendix)

These are codex's adversarial findings promoted to **non-negotiable spec
requirements**. Each gets a real test, not a "be careful":

| # | Constraint | Why |
|---|---|---|
| C1 | Storage opened `ReadOnly: true`; CI test asserts it under the actual `beads` RO SQL user | Write paths otherwise fail at startup on the RO user |
| C2 | Bulk single-pass rollup; default per-column caps; `--limit/--cursor` | N+1 / full-scan = deterministic failure at scale |
| C3 | `internal/rollup` uses `issueops` patterns; **no raw SQL**; narrow read interface | Repo carries documented Dolt JOIN pitfalls |
| C4 | singleflight + TTL cache + `context` deadline + stdout cap + bounded concurrency around the exec | Neutralizes the exec-per-refresh storm (codex #1) |
| C5 | systemd `ExecStartPre` health gate: tailnet IP reachable **and** Dolt SQL ping | `After=tailscaled` is not tailnet/DB readiness |
| C6 | systemd cgroup limits: `MemoryMax`, `CPUQuota`, `TasksMax`, low `Nice`/IO priority | A runaway board request on shared pm1 is a polymarket/Postgres/Redis incident |
| C7 | Backend-error path renders last-good + visible stale banner | Operators must never mistake old data for live |

## Explicit design targets (so the storm-calculus is auditable)

> **≤ ~5 concurrent viewers · refresh cadence ≥ 15s · cache TTL ≤ refresh
> interval · single shared `beads_workspace` DB on tailnet-bound Dolt.**

If these change, C4's guard math must be re-derived and the in-process
escalation path (Approach 2) reconsidered. Stated here so a future maintainer
can audit whether the guarantees still hold.

## Testing strategy

- `internal/rollup` unit tests: column mapping for every built-in status +
  representative custom categories; computed epic column incl.
  closed-with-open-children `conflict`; multi-`project:` determinism; cyclic /
  orphaned graph → diagnostic, no loop; empty "Unassigned" emitted.
- **C1 integration test:** open under the read-only `beads` user, assert no
  write path fires and the rollup succeeds.
- `bd board --json` golden-output test; pagination boundary tests
  (`--limit`/`--cursor`); `--project` scoping.
- `serve-board`: cache hit collapses concurrent requests to one subprocess
  (singleflight); subprocess non-zero exit → last-good + stale banner;
  `context` deadline honored.
- Dolt-backed integration test for the rollup query (catches Dolt-not-MySQL
  regressions), reusing existing integration harness.

## Out of scope (YAGNI for v1)

- Any write/mutation from the board (it is strictly read-only).
- Auth / multi-tenant (tailnet ACL is the boundary).
- Project allowlist / label-typo auto-correction (diagnostics surface them).
- Interactive SPA / drag-and-drop (server-rendered read-only board).
- In-process rollup for `serve-board` — documented **escalation path**, not v1.

## Security / ops carry-over

pm1 (`100.85.126.95`, Tailscale) is a **live co-exist** box (polymarket-rs /
Postgres / Redis in production). The board uses a **dedicated read-only** Dolt
SQL user, binds tailnet-only, and is cgroup-constrained (C6). `34.241.191.3`
is strictly off-limits and untouched by any of this. Secrets remain in the
gitignored `deploy/pm1-beads/beads-client.env`.
