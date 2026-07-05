# bd docs — beads-native living repo documentation

**Date:** 2026-07-04
**Status:** Approved design, pre-implementation
**Owner:** fork (sorcerai/beads); no upstream dependency

## Problem

Each repo needs a living documentation set ("wiki") that describes what the
code is and how it works — the descriptive layer that ARCH.md deliberately
omits (ARCH.md is a terse negative-invariant drift gate, not a reference).
Reference point: [langchain-ai/openwiki](https://github.com/langchain-ai/openwiki),
which generates an `openwiki/` markdown dir via an LLM agent loop, refreshed by
a **blind daily cron**.

We can do better than a clock: beads has an **event stream of completed work**.
The post-close lifecycle hook + reconciliation sweep (shipped 2026-07-03)
guarantee that every `bd close` — including crashes, `update --status closed`,
proxied closes, molecule auto-closes, and closes synced from other machines —
eventually fires a hook on each machine. Docs keyed to that stream update when
real work happens, scoped to what changed, with the issue as the change context.

**Durability requirement (user):** every close is recorded; a crash must never
lose documentation state.

## Consumers

Agents first (context injection, like ARCH.md/CLAUDE.md), humans second
(browsable markdown; pm1 board panel is a follow-up, not v1). The wiki is
committed per-repo and portable to GitHub docs by copying markdown.

## Design: two tiers (mirrors the bd arch pattern)

### Tier 1 — `bd docs update <ids…>` — every close, deterministic, durable

Runs from the post-close hook. **No LLM. Never blocks or fails a close**
(advisory contract; log-and-continue on every error).

For each closed issue, writes `wiki/log/<issue-id>.md` containing, generated
**deterministically from Dolt data only**: title, type, priority, close reason,
epic/parent, dependency links, touched files (via the `bd explain`
issue↔git-history intersection), created/closed timestamps.

**Idempotent by construction (resolves the global-once problem):** a wiki
write is a global-once effect riding per-machine hook infra. Machine A closes
and writes the entry; machine B's sweep later fires for the same synced-in
close. Idempotence is a **file-existence check** (same-close ts), not byte
equality: the issue-data core is deterministic from Dolt, but the
touched-files section derives from local git state and may differ between
checkouts. Accepted residual risk: two machines independently *creating* the
same entry before either syncs git can produce a trivial one-file conflict —
rare (requires both to fire before either pushes) and mechanically resolvable.
A **reclose** (existing file's closed ts ≠ issue's ClosedAt) overwrites; both
machines see the same Dolt ClosedAt and converge. One-file-per-issue (the
ADR-tools lesson) avoids the append-to-one-CHANGELOG design, which is both
non-idempotent and a guaranteed git merge conflict (two machines appending at
EOF).

**Weak `bd explain` association** (no issue IDs in commit messages) degrades
the entry to issue-data-only, noted inline in the entry.

### `wiki/log/` is an inbox, not an archive (size control)

Dolt is the source of truth; log entries are **derived staging** for the next
regen. Tier 2 consumes them — folds meaningful ones into narrative pages —
and **deletes the processed files in the same change**. Recovery paths:
issues remain in Dolt, deleted files remain in git history, and
`bd docs log --since <date>` regenerates any range on demand.

Backstop: if the inbox exceeds ~200 entries with no regen, Tier 1 compacts the
oldest into a single `wiki/log/backlog.md` digest instead of growing file
count. Steady-state working tree: narrative pages + small inbox (tens of KB),
so branch/worktree checkout weight stays trivial at fleet scale (git worktrees
share the object store; only checkout weight multiplies).

Post-regen sweep replays (machine B firing for an already-consumed close) hit
"issue closed before regen watermark" → skip; consumed entries don't resurrect.

### Tier 2 — `bd docs regen` — batched, LLM, best-effort

Regenerates the narrative reference: `wiki/README.md` (index/overview),
`wiki/architecture.md` (links to ARCH.md; never duplicates invariants),
`wiki/components/*.md` (per-module guides). Input context: the repo, ARCH.md,
and the **inbox entries since the last regen** — so it focuses on what changed
instead of blind full re-scans.

**Harness model (inverted from CLI-detection):** `bd close` is almost always
run by an agent already in a session (Claude Code, pi, codex, antigravity).
The hook does not spawn a second harness. When the dirty counter crosses the
threshold (default 10 closes, config `docs.regen-threshold`; an epic close
counts as threshold-met immediately), the hook prints a stderr nudge:
`wiki: N closes since last regen — run 'bd docs regen'`. The **resident agent**
runs the regen in whatever harness is live — automatic harness matching, no
detection code, no nested sessions, no double token billing.
`bd docs regen` (no flags) emits the harness-agnostic prompt + context for the
resident agent to execute; `bd docs regen --exec <cli>` is the **headless
fallback** (cron/pm1), defaulting to `agy`/`pi` (always available; claude/codex
may be token-limited). Known landmines: `agy --model` is broken in `--print`
mode (use its default model); Gemini goes via agy, never pi.

Tier 2 **never runs inside the hook** (60s hook timeout; regen takes minutes).
The hook only writes Tier-1 entries, bumps the counter, and nudges.

### State: `wiki/.docs-state` (committed — travels with the wiki)

Regen watermark (timestamp docs are current through) + `regen_started`
(snapshot taken when a regen prompt is built; zero when no regen in flight).
Committed because it must travel with the wiki via git; the opposite choice
from the post-close fired-ledger (machine-local, correct for per-machine
effects). The two sync channels (git for wiki, Dolt for issues) run at
different cadences; lag is harmless, and the in-wiki watermark is what makes
regens converge.

**No stored dirty counter** (review finding): the dirty count is *derived* —
the number of unconsumed `log/*.md` entries. The filesystem is the counter,
which eliminates the read-modify-write race between concurrent updates.

**Lost-update protection** (review finding): `--complete` consumes only
entries closed ≤ `regen_started` and advances the watermark to
`regen_started`, never to now — entries written while a regen was in flight
survive to the next cycle. `backlog.md` is included in the prompt and consumed
with the inbox. An issue that syncs in with a ClosedAt older than an
already-advanced watermark and no existing entry is skipped **visibly**
(stderr warning naming the recovery command), not silently.

**Guard scope** (review finding): `BD_DOCS_RUNNING=1` suppresses only the
nudge — entries are still written, because during `--exec` the fired-ledger
means the sweep never replays the agent's own closes; a full skip would lose
them permanently. `BD_NO_DOCS=1` remains the full opt-out.

**Prompt injection** (review finding): inbox content is fenced as untrusted
DATA in the regen prompt; `--exec` retains residual indirect-injection risk on
repos where outsiders can influence issue text — documented in the command
help, acceptable for this internal single-user fleet.

### Commit model (decided): leave dirty; session-close commits

The hook writes files but **never commits**. The agent's session-close protocol
(quality gates → commit) picks the entries up, so doc updates usually ride the
same commit as the work. Crash before commit: the sweep regenerates the file.
bd never creates commits in user repos.

### Guards

- `BD_DOCS_RUNNING=1` — reentrancy guard, set for any Tier-2 execution: the
  regen agent's own bd calls (`bd show`, `bd ready`, …) must never re-trigger
  docs hooks or sweep nudges. Same pattern as `BD_GIT_HOOK`.
- `BD_NO_DOCS=1` — opt-out, mirrors `BD_NO_CLOSE_HOOK`.
- `docs.dir` config — output dir name, default `wiki/` (collision-safe).

## CLI surface

| Command | Role |
|---|---|
| `bd docs init` | Scaffold `wiki/` + `.docs-state`; wire the post-close hook line (pattern: `bd arch init`) |
| `bd docs update <ids…>` | Tier 1 writer; called by the hook; idempotent |
| `bd docs regen [--exec <cli>]` | Tier 2; prompt for resident agent, or headless exec; advances watermark, resets counter, consumes inbox |
| `bd docs status` | Dirty count, last regen, staleness findings |
| `bd docs log --since <date>` | Regenerate historical entries from Dolt on demand |

New files only (`cmd/bd/docs*.go`) to keep upstream merges clean.

## Quality & error handling

- Tier-2 prompt contract: cite real paths; update-don't-rewrite unchanged
  pages; ARCH.md is the invariant source of truth; inbox entries are the
  change context.
- **Reuse `arch_stale.go`** on `wiki/*.md` after regen: dangling backtick refs
  reported advisorily via `bd docs status`.
- All failures advisory: Tier-1 errors log-and-continue (never block closes);
  a failed regen leaves the watermark unmoved — nothing lost, retry later.

## Testing

- Table-driven: entry determinism (same Dolt data ⇒ same bytes), idempotent
  re-fire, watermark/counter roundtrip, inbox compaction threshold, reentrancy
  guard, nudge threshold.
- Embedded (`BEADS_TEST_EMBEDDED_DOLT=1`): close → hook → `wiki/log/<id>.md`
  exists; sweep-replay convergence (fire twice, identical tree); regen-consumed
  entry does not resurrect.
- No LLM in tests; Tier 2 tested to the prompt boundary.

## Non-goals (v1)

- serve-board wiki panel (follow-up bead)
- Automatic GitHub Wiki/Pages publishing (manual copy is fine)
- CI cron workflows (OpenWiki's model; ours is event-driven — headless `--exec`
  covers unattended repos if wanted later)
- Rewriting ARCH.md content into the wiki (link, don't duplicate)

## Rollout

Dogfood on the beads repo first (`bd docs init` + a manual first regen), then
`~/.beads-sync/register.sh`-style opt-in per repo. Fleet-wide enablement only
after the beads-repo wiki proves useful.
