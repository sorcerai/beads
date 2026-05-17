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
          "conflict": false,            // true = closed epic with non-done children (flag this first)
          "children": [ { ...card... } ] } ],
      "loose": [ { ...card... } ] } ],
  "diagnostics": [ {"kind":"multi_project|invalid_graph","issue_id","detail"} ]
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
- **`loose`** holds issues whose parent epic is absent from that project
  group (e.g. parent in another project) — they are not under any epic.
- **`diagnostics`** flags data problems: `multi_project` (issue had >1
  `project:` label; first lexicographic won), `invalid_graph` (a parent
  cycle/orphan — those nodes are excluded from counts). Surface these when
  summarising; do not silently ignore them.
- `generated_at` is when the rollup was computed. The CLI is always fresh;
  the web dashboard may show a cached value with a stale banner.

## Example

> "What's the state of project alpha?"

```
bd board --project alpha --json
```

Then summarise: per epic, its computed column, child counts per column, and
**any `conflict: true` epics or diagnostics first**.
