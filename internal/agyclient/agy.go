// Package agyclient runs one prompt through the `agy` CLI, with the retry and
// backup-command fallback every bd caller wants and none of them should own.
// It is the only place in bd that shells out to a model.
package agyclient

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/ui"
)

// DefaultModel is the label for the model agy answers with when it is not told
// otherwise. It is RECORDED (on a summary row, in a progress line), not passed:
// agy's --model flag is broken in --print mode, so bd never sends one. The name
// comes from bd's own note about agy's default, not from a model registry.
const DefaultModel = "gemini-3.1-pro"

// BackupCmdEnv names a shell command that reads a prompt on stdin and prints
// the answer. It is the escape hatch for swapping models, since --model cannot.
const BackupCmdEnv = "BD_ARCH_DRAFT_BACKUP_CMD"

// run is the exec seam: it takes the command already built, so every argv in
// this package stays a literal at its call site. Tests replace it.
var run = execRun

func execRun(cmd *exec.Cmd) (string, error) {
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

// Available reports whether agy is on PATH, so a caller can refuse before it
// prints "summarizing 400 files" and then fails on the first call.
func Available() error {
	_, err := exec.LookPath("agy")
	return err
}

// Call runs `agy --print <prompt>` (agy's default model — the --model flag is
// broken in print mode, so model is accepted for the record and not sent). On
// failure it retries once, then falls back to backup, a shell command reading
// the prompt on stdin; an empty backup falls back to BD_ARCH_DRAFT_BACKUP_CMD.
func Call(prompt, model, backup string) (string, error) {
	_ = model // ponytail: recorded by callers (summary_model), not passed — agy --model is broken in --print mode.
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
	// CLI here since agy's --model flag can't swap.
	if backup == "" {
		backup = os.Getenv(BackupCmdEnv)
	}
	if backup != "" {
		fmt.Fprintf(os.Stderr, "  %s trying backup command: %s\n", ui.RenderWarn("⚠"), backup)
		// #nosec G702,G204 -- the escape hatch IS a shell command, supplied by
		// the operator through BD_ARCH_DRAFT_BACKUP_CMD or by bd's own caller.
		cmd := exec.Command("sh", "-c", backup)
		cmd.Stdin = strings.NewReader(prompt)
		out, err := run(cmd)
		if err == nil {
			return out, nil
		}
		return "", fmt.Errorf("backup command also failed: %w", err)
	}
	return "", fmt.Errorf("agy synthesis failed and no %s set", BackupCmdEnv)
}

// callAgOnce passes the prompt as one argv, deliberately. `agy --help`
// documents stdin for ONE input format — "--input-format … stream-json reads
// one NDJSON message per line from stdin" — and describes --print/--prompt as
// taking the prompt as their value. Nothing there says the default text format
// reads stdin, so switching to it would be a guess about another tool's
// contract. The ceiling is ARG_MAX; the fix, if a prompt ever reaches it, is
// --input-format stream-json rather than bare stdin.
func callAgOnce(prompt string) (string, error) {
	return run(exec.Command("agy", "--print", prompt))
}
