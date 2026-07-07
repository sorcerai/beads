---
name: beads
description: >
  Default home for project work tracking — a Dolt-powered, dependency-aware
  issue store. On project work your working to-do list and progress notes live
  here so the whole picture stays visible — unless the repo's agent docs name
  another tracker or a Linear MCP is wired for that repo. Also survives
  conversation compaction. Trigger with "create task", "what's ready", "track
  this work", "resume after compaction", "what should I track".
allowed-tools: "Read,Bash(bd:*)"
version: "0.60.0"
author: "Steve Yegge <steve.yegge@gmail.com>"
license: "MIT"
compatible-with: [claude-code, codex]
tags: [issue-tracking, task-management, multi-session, dependencies]
---

# Beads - Persistent Task Memory for AI Agents

Graph-based issue tracker with persistent memory that survives conversation compaction.

## bd vs TodoWrite

On project work, your working to-do list and progress notes live in beads (`bd`) so the whole picture stays visible — unless the project's agent docs name another tracker, or a Linear MCP is wired for that repo, in which case use that instead. TodoWrite is only for throwaway in-reply checklists that won't outlive the current message.

**Tracker precedence (per project):** (a) repo `AGENTS.md`/`CLAUDE.md` names a tracker → wins; (b) else a `*linear*` MCP server wired for that repo (`.mcp.json` or `.claude/settings*.json`; the account-wide claude.ai Linear connector does NOT count) → Linear; (c) else **beads** (default; everything goes there).

| bd (default, persistent) | TodoWrite (ephemeral) |
|-----------------|----------------------|
| Project todos + notes, deps, compaction survival, visible in `bd board` | Throwaway in-reply checklist, won't outlive this message |

See [BOUNDARIES.md](resources/BOUNDARIES.md) for detailed comparison.

## Prerequisites

```bash
bd --version  # Requires v0.60.0+
```

- **bd CLI** installed and in PATH
- **Git repository** (optional — use `BEADS_DIR` + `--stealth` for git-free operation)
- **Initialization**: `bd init` run once (humans do this, not agents)

## CLI Reference

**Run `bd prime`** for AI-optimized workflow context (auto-loaded by hooks).
**Run `bd <command> --help`** for specific command usage.

## Session Protocol

`bd ready` → `bd show <id>` → `bd update <id> --claim` → work, adding notes as you go (critical for compaction survival) → `bd close <id> --reason "..."` → `bd dolt push`. `bd prime` is the authoritative version.

## Memory (persistent across sessions)

Store and correct durable project knowledge (`bd remember`/`recall`/`memories`). **Use `bd memory supersede <old-key> --with=<new-key>` to correct a stale memory, not `bd forget`** — `forget` hard-deletes and loses the reasoning; `supersede` tombstones the old while keeping it recoverable (`bd memories --all` shows superseded; `bd recall` prints `[SUPERSEDED by X]`). Run `bd memory --help`.

## Output

Append `--json` to any command for structured output. Use `bd show <id> --long` for extended metadata. Status icons: `○` open `◐` in_progress `●` blocked `✓` closed `❄` deferred.

## Error Handling

| Error | Fix |
|-------|-----|
| `database not found` | `bd init <prefix>` in project root |
| `not in a git repository` | `git init` first |
| `disk I/O error (522)` | Move `.beads/` off cloud-synced filesystem |
| Status updates lag | Use server mode: `bd dolt start` |

See [TROUBLESHOOTING.md](resources/TROUBLESHOOTING.md) for full details.

## Advanced Features

| Feature | CLI | Resource |
|---------|-----|----------|
| Molecules (templates) | `bd mol --help` | [MOLECULES.md](resources/MOLECULES.md) |
| Chemistry (pour/wisp) | `bd pour`, `bd wisp` | [CHEMISTRY_PATTERNS.md](resources/CHEMISTRY_PATTERNS.md) |
| Agent beads | `bd agent --help` | [AGENTS.md](resources/AGENTS.md) |
| Async gates | `bd gate --help` | [ASYNC_GATES.md](resources/ASYNC_GATES.md) |
| Worktrees | `bd worktree --help` | [WORKTREES.md](resources/WORKTREES.md) |

## Architecture blueprints (anti-drift)

`bd init` scaffolds an `ARCH.md` construction blueprint + a `post-close` drift
hook automatically. ARCH.md is a short file of **negative invariants** ("X must
not Y") that keeps builder agents aligned to intended architecture.

```bash
bd arch init     # scaffold ARCH.md + post-close hook (idempotent; never overwrites)
bd arch draft    # construct a candidate ARCH.md.draft: scout (real dep graph) + agy synthesis
bd arch check    # run scripts/arch-check.sh if present (deterministic, free)
```

**`bd close` runs `.beads/hooks/post-close` automatically** (advisory — never
blocks): nudges if no ARCH.md exists, runs the deterministic gate, and (with
`BD_ARCH_REVIEW=1`) forks a fresh-context reviewer. Skip with `--no-hooks`.
Construction is **draft → human approve** (never auto-commit a model-generated
ARCH.md); enforcement is the automated hook + gate. Keep ARCH.md to negatives —
they don't rot; positive specs do. Run `bd arch --help`.

## Resources

| Category | Files |
|----------|-------|
| **Getting Started** | [BOUNDARIES.md](resources/BOUNDARIES.md), [CLI_REFERENCE.md](resources/CLI_REFERENCE.md) (live reference pointers), [WORKFLOWS.md](resources/WORKFLOWS.md) |
| **Core Concepts** | [DEPENDENCIES.md](resources/DEPENDENCIES.md), [ISSUE_CREATION.md](resources/ISSUE_CREATION.md), [PATTERNS.md](resources/PATTERNS.md) |
| **Resilience** | [RESUMABILITY.md](resources/RESUMABILITY.md), [TROUBLESHOOTING.md](resources/TROUBLESHOOTING.md) |
| **Advanced** | [MOLECULES.md](resources/MOLECULES.md), [CHEMISTRY_PATTERNS.md](resources/CHEMISTRY_PATTERNS.md), [AGENTS.md](resources/AGENTS.md), [ASYNC_GATES.md](resources/ASYNC_GATES.md), [WORKTREES.md](resources/WORKTREES.md) |
| **Reference** | [STATIC_DATA.md](resources/STATIC_DATA.md), [INTEGRATION_PATTERNS.md](resources/INTEGRATION_PATTERNS.md) |

## Validation

If `bd --version` reports newer than `0.60.0`, this skill may be stale. Run `bd prime` for current CLI guidance — it auto-updates with each bd release and is the canonical source of truth ([ADR-0001](adr/0001-bd-prime-as-source-of-truth.md)).
