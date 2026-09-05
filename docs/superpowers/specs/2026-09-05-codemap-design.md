# Code Map — design

Date: 2026-09-05
Status: approved approach (A), spec under review
Tracks: epic to be filed under beads-20x (bd self-comprehension)

## 1. Problem

Every agent session re-discovers the same facts about the codebase by grepping
and reading: which files an issue touches, what those files import, who imports
them, and which other open issues are working in the same place. That
exploration is the single largest token sink in a `bd`-driven session and it
produces nothing durable.

`bd` already has the pieces but none of them is wired to the agent's turn:

- `bd explain <id>` (cmd/bd/serve_board.go:1623) finds files by `git log
  --grep=<id>` and enriches them from `.understand-anything/knowledge-graph.json`,
  which nothing in this repo produces.
- `bd arch draft` (cmd/bd/arch_draft.go:122) has a deterministic dependency
  scout for Go, Rust and Python, used only to feed an ARCH.md synthesizer.
- Issues carry no file linkage; `provenance_events` binds an issue to a SHA or
  PR, never to a path.
- `bd prime`, `bd show` and the agent hooks never mention code at all.

## 2. Goal and success criteria

Give the model, at the moment it needs it, the answer to "what does this issue
touch, what is connected to it, what depends on it, who else is working here"
without exploring.

Success is measured on this repo:

1. `bd update <id> --claim` prints the issue's files with layer, purpose,
   imports and importers, plus sibling open issues on the same files, in under
   2 seconds against the embedded store.
2. When Claude Code is about to Read or Edit a file, the PreToolUse hook injects
   that file's one-line purpose, its importers, and open issues on it, in under
   50 ms, without opening the database.
3. `bd prime` includes a bounded repo shape (layers, packages, hottest files)
   and tells the model which command replaces exploring.
4. After a commit, the deterministic map is updated for the changed packages
   within the post-commit hook budget (10 s), and the commit's files are
   recorded on every issue the commit message names.
5. A file whose content changed after its summary was written is shown as
   stale everywhere the summary appears, and only stale files are re-summarized.
6. The map for a second repository (Rust) builds from the same command with no
   code change outside the scout package.

## 3. Decisions already made (from brainstorm)

| Question | Decision |
|---|---|
| Triggers | claim/show, PreToolUse hook, prime, post-commit record |
| Depth | deterministic graph plus LLM one-line summaries, staleness-tracked by blob hash |
| Storage | Dolt tables in the beads database, keyed by repository |
| Languages v1 | Go, Rust (scout interface is pluggable) |
| Freshness | incremental scout on commit; summaries lazy, on demand |
| LLM | via `agy`, same path `bd arch draft` uses, extracted to a shared package |
| Linkage | hook-observed edits on the active issue, commit-message IDs, explicit `--files` |

## 4. Architecture

```
                 ┌──────────────────────────────────────────────┐
                 │ internal/codemap/scout   (pure, no DB)       │
                 │   Scout interface; goscout; rustscout        │
                 │   Graph{Nodes,Edges} from a repo root         │
                 └───────────────┬──────────────────────────────┘
                                 │ Graph
   git post-commit ──► bd codemap refresh ──► codemapops.Indexer.Apply ──► Dolt
   bd codemap build ─┘        │                                          code_nodes
                              │ stale nodes                              code_edges
   bd codemap refresh --summaries ──► internal/codemap/summarize (agy) ──► summaries
                                                                          │
   bd update --claim ─┐                                                   │
   bd show <id>       ├──► codemapops.Reader / IssueFiles ◄───────────────┘
   bd explain <id>    │              │
   bd prime           ┘              ├──► .beads/codemap.cache.json  (derived, gitignored)
                                     │              ▲
   Claude PreToolUse ──► bd codemap-hook pre-tool ───┘  (reads cache only)
   Claude PostToolUse ─► bd codemap-hook post-tool ──► IssueFiles.Record (active issue)
   git post-commit ────► bd codemap record-commit ───► IssueFiles.Record (IDs in message)
```

