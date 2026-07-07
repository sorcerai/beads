#!/usr/bin/env python3
"""Deterministic architecture-drift analyzer for ARCH.md (free, 0 tokens).

Reads a Go import graph on stdin (one package per line: the import path
followed by its space-separated DIRECT production imports) and the module path
from $MODULE, then enforces the STRUCTURAL negative invariants declared in
ARCH.md. Exits 1 if any invariant is violated, 0 if clean.

The graph is produced by scripts/arch-check.sh via:
    go list -e -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' ./...

Test imports are intentionally excluded (.Imports, not .TestImports): test
files may reach anywhere; the architecture rules constrain production deps.

SEMANTIC invariants (ARCH.md #8: "cmd/bd is wiring only, no business logic")
are NOT checked here — they need judgment, which is the opt-in Tier 2 LLM
review in the post-close hook. This gate covers the 7 structural rules a graph
can decide on its own.

Completeness note: for the "no package P may import X" rules (#1, #4, #6, #7) a
direct-edge scan is complete — any transitive path P->...->X begins with a
direct edge that is itself a violation, so scanning direct edges over the whole
candidate set catches every transitive case. The port/provider isolation rules
(#2, #3, #5) need transitive reachability (a port can legally import an
intermediary that then reaches the forbidden engine/provider), so those use BFS.
"""

import os
import sys
from collections import deque

MODULE = os.environ.get("MODULE", "").strip()
if not MODULE:
    sys.stderr.write("arch_check: $MODULE is empty — cannot resolve module-local packages\n")
    sys.exit(2)

INTERNAL = MODULE + "/internal"
CMD = MODULE + "/cmd"


def is_local(p):
    return p == MODULE or p.startswith(MODULE + "/")


def is_internal(p):
    return p == INTERNAL or p.startswith(INTERNAL + "/")


def under(p, prefix):
    """True if p is `prefix` itself or a subpackage of it."""
    return p == prefix or p.startswith(prefix + "/")


# --- Build the module-local direct-import graph from stdin ---
adj = {}
for line in sys.stdin:
    parts = line.split()
    if not parts:
        continue
    pkg = parts[0]
    imports = [i for i in parts[1:] if is_local(i)]
    adj.setdefault(pkg, set()).update(imports)
# Make sure every referenced node is a key so BFS never KeyErrors.
for src in list(adj):
    for dst in adj[src]:
        adj.setdefault(dst, set())

if not adj:
    sys.stderr.write(
        "arch_check: empty import graph on stdin — is Go installed and the module valid?\n"
    )
    sys.exit(2)


def find_path(src, pred):
    """Shortest path [src, ..., node] over local edges where pred(node) is true
    and node != src, or None. BFS, deterministic (sorted neighbors)."""
    if src not in adj:
        return None
    seen = {src}
    queue = deque([(src, [src])])
    while queue:
        node, path = queue.popleft()
        for nxt in sorted(adj.get(node, ())):
            if nxt in seen:
                continue
            seen.add(nxt)
            npath = path + [nxt]
            if pred(nxt):
                return npath
            queue.append((nxt, npath))
    return None


# --- Invariant targets (paths confirmed against the real tree) ---
TYPES = INTERNAL + "/types"
STORAGE = INTERNAL + "/storage"
TRACKER = INTERNAL + "/tracker"
ENGINES = [STORAGE + "/dolt", STORAGE + "/embeddeddolt"]
PROVIDERS = {  # name -> package-prefix
    "github": INTERNAL + "/github",
    "jira": INTERNAL + "/jira",
    "linear": INTERNAL + "/linear",
}
FOUNDATIONAL = [INTERNAL + "/debug", INTERNAL + "/idgen", INTERNAL + "/git"]

all_internal = sorted(p for p in adj if is_internal(p))

violations = []  # (inv_number, description, path-or-edge list)


def short(p):
    if p == MODULE:
        return "(root)"
    return p[len(MODULE) + 1:] if p.startswith(MODULE + "/") else p


def report(inv, desc, path):
    violations.append((inv, desc, [short(n) for n in path]))


# #1 — internal/types must not depend on any other internal package. (direct = complete)
for imp in sorted(adj.get(TYPES, ())):
    if is_internal(imp) and imp != TYPES:
        report(1, "internal/types must not depend on any other internal package", [TYPES, imp])

# #2 — internal/storage (port) must not depend on a concrete engine. (transitive)
p = find_path(STORAGE, lambda n: any(under(n, e) for e in ENGINES))
if p:
    report(2, "internal/storage (port) must not depend on a concrete engine (dolt/embeddeddolt)", p)

# #3 — internal/tracker (port) must not depend on a concrete provider. (transitive)
p = find_path(TRACKER, lambda n: any(under(n, pre) for pre in PROVIDERS.values()))
if p:
    report(3, "internal/tracker (port) must not depend on a concrete provider (github/jira/linear)", p)

# #4 — no internal package may depend on a cmd/* entrypoint. (direct = complete)
for src in all_internal:
    for imp in sorted(adj.get(src, ())):
        if under(imp, CMD):
            report(4, "internal packages must not depend on cmd/* entrypoints", [src, imp])

# #5 — concrete providers must not depend on one another. (transitive)
seen_pairs = set()
for src in all_internal:
    home = next((pre for pre in PROVIDERS.values() if under(src, pre)), None)
    if home is None:
        continue
    p = find_path(src, lambda n, home=home: any(under(n, pre) for pre in PROVIDERS.values() if pre != home))
    if p:
        pair = (short(home), short(p[-1]).split("/")[0] + "/" + short(p[-1]).split("/")[1])
        if pair not in seen_pairs:
            seen_pairs.add(pair)
            report(5, "concrete tracker providers must not depend on one another", p)

# #6 — foundational utils must not depend on higher-level domain packages. (direct = complete)
# Deterministic reading: a foundational util may depend on other foundational
# utils, but on no other internal package.
foundational_set = set(FOUNDATIONAL)
for src in FOUNDATIONAL:
    for imp in sorted(adj.get(src, ())):
        if is_internal(imp) and imp not in foundational_set:
            report(6, "foundational utils (debug/idgen/git) must not depend on higher-level packages", [src, imp])

# #7 — the root package must not be imported by any internal package. (direct = complete)
for src in all_internal:
    if MODULE in adj.get(src, ()):
        report(7, "the root package must not be imported by any internal package", [src, MODULE])


# --- Report ---
scanned = len(all_internal)
if not violations:
    print(f"✓ arch-check: all 7 structural invariants hold ({scanned} internal packages scanned)")
    print("ℹ #8 (cmd/bd is wiring-only) is SEMANTIC — not checked here; that is the opt-in Tier 2 review.")
    sys.exit(0)

# One line per violation, prefixed "violation:" — the machine-readable format
# the baseline mechanism (scripts/arch-check.sh + bd arch check) diffs against
# .beads/arch-baseline. Keep it deterministic and stable: sorted, ASCII arrow.
print(f"✗ arch-check: {len(violations)} architecture-drift violation(s) — see ARCH.md")
for inv, desc, path in sorted(violations, key=lambda v: (v[0], v[2])):
    print(f"violation: [#{inv}] {desc}: {' -> '.join(path)}")
print()
print("A change that breaks an invariant is DRIFT, not a fix: update ARCH.md first")
print("(with a beads issue + reason), or revert the offending dependency edge.")
sys.exit(1)
