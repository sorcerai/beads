# ARCH.md — Construction blueprint

> **For builder agents.** Short by design: negatives (forbidden edges/patterns)
> don't rot; positive specs do. Inject into agent context at session start. A
> change that breaks one of these is DRIFT, not a fix — stop and update this
> file (with a reason + beads issue) first.

## What this is

A local-first state tracking and issue-management system backed by a Dolt
database. Hexagonal architecture: `internal/types` is the pure core, concrete
adapters (storage engines, tracker providers) sit behind ports, and `cmd/bd`
is the wiring entrypoint.

## Negative invariants (forbidden — breaking one is drift, not a fix)

1. **`internal/types` must not depend on any other internal package.** — STRUCTURAL
   It is the innermost ring; adding an internal dep here inverts the architecture.
2. **`internal/storage` must not depend on concrete database engine
   implementations** (`internal/storage/dolt`, `internal/storage/embeddeddolt`).
   — STRUCTURAL. Storage is a port; engines are adaptors behind it.
3. **`internal/tracker` must not depend on specific provider implementations**
   (`internal/github`, `internal/jira`, `internal/linear`). — STRUCTURAL.
   Same port/adaptor rule; providers are interchangeable adaptors.
4. **Packages within `internal/` must not depend on application entrypoints in
   `cmd/`.** — STRUCTURAL. `cmd/bd` is a leaf; libraries never reach up into it.
5. **Concrete tracker provider implementations** (`internal/github`,
   `internal/linear`) **must not depend on one another.** — STRUCTURAL.
   Providers are independent; one should not import another.
6. **Low-level foundational utilities** (`internal/debug`, `internal/idgen`,
   `internal/git`) **must not depend on higher-level domain packages.** — STRUCTURAL.
7. **The root package (`github.com/steveyegge/beads`) must not be imported by any
   internal package** to prevent cyclical dependency chains. — STRUCTURAL.
8. **The `cmd/bd` package must act only as a dependency-injection wiring layer
   and must not contain business logic.** — SEMANTIC. Logic belongs in
   `internal/`; `cmd/bd` composes it.

## When this file is wrong

If a task genuinely requires breaking an invariant, update THIS file first (with
a beads issue + reason), then change the code. A silent violation is the exact
late-caught drift this file exists to prevent.