Three layers, each testable alone:

1. **Scout** (`internal/codemap/scout`): turns a checkout into a `Graph`. No
   database, no network. Fixture repos under `testdata/`.
2. **Roles** (`codemapops/` leaf, bodies in `internal/storage/codemapops`):
   the only way the map is read or written. Follows
   engdocs/ADDING_AN_ISSUEOPS_ROLE.md step for step.
3. **Front doors** (`cmd/bd/codemap*.go`, hooks, show/claim/prime/explain
   integration, HTTP): call roles and nothing else.

## 5. Data model

Migration `0067_create_codemap.up.sql` (frozen once merged; follow
internal/storage/schema/migrations/README.md). All three tables carry `repo_id`
because one beads database (for example ~/.beads-planning) serves many
checkouts.

`repo_id` is `beads.ComputeRepoIDForPath(root)` (internal/beads/fingerprint.go),
the same fingerprint `bd init` and `bd doctor` use: remote-URL based, so a
clone or worktree of the same repo shares a map and a different repo never
collides.

### code_nodes

| column | type | notes |
|---|---|---|
| id | CHAR(64) PK | sha256(repo_id, kind, path) hex; deterministic so re-index is an upsert |
| repo_id | VARCHAR(64) NOT NULL | |
| kind | VARCHAR(16) NOT NULL | `package` or `file` (v1 has no symbol nodes) |
| path | TEXT NOT NULL | repo-relative, forward slashes |
| path_hash | CHAR(64) NOT NULL | sha256(path); UNIQUE(repo_id, kind, path_hash) keeps TEXT out of the index |
| name | VARCHAR(255) NOT NULL | package import path or file base name |
| package_id | CHAR(64) | for files: id of the containing package node |
| lang | VARCHAR(16) NOT NULL | `go`, `rust` |
| blob_hash | CHAR(40) | git blob of the file at last scout; NULL for packages |
| loc | INT | non-blank lines |
| is_test | TINYINT(1) NOT NULL DEFAULT 0 | |
| exported | INT | count of exported symbols (Go) / pub items (Rust) |
| layer | VARCHAR(64) | assigned by summarizer from a fixed vocabulary; NULL until summarized |
| summary | TEXT | one line, ≤160 chars |
| tags | TEXT | JSON array of short strings |
| summary_blob_hash | CHAR(40) | blob the summary was written against; stale when ≠ blob_hash |
| summary_model | VARCHAR(64) | |
| summarized_at | DATETIME | |
| indexed_at | DATETIME NOT NULL | |

Indexes: `(repo_id, kind)`, `(repo_id, package_id)`.

### code_edges

| column | type | notes |
|---|---|---|
| repo_id | VARCHAR(64) NOT NULL | |
| src_id | CHAR(64) NOT NULL | |
| dst_id | CHAR(64) NOT NULL | |
| kind | VARCHAR(16) NOT NULL | `imports` (package→package and file→package), `contains` (package→file), `tests` (test file→package) |
| weight | INT NOT NULL DEFAULT 1 | number of import sites |
| PK | (repo_id, src_id, dst_id, kind) | |

Index `(repo_id, dst_id, kind)` for "who imports X".

No foreign keys to `code_nodes`: a scout run replaces a package's edges in one
statement and Dolt DDL/FK behaviour in the README makes cascading rebuilds
fragile. Orphan edges are removed by the indexer's `Prune`.

### issue_files

