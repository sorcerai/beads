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
#
# Baseline (opt-in, for legacy repos): the checker emits one machine-readable
# "violation: ..." line per finding. Run with --update-baseline to grandfather
# the current set into .beads/arch-baseline (commit it); after that, only
# violations NOT in the baseline fail, with one grandfathered-count summary
# line. No baseline file = every violation fails (default behavior).
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

OUT=$(mktemp) || exit 1
CUR=$(mktemp) || exit 1
BASE=$(mktemp) || exit 1
trap 'rm -f "$OUT" "$CUR" "$BASE"' EXIT

# One pass over the module: each package + its DIRECT production imports.
# -e keeps going if a package has a build error instead of aborting the gate.
# POSIX sh has no pipefail; that's fine — the checker itself exits 2 on an
# empty/broken graph, so the pipeline status is still the meaningful one.
STATUS=0
go list -e -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' ./... \
  | MODULE="$MODULE" python3 "$ROOT/scripts/arch_check.py" > "$OUT" || STATUS=$?
cat "$OUT"

# Canonical machine-readable violation set (sorted, unique) for baseline diffs.
grep '^violation:' "$OUT" | sort -u > "$CUR" || true

BASELINE=.beads/arch-baseline

if [ "${1:-}" = "--update-baseline" ]; then
  if [ "$STATUS" -ne 0 ] && [ ! -s "$CUR" ]; then
    echo "✗ arch-check: checker failed without machine-readable violations — not writing baseline" >&2
    exit "$STATUS"
  fi
  mkdir -p "$(dirname "$BASELINE")"
  {
    echo "# arch-check baseline — grandfathered violations (see scripts/arch-check.sh)."
    echo "# New violations not listed here fail the gate. Regenerate with:"
    echo "#   bd arch check --update-baseline"
    cat "$CUR"
  } > "$BASELINE"
  echo "✓ arch-check: baseline updated ($(wc -l < "$CUR" | tr -d ' ') violation(s) grandfathered in $BASELINE)"
  exit 0
fi

if [ -f "$BASELINE" ]; then
  if [ "$STATUS" -ne 0 ] && [ ! -s "$CUR" ]; then
    exit "$STATUS"  # checker error, not violations — the baseline can't grandfather that
  fi
  grep '^violation:' "$BASELINE" | sort -u > "$BASE" || true
  NEW=$(comm -13 "$BASE" "$CUR")
  GRAND=$(comm -12 "$BASE" "$CUR" | wc -l | tr -d ' ')
  if [ -n "$NEW" ]; then
    echo "✗ arch-check: new violation(s) not in baseline ($GRAND grandfathered):"
    printf '%s\n' "$NEW" | sed 's/^/  /'
    exit 1
  fi
  echo "✓ arch-check: no new violations ($GRAND grandfathered in baseline)"
  exit 0
fi

exit "$STATUS"
