package main

import (
	"context"
	"fmt"
	"io"
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

const legacyManagedPostCloseTier1Block = `# --- Tier 1: deterministic check (free, 0 tokens) ---
if [ -x ./scripts/arch-check.sh ]; then
  ./scripts/arch-check.sh || echo "⚠  see arch-check output above (advisory)" >&2
fi
`

const gatedManagedPostCloseTier1Block = `# --- Tier 1: deterministic check (explicit opt-in; runs repository code) ---
if [ "${BD_ARCH_CHECK:-0}" = "1" ] && [ -x ./scripts/arch-check.sh ]; then
  ./scripts/arch-check.sh || echo "⚠  see arch-check output above (advisory)" >&2
fi
`

func validatePostCloseHooksDir(hooksDir string) error {
	beadsDir := filepath.Dir(hooksDir)
	beadsInfo, err := os.Lstat(beadsDir)
	if err != nil {
		return err
	}
	if beadsInfo.Mode()&os.ModeSymlink != 0 || !beadsInfo.IsDir() {
		return fmt.Errorf("beads directory is not a real directory")
	}
	if beadsInfo.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("beads directory is writable by group or other")
	}

	dirInfo, err := os.Lstat(hooksDir)
	if err != nil {
		return err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return fmt.Errorf("hooks directory is not a real directory")
	}
	if dirInfo.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("hooks directory is writable by group or other")
	}
	return nil
}

func postCloseHookIsTracked(hookPath string) bool {
	beadsDir := filepath.Dir(filepath.Dir(hookPath))
	if filepath.Base(beadsDir) != ".beads" {
		return false
	}
	repoRoot := filepath.Dir(beadsDir)
	rel, err := filepath.Rel(repoRoot, hookPath)
	if err != nil {
		return true
	}
	cmd := exec.Command("git", "-C", repoRoot, "ls-files", "--error-unmatch", "--", rel)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	return cmd.Run() == nil
}

func readVerifiedRegularFile(path string, disallowedPerm os.FileMode) ([]byte, os.FileMode, error) {
	file, err := os.Open(path) // #nosec G304 -- descriptor is identity-checked against the non-symlink path before its bytes are consumed.
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
		return nil, 0, fmt.Errorf("not a regular file")
	}
	if info.Mode().Perm()&disallowedPerm != 0 {
		return nil, 0, fmt.Errorf("file has unsafe permissions")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, 0, err
	}
	return data, info.Mode().Perm(), nil
}

func replaceOwnerControlledRegularFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".post-close-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()

	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func loadTrustedPostCloseHook(hookPath string) ([]byte, os.FileMode, error) {
	if err := validatePostCloseHooksDir(filepath.Dir(hookPath)); err != nil {
		return nil, 0, err
	}
	if postCloseHookIsTracked(hookPath) {
		return nil, 0, fmt.Errorf("repository-tracked hooks are untrusted")
	}
	data, mode, err := readVerifiedRegularFile(hookPath, 0o022)
	if err != nil {
		return nil, 0, fmt.Errorf("hook is not an owner-controlled regular file: %w", err)
	}
	if strings.Contains(string(data), legacyManagedPostCloseTier1Block) {
		return nil, 0, fmt.Errorf("legacy generated hook executes repository code without BD_ARCH_CHECK=1; rerun bd arch init")
	}
	return data, mode, nil
}

func validatePostCloseHookPath(hookPath string) error {
	_, _, err := loadTrustedPostCloseHook(hookPath)
	return err
}

func snapshotPostCloseHook(hookPath string) (string, func(), error) {
	data, mode, err := loadTrustedPostCloseHook(hookPath)
	if err != nil {
		return "", nil, err
	}
	if mode&0o111 == 0 {
		return "", nil, fmt.Errorf("post-close hook exists but is not executable: %s", hookPath)
	}
	snapshotDir, err := os.MkdirTemp("", "bd-post-close-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(snapshotDir) }
	snapshot, err := os.CreateTemp(snapshotDir, "hook-")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	snapshotPath := snapshot.Name()
	if err := snapshot.Chmod(mode); err != nil {
		_ = snapshot.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := snapshot.Write(data); err != nil {
		_ = snapshot.Close()
		cleanup()
		return "", nil, err
	}
	if err := snapshot.Sync(); err != nil {
		_ = snapshot.Close()
		cleanup()
		return "", nil, err
	}
	if err := snapshot.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return snapshotPath, cleanup, nil
}

func upgradeLegacyPostCloseHook(hookPath string) error {
	if err := validatePostCloseHooksDir(filepath.Dir(hookPath)); err != nil {
		return err
	}
	if postCloseHookIsTracked(hookPath) {
		return fmt.Errorf("repository-tracked hooks are untrusted")
	}
	data, mode, err := readVerifiedRegularFile(hookPath, 0o022)
	if err != nil {
		return fmt.Errorf("hook is not an owner-controlled regular file: %w", err)
	}
	content := string(data)
	if !strings.Contains(content, legacyManagedPostCloseTier1Block) {
		return nil
	}
	updated := strings.Replace(content, legacyManagedPostCloseTier1Block, gatedManagedPostCloseTier1Block, 1)
	return replaceOwnerControlledRegularFile(hookPath, []byte(updated), mode)
}

func postCloseHookEnv(beadsDir string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
		"USER": true, "LOGNAME": true, "SHELL": true,
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true, "NO_COLOR": true,
		"BD_NO_DOCS": true, "BD_DOCS_RUNNING": true, "BD_ARCH_CHECK": true, "BD_ARCH_REVIEW": true,
	}
	env := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && allowed[name] {
			env = append(env, entry)
		}
	}
	if beadsDir != "" {
		env = append(env, "BEADS_DIR="+beadsDir)
	}
	return env
}

// runPostCloseHookAt executes the resolved post-close hook. Output goes to
// stderr; the exit code is logged but ignored (advisory contract).
func runPostCloseHookAt(ctx context.Context, hookPath string, closedIDs []string) {
	snapshotPath, cleanup, err := snapshotPostCloseHook(hookPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "beads: refusing unsafe post-close hook %s: %v\n", hookPath, err)
		return
	}
	defer cleanup()

	timeout := postCloseHookTimeoutValue()
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, snapshotPath, closedIDs...)
	beadsDir := filepath.Dir(filepath.Dir(hookPath))
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Stdout = os.Stderr // advisory output must never reach stdout (JSON consumers)
	cmd.Stderr = os.Stderr
	// Hooks receive only the environment needed by managed templates. Ambient
	// credential variables must not cross this execution boundary.
	cmd.Env = postCloseHookEnv(beadsDir)
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
		if _, err := os.Lstat(p); err == nil {
			if validationErr := validatePostCloseHookPath(p); validationErr != nil {
				debug.Logf("post-close: refusing unsafe store hook %s: %v\n", p, validationErr)
				return ""
			}
			return p
		} else if !os.IsNotExist(err) {
			debug.Logf("post-close: cannot inspect store hook %s: %v\n", p, err)
			return ""
		}
		if dirErr := validatePostCloseHooksDir(filepath.Dir(p)); dirErr != nil && !os.IsNotExist(dirErr) {
			debug.Logf("post-close: refusing unsafe store hooks directory %s: %v\n", filepath.Dir(p), dirErr)
			return ""
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
		if err := validatePostCloseHookPath(p); err == nil {
			return p
		}
	}
	return ""
}
