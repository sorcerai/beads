# ADR-0004: Code Map

## Status

Proposed

## Date

2026-09-06

## Decision Drivers

- **Rediscovery is the dominant cost.** An agent handed an issue spends its
  first several thousand tokens re-deriving which files the work touches, what
  they import, and who imports them. The repository already knows this; nothing
  in bd recorded it.
- **Collision awareness.** Two agents claiming issues that touch the same file
  is a merge conflict nobody sees coming. The information needed to warn them —
  which open issues sit on which paths — has no home.
- **Freshness must be nearly free.** A map that is a manual chore is a map that
  is wrong. It has to update itself on commit without being noticed.
- **Injection must be cheaper than the thing it saves.** A hook that fires on
  every file read has a latency budget measured in tens of milliseconds, not
  hundreds.
- **Honesty over coverage.** A code map that guesses at an unresolvable
  reference is worse than one that admits the gap, because the guess is
  indistinguishable from a fact at the point of reading.

## Context

bd stores issues. It knows nothing about the code those issues are about. The
link between `bd-4h2` and `internal/storage/dolt/store.go` exists only in a
human's head, in a commit message, or in whatever the agent working the issue
happened to grep for.

Meanwhile bd already has adjacent machinery that wants exactly this data:
`bd prime` injects session context, `bd show` renders an issue, `bd explain`
reads an Understand-Anything knowledge graph, and `bd setup claude` writes hooks
into a coding agent's tool loop. Each of them is a surface a code map could
reach without inventing a new one.

## Decision

Add a per-repository code map: a scanned graph of files, packages and import
edges, plus a record of which issues have touched which paths. Surface it on
`bd show`, `bd update --claim`, `bd prime`, a post-commit git hook, and two
Claude Code tool hooks. Command group: `bd codemap`.

Key design points follow.

### Three roles, not one

The storage surface is split into three role interfaces rather than one code-map
object:

| Role | Answers |
| --- | --- |
| **Indexer** | "Write this graph" — `Apply`, `SetSummaries` |
| **Reader** | "What is at this path" — `FileContext`, `PackageContext`, `Shape`, `Stale` |
| **IssueFiles** | "Which issues touch this path, and which paths does this issue touch" — `Record`, `ByIssue`, `ByPath`, `IssueCodeContext` |

The split is not decoration; each role has a different caller, a different write
posture, and a different failure cost.

The Indexer is the only writer of graph structure, and it is called by exactly
two commands. The Reader is called by everything that renders, is read-only, and
is the one that has to be cheap. IssueFiles is the only part with a foreign key
into `issues`, and therefore the only part whose rows are meaningful without a
scan having happened.

Collapsing them into one interface would have meant every consumer of a path
lookup transitively depending on the write path, and it would have made the
storage decorator layer (timing, telemetry, read-only enforcement) wrap a single
fat surface where a read and a whole-repository rewrite are the same shape. The
decorator census tests in `internal/storage` exist precisely to catch a role that
grew a method nobody wrapped; three narrow roles keep that check meaningful.

The cost of the split is real and accepted: three accessors to thread through
every provider, three entries in the facade map, three rows in each decorator
table. That is bookkeeping a test enforces, which is the kind of cost worth
paying.

### Per-repository tables, keyed by `beads.ComputeRepoIDForPath`

Every code-map row carries a `repo_id` derived from the checkout's path. One
beads database can therefore hold maps for several repositories — which is not
hypothetical, because bd's own redirect and shared-server modes routinely point
several clones at one database.

The alternative was a map per database. Rejected: it would make the code map the
one bd feature that cannot be used in the configurations bd itself ships, and it
would silently mix two repositories' import graphs into one nonsense graph the
first time someone used a redirect.

The honest cost is that `repo_id` is derived from a **path**, so moving a
checkout orphans its map. A full `bd codemap build` at the new path fixes it.
Content-addressing the repository instead (say, by the root commit) was
considered and dropped: it would tie a working-tree index to git history, and it
breaks for a repository with no commits, which is exactly the state `bd init`
leaves you in.

### Blob-pinned summaries

A file summary is stored with the **blob hash** of the file it describes.
Staleness is then a comparison, not a heuristic: the summary is stale when the
file's current blob hash differs from the one the summary was written against.

This is what makes `bd codemap stale` truthful and `--summaries` re-runnable —
a second pass summarizes exactly the files that drifted, not everything, and not
a timestamp-based guess at everything.

Timestamps were the alternative and are worse in both directions. A touched but
unchanged file looks stale; a file restored to an earlier state looks fresh. The
blob hash is already computed by the scan, so pinning to it costs nothing.

Summaries are **not** written by a plain build. They cost a model call, and a
command whose latency depends on a model is a command people stop running.
`--summaries` is the opt-in, and `--max-files` bounds one pass.

One honest wart: `codemap.summaries.model` is a **label recorded on the row**,
not a model selector. `agy`'s `--model` flag does not work in print mode, so bd
does not pass it. The config key exists so a reader can tell which model wrote a
summary; it does not choose one. Documenting it as a switch would be a lie, so it
is documented as a label.

### A derived cache, so the hook never opens the store

`.beads/codemap.cache.json` is a small derived file rebuilt whenever the map is.
The `PreToolUse` hook reads it and **never opens the store**.

This is the single decision the tool-hook feature stands on. Opening the Dolt
store is the expensive part of any bd invocation. A hook that fires before every
`Read`, `Edit`, `Write` and `MultiEdit` cannot afford it, and a hook that
sometimes takes a workspace lock in front of a person's keystroke is a hook that
gets uninstalled.