| column | type | notes |
|---|---|---|
| issue_id | VARCHAR(255) NOT NULL | FK issues(id) ON DELETE CASCADE ON UPDATE CASCADE, same as provenance_events |
| repo_id | VARCHAR(64) NOT NULL | |
| path_hash | CHAR(64) NOT NULL | |
| path | TEXT NOT NULL | |
| source | VARCHAR(16) NOT NULL | `hook`, `commit`, `manual`, `branch` |
| commit_sha | CHAR(40) | for `commit` |
| first_seen | DATETIME NOT NULL | |
| last_seen | DATETIME NOT NULL | |
| touches | INT NOT NULL DEFAULT 1 | |
| PK | (issue_id, repo_id, path_hash) | |

Index `(repo_id, path_hash)` for "which issues touch X".

`issue_files` is added to `permanentIssueAuxTables`
(internal/storage/dolt/ephemeral_routing.go:15) and both table lists in
internal/storage/dolt/issues.go (669, 751) so delete/purge and wisp routing
treat it like the other per-issue tables. `code_nodes` and `code_edges` are
not per-issue and are excluded from those lists.

### Config keys (KV plane)

- `codemap.<repo_id>.last_sha` — HEAD at last successful scout.
- `codemap.<repo_id>.lang` — detected languages, comma separated.
- `codemap.summaries.model` — override for the summarizer model.
- `codemap.prime.max_lines` — cap for the prime section (default 20).

### Derived cache

`.beads/codemap.cache.json` (gitignored, written after every `Indexer.Apply`
and every summary batch, atomically via internal/atomicfile). Contents: for
every file node, `{path, summary, stale, layer, imports:[...], importers:[...],
open_issues:[{id,title,status}]}` plus `generated_at` and `head_sha`. The
PreToolUse hook reads only this file. It is a cache, never a source; `bd
codemap status` reports when it is older than `last_sha`.

## 6. Scout

Package `internal/codemap/scout`.

```go
type Node struct { Kind, Path, Name, PackagePath, Lang, BlobHash string; LOC, Exported int; IsTest bool }
type Edge struct { SrcPath, DstPath, Kind string; Weight int }   // paths, resolved to ids by the indexer
type Graph struct { Lang string; Nodes []Node; Edges []Edge }

type Scout interface {
    Name() string
    Detect(root string) bool                       // go.mod / Cargo.toml present
    Scan(root string, only []string) (Graph, error) // only: package dirs to rescan; nil = whole repo
}
```

`Detect` and `Scan` never write and never call the network. Every scout
excludes `vendor/`, `testdata/`, `node_modules/`, `.git/`, generated files
(`Code generated .* DO NOT EDIT`, `*.pb.go`), and anything matched by
`.beads/codemap-ignore` (gitignore syntax, optional).

**Go scout.** `go list -json -deps=false ./...` for packages and their
`Imports`; keeps only imports inside the module path (readGoModulePath and
filterGoGraphToModule move here from cmd/bd/arch_draft.go). Per file,
`go/parser` in `ImportsOnly` mode for file→package imports, then a second
`ParseFile` without bodies to count exported declarations; `_test.go` files
produce a `tests` edge to the package under test (external `_test` packages
included). Blob hashes come from one `git ls-files -s` call, not per-file
hashing. Incremental `only` re-lists just those package dirs.

**Rust scout.** `cargo metadata --format-version 1 --no-deps` for workspace
crates and their in-workspace dependencies (listRustWorkspaceMembers and
listRustInternalDeps move here). Per file, a line scanner for `mod x;`,
`pub mod`, `use crate::a::b`, `use super::`, and `use <workspace_crate>::`,
resolved against the crate's `src/` tree (`a/mod.rs` and `a.rs` both
accepted). Unresolved paths are dropped and counted in `Graph.Dropped`, which
`bd codemap status` shows; the scout never guesses.

**Python** stays in `bd arch draft` for now. Adding it later is a new file
implementing `Scout`; nothing else changes.

Detection runs every registered scout; a repo can be Go and Rust at once and
gets both graphs with `lang` on every node.

## 7. Roles

Three roles in leaf package `codemapops/` (repo root, beside `memoryops/`),
imports `beadserrors` and stdlib only. Doc comments are the specification the
conformance cases cite.

