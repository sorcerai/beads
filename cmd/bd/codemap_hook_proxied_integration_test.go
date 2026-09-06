//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPostToolRecordsOnProxiedServer covers the second route through
// ensureRecorderStore. post-tool skips the root pre-run's store init so its
// budget can cover the open, which means it also skipped the config resolution
// that picks direct-vs-proxied — and a proxied workspace taking the direct arm
// fails with "proxy server store should be uow provider" and records nothing.
// This is the test that catches that.
func TestPostToolRecordsOnProxiedServer(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "pxh")
	writeRepoFile(t, p.dir, "a/a.go", "package a\n")

	issue := bdProxiedCreate(t, bd, p.dir, "proxied hook")
	if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--claim"); err != nil {
		t.Fatalf("bd update --claim: %v\n%s", err, out)
	}

	// Seeded by hand because the proxied create/claim path never calls
	// SetLastTouchedID, so .beads/last-touched does not exist on a proxied
	// workspace at all. That is a separate gap — post-tool's active-issue
	// source is empty there regardless of which store route it takes — and it
	// would otherwise mask what this test is for, which is the store OPEN.
	lastTouched := filepath.Join(p.beadsDir, lastTouchedFile)
	if err := os.WriteFile(lastTouched, []byte(issue.ID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	in := claudeHookPayload(t, "s1", p.dir, "Edit", filepath.Join(p.dir, "a", "a.go"))
	out, err := runBDEnvRaw(t, bd, p.dir, in, bdProxiedEnv(p.dir), "codemap-hook", "post-tool")
	if err != nil {
		t.Fatalf("post-tool must never fail a tool call: %v\n%s", err, out)
	}
	if !strings.Contains(out, "{}") {
		t.Fatalf("expected {} on stdout: %s", out)
	}

	// Read it back with `who`, not `files`: `files` resolves its issue argument
	// through resolveCodemapIssueID, which calls ensureDirectMode
	// unconditionally and so cannot run on a proxied workspace at all. That is
	// a Task 16 bug, reported separately; `who` is route-agnostic.
	who, err := bdProxiedRun(t, bd, p.dir, "codemap", "who", "a/a.go", "--all")
	if err != nil {
		t.Fatalf("bd codemap who: %v\n%s", err, who)
	}
	if !strings.Contains(string(who), issue.ID) {
		t.Fatalf("a proxied workspace must record the edit too: %s", who)
	}
}
