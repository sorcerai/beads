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

	// NOT seeded by hand: the proxied create and claim paths now write
	// .beads/last-touched exactly as the direct ones do, so post-tool's active
	// issue resolves on this route too. That is half of what this test pins.
	if _, err := os.Stat(filepath.Join(p.beadsDir, lastTouchedFile)); err != nil {
		t.Fatalf("proxied claim must record the last-touched issue: %v", err)
	}

	in := claudeHookPayload(t, "s1", p.dir, "Edit", filepath.Join(p.dir, "a", "a.go"))
	out, err := runBDEnvRaw(t, bd, p.dir, in, bdProxiedEnv(p.dir), "codemap-hook", "post-tool")
	if err != nil {
		t.Fatalf("post-tool must never fail a tool call: %v\n%s", err, out)
	}
	if !strings.Contains(out, "{}") {
		t.Fatalf("expected {} on stdout: %s", out)
	}

	who, err := bdProxiedRun(t, bd, p.dir, "codemap", "who", "a/a.go", "--all")
	if err != nil {
		t.Fatalf("bd codemap who: %v\n%s", err, who)
	}
	if !strings.Contains(string(who), issue.ID) {
		t.Fatalf("a proxied workspace must record the edit too: %s", who)
	}

	// `files` and `link` resolve their issue argument through
	// resolveCodemapIssueID, which used to force direct mode and so could not
	// run on a proxied workspace at all. Write with one, read with the other.
	if out, err := bdProxiedRun(t, bd, p.dir, "codemap", "link", issue.ID, "b/b.go"); err != nil {
		t.Fatalf("bd codemap link: %v\n%s", err, out)
	}
	files, err := bdProxiedRun(t, bd, p.dir, "codemap", "files", issue.ID)
	if err != nil {
		t.Fatalf("bd codemap files: %v\n%s", err, files)
	}
	for _, want := range []string{"a/a.go", "hook", "b/b.go", "manual"} {
		if !strings.Contains(string(files), want) {
			t.Fatalf("codemap files missing %q on a proxied workspace: %s", want, files)
		}
	}
}
