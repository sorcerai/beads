#!/usr/bin/env sh
# scripts/arch-check.sh — deterministic architecture-drift gate (free, 0 tokens).
#
# Enforces the STRUCTURAL negative invariants in ARCH.md by analyzing the Go
# import graph. Exits non-zero on any violation, 0 when clean.
#
# Two entry points, same check:
#   - Tier 1 of .beads/hooks/post-close: runs automatically after `bd close`
#     (advisory there — the hook cannot undo a close; a finding is just surfaced).
#   - CI / pre-push / on demand: run `./scripts/arch-check.sh` directly; a
#     non-zero exit fails the job.
#
# SEMANTIC invariants (ARCH.md #8 — "cmd/bd is wiring only, no business logic")
# are NOT decided here; they are the opt-in Tier 2 LLM review (BD_ARCH_REVIEW=1).
set -eu

# Resolve from the repo root so ./... and ARCH.md work regardless of caller cwd.
ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$ROOT"

# Toolchain presence: in the post-close hook context, missing tools must NOT
# block a close, so degrade to an advisory skip (exit 0) rather than failing.
if ! command -v go >/dev/null 2>&1; then
  echo "⚠  arch-check: 'go' not found — skipping structural gate" >&2
  exit 0
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "⚠  arch-check: 'python3' not found — skipping structural gate" >&2
  exit 0
fi

MODULE=$(go list -m 2>/dev/null || echo "github.com/steveyegge/beads")

# One pass over the module: each package + its DIRECT production imports.
# -e keeps going if a package has a build error instead of aborting the gate.
go list -e -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' ./... \
  | MODULE="$MODULE" python3 "$ROOT/scripts/arch_check.py"
