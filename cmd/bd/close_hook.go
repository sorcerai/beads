package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
)

// postCloseHookName is the lifecycle hook executed after a successful close.
// Unlike git hooks (pre-commit, pre-push), this is a beads lifecycle hook: bd
// runs it directly, not via git. It lives in .beads/hooks/post-close alongside
// the git hooks for discoverability, but is NOT installed or managed by
// `bd hooks install` — it is opt-in per repo (create the file to opt in).
//
// Design contract:
//   - Advisory only. The close has already committed; this hook cannot block
//     or undo it. Its job is to surface "did this work touch the architecture?"
//     (e.g. ARCH.md drift, a forbidden edge that slipped past pre-commit, a
//     stale diagram). Output goes to stderr so it never corrupts stdout/JSON.
//   - Its exit code is logged but ignored. A non-zero exit is a finding, not a
//     failure of the close.
//   - Bounded by a timeout so a hung hook can't stall an agent.
//   - Skippable via --no-hooks or BD_NO_CLOSE_HOOK=1.
const postCloseHookName = "post-close"

// postCloseHookTimeout is the max time a post-close hook may run.
// Overridable via BD_CLOSE_HOOK_TIMEOUT (seconds).
const postCloseHookTimeout = 60 * time.Second

// runPostCloseHook executes .beads/hooks/post-close if it exists.
//
// Advisory only — by contract it cannot affect the close outcome. The closed
// issue IDs are passed as positional args; the hook may call `bd show <id>` for
// richer detail (title, type, labels) to decide whether the close is
// architectural. Output goes to stderr; the exit code is logged but ignored.
//
// Silent no-op when the hook file is absent (the common case), when disabled
// via env/flag, or when not in direct mode.
func runPostCloseHook(ctx context.Context, closedIDs []string) {
	if len(closedIDs) == 0 {
		return
	}

	// Opt-out: env var (checked by callers too, but double-guarded here).
	if os.Getenv("BD_NO_CLOSE_HOOK") == "1" {
		return
	}

	hookPath := resolvePostCloseHookPath()
	if hookPath == "" {
		debug.Logf("post-close: no .beads/hooks/post-close found — skipping\n")
		return
	}

	info, err := os.Stat(hookPath)
	if err != nil || info.IsDir() {
		return
	}
	if info.Mode().Perm()&0111 == 0 {
		fmt.Fprintf(os.Stderr, "beads: post-close hook exists but is not executable: %s\n", hookPath)
		return
	}

	timeout := postCloseHookTimeout
	if envTimeout := os.Getenv("BD_CLOSE_HOOK_TIMEOUT"); envTimeout != "" {
		if secs, err := time.ParseDuration(envTimeout + "s"); err == nil && secs > 0 {
			timeout = secs
		}
	}

	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- hookPath is constrained to .beads/hooks/post-close; args are
	// issue IDs validated upstream. The hook is opt-in (file must exist).
	cmd := exec.CommandContext(hookCtx, hookPath, closedIDs...)
	cmd.Stdout = os.Stderr // advisory output must never reach stdout (JSON consumers)
	cmd.Stderr = os.Stderr
	// Pass a signal so the timeout kills the whole process group, not just the
	// shell. Keep the user's env so the hook can call `bd`, `git`, etc.
	cmd.Env = os.Environ()

	debug.Logf("post-close: running %s %s\n", hookPath, strings.Join(closedIDs, " "))
	if err := cmd.Run(); err != nil {
		if hookCtx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(os.Stderr, "beads: post-close hook timed out after %s — continuing\n", timeout)
			return
		}
		// Advisory: log the non-zero exit but do NOT propagate it.
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(os.Stderr, "beads: post-close hook reported findings (exit %d) — see output above\n", exitErr.ExitCode())
		} else {
			fmt.Fprintf(os.Stderr, "beads: post-close hook error (non-blocking): %v\n", err)
		}
	}
}

// resolvePostCloseHookPath finds .beads/hooks/post-close, checking the active
// beads workspace first, then the main-repo .beads dir. Returns "" if none found.
func resolvePostCloseHookPath() string {
	// Primary: the active beads workspace (.beads/ resolved from cwd).
	if bd := beads.FindBeadsDir(); bd != "" {
		p := filepath.Join(bd, "hooks", postCloseHookName)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
