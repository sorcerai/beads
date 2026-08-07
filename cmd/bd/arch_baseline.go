package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Diff-scoped baseline for the arch-check gate (qb7.6).
//
// Without a baseline, one violation anywhere fails the gate — on a legacy repo
// the first run floods and the gate gets disabled (alert fatigue). The baseline
// grandfathers the existing violations: `bd arch check --update-baseline`
// writes them to .beads/arch-baseline (committed, sorted, stable format), and
// subsequent runs fail ONLY on violations not in the baseline. No baseline
// file = current behavior (all violations fail), so it is opt-in.
//
// Protocol: a "violation" is any output line from scripts/arch-check.sh that
// starts with "violation:". Everything else is human decoration. The dogfooded
// scripts/arch_check.py emits one such line per finding, deterministic and
// sorted, so the baseline diffs cleanly in git.

// violationPrefix marks machine-readable violation lines in arch-check output.
const violationPrefix = "violation:"

// archBaselineRelPath is where the baseline lives, relative to the repo root.
// The shell-script variant of the gate reads the same file.
const archBaselineRelPath = ".beads/arch-baseline"

// extractViolations pulls canonical violation lines out of arch-check output.
// Returns them sorted and deduplicated (stable baseline order).
func extractViolations(output string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, violationPrefix) || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// readBaseline reads the baseline file: one violation per line, '#' comments
// and blank lines ignored. Returns sorted unique lines. A missing file returns
// the os.ReadFile error (callers use os.IsNotExist to mean "no baseline").
func readBaseline(path string) ([]string, error) {
	// #nosec G304 -- path is .beads/arch-baseline, constructed by us.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sort.Strings(out)
	return out, nil
}

// writeBaseline writes the baseline file deterministically (header comment,
// sorted, deduplicated, trailing newline) so it commits and diffs cleanly.
func writeBaseline(path string, violations []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	sorted := append([]string(nil), violations...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("# arch-check baseline — grandfathered violations (see scripts/arch-check.sh).\n")
	b.WriteString("# New violations not listed here fail the gate. Regenerate with:\n")
	b.WriteString("#   bd arch check --update-baseline\n")
	prev := ""
	for _, v := range sorted {
		if v == "" || v == prev {
			continue
		}
		prev = v
		b.WriteString(v)
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// diffBaseline splits current violations into new ones (not grandfathered)
// and the count of grandfathered ones.
func diffBaseline(current, baseline []string) (newViolations []string, grandfathered int) {
	base := map[string]bool{}
	for _, b := range baseline {
		base[b] = true
	}
	for _, c := range current {
		if base[c] {
			grandfathered++
		} else {
			newViolations = append(newViolations, c)
		}
	}
	return newViolations, grandfathered
}

// gateCrashed reports whether a non-nil arch-check error is a genuine crash
// rather than the conventional "violations found" exit.
//
// Exit-code convention: exit 1 means the gate ran fine and FOUND violations —
// grandfatherable via the baseline. Any OTHER non-zero exit (or a failure to
// run the script at all) is a crash the baseline cannot grandfather, so it must
// fail the gate regardless of what violation lines were captured. The bundled
// scripts/arch-check.sh follows this (arch_check.py exits 1 on violations, 2 on
// error; the wrapper propagates both).
func gateCrashed(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode() != 1
	}
	return true // couldn't even run the script — treat as a crash
}

// archGateFailed decides whether the gate fails when a baseline is present: new
// (non-grandfathered) violations fail, and so does a crash (see gateCrashed) —
// even if every captured violation was grandfathered. Grandfathering only
// forgives the conventional exit-1 "violations found" path.
func archGateFailed(newViolations []string, gateErr error) bool {
	return len(newViolations) > 0 || gateCrashed(gateErr)
}

// runScriptCapture executes a script in dir, streaming stdout/stderr to the
// parent while capturing stdout for violation extraction.
func runScriptCapture(scriptPath, dir string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(rootCtx, scriptPath)
	cmd.Dir = dir
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return buf.String(), err
}

// archBaselinePath resolves the baseline location for a repo root.
func archBaselinePath(repoRoot string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(archBaselineRelPath))
}

// summarizeGrandfathered renders the single baseline summary line.
func summarizeGrandfathered(grandfathered int) string {
	return fmt.Sprintf("%d grandfathered violation(s) in %s", grandfathered, archBaselineRelPath)
}
