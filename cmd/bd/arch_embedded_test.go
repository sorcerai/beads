//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bdArchInit runs "bd arch init" and returns stdout.
func bdArchInit(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"arch", "init"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd arch init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// TestArchInit verifies the construction-blueprint scaffolding: ARCH.md + the
// post-close hook are created, and the operation is idempotent (never overwrites).
func TestArchInit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ta")

	t.Run("scaffolds_arch_md_and_hook", func(t *testing.T) {
		out := bdArchInit(t, bd, dir)
		if !strings.Contains(out, "ARCH.md") {
			t.Errorf("expected output to mention ARCH.md: %s", out)
		}
		archPath := filepath.Join(dir, "ARCH.md")
		if _, err := os.Stat(archPath); err != nil {
			t.Fatalf("ARCH.md was not created: %v", err)
		}
		content, _ := os.ReadFile(archPath)
		if !strings.Contains(string(content), "Negative invariants") {
			t.Errorf("ARCH.md scaffold missing the negative-invariants section")
		}
		// post-close hook should be installed too
		hookPath := filepath.Join(beadsDir, "hooks", "post-close")
		if _, err := os.Stat(hookPath); err != nil {
			t.Errorf("post-close hook was not created: %v", err)
		}
	})

	t.Run("idempotent_does_not_overwrite_arch", func(t *testing.T) {
		// Hand-curate ARCH.md with a marker line.
		archPath := filepath.Join(dir, "ARCH.md")
		os.WriteFile(archPath, []byte("# MY CURATED BLUEPRINT\n# do not touch\n"), 0o644)

		bdArchInit(t, bd, dir)

		content, _ := os.ReadFile(archPath)
		if !strings.Contains(string(content), "MY CURATED BLUEPRINT") {
			t.Errorf("bd arch init overwrote a hand-curated ARCH.md: %s", content)
		}
	})

	t.Run("bd_init_seeds_blueprint", func(t *testing.T) {
		// bd init on a fresh repo should scaffold ARCH.md automatically.
		freshDir := t.TempDir()
		initGitRepoAt(t, freshDir)
		cmd := exec.Command(bd, "init", "--prefix", "tb", "--non-interactive")
		cmd.Dir = freshDir
		cmd.Env = bdEnv(freshDir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "ARCH.md") {
			t.Errorf("bd init should scaffold ARCH.md: %s", out)
		}
		archPath := filepath.Join(freshDir, "ARCH.md")
		if _, err := os.Stat(archPath); err != nil {
			t.Errorf("bd init did not create ARCH.md: %v", err)
		}
	})
}
