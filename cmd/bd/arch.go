package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/ui"
)

// archCmd is the parent for construction-blueprint (ARCH.md) commands.
// "Construction blueprint" = a short file of negative invariants (forbidden
// edges/patterns) that keeps builder agents aligned to intended architecture,
// the way a contractor stays on a blueprint. See ARCH.md philosophy: negatives
// don't rot; positives do.
var archCmd = &cobra.Command{
	Use:     "arch",
	GroupID: "setup",
	Short:   "Manage the construction-blueprint (ARCH.md) + drift gate",
	Long: `Manage the project's construction blueprint: a short ARCH.md of negative
invariants (forbidden edges/patterns) plus an optional deterministic gate that
enforces them.

A blueprint you only read rots in days; a blueprint that is checked lives.
Negatives ("X must not depend on Y") stay stable for years; positive specs
("the system works like X") rot fast. This command scaffolds the negative-first
shape and wires the drift checkpoint.

Subcommands:
  init    Scaffold ARCH.md + the post-close drift hook (idempotent)
  check   Run the deterministic gate (scripts/arch-check.sh) if present`,
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

// archInitCmd scaffolds ARCH.md (if absent) and the post-close hook (if absent).
// Idempotent: never overwrites a hand-curated ARCH.md.
var archInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold ARCH.md + the post-close drift hook",
	Long: `Scaffold a construction-blueprint ARCH.md and wire the post-close drift hook.

Creates ARCH.md from a negative-invariant template if none exists, and installs
.beads/hooks/post-close (the advisory checkpoint wired to 'bd close') if absent.
Never overwrites an existing ARCH.md — it is hand-curated truth.

The scaffolded ARCH.md is a STRUCTURE, not content: it encodes the philosophy
(negatives > positives; a checkable negative beats a readable positive) and
leaves the actual invariants for the human/agent to fill in from the real
architecture. Filling it in is the work; this just makes the empty form visible.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		force, _ := cmd.Flags().GetBool("force")
		repoRoot := findRepoRootForArch()
		if repoRoot == "" {
			FatalErrorRespectJSON("not in a git repository (ARCH.md needs a repo root)")
		}

		createdArch := false
		archPath := filepath.Join(repoRoot, "ARCH.md")
		if _, err := os.Stat(archPath); err == nil {
			if force {
				fmt.Fprintf(os.Stderr, "%s ARCH.md exists — leaving it (use --force is a no-op; ARCH.md is never overwritten)\n", ui.RenderWarn("⚠"))
			} else {
				fmt.Fprintf(os.Stderr, "%s ARCH.md already exists — leaving it untouched\n", ui.RenderPass("✓"))
			}
		} else {
			if err := os.WriteFile(archPath, []byte(archMdTemplate), 0o644); err != nil {
				FatalErrorRespectJSON("writing ARCH.md: %v", err)
			}
			createdArch = true
			fmt.Printf("%s Created ARCH.md (construction blueprint stub)\n", ui.RenderPass("✓"))
		}

		// Seed the post-close hook alongside ARCH.md so the checkpoint is wired
		// from day one. Idempotent: skip if it already exists.
		hookInstalled := seedPostCloseHook(repoRoot)
		if hookInstalled {
			fmt.Printf("%s Installed .beads/hooks/post-close (drift checkpoint)\n", ui.RenderPass("✓"))
		}

		if createdArch {
			fmt.Printf("\nNext: fill in ARCH.md with your project's NEGATIVE invariants (forbidden\n")
			fmt.Printf("edges/patterns). Negatives don't rot; positives do. Aim for 5-10 lines that\n")
			fmt.Printf("name what must NOT happen, plus a 2-line positive anchor of what the system is.\n")
			fmt.Printf("Then add a deterministic check (scripts/arch-check.sh) for the structural ones.\n")
		}
	},
}

// archCheckCmd runs the deterministic gate if present.
var archCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Run the deterministic architecture gate",
	Long: `Run the deterministic architecture gate (scripts/arch-check.sh) if it exists.

This is the free (0-token) tier of the drift checkpoint: it checks structural
invariants against the code graph. The hard-block version runs in pre-commit;
this is the on-demand / post-close version. Exits non-zero on a violation.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		repoRoot := findRepoRootForArch()
		if repoRoot == "" {
			FatalErrorRespectJSON("not in a git repository")
		}
		checkPath := filepath.Join(repoRoot, "scripts", "arch-check.sh")
		if _, err := os.Stat(checkPath); err != nil {
			fmt.Fprintf(os.Stderr, "No scripts/arch-check.sh — nothing to check deterministically.\n")
			fmt.Fprintf(os.Stderr, "Create one (see 'bd arch init') or rely on the LLM post-close tier.\n")
			return
		}
		if err := runScriptInDir(checkPath, repoRoot); err != nil {
			FatalErrorRespectJSON("%v", err)
		}
	},
}