**`codemapops.Indexer`** (write)

```go
Apply(ctx, ApplyRequest{RepoID, HeadSHA string; Graph scout.Graph; Only []string}) (ApplyResult, error)
    // upserts nodes, replaces edges whose src is in the applied set,
    // deletes nodes no longer present (whole-repo) or in Only (incremental),
    // prunes orphan edges, records last_sha. One transaction.
SetSummaries(ctx, RepoID string, items []Summary) (int, error)
    // Summary{Path, Summary, Layer, Tags, BlobHash, Model}; refuses an item
    // whose BlobHash differs from the stored blob_hash (the file moved on).
```

**`codemapops.Reader`** (read)

```go
FileContext(ctx, RepoID, path string) (FileContext, error)   // node, imports, importers, tests, stale flag
PackageContext(ctx, RepoID, pkg string) (PackageContext, error)
Stale(ctx, RepoID string, limit int) ([]NodeRef, error)
Shape(ctx, RepoID string, opts ShapeOptions) (Shape, error) // layers→packages, top fan-in files, counts, last_sha
```

**`codemapops.IssueFiles`** (read and write; one caller is entitled to both,
the same way `memoryops.Memories` is)

```go
Record(ctx, RecordRequest{IssueID, RepoID string; Paths []string; Source, CommitSHA string}) (RecordResult, error)
ByIssue(ctx, issueID string) ([]IssueFile, error)
ByPath(ctx, RepoID, path string, openOnly bool) ([]IssueRef, error)
IssueCodeContext(ctx, issueID, repoID string) (IssueCodeContext, error)
    // ByIssue joined with Reader.FileContext per file and ByPath siblings;
    // the one shape show/claim/explain print
```

Why three and not one: an agent hook reading context must not be able to
write the map; the scout must not be able to bind issues to paths; and
issue↔path binding is a different question from the map (it survives a map
rebuild and cascades with the issue). `IssueCodeContext` lives on IssueFiles
because its identity is the issue.

Bodies are `…InTx` functions in `internal/storage/codemapops` taking the
`DBTX` interface, so dolt, embeddeddolt and the unit-of-work leg all reach one
body (the TreeWalker shape: three legs, one reading plus an engine check).
Pure functions beside them decide meaning without a database:
`StaleOf(node)`, `RankFanIn(edges)`, `BuildShape(nodes, edges, opts)`,
`MergeRecord(existing, incoming)`.

Per role: hook wrapper (`internal/storage/hook_codemap_*.go`, all recurse:
`internal/hooks` has no vocabulary for map or file events), telemetry wrapper,
dolt and embeddeddolt accessors, uow source interface, accessor on
`storage.Storage`, both decorator enumerations, layering pin, conformance
contract in `backend/conformance/codemap_*_contract.go` with three wirings, and
`.golangci.yml` entries. `Indexer.Apply` bulk-writes through one multi-row
INSERT … ON DUPLICATE KEY UPDATE per 500 rows.

## 8. Summarizer

Package `internal/codemap/summarize`. `callAgWithFallback` and `callAgOnce`
move from cmd/bd/arch_draft.go to `internal/agyclient` so both commands share
one process-spawn and fallback rule.

- Input: stale file nodes (from `Reader.Stale`) plus package context. Batches
  of 25 files; each file contributes its path, package, first 60 non-blank
  lines, exported names, importers count.
- Prompt asks for strict JSON: `[{path, summary ≤160 chars, layer ∈
  vocabulary, tags ≤5}]`. Vocabulary is fixed per repo: top-level directories
  plus `cli`, `storage`, `domain`, `integration`, `test`, `tooling`; it is
  written into config `codemap.<repo_id>.layers` on first build and editable.
- Every response item is validated (path in batch, layer in vocabulary,
  lengths) and written with `SetSummaries`; malformed items are dropped and
  reported, never retried blindly. One retry per batch on transport error.
