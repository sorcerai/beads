---
name: work-tracking-discipline
description: >
  Decide WHERE working todos and progress notes go before starting project
  work. Fires at the entry to a piece of work — planning, breaking down,
  kicking off, or "what should I do first". Default: track everything in
  beads so the whole picture stays visible; defer to Linear only when a
  Linear MCP is wired for that repo or the repo's agent docs name another
  tracker. Trigger with "starting this feature", "plan this", "break down
  the work", "kick off", "what should I work on first", "how should I track
  this".
allowed-tools: "Read,Bash(bd:*)"
version: "1.0.0"
license: "MIT"
compatible-with: [claude-code, codex]
tags: [work-tracking, task-management, beads, linear, discipline]
---

# Work-Tracking Discipline

Before starting any non-trivial piece of project work, decide where your
working to-do list and progress notes will live — *then* start. This skill
only answers "where"; the `beads` skill covers the `bd` how-to.

## The rule

On project work, your working to-do list and progress notes live in beads (`bd`) so the whole picture stays visible — unless the project's agent docs name another tracker, or a Linear MCP is wired for that repo, in which case use that instead.

This means: every task, every discovered follow-up, every status note goes
into the selected tracker as you go — not into a throwaway in-reply
checklist that vanishes with the conversation. The point is **visibility**:
work that isn't in the tracker is invisible in `bd board` / the dashboard.

## Tracker precedence (per project)

Check, in order, once at the start of the work:

1. **Repo agent docs.** Does this repo's `AGENTS.md` / `CLAUDE.md` name a
   tracker explicitly? → that wins (this is the per-repo override knob).
2. **Project-wired Linear MCP.** Else, is a `*linear*` MCP server wired for
   *this repo* — a server/tools matching `*linear*` (e.g. `linear-server`,
   tools `mcp__linear-server__*`) in the repo's `.mcp.json` or
   `.claude/settings*.json`? → use Linear. The account-wide claude.ai Linear
   connector does **not** count: it is present in every session regardless
   of repo and is not a project signal.
3. **Default.** Else → **beads**. Everything goes there.

Pick one. Never mirror the same work into two trackers.

## After deciding

- **beads** → use the `beads` skill; track todos with `bd`, add progress
  notes as you work (critical for compaction survival), link discovered
  work with `discovered-from`.
- **Linear** → use the project's Linear MCP tools; do not also create beads
  issues for the same work.