// findRepoRootForArch resolves the git repo root, falling back to cwd.
func findRepoRootForArch() string {
	// Prefer git toplevel so ARCH.md lands at the repo root, not in .beads/.
	if out, err := runGitOutput("rev-parse", "--show-toplevel"); err == nil {
		return strings.TrimSpace(out)
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

// runGitOutput runs a git command and returns its stdout (errors returned).
func runGitOutput(args ...string) (string, error) {
	cmd := exec.CommandContext(rootCtx, "git", args...)
	out, err := cmd.Output()
	return string(out), err
}

// runScriptInDir executes a script in dir, streaming stdout/stderr to the parent.
// Returns an error if it exits non-zero. Used by 'bd arch check'.
func runScriptInDir(scriptPath, dir string) error {
	cmd := exec.CommandContext(rootCtx, scriptPath)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// seedPostCloseHook installs .beads/hooks/post-close if absent. Idempotent.
// Returns true if it created the hook.
func seedPostCloseHook(repoRoot string) bool {
	bd := beads.FindBeadsDir()
	hooksDir := filepath.Join(bd, "hooks")
	if bd == "" {
		hooksDir = filepath.Join(repoRoot, ".beads", "hooks")
	}
	hookPath := filepath.Join(hooksDir, "post-close")
	if _, err := os.Stat(hookPath); err == nil {
		return false // already exists — never overwrite
	}
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not create hooks dir %s: %v\n", hooksDir, err)
		return false
	}
	if err := os.WriteFile(hookPath, []byte(postCloseHookTemplate), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write post-close hook: %v\n", err)
		return false
	}
	return true
}

func init() {
	archInitCmd.Flags().Bool("force", false, "Reserved (ARCH.md is never overwritten)")
	archCmd.AddCommand(archInitCmd)
	archCmd.AddCommand(archCheckCmd)
	rootCmd.AddCommand(archCmd)
}

// scaffoldArchBlueprint is the inline entry point used by 'bd init': it creates
// ARCH.md + the post-close hook if absent. Idempotent, never overwrites, and
// quiet on the no-op paths so it doesn't clutter init output. It reuses the
// archInitCmd logic but is callable directly without dispatching a subprocess.
func scaffoldArchBlueprint() {
	repoRoot := findRepoRootForArch()
	if repoRoot == "" {
		return // not a git repo — nothing to scaffold into
	}
	createdArch := false
	archPath := filepath.Join(repoRoot, "ARCH.md")
	if _, err := os.Stat(archPath); err != nil {
		if err := os.WriteFile(archPath, []byte(archMdTemplate), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write ARCH.md: %v\n", err)
			return
		}
		createdArch = true
	}
	hookInstalled := seedPostCloseHook(repoRoot)

	// Emit output only when we actually created something (avoid init noise on
	// repos that already have the blueprint).
	if createdArch || hookInstalled {
		fmt.Printf("%s Construction blueprint:\n", ui.RenderPass("✓"))
		if createdArch {
			fmt.Printf("  - ARCH.md scaffolded (fill in negative invariants; see 'bd arch init')\n")
		}
		if hookInstalled {
			fmt.Printf("  - .beads/hooks/post-close drift checkpoint installed\n")
		}
	}
}

// archMdTemplate is the scaffolded ARCH.md. Negative-first by design: it
// teaches the philosophy and leaves the invariants blank for the human/agent.
const archMdTemplate = `# ARCH.md — Construction blueprint

> **For builder agents.** Short by design: negatives (forbidden edges/patterns)
> don't rot; positive specs do. Inject into agent context at session start. A
> change that breaks one of these is DRIFT, not a fix — stop and update this
> file (with a reason + beads issue) first.

## What this is (2-line positive anchor)

<!-- TODO: one or two lines on what this system IS. e.g. "A daemon that owns X
     over LAN, turns events into Y, and does Z — no cloud in the runtime path." -->

## Negative invariants (forbidden — breaking one is drift, not a fix)

<!-- TODO: 5-10 lines. Phrase each as a NEGATIVE ("X must not...") and make it
     machine-checkable where possible (a forbidden edge, a single-ownership rule,
     a layering constraint). Examples:
       1. <core module> depends on NO other internal module (innermost ring).
       2. Nothing depends on the <daemon/binary> crate (it's a leaf).
       3. <cheap thing> runs BEFORE <expensive thing> (never call the expensive one raw).
       4. Only <one owner> may touch <shared resource> (single-writer rule).
     Negatives stay true for years; that's why this file is negatives, not a
     full architecture description. -->

## When this file is wrong

If a task genuinely requires breaking an invariant, update THIS file first (with
a beads issue + reason), then change the code. A silent violation is the exact
late-caught drift this file exists to prevent.
`

// postCloseHookTemplate is the seeded .beads/hooks/post-close. Advisory only:
// it cannot block or undo a close. Tier 1 = free deterministic check (if
// scripts/arch-check.sh exists); Tier 2 = opt-in fresh-context LLM review.
const postCloseHookTemplate = `#!/usr/bin/env sh
# .beads/hooks/post-close — architecture-drift checkpoint (advisory).
# Fires after 'bd close' succeeds. CANNOT block or undo the close.
# Scaffolded by 'bd arch init' — edit freely. Output to stderr only.
#
# No ARCH.md yet? Run 'bd arch init' to scaffold the construction blueprint.
# Args: $@ = closed issue IDs. cwd = repo root.

set -euo pipefail

# --- nudge: no ARCH.md means no blueprint to check against ---
if [ ! -f ARCH.md ]; then
  echo "ℹ  no ARCH.md — run 'bd arch init' to scaffold construction guardrails." >&2
fi

# --- Tier 1: deterministic check (free, 0 tokens) ---
if [ -x ./scripts/arch-check.sh ]; then
  ./scripts/arch-check.sh || echo "⚠  see arch-check output above (advisory)" >&2
fi

# --- Tier 2 (opt-in): fresh-context LLM review of the diff vs ARCH.md ---
# Enable by setting BD_ARCH_REVIEW=1. Uses a FRESH pi process so the reviewer
# has zero investment in justifying the builder's work. Costs tokens + ~30s.
if [ "${BD_ARCH_REVIEW:-0}" = "1" ] && command -v pi >/dev/null 2>&1; then
  DIFF_FILE=$(mktemp /tmp/arch-diff.XXXXXX)
  trap 'rm -f "$DIFF_FILE"' EXIT
  git diff HEAD~1 HEAD 2>/dev/null | head -400 > "$DIFF_FILE" || true
  if [ -s "$DIFF_FILE" ]; then
    for id in "$@"; do
      pi -p --approve --no-prompt-templates "You are a FRESH-CONTEXT architecture reviewer with no investment in this work. Read ARCH.md. Review the git diff below + closed issue $id. Does the diff violate any invariant (cite the #), make ARCH.md stale, or look like drift? If fine, reply NO DRIFT only.
$(cat "$DIFF_FILE")" 2>&1 | sed 's/^/  arch-review> /' >&2 || true
    done
  fi
fi

exit 0
`