- Never runs implicitly. `bd codemap refresh --summaries` and `bd codemap
  build --summaries` are the only entry points; both print the batch count
  before spending tokens and accept `--max-files`.

## 9. Linkage sources

1. **Hook-observed** (source `hook`): Claude Code PostToolUse for
   `Edit|Write|MultiEdit|NotebookEdit`. `bd codemap-hook post-tool` reads the
   hook JSON, takes `tool_input.file_path`, resolves the active issue as the
   last-touched ID (`GetLastTouchedID`, the file `SetLastTouchedID` already
   maintains) provided that issue is `in_progress`; otherwise it records
   nothing. Debounced with a per-session marker (agentHookMarkerPath) so one
   file is recorded once per session per issue.
2. **Commit** (source `commit`): a new managed git hook `post-commit` added to
   `managedHookNames` (cmd/bd/hooks.go:24) runs `bd codemap record-commit
   HEAD`, which extracts issue IDs from the message with the existing ID
   pattern, records `git show --name-only` paths on each, then runs the
   incremental scout for the changed package dirs. Whole hook bounded by
   `BD_CODEMAP_HOOK_TIMEOUT` (default 10 s), skipped by `BD_NO_CODEMAP=1` or
   `--no-hooks`; failure is a stderr warning, never a failed commit.
3. **Manual** (source `manual`): `bd codemap link <id> <path>...` and
   `bd update <id> --files a,b`.
4. **Branch** (source `branch`): `bd explain` and `IssueCodeContext` fold in
   `git diff --name-only <base>...HEAD` when the branch name contains the
   issue ID, as explain does today, but tagged so it is visibly inferred.

`bd explain` reads `IssueFiles.ByIssue` first and falls back to the commit
grep; graph enrichment comes from `Reader` when the repo is indexed and from
the JSON file otherwise, so external `/understand` users lose nothing.

## 10. Surfacing

**`bd show <id>`** gains a `CODE` section after dependents:

```
CODE  (map 2h old · 3 of 7 summaries stale · bd codemap refresh --summaries)
  ◑ cmd/bd/show.go            cli      Renders one issue with deps, comments, code context
      imports 6 · imported by 0 · tests: show_test.go
  ● internal/storage/codemapops/reader.go   storage   ⚠ stale summary
      imports 3 · imported by dolt/codemap.go, embeddeddolt/codemap.go, uow/codemap.go
  ALSO TOUCHING THESE FILES
    beads-20x.3  open  bd search: cover description/notes…   (show.go)
```

JSON output adds `code` (`IssueCodeContext`) to the show payload; the
`types.IssueDetails` struct is not changed.

**`bd update --claim`** prints the same section after the claim line (text
and JSON). `--quiet` suppresses it.

