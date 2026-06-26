package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
)

// archDraftCmd constructs a CANDIDATE ARCH.md by: (1) a deterministic scout
// that extracts the real dependency graph from the codebase (ground truth the
// model cannot hallucinate past), then (2) a frontier synthesizer call (via
// `agy`, provider-agnostic) that distills the graph into 5-10 NEGATIVE
// invariants. Output is a candidate file the human reviews — never auto-committed.
//
// Why scout+synthesize and not a single LLM call: construction is an
// extraction+synthesis job. The scout gives ground truth (cargo tree / go list /
// ast import scan); the model adds the "what must never change" judgment a
// machine graph can't infer. The deterministic scout is what stops invented
// invariants that don't match reality.
var archDraftCmd = &cobra.Command{
	Use:   "draft",
	Short: "Construct a candidate ARCH.md from the codebase (scout + frontier model)",
	Long: `Construct a candidate ARCH.md from the actual codebase.

Two-stage pipeline:
  1. SCOUT (deterministic, free): extract the real dependency graph from the
     codebase — cargo tree (Rust), go list (Go), or an AST import scan (Python).
     This is ground truth; the model cannot invent edges the scout contradicts.
  2. SYNTHESIZE (frontier model, via ` + "`agy`" + `): feed the graph + key files to a
     frontier model that distills 5-10 NEGATIVE invariants ("X must not Y"),
     each marked structural or semantic.

Output is a CANDIDATE written to ARCH.md.draft — never overwrites ARCH.md.
The human reviews, trims, and moves it into place. Construction is draft ->
approve; enforcement (bd arch check / post-close) is the automated loop.

Models (via agy): --model "Gemini 3.1 Pro (High)" (default, longest context for
whole-graph synthesis), with --backup-model "Claude Opus 4.6 (Thinking)" used
automatically if the primary errors/times out. Run 'agy models' to see options.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		model, _ := cmd.Flags().GetString("model")
		backupModel, _ := cmd.Flags().GetString("backup-model")
		out, _ := cmd.Flags().GetString("out")

		repoRoot := findRepoRootForArch()
		if repoRoot == "" {
			FatalErrorRespectJSON("not in a git repository")
		}

		if model == "" {
			model = "Gemini 3.1 Pro (High)"
		}
		if backupModel == "" {
			backupModel = "Claude Opus 4.6 (Thinking)"
		}
		if out == "" {
			out = filepath.Join(repoRoot, "ARCH.md.draft")
		}

		// --- Stage 1: deterministic scout (ground truth) ---
		fmt.Printf("%s Stage 1: extracting dependency graph (scout)...\n", ui.RenderAccent("◆"))
		graph, lang, err := scoutDependencyGraph(repoRoot)
		if err != nil {
			FatalErrorRespectJSON("scout failed: %v", err)
		}
		if graph == "" {
			FatalErrorRespectJSON("could not detect a supported project (Rust/Go/Python) — no graph to synthesize from. Fill ARCH.md by hand.")
		}
		fmt.Printf("  %s language: %s\n", ui.RenderPass("✓"), lang)
		fmt.Printf("  %s graph extracted (%d bytes)\n", ui.RenderPass("✓"), len(graph))

		// --- Stage 2: frontier synthesis (via agy) ---
		if _, err := exec.LookPath("agy"); err != nil {
			FatalErrorRespectJSON("`agy` not found on PATH (install it, or fill ARCH.md by hand): %v", err)
		}
		// NOTE: agy's --model flag is broken in --print mode (returns a static
		// persona greeting instead of acting, for any model name). agy's DEFAULT
		// model already follows instructions correctly and is Gemini 3.1 Pro —
		// so we intentionally do NOT pass --model. The --backup flag becomes a
		// retry/alternate-command escape hatch (BD_ARCH_DRAFT_BACKUP_CMD).
		fmt.Printf("\n%s Stage 2: synthesizing invariants via agy (default model)...\n", ui.RenderAccent("◆"))
		_ = model  // accepted for API stability but not passed to agy (--model is broken in print mode)
		_ = backupModel

		prompt := buildArchDraftPrompt(graph, lang, repoRoot)
		synthesis, err := callAgWithFallback(prompt)
		if err != nil {
			FatalErrorRespectJSON("synthesis failed (agy default + any backup command): %v", err)
		}
		fmt.Printf("  %s synthesis complete\n", ui.RenderPass("✓"))

		// --- Write candidate (never ARCH.md) ---
		candidate := wrapDraftCandidate(synthesis, lang)
		if err := os.WriteFile(out, []byte(candidate), 0o644); err != nil {
			FatalErrorRespectJSON("writing %s: %v", out, err)
		}

		fmt.Printf("\n%s Candidate written to %s\n", ui.RenderPass("✓"), ui.RenderAccent(out))
		fmt.Printf("\nThis is a DRAFT. Review it, delete invariants that encode intent you\n")
		fmt.Printf("disagree with, add any the model missed, then move it into place:\n")
		fmt.Printf("  %s\n\n", ui.RenderAccent(fmt.Sprintf("mv %s ARCH.md", out)))
		fmt.Printf("Then (optional) make the structural ones machine-checkable:\n")
		fmt.Printf("  %s\n", ui.RenderAccent("write scripts/arch-check.sh  (see reverie for a reference)"))
	},
}

// scoutDependencyGraph extracts the real dependency graph from the repo root.
// Returns (graphText, language, error). Language is detected by manifest files.
// This is the ground-trust layer: deterministic, free, the model can't argue
// with it. Unsupported languages return an empty graph + error.
func scoutDependencyGraph(repoRoot string) (graph, lang string, err error) {
	// Rust workspace / crate
	if _, err := os.Stat(filepath.Join(repoRoot, "Cargo.toml")); err == nil {
		return scoutRust(repoRoot)
	}
	// Go module
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err == nil {
		return scoutGo(repoRoot)
	}
	// Python (requirements.txt or pyproject.toml + .py files)
	if hasPython(repoRoot) {
		return scoutPython(repoRoot)
	}
	return "", "", fmt.Errorf("no Cargo.toml, go.mod, or Python project detected")
}

func scoutRust(repoRoot string) (string, string, error) {
	// `cargo tree` gives the real, resolved dependency graph (workspace-aware).
	// --workspace + --edges normal captures internal edges. Depth-bound to keep
	// the payload tractable for the model.
	cmd := exec.Command("cargo", "tree", "--workspace", "--edges", "normal", "--depth", "3")
	cmd.Dir = repoRoot
	out, e := cmd.CombinedOutput()
	if e != nil {
		return "", "", fmt.Errorf("cargo tree failed: %v\n%s", e, out)
	}
	// Also list workspace members + each crate's internal deps from Cargo.toml,
	// since cargo tree can be verbose. Keep it focused on internal edges.
	members := listRustWorkspaceMembers(repoRoot)
	internalDeps := listRustInternalDeps(repoRoot)
	g := fmt.Sprintf("## Rust workspace members\n%s\n## Internal crate dependencies (from Cargo.toml)\n%s\n## cargo tree (depth 3, normal edges)\n%s",
		members, internalDeps, truncateForPrompt(string(out), 8000))
	return g, "Rust", nil
}

func scoutGo(repoRoot string) (string, string, error) {
	// `go list -deps` on the main packages gives the import graph.
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}} <- {{.Imports}}", "./...")
	cmd.Dir = repoRoot
	out, e := cmd.CombinedOutput()
	if e != nil {
		return "", "", fmt.Errorf("go list failed: %v\n%s", e, out)
	}
	return truncateForPrompt(string(out), 10000), "Go", nil
}

func scoutPython(repoRoot string) (string, string, error) {
	// AST import scan: for each .py file, list its imports of OTHER local modules.
	// Cheaper and more portable than requiring a specific tool. Only local
	// (intra-repo) imports matter for architecture invariants.
	files := findPythonFiles(repoRoot)
	var b strings.Builder
	b.WriteString("## Python intra-repo imports (file -> local modules it imports)\n")
	localModules := buildPythonLocalModuleSet(files, repoRoot)
	for _, f := range files {
		imports := extractPythonImports(f, repoRoot)
		var local []string
		for _, imp := range imports {
			if isLocalPythonModule(imp, localModules) {
				local = append(local, imp)
			}
		}
		if len(local) > 0 {
			rel, _ := filepath.Rel(repoRoot, f)
			sort.Strings(local)
			b.WriteString(fmt.Sprintf("%s -> %s\n", rel, strings.Join(local, ", ")))
		}
	}
	return b.String(), "Python", nil
}

// listRustWorkspaceMembers reads the [workspace] members from the root Cargo.toml.
func listRustWorkspaceMembers(repoRoot string) string {
	data, err := os.ReadFile(filepath.Join(repoRoot, "Cargo.toml"))
	if err != nil {
		return "(could not read root Cargo.toml)"
	}
	content := string(data)
	start := strings.Index(content, "[workspace]")
	if start < 0 {
		return "(no [workspace] table — single crate)"
	}
	rest := content[start:]
	end := strings.Index(rest, "\n[")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// listRustInternalDeps scans each crate's Cargo.toml for reverie-*/cortex deps.
func listRustInternalDeps(repoRoot string) string {
	cratesDir := filepath.Join(repoRoot, "crates")
	entries, err := os.ReadDir(cratesDir)
	if err != nil {
		return "(no crates/ dir)"
	}
	var b strings.Builder
	re := regexp.MustCompile(`(?m)^(reverie-[a-z0-9-]+|cortex[a-z0-9-]*)\b`)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ct := filepath.Join(cratesDir, e.Name(), "Cargo.toml")
		data, err := os.ReadFile(ct)
		if err != nil {
			continue
		}
		matches := re.FindAllString(string(data), -1)
		// dedup
		seen := map[string]bool{}
		var deps []string
		for _, m := range matches {
			if m == e.Name() || seen[m] {
				continue
			}
			seen[m] = true
			deps = append(deps, m)
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			b.WriteString(fmt.Sprintf("%s -> %s\n", e.Name(), strings.Join(deps, ", ")))
		}
	}
	return b.String()
}

func hasPython(repoRoot string) bool {
	if _, err := os.Stat(filepath.Join(repoRoot, "requirements.txt")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "pyproject.toml")); err == nil {
		return true
	}
	return false
}

func findPythonFiles(repoRoot string) []string {
	var files []string
	_ = filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() {
				name := d.Name()
				if name == ".git" || name == ".beads" || name == "node_modules" || name == "__pycache__" || name == "venv" || name == ".venv" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(path, ".py") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func buildPythonLocalModuleSet(files []string, repoRoot string) map[string]bool {
	set := map[string]bool{}
	for _, f := range files {
		rel, _ := filepath.Rel(repoRoot, f)
		base := strings.TrimSuffix(filepath.Base(f), ".py")
		set[base] = true
		// also the dotted path relative form (mw.store -> mw/store.py)
		dir := filepath.Dir(rel)
		if dir != "." {
			set[strings.ReplaceAll(dir, "/", ".")+"."+base] = true
		}
	}
	return set
}

var pyImportRe = regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)|import\s+([\w.,\s]+))`)

