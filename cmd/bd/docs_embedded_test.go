//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocsEndToEndOnClose: bd docs init + bd close => wiki/log/<id>.md exists,
// and a replayed hook invocation (sweep semantics) converges instead of duplicating.
func TestDocsEndToEndOnClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	// Not t.Parallel(): t.Setenv below forbids it.

	bd := buildEmbeddedBD(t)
	// The post-close hook shells out to `bd docs update` via `command -v bd`
	// (PATH lookup, not the exact binary path) — put the freshly-built test
	// binary first on PATH so the hook doesn't resolve a stale system-wide
	// `bd` that predates the docs command. bdEnv(dir) copies os.Environ() at
	// call time, so this Setenv is visible to every bdEnv() call below it.
	t.Setenv("PATH", filepath.Dir(bd)+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir, _, _ := bdInit(t, bd, "--prefix", "dw")

	run := func(args ...string) string {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	run("docs", "init")
	issue := bdCreate(t, bd, dir, "Docs e2e issue", "--type", "task")
	bdClose(t, bd, dir, issue.ID)

	entry := filepath.Join(dir, "wiki", "log", issue.ID+".md")
	data, err := os.ReadFile(entry)
	if err != nil {
		t.Fatalf("entry not written by post-close hook: %v", err)
	}
	if !strings.Contains(string(data), issue.ID) {
		t.Fatalf("entry lacks issue ID:\n%s", data)
	}

	// Sweep-replay convergence: manual re-invocation must not duplicate/rewrite.
	before, _ := os.ReadFile(entry)
	run("docs", "update", issue.ID)
	after, _ := os.ReadFile(entry)
	if string(before) != string(after) {
		t.Fatal("replayed docs update rewrote the entry (idempotence broken)")
	}

	// bd docs log --since renders the closed issue from Dolt.
	out := run("docs", "log", "--since", "2026-01-01")
	if !strings.Contains(out, issue.ID) {
		t.Fatalf("docs log missing %s:\n%s", issue.ID, out)
	}
}
