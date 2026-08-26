---
title: Graphify Review Wrapper Design
status: proposed
---

## Purpose

Give every Beads contributor a reproducible, local code-relationship view during review without making Graphify a source of truth, a CI gate, or part of Beads/Dolt synchronization.

## Scope

The repository will add `scripts/graphify`, a standalone wrapper around exactly Graphify `0.9.27`.

Included commands:

```text
./scripts/graphify status
./scripts/graphify refresh
./scripts/graphify query <symbol-or-text>
./scripts/graphify affected <path>
```

Out of scope:

- P2P Dolt replication, federation, peer discovery, dashboard topology, and authorization.
- `bd` subcommands or changes to the Beads issue dependency graph.
- Git hooks, watchers, background processes, network calls, CI jobs, source mutation, and committed graph artifacts.
- Claims that Graphify proves a file is dead, a refactor is safe, or a historical decision exists.

## Contract

The wrapper invokes only the installed `graphify` binary. It requires the exact semantic version `0.9.27`; a missing or incompatible binary fails with an explicit remediation message.

The cache root is:

```text
${XDG_CACHE_HOME:-~/.cache}/omp/graphify/<sha256(canonical-repo-path)>
```

The wrapper resolves the repository path canonically before deriving the cache key. It rejects a cache path, generation directory, active-generation parent, or output directory that is a symlink. Created cache directories use mode `0700`; the cache must be outside the repository.

A generation is bound to an exact visible-worktree fingerprint. The fingerprint includes Git-tracked files plus untracked, non-ignored files and their contents; it excludes `.git` and the external cache. Git enumeration/access failures are errors, not fallbacks. A source edit makes a graph `stale`.

`refresh` takes an exclusive cache lock, builds into a fresh staging directory with:

```text
graphify extract <repo> --code-only --no-cluster --out <staging>
```

It validates the expected regular `graph.json`, verifies that the pre- and post-build fingerprints match, then atomically publishes one immutable generation and metadata. A failed refresh leaves the previous generation intact. All staging directories, temporary metadata files, lock artifacts, and failed-build output are removed through a single cleanup path on success, failure, interruption, and signal termination.

`status` reports `missing`, `stale`, or `current` as machine-readable JSON. `query` and `affected` refuse `missing` or `stale` graphs and print the exact `refresh` command. No command automatically refreshes.

## Contributor use

```sh
./scripts/graphify status
./scripts/graphify refresh
./scripts/graphify query "resolveConfig"
./scripts/graphify affected internal/storage/dolt/store.go
```

Refresh only after `status` reports `missing` or `stale`. `query` and `affected`
only read the current managed generation; they never accept a caller-supplied
graph path or refresh automatically.

## Trust boundary

Graphify output is advisory. Reviewers must corroborate relationship, impact, stale-code, or deletion conclusions with source inspection and applicable LSP references, registrations/configuration, tests, and runtime evidence. Output labels must distinguish `current-graph` from unresolved conclusions.

## Error handling

- Do not create cache state before validating canonical containment and symlink safety.
- Do not publish partial graphs or metadata.
- Do not remove a previously published valid generation after a refresh failure.
- Preserve the original failure exit status after cleanup.
- Emit no graph result on missing, malformed, stale, or incompatible state.

## Tests

Go tests use `t.TempDir()` and a fake Graphify executable to cover exact-version enforcement; external cache containment; cache, ancestor, metadata, generation, and output symlink rejection; tracked/untracked/ignored freshness; atomic publication; failed-refresh preservation and cleanup; malformed metadata traversal; stale/missing read refusal; and safe current-graph query/affected delegation.

## Acceptance

A contributor with Graphify `0.9.27` can explicitly refresh and query a current local graph. A contributor without it receives a deterministic remediation message. No Graphify artifact, cache entry, hook, background process, CI dependency, or change to Beads/Dolt state is created inside the repository.
