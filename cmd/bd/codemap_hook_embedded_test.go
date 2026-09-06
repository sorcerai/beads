//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/codemap/cache"
)

// claudeHookPayload builds the JSON Claude Code writes to a tool hook's stdin.
func claudeHookPayload(t *testing.T, session, cwd, tool, filePath string) string {
	t.Helper()
	payload := map[string]any{
		"session_id": session,
		"cwd":        cwd,
		"tool_name":  tool,
		"tool_input": map[string]any{"file_path": filePath},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestPreToolHookDedupesPerSessionAndIgnoresMissingCache covers the read half of
// Task 18: no cache is silence, the first read of a file injects its context,
// and the same file in the same session never injects twice.
func TestPreToolHookDedupesPerSessionAndIgnoresMissingCache(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pt")
	writeGoMini(t, dir)

	in := claudeHookPayload(t, "s1", dir, "Read", filepath.Join(dir, "a", "a.go"))

	out, err := runBDStdin(t, bd, dir, in, "codemap-hook", "pre-tool")
	if err != nil {
		t.Fatalf("pre-tool must never fail: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "{}" {
		t.Fatalf("no cache → {} ; got %s", out)
	}

	if err := cache.Write(beadsDir, cache.File{Files: map[string]cache.Entry{"a/a.go": {Path: "a/a.go", Summary: "A"}}}); err != nil {
		t.Fatal(err)
	}

	out, err = runBDStdin(t, bd, dir, in, "codemap-hook", "pre-tool")
	if err != nil {
		t.Fatalf("pre-tool must never fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "additionalContext") || !strings.Contains(out, "a/a.go") {
		t.Fatalf("first read must inject: %s", out)
	}
	if !strings.Contains(out, `"hookEventName":"PreToolUse"`) {
		t.Fatalf("wrong hook envelope: %s", out)
	}

	out, err = runBDStdin(t, bd, dir, in, "codemap-hook", "pre-tool")
	if err != nil {
		t.Fatalf("pre-tool must never fail: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "{}" {
		t.Fatalf("second read in the same session must be silent: %s", out)
	}

	// A different session has its own memory of what it has been told.
	other := claudeHookPayload(t, "s2", dir, "Read", filepath.Join(dir, "a", "a.go"))
	if out, err := runBDStdin(t, bd, dir, other, "codemap-hook", "pre-tool"); err != nil || !strings.Contains(out, "additionalContext") {
		t.Fatalf("a new session must be told once too: %v %s", err, out)
	}

	// A path outside the repository is not the map's business.
	outside := claudeHookPayload(t, "s3", dir, "Read", "/etc/hosts")
	if out, err := runBDStdin(t, bd, dir, outside, "codemap-hook", "pre-tool"); err != nil || strings.TrimSpace(out) != "{}" {
		t.Fatalf("a path outside the repo must be silent: %v %s", err, out)
	}
}

// TestPostToolRecordsOnlyForInProgressActiveIssue covers the write half: an
// edit is attributed to the active issue, but only while that issue is actually
// being worked, so merely having created an issue does not start hoovering up
// every file the agent touches.
func TestPostToolRecordsOnlyForInProgressActiveIssue(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "po")
	writeGoMini(t, dir)

	id := createIssue(t, bd, dir, "hook")
	in := claudeHookPayload(t, "s1", dir, "Edit", filepath.Join(dir, "a", "a.go"))

	if out, err := runBDStdin(t, bd, dir, in, "codemap-hook", "post-tool"); err != nil {
		t.Fatalf("post-tool must never fail: %v\n%s", err, out)
	}
	if out := runCodemap(t, bd, dir, "files", id); strings.Contains(out, "a/a.go") {
		t.Fatalf("open issue is not in_progress: nothing must be recorded: %s", out)
	}

	if stdout, stderr, err := runBDRaw(t, bd, dir, "update", id, "--claim"); err != nil {
		t.Fatalf("bd update --claim: %v\n%s\n%s", err, stdout, stderr)
	}

	if out, err := runBDStdin(t, bd, dir, in, "codemap-hook", "post-tool"); err != nil {
		t.Fatalf("post-tool must never fail: %v\n%s", err, out)
	}
	out := runCodemap(t, bd, dir, "files", id)
	if !strings.Contains(out, "a/a.go") || !strings.Contains(out, "hook") {
		t.Fatalf("expected hook record: %s", out)
	}

	// A read is not an edit: the tool filter is what keeps the record honest.
	readIn := claudeHookPayload(t, "s1", dir, "Read", filepath.Join(dir, "b", "b.go"))
	if _, err := runBDStdin(t, bd, dir, readIn, "codemap-hook", "post-tool"); err != nil {
		t.Fatalf("post-tool must never fail: %v", err)
	}
	if out := runCodemap(t, bd, dir, "files", id); strings.Contains(out, "b/b.go") {
		t.Fatalf("a Read must record nothing: %s", out)
	}
}
