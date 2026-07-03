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
	"github.com/steveyegge/beads/internal/storage"
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

// postCloseHookArgvChunk caps issue IDs per hook invocation so a large sweep
// batch (a long-offline machine syncing a big closed backlog) can't overflow the
// OS argv limit. Each chunk is an independent, timeout-bounded hook run.
const postCloseHookArgvChunk = 500

// postCloseHookTimeoutValue resolves the post-close hook timeout, honoring
// BD_CLOSE_HOOK_TIMEOUT (seconds). The reconciliation sweep reuses it to bound
// its query, so both share one knob.
func postCloseHookTimeoutValue() time.Duration {
	timeout := postCloseHookTimeout
	if envTimeout := os.Getenv("BD_CLOSE_HOOK_TIMEOUT"); envTimeout != "" {
		if secs, err := time.ParseDuration(envTimeout + "s"); err == nil && secs > 0 {
			timeout = secs
		}
	}
	return timeout
}

// firePostCloseHook is the immediate-fire path for the close/update/proxied
// commands: direct `bd close`, `bd update --status closed`, and proxied/server
// mode all route through it. It fires .beads/hooks/post-close for the issues that
// just transitioned to closed, enforcing the advisory contract in one place
// (opt-out via --no-hooks/BD_NO_CLOSE_HOOK, fire-after-commit, exit code
// logged-not-propagated).
//
// It is NOT the only close path: the many sibling CloseIssue callers (epic,
// batch, gate, duplicates, human, todo, mol_squash, ado) close issues without
// routing through here — those are covered by the reconciliation sweep
// (sweepMissedCloses), which fires the hook for any close this machine did not.
//
// A handled ID is appended to the fired-ledger (recordPostCloseFired) on two
// outcomes — the hook fired, or it was explicitly opted out
// (--no-hooks/BD_NO_CLOSE_HOOK) — so the sweep treats them as done and never
// re-fires. That append is what makes a direct `bd close` safe from a redundant
// re-fire on the next `bd ready`. It is deliberately NOT written when no hook
// resolves: a close made while the hook was absent is a genuine miss the sweep
// must fire once the hook is installed.
//
// s is the store that performed the close; the hook is resolved from that
// store's workspace so a routed close (beads.role=contributor lands the close in
// a different workspace than cwd) still finds the right hook. Pass nil to
// resolve from cwd only — proxied server mode holds a UOW, not a store.
//
// Advisory only — by contract it cannot affect the close outcome. The closed
// issue IDs are passed as positional args; the hook may call `bd show <id>` for
// richer detail (title, type, labels) to decide whether the close is
// architectural.
func firePostCloseHook(ctx context.Context, s storage.DoltStorage, closedIDs []string) {
	if len(closedIDs) == 0 {
		return
	}

	// Opt-out: env var (also set from the --no-hooks flag by callers). Still
	// ledger these — they are handled on this machine (explicitly opted out), so
	// the sweep must not re-fire them later when the env var is gone.
	if os.Getenv("BD_NO_CLOSE_HOOK") == "1" {
		recordPostCloseFired(s, closedIDs)
		return
	}

	hookPath := resolvePostCloseHookPathForStore(s)
	if hookPath == "" {
		// No hook to run: do NOT ledger. If a hook is installed later, this close
		// is a genuine miss the reconciliation sweep must fire.
		debug.Logf("post-close: no .beads/hooks/post-close resolved for %s — skipping\n", strings.Join(closedIDs, " "))
		return
	}

	for i := 0; i < len(closedIDs); i += postCloseHookArgvChunk {
		end := i + postCloseHookArgvChunk
		if end > len(closedIDs) {
			end = len(closedIDs)
		}
		runPostCloseHookAt(ctx, hookPath, closedIDs[i:end])
	}
	recordPostCloseFired(s, closedIDs)
}

// runPostCloseHookAt executes the resolved post-close hook. Output goes to
// stderr; the exit code is logged but ignored (advisory contract).
func runPostCloseHookAt(ctx context.Context, hookPath string, closedIDs []string) {
	info, err := os.Stat(hookPath)
	if err != nil || info.IsDir() {
		return
	}
	if info.Mode().Perm()&0111 == 0 {
		fmt.Fprintf(os.Stderr, "beads: post-close hook exists but is not executable: %s\n", hookPath)
		return
	}

	timeout := postCloseHookTimeoutValue()

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

// resolvePostCloseHookPathForStore prefers the workspace of the store that
// performed the close (routing can land the close in a different .beads than
// cwd), falling back to cwd resolution when the store's workspace has no hook or
// the store can't report its path (nil store, shared-server mode with empty
// Path()).
func resolvePostCloseHookPathForStore(s storage.DoltStorage) string {
	if bd := storeBeadsDir(s); bd != "" {
		p := filepath.Join(bd, "hooks", postCloseHookName)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return resolvePostCloseHookPath()
}

// storeBeadsDir returns the .beads directory backing a store, or "" if it can't
// be determined. Both store engines set Path() to <beadsDir>/<engine>
// (.beads/dolt or .beads/embeddeddolt), so the parent is the .beads dir.
func storeBeadsDir(s storage.DoltStorage) string {
	if s == nil {
		return ""
	}
	locator, ok := storage.UnwrapStore(s).(storage.StoreLocator)
	if !ok {
		return ""
	}
	p := strings.TrimSpace(locator.Path())
	if p == "" {
		return ""
	}
	return filepath.Dir(p)
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