Measured, 20 invocations per row:

| | beads repo | scratch repo |
| --- | --- | --- |
| `bd version` (process floor) | 69 ms | 42 ms |
| `pre-tool`, first read | 87 ms | 59 ms |
| `pre-tool`, repeat read | 66 ms | — |
| **hook's own work** | **18 ms** | **17 ms** |

The spec asked for "under 50 ms". **That is not met as total wall time, and
cannot be by any bd command**: bd's process startup floor alone is 42 to 69 ms.
It is met as the hook's *marginal* cost, which is 17 to 18 ms, of which about
7 ms is one permitted `git rev-parse`. We are recording this as the honest
reading of the criterion rather than quietly redefining the number. Anyone who
needs the total under 50 ms needs a resident process, not a faster hook.

The `PostToolUse` hook does open the store, because recording a touch requires
writing. It runs under a hard 2-second budget that covers the open itself — it
deliberately skips the normal pre-run store open so the context actually binds
the latency a person is waiting on.

The cache being *derived* is what makes this safe: it is never a source of
truth, it is rebuilt from the graph plus the previous cache plus one `Stale`
call, and losing it costs one `bd codemap build`.

### The scout never guesses

The scan records what it could resolve and counts what it could not:

- **`Dropped`** — references the scout could not resolve. Not attached to the
  most plausible candidate. Counted and reported.
- **`ExternalPackages`** — packages outside the scanned module, recorded as
  external rather than fabricated as local nodes. `Validate` accepts an edge
  whose destination is listed here, which is what lets an incremental scan
  reference a package it did not itself scan.
- **`ScopedPackages`** — every package path a scan was *asked* to cover,
  including ones that turned out not to exist. An incremental refresh takes its
  deletion set from the union of this and the packages actually found, which is
  how a deleted package's nodes get removed instead of orphaned.

The rejected alternative was nearest-match resolution. It would have produced a
denser, more impressive-looking graph and a strictly less useful one: an agent
reading a fabricated import edge follows it, and the map has then actively cost
more than it saved. A `dropped` count is a number someone can act on.

This is not free. It is honest about one known gap: importer names carried
forward across an incremental refresh can linger when a package is deleted in a
range no refresh revisits. The map says a file is imported by something that no
longer exists until the next full build. We chose to document that and recommend
a periodic full build rather than paper over it.

### Hooks never fail a commit or a tool call

The post-commit hook's code-map half always exits 0. Git has already written the
commit by the time post-commit runs, so a non-zero exit only prints noise at the
end of a success. A *chained* hook's exit code is still propagated, because
installing beads into post-commit must not silently disarm a hook the user
already had.

`bd codemap record-commit` inherits the same posture conditionally: under
`BD_GIT_HOOK=1` a hard failure is one stderr line and exit 0; run by hand it is
the same line and a non-zero exit, because a person who typed it wants to know it
did not work.

The tool hooks likewise never fail a tool call. A code map is a convenience; a
blocked edit is not.

A repository whose map was never built is skipped entirely by the commit hook. A
commit is the wrong moment to spend an uninvited whole-repository scan.

## Considered Alternatives

### Reuse `bd explain` / Understand-Anything as the store

Rejected as the *store*, adopted as an *export*. Understand-Anything's knowledge
graph is a document: a whole-file JSON artifact, regenerated wholesale, with no
incremental update path and no place to hang issue links. Making it the source
of truth would have meant rewriting the whole document on every commit and
inventing an issue-linkage side-table anyway.

`bd codemap export` writes that document *from* the indexed map instead. The
export is closed (every edge endpoint names a node the document carries) and
byte-idempotent for an unchanged map, so `bd explain` gets a graph that is as
fresh as the last refresh without a second scan.

Two exported fields stay empty: `tags` and `complexity`. The indexed map carries
neither, and filling them would mean reworking the cache's carry-forward path.
That is a thinner document for `/understand` users, recorded here rather than
hidden.

### Language servers instead of scouts

Rejected for now. An LSP-backed index would resolve references the scouts drop
and would extend past Go and Rust. It also means a resident process per language,
a startup cost measured in seconds, and a dependency on a toolchain being
installed and healthy in every environment where a commit hook runs. The scouts
are a `go list` and a source parse; they work in CI, in a container, and on a
laptop with a cold cache.

The `dropped` count is the deliberate seam here: it measures exactly how much an
LSP would buy, so the decision can be revisited with a number instead of a
feeling.

### Summaries on every build

Rejected. It makes the latency of a routine command depend on a model call, and
it burns tokens on files nobody asked about. Opt-in via `--summaries`, bounded by
`--max-files`, re-runnable because summaries are blob-pinned.

### One `bd codemap` verb that does everything

Rejected. `build`, `refresh` and `status` have genuinely different costs (seconds,
sub-second, instant) and different callers (a person, a hook, a person checking
on a hook). Collapsing them would have meant a command whose runtime a caller
cannot predict, which is the property that makes a commit hook unacceptable.

## Known Limitations

- Go and Rust only.
- `repo_id` is path-derived; moving a checkout orphans its map until a rebuild.
- Carried-forward importer names can lag a deleted package until a full build.
- Issue links are recorded against file paths, so a package lookup has no issue
  list of its own.
- The `codemap` read verbs are not registered as read-only commands. The
  registration map is keyed by bare command name and cannot express a
  subcommand, so `codemap show` cannot be registered without also registering
  `codemap build`. Cost: file-watcher churn in workspaces that watch for writes
  ([GH#804](https://github.com/gastownhall/beads/issues/804)).
