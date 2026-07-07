//go:build cgo

// close_hook_paths_test.go — regression tests for post-close hook exhaustiveness
// (beads-qb7.1). The post-close lifecycle hook must fire for EVERY path that
// transitions an issue to closed, not just direct-mode `bd close`:
//
//	Bug: `bd update <id> --status closed` did not fire .beads/hooks/post-close.
//	Bug: proxied/server-mode `bd close` did not fire .beads/hooks/post-close.
//	Bug: scaffolded hook template used `set -euo pipefail` under a sh shebang,
//	     which kills the hook at startup on strict POSIX sh (dash on Linux).
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installMarkerHook writes a post-close hook that appends its args to marker.
func installMarkerHook(t *testing.T, beadsDir, marker string) string {
	t.Helper()
	hookDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookPath := filepath.Join(hookDir, "post-close")
	hookContent := "#!/usr/bin/env sh\necho \"$@\" >> " + marker + "\n"
	if err := os.WriteFile(hookPath, []byte(hookContent), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	return hookPath
}

// TestPostCloseHookFiresOnUpdateStatusClosed verifies that
// `bd update <id> --status closed` fires the post-close lifecycle hook,
// exactly as `bd close <id>` does. Both are the same state transition.
func TestPostCloseHookFiresOnUpdateStatusClosed(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "uh")

	issue := bdCreate(t, bd, dir, "Update-close hook test", "--type", "task")

	marker := filepath.Join(dir, ".post-close-marker-update")
	os.Remove(marker)
	hookPath := installMarkerHook(t, beadsDir, marker)
	defer os.Remove(hookPath)

	cmd := exec.Command(bd, "update", issue.ID, "--status", "closed")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd update --status closed failed: %v\n%s", err, out)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("post-close hook did not fire on `bd update --status closed` (marker not created): %v", err)
	}
	if !strings.Contains(string(data), issue.ID) {
		t.Errorf("expected hook to receive %q, got %q", issue.ID, strings.TrimSpace(string(data)))
	}

	// Re-closing an already-closed issue must NOT re-fire the hook
	// (transition-edge semantics, not level semantics).
	os.Remove(marker)
	cmd = exec.Command(bd, "update", issue.ID, "--status", "closed")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	_, _ = cmd.CombinedOutput() // outcome irrelevant; only hook firing matters
	if _, err := os.Stat(marker); err == nil {
		t.Error("hook re-fired on close→close no-op transition; must only fire on an actual transition")
	}
}

// TestPostCloseHookFiresForAutoClosedMolecule verifies gap 3 (beads-qb7.1):
// when closing the final step auto-closes the parent molecule, the auto-closed
// parent ID reaches the post-close hook too — not only the explicitly closed
// step IDs. A template-labeled epic auto-closes once all its steps complete.
func TestPostCloseHookFiresForAutoClosedMolecule(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "am")

	root := bdCreate(t, bd, dir, "Molecule root", "-t", "epic", "--labels", "template")
	s1 := bdCreate(t, bd, dir, "Step 1", "--parent", root.ID)
	s2 := bdCreate(t, bd, dir, "Step 2", "--parent", root.ID)

	marker := filepath.Join(dir, ".post-close-marker-molecule")
	os.Remove(marker)
	hookPath := installMarkerHook(t, beadsDir, marker)
	defer os.Remove(hookPath)

	// Closing the final step auto-closes the template-labeled root.
	bdClose(t, bd, dir, s1.ID, s2.ID)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("post-close hook did not fire (marker not created): %v", err)
	}
	got := string(data)
	if !strings.Contains(got, root.ID) {
		t.Errorf("auto-closed molecule root %q missing from post-close hook args; got %q", root.ID, strings.TrimSpace(got))
	}
	for _, step := range []string{s1.ID, s2.ID} {
		if !strings.Contains(got, step) {
			t.Errorf("closed step %q missing from post-close hook args; got %q", step, strings.TrimSpace(got))
		}
	}
}

// TestPostCloseHookFiresOnProxiedClose verifies that proxied/server-mode
// `bd close` fires the post-close lifecycle hook, closing the gap where
// runCloseProxiedServer never invoked it (beads-qb7.1). Uses the proxied
// integration harness (bdProxiedInitWithHooks / bdProxiedClose).
func TestPostCloseHookFiresOnProxiedClose(t *testing.T) {
	requireProxiedServerEnv(t)
	if runtime.GOOS == "windows" {
		t.Skip("hook script form is POSIX shell")
	}
	bd := buildEmbeddedBD(t)

	marker := filepath.Join(t.TempDir(), "post-close-marker-proxied")
	script := "#!/usr/bin/env sh\necho \"$@\" >> " + shellQuote(marker) + "\n"
	p := bdProxiedInitWithHooks(t, bd, "pcx", map[string]string{"post-close": script})

	issue := bdProxiedCreate(t, bd, p.dir, "Proxied post-close hook test")
	bdProxiedClose(t, bd, p.dir, issue.ID)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("post-close hook did not fire on proxied `bd close` (marker not created): %v", err)
	}
	if !strings.Contains(string(data), issue.ID) {
		t.Errorf("expected post-close hook to receive %q, got %q", issue.ID, strings.TrimSpace(string(data)))
	}
}

// TestPostCloseHookScaffoldIsPOSIXCompatible verifies the seeded hook template
// runs under strict POSIX sh. `set -euo pipefail` is a bashism: dash (the
// default /bin/sh on Debian/Ubuntu, e.g. the pm1 deploy target) rejects
// `pipefail`, killing the hook at startup — the close then "silently" skips
// the drift checkpoint.
func TestPostCloseHookScaffoldIsPOSIXCompatible(t *testing.T) {
	t.Parallel()

	if strings.Contains(postCloseHookTemplate, "pipefail") &&
		!strings.Contains(postCloseHookTemplate, "#!/usr/bin/env bash") {
		t.Fatalf("postCloseHookTemplate uses `pipefail` under a sh shebang; dash aborts at startup.\nEither drop pipefail or use a bash shebang.")
	}
}