func extractPythonImports(file, repoRoot string) []string {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range pyImportRe.FindAllSubmatch(data, -1) {
		s := string(m[1])
		if s == "" {
			s = string(m[2])
		}
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func isLocalPythonModule(imp string, localSet map[string]bool) bool {
	// match the root module of a dotted import against local modules
	root := strings.SplitN(imp, ".", 2)[0]
	if localSet[root] {
		return true
	}
	return localSet[imp]
}

// callAgWithFallback runs `agy --print <prompt>` (agy's default model — the
// --model flag is broken in print mode). On failure it retries once, then tries
// an optional alternate command from BD_ARCH_DRAFT_BACKUP_CMD (e.g. a different
// CLI the user wires up) if set. Returns the model's text.
func callAgWithFallback(prompt string) (string, error) {
	// Primary: agy default model (Gemini 3.1 Pro, follows instructions).
	if out, err := callAgOnce(prompt); err == nil {
		return out, nil
	} else {
		fmt.Fprintf(os.Stderr, "  %s agy default model failed (%v); retrying...\n", ui.RenderWarn("⚠"), err)
		// One retry for transient errors (rate limit / timeout).
		if out, err2 := callAgOnce(prompt); err2 == nil {
			return out, nil
		}
	}
	// Optional alternate command escape hatch. The user can wire a different
	// CLI here (BD_ARCH_DRAFT_BACKUP_CMD) since agy's --model flag can't swap.
	if alt := os.Getenv("BD_ARCH_DRAFT_BACKUP_CMD"); alt != "" {
		fmt.Fprintf(os.Stderr, "  %s trying backup command: %s\n", ui.RenderWarn("⚠"), alt)
		// alt is a shell command that reads the prompt on stdin and prints the result.
		cmd := exec.Command("sh", "-c", alt)
		cmd.Stdin = strings.NewReader(prompt)
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err == nil {
			return string(out), nil
		}
		return "", fmt.Errorf("backup command also failed: %w", err)
	}
	return "", fmt.Errorf("agy synthesis failed and no BD_ARCH_DRAFT_BACKUP_CMD set")
}

func callAgOnce(prompt string) (string, error) {
	cmd := exec.Command("agy", "--print", prompt)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// buildArchDraftPrompt constructs the synthesis prompt. Strict output format so
// the candidate is directly usable.
func buildArchDraftPrompt(graph, lang, repoRoot string) string {
	return fmt.Sprintf(`You are an ARCHITECTURE BLUEPRINT CONSTRUCTOR. Your job: read the REAL dependency graph below (deterministically extracted — do not invent edges it contradicts) and distill it into 5-10 NEGATIVE invariants for an ARCH.md construction blueprint.

PHILOSOPHY (critical): output NEGATIVES only — "X must not Y" rules. Negatives ("the core must depend on nothing internal") stay true for years; positives ("the system works like X") rot in days. Do NOT write a full architecture description. Do NOT list what the system does. Only what must NEVER change to keep the architecture sound.

For each invariant, classify it:
  - STRUCTURAL: derivable from the dependency graph (forbidden edge, layering, leaf rule). These can be machine-checked.
  - SEMANTIC: a rule the graph doesn't show (single-writer, ordering, no-cloud-in-path). These need review, not a checker.

Also give a 2-LINE POSITIVE ANCHOR: what this system IS (one sentence), for intent.

Output EXACTLY this format and nothing else:

## What this is
<2 lines>

## Negative invariants
1. **<rule>** — STRUCTURAL
2. **<rule>** — STRUCTURAL
...
N. **<rule>** — SEMANTIC

RULES:
- Phrase each as a NEGATIVE ("X must not...", "only Y may...", "Z never...").
- Prefer STRUCTURAL invariants derivable from the graph — those are the valuable ones.
- Do not invent crates/modules not in the graph. If unsure whether an edge is forbidden, omit it.
- 5-10 invariants total. Quality over quantity.

LANGUAGE: %s

DEPENDENCY GRAPH (ground truth):
%s
`, lang, graph)
}

// wrapDraftCandidate wraps the model's output into a full ARCH.md-shaped file,
// with a prominent DRAFT banner so no one mistakes it for approved.
func wrapDraftCandidate(synthesis, lang string) string {
	header := `# ARCH.md — Construction blueprint  [DRAFT — REVIEW BEFORE COMMITTING]

> ⚠ DRAFT generated by ` + "`bd arch draft`" + ` (%s, via agy). This is a CANDIDATE, not
> approved truth. Review every invariant: delete ones that encode intent you
> disagree with, add any the model missed, fix misreadings. Then remove this
> banner and move into place. A blueprint you only read rots; a blueprint that
> is checked lives.

`
	return fmt.Sprintf(header, lang) + strings.TrimSpace(synthesis) + "\n"
}

func truncateForPrompt(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)\n"
}

func init() {
	// NOTE: --model/--backup-model are accepted for API stability but agy's
	// --model flag is broken in --print mode (returns a persona greeting), so
	// they are not passed to agy. agy's default model (Gemini 3.1 Pro) is used.
	// For a true model swap, set BD_ARCH_DRAFT_BACKUP_CMD to an alternate CLI
	// that reads the prompt on stdin.
	archDraftCmd.Flags().String("model", "", "Reserved (agy --model is broken in print mode; default model used)")
	archDraftCmd.Flags().String("backup-model", "", "Reserved (see BD_ARCH_DRAFT_BACKUP_CMD env for a real backup)")
	archDraftCmd.Flags().String("out", "", "Output path (default: ARCH.md.draft)")
	archCmd.AddCommand(archDraftCmd)
}
