---
title: Code Map
description: The per-repository index of files, packages, imports, importers and the issues touching them — building it, reading it, keeping it fresh, the hooks that surface it automatically, and what it deliberately refuses to guess.
---

An agent handed `bd-4h2` and told to fix it starts by exploring. It greps, it
opens files, it follows imports, and it spends its first several thousand tokens
rediscovering something the repository already knows: which files this work
touches, what they import, who imports them, and which other open issues are
sitting on the same lines.

The **code map** records that. It is a per-repository index of files and
packages, the import edges between them, and the issues that have touched each
path — built by scanning the tree, kept fresh incrementally on each commit, and
surfaced on `bd show`, `bd prime` and (optionally) directly into a coding
agent's tool calls.

It is a map, not an oracle. It records what a scan can see and refuses to guess
at the rest.

## The three moving parts

The code map is one feature with three separable jobs, and knowing which is
which explains most of its behaviour.

| Part | Job | Where it lives |
| --- | --- | --- |
| **Scout** | Read the tree, emit a graph of nodes and edges | Runs in-process during `build` / `refresh` |
| **Index** | Store that graph, per repository, and answer queries about it | The Dolt database |
| **Cache** | A small derived JSON file the tool hooks read without opening the store | `.beads/codemap.cache.json` |

Everything is keyed by a **repository id** derived from the checkout's path, so
one beads database can hold maps for several repositories without them mixing.

## Building the map

```bash
bd codemap build
```

This scans the whole repository, writes the graph, and rebuilds the derived
cache. On a repository the size of beads itself it takes roughly 7 to 16
seconds. Go and Rust are the languages the scouts read.

`--json` reports what it wrote:

```json
{
  "repo_id": "…",
  "head_sha": "…",
  "nodes_upserted": 6,
  "nodes_deleted": 0,
  "edges_written": 4,
  "edges_pruned": 0,
  "dropped": 0,
  "languages": ["go"]
}
```

