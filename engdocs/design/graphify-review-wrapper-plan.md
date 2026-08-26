# Graphify Review Wrapper Implementation Plan

**Goal:** Add a local, manually refreshed, advisory Graphify code graph with no repository artifacts or CI integration.

**Architecture:** `scripts/graphify` is a Python 3 standard-library wrapper around exactly Graphify `0.9.27`. It keeps a per-repository cache outside the checkout, fingerprints all visible non-ignored source, atomically publishes immutable graph generations, and rejects all stale/missing reads. `scripts/graphify_test.go` drives a fake Graphify binary from temporary Git repositories.

## Constraints

- Cache: `${XDG_CACHE_HOME:-~/.cache}/omp/graphify/<sha256(canonical-repo-path)>`; mode `0700`; never under the repo.
- Require exact `graphify 0.9.27`; no installation or network access.
- Reject symlinks for cache, staging, generation, metadata, and graph output paths.
- Fingerprint `git ls-files -z --cached --others --exclude-standard` entries and bytes in deterministic order; Git errors fail closed.
- `refresh` is explicit. No hooks, watchers, CI, source writes, or committed artifacts.
- Preserve a prior generation if refresh fails; clean invocation-owned staging, temporary metadata, and lock paths on every exit path.
- `query` and `affected` are advisory and run only against a current managed graph.

## Task 1 — Contract and harness

**Files:** create `scripts/graphify`; create `scripts/graphify_test.go`.

1. Add Go helpers that create `t.TempDir()` Git worktrees, install a fake `graphify` on `PATH`, and run `scripts/graphify` with an isolated `XDG_CACHE_HOME`.
2. Add failing tests for missing Graphify, wrong version, invalid CLI shape, and `status` returning `{"state":"missing",...}`.
3. Implement Python command parsing, exact-version validation via `graphify --version`, and stable exit code `2` for usage/configuration errors.
4. Verify: `go test ./scripts -run '^TestGraphify' -count=1`.

## Task 2 — Safe cache and freshness

**Files:** modify `scripts/graphify`; modify `scripts/graphify_test.go`.

1. Add failing tests for canonical-path cache hashing, external cache containment, `0700` mode, and every managed-path symlink rejection.
2. Add current→stale tests for tracked and untracked non-ignored source mutations; verify ignored-file changes do not stale a graph.
3. Implement canonical containment, safe directory creation, deterministic file enumeration/fingerprinting, and validated metadata `{repository,fingerprint,generation}`.
4. Verify: `go test ./scripts -run '^TestGraphify(Cache|Status|RejectsSymlink)' -count=1`.

## Task 3 — Atomic refresh and cleanup

**Files:** modify `scripts/graphify`; modify `scripts/graphify_test.go`.

1. Add failing fake-Graphify scenarios: success, non-zero exit, no graph, symlink graph, and source mutation during extraction.
2. Implement an exclusive per-cache lock; one cleanup handler registered through `atexit` and `SIGINT`/`SIGTERM`; staging directly inside the managed cache; exact `graphify extract <repo> --code-only --no-cluster --out <staging>` invocation.
3. Require a regular `graphify-out/graph.json`; compare pre/post fingerprints; atomically rename generation and then metadata; retain the previous generation until after successful publication.
4. Assert no temporary paths remain after each outcome and verify: `go test ./scripts -run '^TestGraphifyRefresh' -count=1`.

## Task 4 — Guarded advisory reads

**Files:** modify `scripts/graphify`; modify `scripts/graphify_test.go`; modify `engdocs/design/graphify-review-wrapper.md`.

1. Add failing tests that missing/stale/malformed state refuses reads with the exact `./scripts/graphify refresh` hint, that `--graph` overrides are rejected, and that valid reads pass only the managed graph path to Graphify.
2. Implement positional-only `query <term>` and `affected <path>` delegation after status validation; preserve delegated output and exit status.
3. Add copyable usage examples and advisory-evidence wording to the design.
4. Verify: `go test ./scripts -run '^TestGraphify' -count=1`; `python3 scripts/graphify status`; inspect a failed-refresh cache fixture to confirm cleanup and prior-generation preservation.
