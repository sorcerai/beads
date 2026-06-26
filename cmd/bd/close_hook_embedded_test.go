//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedPostCloseHook verifies the post-close lifecycle hook fires after
// a successful close, receives the closed issue ID as an arg, and is advisory
// (a non-zero hook exit does NOT fail the close).
func TestEmbeddedPostCloseHook(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ph")

	t.Run("hook_fires_with_closed_id", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Hook fire test", "--type", "task")

		// Marker file the hook writes to so the test can observe it fired.
		marker := filepath.Join(dir, ".post-close-marker")
		os.Remove(marker)

		// Install a post-close hook that writes its args to the marker.
		hookDir := filepath.Join(beadsDir, "hooks")
		if err := os.MkdirAll(hookDir, 0o700); err != nil {
			t.Fatalf("mkdir hooks: %v", err)
		}
		hookPath := filepath.Join(hookDir, "post-close")
		hookContent := "#!/usr/bin/env sh\necho \"$@\" > " + marker + "\n"
		if err := os.WriteFile(hookPath, []byte(hookContent), 0o755); err != nil {
			t.Fatalf("write hook: %v", err)
		}

		bdClose(t, bd, dir, issue.ID)

		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("post-close hook did not fire (marker not created): %v", err)
		}
		if !strings.Contains(string(data), issue.ID) {
			t.Errorf("expected hook to receive %q, got %q", issue.ID, strings.TrimSpace(string(data)))
		}
		os.Remove(marker)
		os.Remove(hookPath)
	})

	t.Run("hook_nonzero_does_not_block_close", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Advisory hook test", "--type", "task")

		// Install a hook that exits non-zero (simulates a drift finding).
		hookDir := filepath.Join(beadsDir, "hooks")
		if err := os.MkdirAll(hookDir, 0o700); err != nil {
			t.Fatalf("mkdir hooks: %v", err)
		}
		hookPath := filepath.Join(hookDir, "post-close")
		hookContent := "#!/usr/bin/env sh\necho 'drift found' >&2\nexit 1\n"
		if err := os.WriteFile(hookPath, []byte(hookContent), 0o755); err != nil {
			t.Fatalf("write hook: %v", err)
		}

		// Close must succeed despite the hook exiting 1 (advisory contract).
		bdClose(t, bd, dir, issue.ID)

		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected close to succeed despite non-zero hook; status=%s", got.Status)
		}
		os.Remove(hookPath)
	})

	t.Run("no_hooks_flag_skips_hook", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Skip hook test", "--type", "task")

		marker := filepath.Join(dir, ".post-close-marker-skip")
		os.Remove(marker)

		hookDir := filepath.Join(beadsDir, "hooks")
		if err := os.MkdirAll(hookDir, 0o700); err != nil {
			t.Fatalf("mkdir hooks: %v", err)
		}
		hookPath := filepath.Join(hookDir, "post-close")
		hookContent := "#!/usr/bin/env sh\necho fired > " + marker + "\n"
		if err := os.WriteFile(hookPath, []byte(hookContent), 0o755); err != nil {
			t.Fatalf("write hook: %v", err)
		}

		// Close with --no-hooks should NOT fire the hook.
		cmd := exec.Command(bd, "close", issue.ID, "--no-hooks")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd close --no-hooks failed: %v\n%s", err, out)
		}

		if _, err := os.Stat(marker); err == nil {
			t.Error("expected hook NOT to fire with --no-hooks, but marker was created")
		}
		os.Remove(marker)
		os.Remove(hookPath)
	})
}