`dropped` counts references the scout could not resolve and **refused to
guess at**. That refusal is deliberate; see [What it will not
guess](#what-it-will-not-guess).

### Keeping it fresh

```bash
bd codemap status
bd codemap refresh
```

`status` reports whether the map matches the working tree. It prints `up to
date` when the indexed head matches, and `behind HEAD` when commits have landed
since. `refresh` rescans **only the packages whose files changed** since the
last build, which is why it is fast enough to run from a commit hook.

```json
{
  "repo_id": "…",
  "head_sha": "…",
  "only": ["example.com/mini/b"],
  "nodes_upserted": 2,
  "nodes_deleted": 0,
  "edges_written": 2,
  "edges_pruned": 0
}
```

The `only` array names the packages that were rescanned. A `nil` value means the
whole repository.

`status --json` adds `last_sha`, `up_to_date`, `nodes`, `edges`,
`stale_summaries`, `languages` and `indexed_at`, plus `behind_commits` and
`cache_age_seconds` when those are knowable.

### Run a full build periodically

Incremental refresh is correct about the packages it visits, but one thing can
lag: when a package is **deleted** in a commit range that no refresh revisits,
the importer names carried forward from the previous map can linger. A periodic
`bd codemap build` resets that. Weekly, or whenever the map looks wrong, is
enough — there is no correctness cliff, only names that are staler than the
tree.

## Reading the map

### One file or package

```bash
bd codemap show b/b.go
```

Prints the node, its package, what it imports, what imports it, its tests, and
any open issues recorded against that path. A path is tried as a file first and
as a package only if no file matches, so `bd codemap show example.com/mini/b`
works too.

`--json` returns `{"file": …, "issues": …}` for a file, or `{"package": …,
"issues": …}` for a package. The wire shapes are **snake_case**: a file context
carries `node`, `package`, `imports`, `importers` and `tests`, and each node
reference carries `path`, `name`, `lang`, `layer`, `summary`, `kind`, `stale`
and `is_test`.

### Walking the import graph

```bash
bd codemap deps a/a.go --reverse
```

Walks importers instead of imports. `--depth N` sets how many hops to follow
(default 2; the walk cuts cycles, so a mutually importing pair terminates).

`--json` returns `{"root": …, "reverse": …, "depth": …, "nodes": [{"depth": 1,
"path": "b/b.go"}, …]}`.

### Who is working here

```bash
bd codemap who b/b.go
```

Lists the issues touching a path. Open issues only by default; `--all` includes
closed ones. `--json` returns `{"path": …, "issues": [{"id", "title",
"status"}, …]}`.

### What an issue has touched

```bash
bd codemap files bd-4h2
```

Lists the files an issue has touched, each with the source that recorded it
(`manual` or `commit`), the commit sha where applicable, first-seen and
last-seen timestamps, and a touch count. `--json` returns `{"issue_id": …,
"files": […]}`.

### Files whose summary has drifted

```bash
bd codemap stale
```

Lists files whose stored summary predates their current contents, capped by
`--limit` (default 50). `--json` returns `{"stale": […], "count": N}`. A clean
map prints `0 stale summaries`.

## Linking issues to files

Two things write issue-to-file links, and both record which one did it.

### By hand

```bash
bd codemap link bd-4h2 b/b.go a/a.go
```

Records that an issue touches those paths, with source `manual`. Paths that
escape the repository root are refused. `--json` returns `{"issue_id",
"repo_id", "paths", "inserted", "updated"}`.

The same thing rides an update, for when you are already editing the issue:

```bash
bd update bd-4h2 --files b/b.go
```

### From a commit

```bash
bd codemap record-commit <rev>
```

Reads the commit's message for this workspace's issue ids, links each of them to
the files that commit touched with source `commit`, and then runs the same
incremental refresh `bd codemap refresh` runs — so the map describes the tree the
commit produced.

An id in the message that is not an issue in this workspace is reported on
stderr and skipped, not treated as a failure. A merge commit lists no files,
which is correct: its files are already attributed to the commits it merges.

This verb has no `--json`; it prints the linked ids and the paths.

## Where the map shows up

### On `bd show`

An issue with linked files gets a **CODE** section: the files, what they import,
and an **ALSO TOUCHING** list naming the other open issues sitting on the same
paths. An issue with no linked files gets no section at all rather than an empty
one.

`bd show --json` emits an **array** of issues, and the code context is a `code`
key on each element, in snake_case:

```json
[
  {
    "id": "bd-4h2",
    "code": {
      "issue_id": "bd-4h2",
      "repo_id": "…",
      "indexed": true,
      "shape": { … },
      "files": [
        { "path": "b/b.go", "context": { … }, "siblings": [ … ] }
      ]
    }
  }
]
```

So the jq path is `.[0].code`, not `.code`.

The `code` key is **omitted entirely** when the issue has no linked files, or
when the workspace has no repository or no map — reading code context must never
turn a successful `bd show` into an error. When it is present, `indexed` is
`false` if the repository itself is not indexed, and a file's `context` is null
when that path is unknown to the map.

`bd update <id> --claim --json` carries the same block on each issue in its
array, so an agent claiming work receives the code context in the same call. A
`bd update --json` **without** `--claim` is unchanged: the context answers "what
code did I just take over", which is a question only a claim asks.

### On `bd prime`

`bd prime` injects a `## Code map` section: the indexed head, the shape
(packages and files per layer), the top fan-in files, a note when summaries are
stale, and two instruction lines telling the reader to run `bd show <id>` and
`bd codemap show <path>` before exploring.

The section is capped at 20 lines by default. `codemap.prime.max_lines` changes
the cap. The header, the freshness line and both instruction lines survive any
cap. When the repository has no map, or no store, the section is simply absent —
`bd prime` runs from a session-start hook and must never fail.

## Hooks

### The post-commit git hook

`bd hooks install` now writes a managed **post-commit** hook that runs
`bd codemap record-commit HEAD`.

The code-map half of that hook **never fails a commit**. Git has already written
the commit by the time post-commit runs, so a non-zero exit there would only
print noise at the end of a success. A chained hook's exit code is still
propagated, because installing beads into post-commit must not silently disarm a
hook you already had.

It also does nothing at all in a repository whose map was never built — a commit
is the wrong moment to spend an uninvited whole-repository scan.

The subprocess is bounded at 10 seconds; `BD_CODEMAP_HOOK_TIMEOUT` overrides
that with a Go duration (`15s`) or bare seconds.

> **A released `bd` on your PATH will print `unknown hook: post-commit`** until
> you upgrade. The hook name is new; an older binary running a
> newly-installed hook does not recognise it. Rebuild or reinstall `bd` to clear
> it.

### The Claude Code tool hooks

```bash
bd setup claude
```

registers two hooks in `settings.json` beyond the ones it already wrote:

| Event | Matcher | Command |
| --- | --- | --- |
| `PreToolUse` | `Read\|Edit\|Write\|MultiEdit` | `bd codemap-hook pre-tool` |
| `PostToolUse` | `Edit\|Write\|MultiEdit\|NotebookEdit` | `bd codemap-hook post-tool` |

**pre-tool** injects what the map already knows about the file the agent is
about to open. It reads the derived cache and **never opens the store**, which
is the whole reason it is cheap enough to sit in front of every `Read`.

**post-tool** records the touch against the issue the session last claimed. It
does open the store, under a hard 2-second budget covering the open itself,
because a tool call is a person waiting. `BD_CODEMAP_TOOL_TIMEOUT` overrides
that budget.

Neither hook ever fails a tool call.

#### What they cost

Measured on this repository, 20 invocations per row, wall time per invocation:

| | beads repo | scratch repo |
| --- | --- | --- |
| `bd version` (bd's process floor) | 69 ms | 42 ms |
| `pre-tool`, first read of a file | 87 ms | 59 ms |
| `pre-tool`, repeat read (marker hit) | 66 ms | — |
| **the hook's own work** | **18 ms** | **17 ms** |

The honest reading: the hook's own marginal cost is 17 to 18 ms, of which about
7 ms is one permitted `git rev-parse`. The rest is bd's process startup floor,
which no `bd` subcommand can go below. The repeat path is within 3 ms of that
floor, i.e. effectively free.

### Turning them off

`BD_NO_CODEMAP=1` disables the code-map hooks — both the post-commit git hook
and the two Claude Code tool hooks. There is no per-hook opt-out; a switch that
silenced only one of them would be a trap.

## Summaries

File summaries are **not** written by a plain build. They cost a model call, so
they are opt-in:

```bash
bd codemap build --summaries
bd codemap refresh --summaries --max-files 20
```

Summaries are generated through `agy` and are **pinned to the blob hash** of the
file they describe, which is how `bd codemap stale` knows a summary has drifted
from its file. `--max-files N` caps how many files one pass will summarize; a
later pass picks up the ones the cap left behind.

`BD_CODEMAP_FAKE_AGY=<file>` substitutes that file's contents for the model,
which is how the tests exercise this path without a model call.

## Configuration

| Key | Meaning |
| --- | --- |
| `codemap.prime.max_lines` | Cap on the `## Code map` section in `bd prime` (default 20) |
| `codemap.summaries.model` | The model label recorded on summary rows |
| `codemap.<repo_id>.layers` | The layer vocabulary for one repository |

`codemap.summaries.model` is **a label, not a switch**. `agy`'s `--model` flag
does not work in print mode, so beads does not pass it; the configured value is
recorded on the rows so you can tell later which model wrote them. It does not
select the model.

`codemap.<repo_id>.layers` is written by `bd codemap build --summaries` and
`bd codemap refresh --summaries`, and is legitimately set by hand to pin a
repository's layer vocabulary.

## Environment variables

| Variable | Effect |
| --- | --- |
| `BD_NO_CODEMAP=1` | Disable the post-commit hook and both Claude Code tool hooks |
| `BD_CODEMAP_HOOK_TIMEOUT` | Post-commit subprocess budget (Go duration or bare seconds; default 10s) |
| `BD_CODEMAP_TOOL_TIMEOUT` | `post-tool` budget, store open included (default 2s) |
| `BD_CODEMAP_FAKE_AGY` | Path to a file whose contents stand in for the model during summarization |

## Exporting to Understand-Anything

```bash
bd codemap export
```

Writes the map as an Understand-Anything knowledge graph — the same document
`bd explain` already knows how to read — to
`.understand-anything/knowledge-graph.json` under the repository root.
`--out` writes it elsewhere.

The graph is **closed**: every edge endpoint names a node the document carries,
so a reader never dereferences an id that is not present. Node ids are
`file:<path>` and `package:<import path>`; edge kinds are `imports`, `contains`
and `tests`. A second export of an unchanged map writes the same bytes.

`--json` returns `{"path", "nodes", "edges", "layers"}`.

Two fields in the exported nodes are currently always empty: `tags` and
`complexity`. The indexed map does not carry either, and filling them would mean
reworking how the cache carries values forward. Consumers should treat them as
absent rather than as "none".

## What it will not guess

The scouts refuse to resolve a reference they cannot actually see, and count the
refusal instead:

- **`dropped`** counts references the scout could not resolve. It does not
  invent an edge to the most plausible candidate.
- **`external_packages`** names packages outside the scanned module, recorded as
  external rather than fabricated as local nodes.
- **`scoped_packages`** records every package path a scan was asked to cover,
  including ones that turned out not to exist, so an incremental refresh knows
  which nodes to delete rather than leaving orphans behind.

A map that admits it does not know something is usable. A map that guesses is
worse than no map, because the guess is indistinguishable from a fact.

## Known limitations

- Go and Rust only.
- A package that was deleted in a commit range no refresh revisited can keep
  stale importer names until the next full `bd codemap build`.
- Issue-to-file links are recorded against file paths, so `bd codemap show`
  lists issues for a file and not for a package.
- The `codemap` read verbs are not registered as read-only commands, so they can
  cause file-watcher churn in workspaces that watch for writes
  ([GH#804](https://github.com/gastownhall/beads/issues/804)).

## See also

- [ADR-0004: Code Map](https://github.com/gastownhall/beads/blob/main/engdocs/adr/0004-code-map.md) — why it is shaped this way
- [Events Journal](/reference/events-journal) — the other durable record a consumer can tail