**`bd prime`** gains a `## Code map` section between memories and the command
reference, capped by `codemap.prime.max_lines`: freshness line, layers with
package counts, the ten highest fan-in files with one-line summaries, and two
instructions: "Run `bd show <id>` before exploring; run `bd codemap show
<path>` before opening a file you do not know." Absent when the repo is not
indexed, replaced by one line saying how to build it.

**PreToolUse hook** (`bd codemap-hook pre-tool`, matcher `Read|Edit|Write|
MultiEdit`): reads the cache, emits `additionalContext` of at most four lines
for the path; once per path per session via a marker; empty `{}` when the path
is unknown or the cache is missing. Installed by `bd setup claude` alongside
SessionStart; removed by the same migration sweep pattern.

**`bd codemap` command family**

| command | does |
|---|---|
| `build [--summaries]` | full scout, apply, cache; optional summaries |
| `refresh [--summaries] [--max-files N]` | incremental scout since last_sha; summaries for stale only |
| `status` | last_sha vs HEAD, node/edge counts, stale count, dropped edges, cache age |
| `show <path\|package>` | FileContext or PackageContext |
| `deps <path> [--reverse] [--depth N]` | import tree either direction |
| `stale [--limit]` | files whose summary is behind their blob |
| `link <id> <path>...` | manual issue↔file |
| `files <id>` | IssueFiles.ByIssue |
| `who <path>` | IssueFiles.ByPath, open by default |
| `export [--out]` | Understand-Anything knowledge-graph.json |
| `record-commit <sha>` | hook target |

Every command supports `--json`.

**HTTP** (written first in `internal/httpapi/spec/openapi.v0.yaml`, then
`make api-gen`): `GET /codemap/files/{path}`, `GET /codemap/shape`,
`GET /issues/{id}/files`, `POST /issues/{id}/files`.

## 11. Error handling

- Nil store accessor returns `*storage.ErrUnsupported` naming the op.
- Repo not indexed: reads return `codemapops.ErrNotIndexed` (typed, with
  RepoID); front doors print one hint line, never a stack.
- Scout tool missing (`go`, `cargo` not on PATH): `scout.ErrToolMissing`;
  `build` continues with the other language and reports it.
- Summarizer: transport errors retried once; validation failures dropped and
  counted; the command exits 0 with a report unless zero items succeeded.
- Hooks: any error is one stderr line and exit 0. A hook must never fail a
  commit or a tool call.
- Cache: written atomically; a corrupt cache is ignored by the hook and
  rewritten by the next apply.
- `Indexer.Apply` refuses a graph whose `RepoID` does not match the request
  and an incremental apply when `last_sha` is unset (must build first).

## 12. Testing

- **Scout**: fixture repos under `internal/codemap/scout/testdata/{go-mini,
  rust-mini}` with golden `Graph` JSON; tests for exclusions, generated-file
  skip, test edges, incremental `only`, unresolved Rust paths counted not
  guessed. Tool-missing path covered with PATH scrubbed.
- **Pure functions**: `StaleOf`, `RankFanIn`, `BuildShape`, `MergeRecord`,
  ID hashing, cache marshalling.
- **Contracts**: `backend/conformance/codemap_indexer_contract.go`,
  `codemap_reader_contract.go`, `issue_files_contract.go`, each case citing
  the leaf doc line; three wirings per contract; cascade on issue delete
  asserted; upsert idempotence asserted by applying the same graph twice.
- **Decorators**: both `role_accessor_decorator_test.go` files and the
  layering pin.
- **Commands**: embedded-store tests in the existing `*_embedded_test.go`
  pattern for build/refresh/show/link/who/record-commit; golden text for the
  `CODE` section and the prime section with caps.
- **Hooks**: stdin JSON fixtures for pre-tool and post-tool; marker
  debounce; missing cache; non-in-progress active issue records nothing.
- **Migration**: `scripts/check-migration-hygiene.sh` passes; parity tests
  in internal/storage/schema pick up 0067 automatically.
- **Perf gate**: `bd update --claim` on this repo under 2 s embedded; pre-tool
  hook under 50 ms on a 5 000-file cache (benchmark test, not CI-blocking).

## 13. Rollout

1. Schema and roles land first with no front door (contracts prove them).
2. Scout and `bd codemap build/status/show` land; dogfood on this repo.
3. Linkage (hooks, post-commit) and show/claim integration.
4. Prime section and PreToolUse hook.
5. Summarizer.
6. Export, explain integration, HTTP.

Each step is shippable on its own and gated by its tests. Nothing is enabled
implicitly: a repo without `bd codemap build` behaves exactly as today except
for one hint line in prime.

## 14. Non-goals

- Call graphs and symbol-level nodes (package/file only in v1).
- TypeScript and Python scouts (interface ready; not built).
- Diagram output (archify or otherwise). `export` feeds existing viewers.
- Architectural invariants; `bd arch` owns those.
- Auto-running the summarizer on any hook or sync.
- Cross-repo edges.
